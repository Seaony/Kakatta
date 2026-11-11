package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/debugger"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/log"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"

	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

const formalUsername = "formal-local"
const formalPassword = "local-formal-password-2026"
const formalBaseURL = "http://127.0.0.1:18080"

type formalMetadata struct {
	Site         web.Site `json:"-"`
	ModelBaseURL string   `json:"model_base_url"`
	PushURL      string   `json:"push_url"`
	ModelKey     string   `json:"model_key"`
	ControlURL   string   `json:"control_url"`
}

type formalPathStep struct {
	Number int    `json:"number"`
	Result string `json:"result"`
}

type formalPathReport struct {
	Stage                string           `json:"stage"`
	Passed               bool             `json:"passed"`
	Steps                []formalPathStep `json:"steps"`
	BrowserClosed        bool             `json:"browser_closed"`
	ProcessCleanupPassed bool             `json:"process_cleanup_passed"`
	Error                string           `json:"error,omitempty"`
}

func TestFormalPathJourney(t *testing.T) {
	stage := requireFormalPath(t)
	report := formalPathReport{Stage: stage}
	defer func() {
		report.Steps = formalReadSteps(t, stage)
		report.Passed = !t.Failed() && report.BrowserClosed && report.ProcessCleanupPassed
		if !report.Passed {
			report.Error = "正式网页路径失败，请查看本阶段日志和截图"
		}
		writeFormalReport(t, report)
	}()
	metadata := loadFormalMetadata(t)
	allocator, stopAllocator := chromedp.NewExecAllocator(t.Context(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath("/usr/bin/google-chrome-stable"), chromedp.UserDataDir("/work/client-browser"))...)
	browser, stopBrowser := chromedp.NewContext(allocator)
	var events webSmokeEvents
	events.base, events.page = formalBaseURL, "正式网页/"+stage
	if err := os.MkdirAll("/work/reports", 0700); err != nil {
		t.Fatal(err)
	}
	trace, err := os.OpenFile("/work/reports/"+stage+"-requests.jsonl", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal("打开正式请求记录失败", err)
	}
	audit := newFormalRequestAudit(stage, trace, &events, func(id network.RequestID) ([]byte, error) { return formalReadResponseBody(browser, id) })
	browser = context.WithValue(browser, formalNetworkKey{}, audit)
	listening, stopListening := context.WithCancel(browser)
	defer func() {
		stopListening()
		audit.finish()
		if err := trace.Sync(); err != nil {
			t.Error("保存正式请求记录失败", err)
		}
		if err := trace.Close(); err != nil {
			t.Error(err)
		}
		events.mu.Lock()
		issues := append([]string{}, events.issues...)
		events.mu.Unlock()
		if len(issues) != 0 {
			t.Errorf("正式请求或控制台错误：\n%s", strings.Join(issues, "\n"))
		}
		closing, cancel := context.WithTimeout(context.WithoutCancel(browser), 10*time.Second)
		defer cancel()
		if t.Failed() {
			_ = os.MkdirAll("/work/screens", 0700)
			if shot, err := chromedp.Run(closing, chromedp.CaptureScreenshot()); err == nil {
				_ = os.WriteFile("/work/screens/"+stage+"-failure.png", shot, 0600)
			}
			if body, err := chromedp.Run(closing, chromedp.Evaluate[string](`document.body.innerText`)); err == nil {
				_ = os.WriteFile("/work/screens/"+stage+"-failure.txt", []byte(body), 0600)
			}
		}
		if err := chromedp.Cancel(closing); err != nil {
			t.Error("关闭网页验收浏览器失败", err)
		} else {
			report.BrowserClosed = true
		}
		stopBrowser()
		stopAllocator()
		for range 50 {
			if formalBrowserProcessesClosed() {
				report.ProcessCleanupPassed = true
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if !report.ProcessCleanupPassed {
			t.Error("网页验收进程仍有浏览器子进程残留")
		}
	}()
	if _, err := chromedp.Run(browser, chromedp.Evaluate[string](`navigator.userAgent`)); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Listen(listening, audit.receive); err != nil {
		t.Fatal(err)
	}
	runWebSmoke(t, browser, &events, chromedp.Func(func(ctx context.Context, _ *chromedp.Target) error {
		if _, err := chromedp.Call(ctx, network.Enable, network.EnableParams{}); err != nil {
			return err
		}
		if _, err := chromedp.Call(ctx, debugger.Enable, debugger.EnableParams{}); err != nil {
			return err
		}
		if _, err := chromedp.Call(ctx, log.Enable, cdp.Empty{}); err != nil {
			return err
		}
		_, err := chromedp.Call(ctx, emulation.SetDeviceMetricsOverride, emulation.SetDeviceMetricsOverrideParams{
			Width: 1280, Height: 900, DeviceScaleFactor: 1, Mobile: false,
		})
		return err
	}), smokeBrowserLanguage("zh-CN"))
	if stage == "first" {
		formalSetup(t, browser, &events, metadata)
		formalMonitoring(t, browser, &events, metadata)
		formalFailures(t, browser, &events, metadata)
		data, err := json.Marshal(formalPersistence(t))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile("/work/reports/persistence.json", data, 0600); err != nil {
			t.Fatal("保存重启前的数据核对记录失败", err)
		}
	} else {
		formalResume(t, browser, &events, metadata, stage)
	}
	events.assert(t)
}

func formalBrowserProcessesClosed() bool {
	paths, err := filepath.Glob("/proc/[0-9]*/comm")
	if err != nil {
		return false
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		name := strings.TrimSpace(string(data))
		if strings.HasPrefix(name, "chrome") || strings.HasPrefix(name, "google-chrome") {
			return false
		}
	}
	return true
}

func requireFormalPath(t *testing.T) string {
	t.Helper()
	if os.Getenv("KAKATTA_FORMAL_PATH") != "1" {
		t.Skip("由 deploy/test-formal-path.sh 在断网 Compose 中运行")
	}
	interfaces, err := net.Interfaces()
	if err != nil || len(interfaces) != 1 || interfaces[0].Flags&net.FlagLoopback == 0 {
		t.Fatal("正式路径回归只能在只有回环网卡的容器里运行")
	}
	if os.Getenv("KAKATTA_FORMAL_DIR") != "/work" || os.Getenv("KAKATTA_FORMAL_DATA") != "/data" {
		t.Fatal("正式路径回归必须使用独立的 /work 和 /data 测试挂载")
	}
	stage := os.Getenv("KAKATTA_FORMAL_STAGE")
	if stage != "first" && stage != "restart" && stage != "upgrade" {
		t.Fatal("正式路径回归阶段无效")
	}
	return stage
}

func loadFormalMetadata(t *testing.T) formalMetadata {
	t.Helper()
	data, err := os.ReadFile("/work/fixture.json")
	if err != nil {
		t.Fatal("读取本地夹具地址失败", err)
	}
	var metadata formalMetadata
	var site struct {
		Site struct {
			Home    string `json:"home"`
			APIHost string `json:"api_host"`
		} `json:"site"`
	}
	if json.Unmarshal(data, &metadata) != nil || json.Unmarshal(data, &site) != nil {
		t.Fatal("本地夹具地址格式无效")
	}
	metadata.Site = web.Site{Home: site.Site.Home, APIHost: site.Site.APIHost}
	for _, address := range []string{metadata.Site.Home, metadata.ModelBaseURL, metadata.PushURL, metadata.ControlURL} {
		if !strings.HasPrefix(address, "http://127.0.0.1:") {
			t.Fatal("本地夹具地址必须是容器回环地址")
		}
	}
	if metadata.ModelKey != "local-formal-key" {
		t.Fatal("本地夹具必须使用合成模型凭据")
	}
	return metadata
}

func formalControl(t *testing.T, action string, fields map[string]any) {
	t.Helper()
	values := map[string]any{}
	for key, value := range fields {
		values[key] = value
	}
	values["action"] = action
	body, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://127.0.0.1:19090/control", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		t.Fatal("切换本地假服务失败", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		t.Fatalf("本地夹具动作 %s 失败：HTTP %d，%s", action, response.StatusCode, data)
	}
}

func formalFixtureState(t *testing.T) map[string]any {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://127.0.0.1:19090/state", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		t.Fatal("读取本地假服务回执失败", err)
	}
	defer response.Body.Close()
	var state map[string]any
	if response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&state) != nil {
		t.Fatal("本地假服务回执格式无效")
	}
	return state
}

func formalReadDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:/data/kakatta.db?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func formalScalar(t *testing.T, query string, args ...any) int64 {
	t.Helper()
	if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(query)), "SELECT ") {
		t.Fatal("正式路径数据库只允许只读核对")
	}
	var value int64
	db := formalReadDB(t)
	defer db.Close()
	if err := db.QueryRowContext(t.Context(), query, args...).Scan(&value); err != nil {
		t.Fatal("只读核对失败", query, err)
	}
	return value
}

func formalText(t *testing.T, query string, args ...any) string {
	t.Helper()
	if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(query)), "SELECT ") {
		t.Fatal("正式路径数据库只允许只读核对")
	}
	var value string
	db := formalReadDB(t)
	defer db.Close()
	if err := db.QueryRowContext(t.Context(), query, args...).Scan(&value); err != nil {
		t.Fatal("只读核对失败", query, err)
	}
	return value
}

func formalRecord(t *testing.T, step int, result string) {
	t.Helper()
	stage := requireFormalPath(t)
	if err := os.MkdirAll("/work/reports", 0700); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(filepath.Join("/work/reports", stage+"-steps.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	err = json.NewEncoder(file).Encode(formalPathStep{Number: step, Result: result})
	if closeErr := file.Close(); err != nil || closeErr != nil {
		t.Fatal("保存正式路径步骤失败", err, closeErr)
	}
	t.Logf("步骤%d：%s", step, result)
}

func formalSnapshot(t *testing.T, browser context.Context, events *webSmokeEvents, name string) {
	t.Helper()
	if filepath.Base(name) != name || name == "." {
		t.Fatal("截图名称无效")
	}
	if err := os.MkdirAll("/work/screens", 0700); err != nil {
		t.Fatal(err)
	}
	writeWebSmokeScreenshot(t, browser, events, filepath.Join("/work/screens", requireFormalPath(t)+"-"+name+".png"))
}

func formalWait(t *testing.T, browser context.Context, what string, limit time.Duration, ready func() bool) {
	t.Helper()
	deadline := time.NewTimer(limit)
	defer deadline.Stop()
	tick := time.NewTicker(150 * time.Millisecond)
	defer tick.Stop()
	for !ready() {
		select {
		case <-browser.Done():
			t.Fatal(what, context.Cause(browser))
		case <-deadline.C:
			t.Fatal("等待正式路径步骤超时：" + what)
		case <-tick.C:
		}
	}
}

func formalReadSteps(t *testing.T, stage string) []formalPathStep {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("/work/reports", stage+"-steps.jsonl"))
	if err != nil && !os.IsNotExist(err) {
		t.Error(err)
	}
	steps := []formalPathStep{}
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var step formalPathStep
		if err := json.Unmarshal(line, &step); err != nil {
			t.Error("步骤记录无效", err)
			continue
		}
		steps = append(steps, step)
	}
	return steps
}

func writeFormalReport(t *testing.T, report formalPathReport) {
	t.Helper()
	if err := os.MkdirAll("/work/reports", 0700); err != nil {
		t.Error(err)
		return
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Error(err)
		return
	}
	if err := os.WriteFile(filepath.Join("/work/reports", report.Stage+".json"), append(data, '\n'), 0600); err != nil {
		t.Error(err)
	}
}

func formalCountFromState(t *testing.T, values map[string]any, key string) int64 {
	t.Helper()
	value, ok := values[key].(float64)
	if !ok {
		t.Fatal(fmt.Sprintf("本地回执缺少计数 %s", key))
	}
	return int64(value)
}
