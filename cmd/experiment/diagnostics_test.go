package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/quota"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

func newDiagnosticsController(t *testing.T) *controller {
	t.Helper()
	c, err := newController(options{Data: t.TempDir(), Account: 1, Details: 2, Keyword: "相机", Fixture: true}, testSettings())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func TestExperimentFailureKeepsSummaryAndOnlyFailedResponse(t *testing.T) {
	c := newDiagnosticsController(t)
	c.beginSummary("run")
	failure := errors.New("缺少商品列表")
	c.recordStep("打开首页", func() error { return nil })
	if err := c.diagnose(t.Context(), nil, web.Diagnostic{Kind: "parse", Step: "解析搜索", Err: failure, Response: []byte(`{"mobile":"13800138000","api_key":"private-model-key","data":{"item":{"title":"商品原文"}}`)}); err != nil {
		t.Fatal(err)
	}
	c.log("搜索商品字段", map[string]any{"标题": "商品原文"})
	c.log("详情字段", map[string]any{"商品": "42"})
	c.phase("failed", failure)
	summaryFiles, err := filepath.Glob(filepath.Join(experimentLogDir(c.opts), "summaries", "*", "summary.json"))
	if err != nil || len(summaryFiles) != 1 {
		t.Fatalf("没有保留单次摘要：%v %v", summaryFiles, err)
	}
	data, err := os.ReadFile(summaryFiles[0])
	if err != nil {
		t.Fatal(err)
	}
	var summary experimentSummary
	if err := json.Unmarshal(data, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Phase != "failed" || summary.Reason != "缺少商品列表" || summary.SearchCount != 1 || summary.DetailCount != 1 || len(summary.Steps) != 2 || len(summary.Diagnostics) != 1 || len(summary.Observations) != len(experimentChecks) || summary.FinishedAt.IsZero() {
		t.Fatalf("摘要缺字段或错误结论：%s", data)
	}
	response, err := os.ReadFile(filepath.Join(summary.Diagnostics[0], "response.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(response, []byte("13800138000")) || bytes.Contains(response, []byte("private-model-key")) || !bytes.Contains(response, []byte("商品原文")) {
		t.Fatalf("异常响应脱敏或原文保留不符：%s", response)
	}
	eventFiles, _ := filepath.Glob(filepath.Join(experimentLogDir(c.opts), "events-*.jsonl"))
	events, _ := os.ReadFile(eventFiles[0])
	if bytes.Contains(events, []byte("private-model-key")) || bytes.Contains(events, []byte(`\"data\"`)) {
		t.Fatal("普通事件保存了接口正文")
	}
	if err := c.diagnose(t.Context(), nil, web.Diagnostic{Kind: "risk", Step: "验证码", Err: web.ErrNeedVerify}); err != nil {
		t.Fatal(err)
	}
	bundles, _ := filepath.Glob(filepath.Join(experimentLogDir(c.opts), "bundles", "*", "response.json"))
	if len(bundles) != 1 {
		t.Fatalf("风控诊断不应另存正常接口正文：%v", bundles)
	}
}

func TestExperimentDiagnosticWriteFailureStopsBeforeAnotherVisit(t *testing.T) {
	c := newDiagnosticsController(t)
	var stderr bytes.Buffer
	c.stderr = &stderr
	job, err := c.reserve(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	defer c.release()
	defer c.journal.finish()
	c.beginSummary("run")
	files, _ := filepath.Glob(filepath.Join(experimentLogDir(c.opts), "events-*.jsonl"))
	if len(files) != 1 {
		t.Fatal(files)
	}
	if err := os.Remove(files[0]); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(files[0], 0700); err != nil {
		t.Fatal(err)
	}
	c.log("下一步", map[string]any{"步骤": "提交搜索"})
	select {
	case <-job.Done():
	case <-time.After(time.Second):
		t.Fatal("落盘失败未取消自动流程")
	}
	var calls int
	budget := diagnosticBudget{inner: countExperimentBudget{&calls}, recorder: c.recorder}
	if err := budget.Acquire(quota.Search, "首页"); !errors.Is(err, web.ErrDiagnostics) || calls != 0 {
		t.Fatalf("日志坏了仍申请闲鱼访问：%d %v", calls, err)
	}
	response := httptest.NewRecorder()
	experimentHandler(c, &access{}).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://local/healthz", nil))
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(stderr.String(), "已停止访问") {
		t.Fatalf("没有明显报告失败：%d %s", response.Code, stderr.String())
	}
}

type countExperimentBudget struct{ calls *int }

func (b countExperimentBudget) Acquire(quota.Kind, string) error { *b.calls++; return nil }

func TestExperimentOutputUsesSameSanitizedEventsAsDisk(t *testing.T) {
	c := newDiagnosticsController(t)
	var output bytes.Buffer
	c.sink = &output
	c.log("读取失败", map[string]any{"Cookie": "private-cookie", "password": "private-password", "mobile": "13800138000", "网址": "http://name:private-proxy@127.0.0.1:8000/?token=private-token&id=42", "标题": "商品原文"})
	for _, secret := range []string{"private-cookie", "private-password", "13800138000", "private-proxy", "private-token"} {
		if strings.Contains(output.String(), secret) {
			t.Fatalf("终端泄露：%s", secret)
		}
	}
	var got event
	if err := json.Unmarshal(output.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	current := c.snapshot().Events
	last := current[len(current)-1]
	if got.Sequence == 0 || got.Sequence != last.Sequence || got.Fields["标题"] != "商品原文" {
		t.Fatalf("网页或终端不是同一条脱敏事件：%s", output.Bytes())
	}
}

func TestExperimentStartupLogDoesNotBlockFirstSuccessfulInitialization(t *testing.T) {
	data := t.TempDir()
	path := filepath.Join(data, "experiment-settings.json")
	if err := os.WriteFile(path, []byte(`{"timezone":"bad/zone"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runCommand([]string{"--data", data, "--fixture"}, io.Discard, io.Discard); err == nil {
		t.Fatal("错误设置通过")
	}
	events, _ := filepath.Glob(filepath.Join(data, "experiment-fixture", "1", "logs", "events-*.jsonl"))
	if len(events) != 1 {
		t.Fatalf("启动错误没有落盘：%v", events)
	}
	settings := testSettings()
	c, err := newController(options{Data: data, Account: 1, Fixture: true, Details: 2}, settings)
	if err != nil {
		t.Fatalf("日志目录让首次初始化被错判损坏：%v", err)
	}
	c.Close()
	contents, _ := os.ReadFile(events[0])
	if !bytes.Contains(contents, []byte("启动失败")) || !bytes.Contains(contents, []byte("启动环境")) {
		t.Fatalf("重启覆盖了此前的失败：%s", contents)
	}
}

func TestExperimentOccupiedPortIsReportedAndPersistedBeforeToken(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	data := t.TempDir()
	var output bytes.Buffer
	err = runCommand([]string{"--data", data, "--fixture", "--listen", listener.Addr().String()}, &output, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "--listen :8180") || strings.Contains(output.String(), "初始化令牌") {
		t.Fatalf("端口占用提示不明确或输出不可用令牌：%v %s", err, output.String())
	}
	files, _ := filepath.Glob(filepath.Join(data, "experiment-fixture", "1", "logs", "events-*.jsonl"))
	content, _ := os.ReadFile(files[0])
	if !bytes.Contains(content, []byte("端口可能已被占用")) {
		t.Fatalf("退出后没有端口错误：%s", content)
	}
}

func TestExperimentEnvironmentRecordsProxyPresenceWithoutCredentials(t *testing.T) {
	c := newDiagnosticsController(t)
	c.opts.Proxy, _ = url.Parse("http://local-user:private-proxy@127.0.0.1:8000")
	t.Setenv("HTTP_PROXY", "http://private-env:private-env-password@127.0.0.1:8001")
	c.log("启动环境", c.environment())
	data, _ := json.Marshal(c.snapshot().Events)
	for _, secret := range []string{"private-proxy", "private-env", "private-env-password"} {
		if bytes.Contains(data, []byte(secret)) {
			t.Fatal("环境记录含代理凭据")
		}
	}
	if !bytes.Contains(data, []byte("proxy_configured")) || !bytes.Contains(data, []byte("rules_version")) || !bytes.Contains(data, []byte("proxy_environment_present")) {
		t.Fatalf("环境信息缺失：%s", data)
	}
}

func TestExperimentSummaryKeepsEachCookieObservation(t *testing.T) {
	c := newDiagnosticsController(t)
	c.beginSummary("login")
	c.log("待确认观察", map[string]any{"事项": "login_cookie", "状态": "已观察", "结果": map[string]any{"before_login_present": false}})
	c.log("待确认观察", map[string]any{"事项": "login_cookie", "状态": "已观察", "结果": map[string]any{"after_login_present": true, "logout_checked": false}})
	c.phase("completed", nil)
	files, _ := filepath.Glob(filepath.Join(experimentLogDir(c.opts), "summaries", "*", "summary.json"))
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	var summary experimentSummary
	if err := json.Unmarshal(data, &summary); err != nil {
		t.Fatal(err)
	}
	observation := summary.Observations["login_cookie"].(map[string]any)["结果"].(map[string]any)
	if observation["before_login_present"] != false || observation["after_login_present"] != true || observation["logout_checked"] != false {
		t.Fatalf("后一次观察覆盖了扫码前证据：%s", data)
	}
}
