package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"slices"

	"github.com/Seaony/Kakatta/internal/browser"
)

var ErrDiagnostics = errors.New("诊断记录写入失败，已停止访问")

// Diagnostic 只在出错时交出当前步骤，正文仅限解析失败的那条响应。
type Diagnostic struct {
	Kind     string
	Step     string
	Err      error
	URL      string
	Response []byte
}

func (r *Runner) ReportFailure(ctx context.Context, step string, err error, response *browser.Response) error {
	if err == nil || errors.Is(err, ErrDiagnostics) {
		return err
	}
	kind := "operation"
	switch {
	case response != nil:
		kind = "parse"
	case IsStop(err):
		kind = "risk"
	case errors.Is(err, browser.ErrNotFound), errors.Is(err, browser.ErrResponseTimeout):
		kind = "element"
	}
	d := Diagnostic{Kind: kind, Step: step, Err: err}
	if response != nil {
		d.URL = diagnosticURL(response.URL)
		d.Response = slices.Clone(response.Body)
	}
	fields := map[string]any{"步骤": step, "分类": kind, "原因": err.Error()}
	var risk *RiskError
	if errors.As(err, &risk) {
		fields["信号"] = risk.Signal
		fields["命中位置"] = risk.Where
		r.signal(map[string]any{"类型": risk.Kind.Error(), "命中位置": risk.Where, "值": risk.Signal})
	}
	if d.URL != "" {
		fields["网址"] = d.URL
	}
	if r.Log != nil {
		r.Log("步骤失败", fields)
	}
	if r.Diagnose != nil {
		if failure := r.Diagnose(ctx, d); failure != nil {
			return errors.Join(err, ErrDiagnostics, failure)
		}
	}
	return err
}

func diagnosticURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	u.User = nil
	u.Fragment = ""
	allowed := url.Values{}
	for _, key := range []string{"q", "id", "userId"} {
		if value := u.Query().Get(key); value != "" {
			allowed.Set(key, value)
		}
	}
	u.RawQuery = allowed.Encode()
	return u.String()
}

func (r *Runner) observation(name string, value any) {
	encoded, _ := json.Marshal(value)
	r.observationMu.Lock()
	if r.observed == nil {
		r.observed = map[string]string{}
	}
	if previous, ok := r.observed[name]; ok && previous == string(encoded) {
		r.observationMu.Unlock()
		return
	}
	r.observed[name] = string(encoded)
	r.observationMu.Unlock()
	if r.Log != nil {
		r.Log("待确认观察", map[string]any{"事项": name, "状态": "已观察", "结果": value, "规则版本": rules.Version})
	}
}
