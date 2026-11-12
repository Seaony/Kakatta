//go:build kakatta_dev

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	modelservice "github.com/Seaony/Kakatta/internal/analysis/service"
	"github.com/Seaony/Kakatta/internal/browser"
	"github.com/Seaony/Kakatta/internal/devfixture"
	"github.com/Seaony/Kakatta/internal/notification"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/log"
	"github.com/chromedp/cdproto/network"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/remote"
)

type developmentOnboardingLog struct{ ready chan string }

func (output developmentOnboardingLog) Write(data []byte) (int, error) {
	var entry struct {
		Message string `json:"msg"`
		Listen  string `json:"listen"`
	}
	if json.Unmarshal(data, &entry) == nil && entry.Message == "服务已启动" {
		select {
		case output.ready <- "http://" + entry.Listen:
		default:
		}
	}
	return len(data), nil
}

func TestDevelopmentFakeSeedOpensOverviewAtRealEntry(t *testing.T) {
	if runtime.GOOS != "darwin" || os.Getenv("KAKATTA_DEV_ONBOARDING_BROWSER_TEST") != "1" {
		t.Skip("本机假数据根入口浏览器验收需显式开启")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	baseDir := filepath.Join(root, "build", "_dev")
	if err := os.MkdirAll(baseDir, 0700); err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp(baseDir, "onboarding-browser.")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	data := filepath.Join(directory, "data")
	seed := exec.CommandContext(ctx, "go", "run", "./cmd/seed", "--data", data)
	seed.Dir = root
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "KAKATTA_SECRET_KEY=") && !strings.HasPrefix(entry, "KAKATTA_SECRET_KEY_FILE=") && !strings.HasPrefix(entry, "KAKATTA_TRUSTED_PROXIES=") {
			seed.Env = append(seed.Env, entry)
		}
	}
	seed.Env = append(seed.Env, "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local")
	if output, err := seed.CombinedOutput(); err != nil {
		t.Fatalf("实际样例入口失败：%v %s", err, output)
	}
	controlListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fixture := devfixture.New(ctx, "http://"+controlListener.Addr().String())
	defer fixture.Close()
	controlServer := &http.Server{Handler: fixture.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go controlServer.Serve(controlListener)
	defer controlServer.Close()
	fixture.EnableDevelopmentAutoLogin()
	metadataPath, controlPath := filepath.Join(directory, "fixture.json"), filepath.Join(directory, "control-token")
	metadata, err := json.Marshal(fixture.Metadata())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metadataPath, metadata, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(controlPath, []byte(strings.Repeat("local-test-", 4)), 0600); err != nil {
		t.Fatal(err)
	}
	serverCtx, stopServer := context.WithCancel(ctx)
	ready, stopped := make(chan string, 1), make(chan struct{})
	lookup := func(key string) (string, bool) {
		switch key {
		case "KAKATTA_SECRET_KEY", "KAKATTA_SECRET_KEY_FILE", "KAKATTA_TRUSTED_PROXIES":
			return "", false
		default:
			return os.LookupEnv(key)
		}
	}
	var serverError error
	go func() {
		defer close(stopped)
		handled, err := runDevelopment(serverCtx, []string{"dev", "--mode", "fake", "--data", data, "--listen", "127.0.0.1:0", "--fixture", metadataPath, "--control-token-file", controlPath}, developmentOnboardingLog{ready: ready}, lookup)
		if !handled {
			err = errors.Join(err, errors.New("开发命令没有进入实际假数据入口"))
		}
		serverError = err
	}()
	defer func() {
		stopServer()
		select {
		case <-stopped:
			if serverError != nil {
				t.Error("临时假数据服务退出失败", serverError)
			}
		case <-time.After(30 * time.Second):
			t.Error("临时假数据服务没有回收")
		}
	}()
	var base string
	select {
	case base = <-ready:
	case <-stopped:
		t.Fatal("临时假数据入口没有启动", serverError)
	case <-ctx.Done():
		t.Fatal("等待临时假数据入口超时", ctx.Err())
	}
	client := &http.Client{Timeout: 5 * time.Second}
	read := func(path string, target any) {
		t.Helper()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal("本地假数据入口请求失败", path, err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatal("本地假数据入口没有完成准备", path, response.StatusCode)
		}
		if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(target); err != nil {
			t.Fatal("本地假数据入口响应无效", path, err)
		}
	}
	var snapshot settings.Snapshot
	read("/api/v1/settings", &snapshot)
	if string(snapshot.Values["general.onboarding_done"]) != "true" || snapshot.Sources["general.onboarding_done"] != settings.SourceStored {
		t.Fatal("实际入口没有保存假数据完成状态")
	}
	var accounts struct{ Items []service.AccountView }
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		read("/api/v1/accounts", &accounts)
		if len(accounts.Items) == 1 && accounts.Items[0].State == "active" && accounts.Items[0].Session == nil {
			break
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("假账号没有通过正常假页登录流程", ctx.Err())
		}
	}
	var providers []modelservice.ProviderView
	var channels []notification.Channel
	var tasks struct{ Items []service.Task }
	var findings struct{ Total int }
	var hidden struct{ Total int }
	read("/api/v1/ai/providers", &providers)
	read("/api/v1/channels", &channels)
	read("/api/v1/tasks", &tasks)
	read("/api/v1/findings?view=all", &findings)
	read("/api/v1/findings?view=hidden", &hidden)
	if len(providers) != 1 || !providers[0].Enabled || providers[0].ErrorState != nil || providers[0].BaseURL != fixture.Metadata().ModelBaseURL || len(channels) != 1 || !channels[0].Enabled || len(tasks.Items) != 3 || findings.Total+hidden.Total != 9 {
		t.Fatalf("假数据入口的模型、渠道或通用样例不完整：模型%d、渠道%d、任务%d、可见结果%d、隐藏结果%d", len(providers), len(channels), len(tasks.Items), findings.Total, hidden.Total)
	}
	var runnableSample bool
	for _, task := range tasks.Items {
		if task.Enabled {
			t.Fatal("准备样例不得自动开启任务")
		}
		if task.Name == "示例 · MacBook监控" {
			runnableSample = task.AccountID != nil && *task.AccountID == accounts.Items[0].ID && task.AccountPinned && len(task.ChannelIDs) == 1 && task.ChannelIDs[0] == channels[0].ID
		}
	}
	if !runnableSample {
		t.Fatal("开发样例任务没有绑定实际假账号与推送渠道")
	}
	chromePath, err := browser.FindChrome()
	if err != nil {
		t.Fatal(err)
	}
	options := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	options = append(options, chromedp.ExecPath(chromePath), chromedp.Headless, chromedp.UserDataDir(filepath.Join(directory, "observer")), chromedp.WindowSize(1280, 900), chromedp.Flag("remote-debugging-port", "0"), remote.WebSocket)
	allocator, stopAllocator := chromedp.NewExecAllocator(ctx, options...)
	defer stopAllocator()
	observer, stopObserver := chromedp.NewContext(allocator)
	defer stopObserver()
	if _, err := chromedp.Run(observer, chromedp.Navigate("about:blank")); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(observer, 5*time.Second)
		defer cancel()
		if err := chromedp.Cancel(cleanup); err != nil {
			t.Error("无头观察浏览器关闭失败", err)
		}
	}()
	events := &webSmokeEvents{base: base, page: "假数据开发根入口", readOnly: true}
	if err := chromedp.Listen(observer, events.receive); err != nil {
		t.Fatal(err)
	}
	if _, err := chromedp.Call(observer, cdpruntime.Enable, cdp.Empty{}); err != nil {
		t.Fatal(err)
	}
	if _, err := chromedp.Call(observer, log.Enable, cdp.Empty{}); err != nil {
		t.Fatal(err)
	}
	if _, err := chromedp.Call(observer, network.Enable, network.EnableParams{}); err != nil {
		t.Fatal(err)
	}
	if _, err := chromedp.Run(observer, chromedp.Navigate(base+"/")); err != nil {
		t.Fatal(err)
	}
	waitWebSmoke(t, observer, events, `() => location.pathname === '/overview' && !!document.querySelector('main h1') && !document.querySelector('main input[name=password]')`)
	events.mu.Lock()
	observedRequests := !events.lastActivity.IsZero()
	events.mu.Unlock()
	if !observedRequests {
		t.Fatal("浏览器验收没有观察到实际网络请求")
	}
	shot, err := chromedp.Run(observer, chromedp.CaptureScreenshot())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "build", "dev-onboarding-overview-headless.png"), shot, 0600); err != nil {
		t.Fatal(err)
	}
	state := fixture.State()
	if len(state["pushes"].([]map[string]any)) != 0 || state["search_count"].(int) != 0 || state["model_counts"].(map[string]int64)["analysis"] != 0 {
		t.Fatal("打开样例总览不应自动搜索、分析或推送")
	}
	t.Log("实际 seed→假数据开发入口→无头浏览器根路由已进入总览；账号/模型/渠道/任务关联齐全，未触发运行")
}
