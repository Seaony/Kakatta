package resources

import (
	"context"
	"errors"
	"os"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/Seaony/Kakatta/internal/buildinfo"
	"github.com/Seaony/Kakatta/internal/diagnostics"
	"github.com/Seaony/Kakatta/internal/quota"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/xianyu"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

type diagnosticBudget struct {
	r    *resource
	next web.Budget
}

func (b diagnosticBudget) Acquire(kind quota.Kind, what string) error {
	if err := b.r.logs.Err(); err != nil {
		return errors.Join(web.ErrDiagnostics, err)
	}
	return b.next.Acquire(kind, what)
}

func resourceEnvironment(spec service.ResourceSpec) map[string]any {
	commit := buildinfo.Commit
	if commit == "" {
		commit = "unknown"
	}
	modified := false
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				if commit == "unknown" {
					commit = setting.Value
				}
			case "vcs.modified":
				modified = setting.Value == "true"
			}
		}
	}
	proxy := ""
	if spec.Proxy != nil {
		proxy = diagnostics.SafeURL(spec.Proxy.String())
	}
	environment := map[string]bool{}
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy"} {
		_, environment[key] = os.LookupEnv(key)
	}
	return map[string]any{
		"version": buildinfo.Version, "commit": commit, "modified": modified,
		"os": runtime.GOOS, "arch": runtime.GOARCH,
		"webgl": "off", "sandbox": "enabled", "rule_version": xianyu.WebProtocol().Version,
		"account_id": spec.AccountID, "timezone": spec.Timezone, "cache_mb": spec.CacheMB,
		"settings": spec.DiagnosticSettings, "pace": spec.Pace, "typing": spec.Typing,
		"proxy_configured": spec.Proxy != nil, "proxy_address": proxy, "proxy_environment_present": environment,
	}
}

func (r *resource) log(name string, fields map[string]any) {
	if r.logs != nil {
		if err := r.logs.Event(name, fields); err != nil {
			r.cancel()
			return
		}
		r.mu.Lock()
		if reason, ok := fields["原因"].(string); ok {
			r.lastError = reason
		}
		if name == "运行阶段" {
			r.phases = append(r.phases, fields)
		}
		if name == "运行摘要" {
			r.summary = fields
		}
		r.mu.Unlock()
	}
	if r.logSink != nil {
		r.logSink(name, fields)
	}
}

func (r *resource) diagnose(ctx context.Context, failure web.Diagnostic) error {
	if r.logs == nil {
		return nil
	}
	if failure.Kind != "element" && failure.Kind != "risk" && failure.Kind != "parse" {
		return r.logs.Err()
	}
	// 风控出现后只读当前页，不能为取证刷新或再打开一个页面。
	read, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	page, _ := r.factory.readPage(read, r.chrome)
	if page.URL == "" {
		page.URL = failure.URL
	}
	path, err := r.logs.Bundle(failure.Kind, diagnostics.Page{
		Screenshot: page.Screenshot, HTML: page.HTML, URL: page.URL, Title: page.Title, Errors: page.Errors, LoginPage: page.LoginPage,
	}, failure.Response)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.bundles = append(r.bundles, path)
	r.mu.Unlock()
	return r.logs.Event("诊断包已保存", map[string]any{"kind": failure.Kind, "step": failure.Step, "path": path})
}

func (r *resource) closeDiagnostics() error {
	r.mu.Lock()
	result := map[string]any{
		"version": 1, "started_at": r.started, "ended_at": time.Now(),
		"duration_ms": time.Since(r.started).Milliseconds(), "steps": r.phases,
		"result": r.summary, "error": r.lastError, "bundles": append([]string(nil), r.bundles...),
	}
	r.mu.Unlock()
	if r.closeErr != nil {
		result["close_error"] = r.closeErr.Error()
	}
	result["events"] = r.logs.Events()
	return errors.Join(r.logs.Summary(result), r.logs.Close())
}
