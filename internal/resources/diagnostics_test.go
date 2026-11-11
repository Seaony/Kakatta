package resources

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Seaony/Kakatta/internal/browser"
	"github.com/Seaony/Kakatta/internal/quota"
	"github.com/Seaony/Kakatta/internal/remote"
	"github.com/Seaony/Kakatta/internal/xianyu"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

type countedDiagnosticBudget struct{ visits int }

func (b *countedDiagnosticBudget) Acquire(quota.Kind, string) error { b.visits++; return nil }

func TestFormalDiagnosticsPersistFailureSceneAndProcessLogs(t *testing.T) {
	f, _, spec := testFactory(t)
	spec.DiagnosticsDir = filepath.Join(t.TempDir(), "logs")
	spec.DiagnosticSettings = map[string]any{"hourly_visits": 30}
	var received []string
	spec.Log = func(event string, _ map[string]any) { received = append(received, event) }
	f.startDisplay = func(_ context.Context, _ int, output io.Writer) (screen, error) {
		_, err := io.WriteString(output, "本地屏幕就绪\n")
		return &testScreen{calls: &calls{}}, err
	}
	f.startChrome = func(_ context.Context, options browser.Options) (*browser.Session, error) {
		_, err := io.WriteString(options.Output, "Cookie: fixture-cookie-private\n")
		return &browser.Session{Capture: &browser.Capture{}}, err
	}
	f.startRemote = func(_ context.Context, _ string, _ func(context.Context) ([]byte, error), output io.Writer) (*remote.Session, error) {
		_, err := io.WriteString(output, "本地远程画面就绪\n")
		return &remote.Session{}, err
	}
	f.chromeVersion = func(context.Context, *browser.Session) (string, error) { return "Chrome/157.test", nil }
	f.readPage = func(context.Context, *browser.Session) (browser.DiagnosticPage, error) {
		return browser.DiagnosticPage{
			Screenshot: []byte("本地截图"),
			HTML:       `<html><head><title>本地失败页面</title></head><body><input name="password" value="fixture-html-private"><p>商品描述保持原样</p></body></html>`,
			URL:        "http://127.0.0.1/item?id=7&token=fixture-url-private", Title: "本地失败页面",
		}, nil
	}
	r, err := f.Open(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.OpenRemote(t.Context()); err != nil {
		t.Fatal(err)
	}
	r.Runner().Log("运行阶段", map[string]any{"阶段": "search", "状态": "started", "毫秒": 0})
	response := &browser.Response{URL: "http://127.0.0.1/h5/mtop.local?id=7&token=fixture-api-private", Body: []byte(`{"ret":["SUCCESS"],"data":{"phone":"13812345678","resultListRenamed":[],"title":"商品描述保持原样"}}`)}
	if err := r.Runner().ReportFailure(t.Context(), "解析搜索", xianyu.ErrSearchStructure, response); !errors.Is(err, xianyu.ErrSearchStructure) || errors.Is(err, web.ErrDiagnostics) {
		t.Fatalf("取证改变了原来的解析失败：%v", err)
	}
	r.Runner().Log("运行摘要", map[string]any{"run_id": 7, "item_count": 0, "原因": "搜索列表字段缺失"})
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	files := map[string][]byte{}
	if err := filepath.WalkDir(spec.DiagnosticsDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			files[path] = data
			for _, secret := range []string{"fixture-cookie-private", "fixture-html-private", "fixture-url-private", "fixture-api-private", "13812345678", "user:password@"} {
				if strings.Contains(string(data), secret) {
					t.Errorf("诊断文件含敏感测试值：%s", filepath.Base(path))
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"chrome.log", "xvfb.log", "x11vnc.log"} {
		if len(files[filepath.Join(spec.DiagnosticsDir, name)]) == 0 {
			t.Errorf("缺少进程输出：%s", name)
		}
	}
	for _, pattern := range []string{"bundles/*/page.json", "bundles/*/page.html", "bundles/*/screenshot.jpg", "bundles/*/response.json", "bundles/*/events.json", "summaries/*/summary.json"} {
		matches, err := filepath.Glob(filepath.Join(spec.DiagnosticsDir, pattern))
		if err != nil || len(matches) != 1 {
			t.Fatalf("诊断内容不完整：%s，%v", pattern, err)
		}
		if strings.HasSuffix(pattern, "response.json") && !strings.Contains(string(files[matches[0]]), "resultListRenamed") {
			t.Fatal("没有保存触发失败的那一条响应")
		}
		if strings.HasSuffix(pattern, "summary.json") {
			var summary map[string]any
			if json.Unmarshal(files[matches[0]], &summary) != nil || len(summary["steps"].([]any)) != 1 || len(summary["bundles"].([]any)) != 1 || summary["error"] != "搜索列表字段缺失" {
				t.Fatal("摘要缺阶段、诊断包或最终错误")
			}
		}
	}
	if len(received) != 3 {
		t.Fatalf("落盘包装没有保留原运行日志：%v", received)
	}
	joined := ""
	for _, data := range files {
		joined += string(data)
	}
	for _, value := range []string{"Chrome/157.test", "rule_version", "Asia/Shanghai", "proxy_environment_present", "hourly_visits", "商品描述保持原样"} {
		if !strings.Contains(joined, value) {
			t.Errorf("诊断资料缺少必要信息：%s", value)
		}
	}
}

func TestFormalDiagnosticsKeepPartialPageWhenCaptureFails(t *testing.T) {
	f, _, spec := testFactory(t)
	spec.DiagnosticsDir = filepath.Join(t.TempDir(), "logs")
	f.chromeVersion = func(context.Context, *browser.Session) (string, error) { return "Chrome/test", nil }
	f.readPage = func(context.Context, *browser.Session) (browser.DiagnosticPage, error) {
		return browser.DiagnosticPage{HTML: "<title>保留部分现场</title>", Errors: map[string]string{"screenshot": "连接已断开"}}, errors.New("截图失败")
	}
	r, err := f.Open(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Runner().ReportFailure(t.Context(), "查找搜索框", browser.ErrNotFound, nil); !errors.Is(err, browser.ErrNotFound) || errors.Is(err, web.ErrDiagnostics) {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	metadata, _ := filepath.Glob(filepath.Join(spec.DiagnosticsDir, "bundles", "*", "page.json"))
	if len(metadata) != 1 {
		t.Fatal("没有保留部分现场")
	}
	data, err := os.ReadFile(metadata[0])
	if err != nil || !strings.Contains(string(data), "连接已断开") {
		t.Fatal("没有记录无法截图的原因")
	}
}

func TestFormalDiagnosticsWriteFailureStopsBeforeNextVisit(t *testing.T) {
	f, _, spec := testFactory(t)
	spec.DiagnosticsDir = filepath.Join(t.TempDir(), "logs")
	budget := &countedDiagnosticBudget{}
	spec.Budget = budget
	f.chromeVersion = func(context.Context, *browser.Session) (string, error) { return "Chrome/test", nil }
	var browserLife context.Context
	f.startChrome = func(ctx context.Context, _ browser.Options) (*browser.Session, error) {
		browserLife = ctx
		return &browser.Session{Capture: &browser.Capture{}}, nil
	}
	r, err := f.Open(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(spec.DiagnosticsDir, spec.DiagnosticsDir+"-saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(spec.DiagnosticsDir, []byte("阻止继续写日志"), 0600); err != nil {
		t.Fatal(err)
	}
	r.Runner().Log("等待", map[string]any{"毫秒": 1})
	if browserLife.Err() == nil {
		t.Fatal("日志写失败后浏览器寿命仍在继续")
	}
	if err := r.Runner().Budget.Acquire(quota.Search, "再次打开页面"); !errors.Is(err, web.ErrDiagnostics) || budget.visits != 0 {
		t.Fatalf("写盘失败仍放行访问：%v，%d", err, budget.visits)
	}
	if err := r.Close(); err == nil {
		t.Fatal("关闭时没有返回明确的诊断写盘失败")
	}
}
