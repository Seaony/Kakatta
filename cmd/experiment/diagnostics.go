package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/Seaony/Kakatta/internal/browser"
	"github.com/Seaony/Kakatta/internal/buildinfo"
	"github.com/Seaony/Kakatta/internal/diagnostics"
	"github.com/Seaony/Kakatta/internal/quota"
	"github.com/Seaony/Kakatta/internal/xianyu"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

var errBrowserDisconnected = errors.New("浏览器连接已中断")

var experimentChecks = []string{"search_input", "search_prefill", "search_same_tab", "search_url", "search_suggestion", "chinese_input", "filter_container", "publish_container", "region_requests", "search_seller_id", "detail_sale_status", "detail_description_path", "seller_item_count", "seller_credit_mapping", "search_area_format", "rating_time_format", "login_cookie", "login_selectors", "login_refresh", "own_profile", "iframe_urls", "rule_selectors"}

type experimentStep struct {
	Name       string `json:"name"`
	Passed     bool   `json:"passed"`
	DurationMS int64  `json:"duration_ms"`
	ErrorClass string `json:"error_class,omitempty"`
	Reason     string `json:"reason,omitempty"`
}
type experimentSummary struct {
	Version      int              `json:"version"`
	Mode         string           `json:"mode"`
	Fixture      bool             `json:"fixture"`
	StartedAt    time.Time        `json:"started_at"`
	FinishedAt   time.Time        `json:"finished_at"`
	DurationMS   int64            `json:"duration_ms"`
	Phase        string           `json:"phase"`
	ErrorClass   string           `json:"error_class,omitempty"`
	Reason       string           `json:"reason,omitempty"`
	Steps        []experimentStep `json:"steps"`
	SearchCount  int              `json:"search_count"`
	DetailCount  int              `json:"detail_count"`
	Signals      []map[string]any `json:"signals"`
	Observations map[string]any   `json:"observations"`
	Diagnostics  []string         `json:"diagnostics"`
	Environment  map[string]any   `json:"environment"`
}

type diagnosticBudget struct {
	inner    web.Budget
	recorder *diagnostics.Recorder
}

func (b diagnosticBudget) Acquire(kind quota.Kind, what string) error {
	if err := b.recorder.Err(); err != nil {
		return errors.Join(web.ErrDiagnostics, err)
	}
	return b.inner.Acquire(kind, what)
}

func experimentLogDir(o options) string {
	branch := "experiment"
	if o.Fixture {
		branch = "experiment-fixture"
	}
	return filepath.Join(o.Data, branch, strconv.Itoa(o.Account), "logs")
}

func (c *controller) beginSummary(mode string) {
	c.mu.Lock()
	loc, _ := time.LoadLocation(c.settings.Timezone)
	c.summary = &experimentSummary{Version: 1, Mode: mode, Fixture: c.opts.Fixture, StartedAt: time.Now().In(loc), Steps: []experimentStep{}, Signals: []map[string]any{}, Observations: map[string]any{}, Diagnostics: []string{}, Environment: c.environment()}
	c.bundled = map[string]bool{}
	c.mu.Unlock()
	c.log("阶段切换", map[string]any{"阶段": "opening", "模式": mode})
	for _, name := range experimentChecks {
		c.log("待确认观察", map[string]any{"事项": name, "状态": "未执行", "原因": "本次流程尚未观测；不为确认额外访问页面"})
	}
}

func (c *controller) environment() map[string]any {
	revision := "unknown"
	modified := false
	if build, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range build.Settings {
			if setting.Key == "vcs.revision" {
				revision = setting.Value
			}
			if setting.Key == "vcs.modified" {
				modified = setting.Value == "true"
			}
		}
	}
	if buildinfo.Commit != "" && buildinfo.Commit != "unknown" {
		revision = buildinfo.Commit
	}
	proxyEnvironment := map[string]bool{}
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy"} {
		_, proxyEnvironment[name] = os.LookupEnv(name)
	}
	proxy := ""
	if c.opts.Proxy != nil {
		proxy = diagnostics.SafeURL(c.opts.Proxy.String())
	}
	installed := "unavailable"
	if version, err := browser.ExecutableVersion(context.Background()); err == nil {
		installed = version
	}
	return map[string]any{"version": buildinfo.Version, "commit": revision, "worktree_modified": modified, "os": runtime.GOOS, "architecture": runtime.GOARCH, "chrome_executable_version": installed, "chrome_version": "未启动", "webgl": c.opts.WebGL, "sandbox": "enabled", "rules_version": xianyu.WebProtocol().Version, "timezone": c.settings.Timezone, "settings": c.settings, "proxy_configured": c.opts.Proxy != nil, "proxy_address": proxy, "proxy_environment_present": proxyEnvironment, "fixture": c.opts.Fixture}
}

func (c *controller) recordStep(name string, action func() error) error {
	start := time.Now()
	c.log("步骤开始", map[string]any{"步骤": name})
	if err := c.recorder.Err(); err != nil {
		return errors.Join(web.ErrDiagnostics, err)
	}
	err := action()
	c.mu.Lock()
	if c.summary != nil && c.summary.FinishedAt.IsZero() {
		c.summary.Steps = append(c.summary.Steps, experimentStep{Name: name, Passed: err == nil, DurationMS: time.Since(start).Milliseconds(), ErrorClass: experimentErrorClass(err), Reason: diagnosticReason(err)})
	}
	c.mu.Unlock()
	fields := map[string]any{"步骤": name, "成功": err == nil, "耗时毫秒": time.Since(start).Milliseconds()}
	if err != nil {
		fields["分类"] = experimentErrorClass(err)
		fields["原因"] = diagnosticReason(err)
	}
	c.log("步骤结束", fields)
	if logErr := c.recorder.Err(); logErr != nil {
		return errors.Join(err, web.ErrDiagnostics, logErr)
	}
	return err
}

func (c *controller) finishSummary(phase string, err error) {
	c.mu.Lock()
	if c.summary == nil || !c.summary.FinishedAt.IsZero() {
		c.mu.Unlock()
		return
	}
	loc, _ := time.LoadLocation(c.settings.Timezone)
	c.summary.FinishedAt = time.Now().In(loc)
	c.summary.DurationMS = c.summary.FinishedAt.Sub(c.summary.StartedAt).Milliseconds()
	c.summary.Phase, c.summary.ErrorClass, c.summary.Reason = phase, experimentErrorClass(err), diagnosticReason(err)
	summary, encodeErr := json.Marshal(c.summary)
	c.mu.Unlock()
	if encodeErr != nil {
		c.failLogging(encodeErr)
		return
	}
	if failure := c.recorder.Summary(json.RawMessage(summary)); failure != nil {
		c.failLogging(failure)
	}
}

func (c *controller) diagnose(ctx context.Context, r *resources, d web.Diagnostic) error {
	key := fmt.Sprintf("%s|%s|%s|%x|%s", d.Kind, d.Step, d.URL, sha256.Sum256(d.Response), diagnosticReason(d.Err))
	c.mu.Lock()
	if c.bundled == nil {
		c.bundled = map[string]bool{}
	}
	already := c.bundled[key]
	c.bundled[key] = true
	c.mu.Unlock()
	if already {
		return c.recorder.Err()
	}
	c.log("故障诊断", map[string]any{"分类": d.Kind, "步骤": d.Step, "原因": diagnosticReason(d.Err), "接口网址": diagnostics.SafeURL(d.URL)})
	page := diagnostics.Page{LoginPage: true, Errors: map[string]string{}}
	if r != nil && r.chrome != nil {
		capture, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		snapshot, captureErr := r.chrome.DiagnosticPage(capture)
		stop()
		page = diagnostics.Page{LoginPage: snapshot.LoginPage, Screenshot: snapshot.Screenshot, HTML: snapshot.HTML, URL: snapshot.URL, Title: snapshot.Title, Errors: snapshot.Errors}
		if captureErr != nil {
			c.log("诊断画面未完整取得", map[string]any{"原因": diagnosticReason(captureErr)})
		}
	} else {
		page.Errors["browser"] = "浏览器未成功启动"
	}
	path, err := c.recorder.Bundle(d.Kind, page, d.Response)
	if err != nil {
		c.failLogging(err)
		return err
	}
	c.mu.Lock()
	if c.summary != nil && c.summary.FinishedAt.IsZero() {
		c.summary.Diagnostics = append(c.summary.Diagnostics, path)
		c.summary.Steps = append(c.summary.Steps, experimentStep{Name: d.Step, Passed: false, ErrorClass: d.Kind, Reason: diagnosticReason(d.Err)})
	}
	c.mu.Unlock()
	c.log("诊断包已保存", map[string]any{"路径": path, "分类": d.Kind, "步骤": d.Step, "包含响应": len(d.Response) > 0})
	return nil
}

func (c *controller) finalError(ctx context.Context, r *resources, step string, err error) error {
	if err == nil {
		return nil
	}
	if r != nil && r.chrome != nil && r.chrome.Context().Err() != nil && ctx.Err() == nil {
		err = errors.Join(err, errBrowserDisconnected)
		c.log("浏览器异常退出", map[string]any{"步骤": step, "pid": r.chrome.PID(), "原因": diagnosticReason(err)})
	}
	if r != nil && r.runner != nil {
		return r.runner.ReportFailure(ctx, step, err, nil)
	}
	return errors.Join(err, c.diagnose(ctx, r, web.Diagnostic{Kind: experimentErrorClass(err), Step: step, Err: err}))
}

func diagnosticReason(err error) string {
	if err == nil {
		return ""
	}
	return diagnostics.RedactText(err.Error())
}
func experimentErrorClass(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, web.ErrDiagnostics):
		return "diagnostics_write"
	case errors.Is(err, web.ErrDenied):
		return "denied"
	case errors.Is(err, web.ErrEnvBlocked):
		return "environment"
	case errors.Is(err, web.ErrNeedVerify):
		return "verification"
	case errors.Is(err, web.ErrLoginExpired):
		return "login_expired"
	case errors.Is(err, web.ErrQuota):
		return "quota"
	case errors.Is(err, browser.ErrResponseTimeout):
		return "response_timeout"
	case errors.Is(err, browser.ErrNotFound):
		return "element"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, web.ErrLoginTimeout):
		return "timeout"
	case errors.Is(err, errBrowserDisconnected):
		return "browser_disconnected"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	default:
		return "operation"
	}
}

func (c *controller) failLogging(err error) {
	if err == nil {
		return
	}
	c.mu.Lock()
	first := c.logErr == nil
	c.logErr = errors.Join(c.logErr, err)
	c.state.Error = "诊断日志写入失败，已停止访问：" + diagnosticReason(err)
	message := c.state.Error
	cancel, r, sink := c.cancelJob, c.res, c.stderr
	c.mu.Unlock()
	if !first {
		return
	}
	if sink == nil {
		sink = os.Stderr
	}
	fmt.Fprintln(sink, message)
	if cancel != nil {
		cancel()
	}
	if r != nil {
		r.close()
	}
}

func (c *controller) appendEvent(name string, fields map[string]any) {
	if err := c.recorder.Event(name, fields); err != nil {
		c.failLogging(err)
		return
	}
	events := c.recorder.Events()
	if len(events) == 0 {
		return
	}
	latest := events[len(events)-1]
	c.mu.Lock()
	c.state.Events = append(c.state.Events, latest)
	if len(c.state.Events) > 200 {
		c.state.Events = append([]event(nil), c.state.Events[len(c.state.Events)-200:]...)
	}
	if c.summary != nil && c.summary.FinishedAt.IsZero() {
		switch name {
		case "搜索商品字段":
			c.summary.SearchCount++
		case "详情字段":
			c.summary.DetailCount++
		case "风控信号":
			c.summary.Signals = append(c.summary.Signals, latest.Fields)
		case "待确认观察":
			if item, ok := latest.Fields["事项"].(string); ok {
				observation := latest.Fields
				if previous, ok := c.summary.Observations[item].(map[string]any); ok {
					oldResult, oldOK := previous["结果"].(map[string]any)
					newResult, newOK := latest.Fields["结果"].(map[string]any)
					if oldOK && newOK {
						merged := map[string]any{}
						for key, value := range oldResult {
							merged[key] = value
						}
						for key, value := range newResult {
							merged[key] = value
						}
						observation = map[string]any{}
						for key, value := range latest.Fields {
							observation[key] = value
						}
						observation["结果"] = merged
					}
				}
				c.summary.Observations[item] = observation
			}
		}
	}
	sink := c.sink
	c.mu.Unlock()
	if sink != nil {
		json.NewEncoder(sink).Encode(latest)
	}
}

func recordStartupFailure(o options, timezone string, err error) error {
	recorder, failure := diagnostics.Open(experimentLogDir(o), timezone)
	if failure != nil {
		return errors.Join(err, fmt.Errorf("启动失败且无法保存诊断：%w", failure))
	}
	defer recorder.Close()
	fields := map[string]any{"步骤": "启动", "分类": experimentErrorClass(err), "原因": diagnosticReason(err)}
	failure = recorder.Event("启动失败", fields)
	failure = errors.Join(failure, recorder.Summary(map[string]any{"version": 1, "mode": "startup", "error": fields}))
	return errors.Join(err, failure)
}

func (r *resources) openLog(recorder *diagnostics.Recorder, name string) (io.Writer, error) {
	output, err := recorder.ProcessLog(name)
	if err == nil {
		r.logs = append(r.logs, output)
	}
	return output, err
}
