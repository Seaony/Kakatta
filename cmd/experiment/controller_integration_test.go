package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/browser"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

func experimentController(t *testing.T, account int) *controller {
	t.Helper()
	if runtime.GOOS != "linux" || os.Getenv("KAKATTA_EXPERIMENT_TESTS") != "1" {
		t.Skip("设置 KAKATTA_EXPERIMENT_TESTS=1，在断网 Linux 容器里运行")
	}
	c, err := newController(options{Data: t.TempDir(), Account: account, Keyword: "笔记本", Details: 2, Fixture: true}, testSettings())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func TestControllerLoginThenFreshSingleRun(t *testing.T) {
	c := experimentController(t, 441)
	login, err := observeExperiment(t, c, c.login)
	if err != nil {
		t.Fatal(err)
	}
	assertExperimentReleased(t, c, login)
	if s := c.snapshot(); s.Phase != "completed" || s.NextAllowedAt == "" {
		t.Fatalf("登录结束后必须关闭并开始间隔：%+v", s)
	}
	var loggedIn bool
	for _, e := range c.snapshot().Events {
		if e.Event == "登录完成" && e.Fields["昵称"] == "测试号" {
			loggedIn = true
		}
	}
	if !loggedIn {
		t.Fatal("没有完成登录状态检查和我的主页昵称读取")
	}
	if err := c.run(context.Background()); err == nil || !strings.Contains(err.Error(), "间隔") {
		t.Fatalf("登录后立即运行必须受账号间隔限制：%v", err)
	}
	if len(c.fixture.SearchQueries()) != 0 || c.fixture.DetailHits() != 0 {
		t.Fatal("被间隔拦住后仍访问了搜索或详情")
	}
	advanceExperimentGap(t, c)
	run, err := observeExperiment(t, c, c.run)
	if err != nil {
		t.Fatal(err)
	}
	if run.chrome.PID() == login.chrome.PID() {
		t.Fatal("登录与单次运行复用了同一个浏览器进程")
	}
	assertExperimentReleased(t, c, run)
	if c.fixture.DetailHits() != 2 {
		t.Fatalf("单次运行应只打开两件详情，实际 %d 次", c.fixture.DetailHits())
	}
	var fields, completed int
	for _, e := range c.snapshot().Events {
		switch e.Event {
		case "详情字段":
			if e.Fields["卖家编号"] != "998" || e.Fields["描述"] != "电池 92%，无拆修" {
				t.Fatalf("详情字段未完整经过编排：%+v", e)
			}
			fields++
		case "单次实验流程完成":
			if e.Fields["详情数"] != 2 || e.Fields["失败数"] != 0 {
				t.Fatalf("单次流程没有完整结束：%+v", e)
			}
			completed++
		}
	}
	if fields != 2 || completed != 1 || c.snapshot().Phase != "completed" {
		t.Fatalf("流程次数不符：详情 %d 次，完成 %d 次，阶段 %s", fields, completed, c.snapshot().Phase)
	}
}

func TestControllerLoginWaitsWhenHomeReportsExpiredSession(t *testing.T) {
	c := experimentController(t, 448)
	c.fixture.RequireLoginOnHome()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.login(ctx) }()
	r := waitExperimentResource(t, c, done)
	waitExperiment(t, "等待失效页面上的人工登录", func() bool {
		select {
		case err := <-done:
			t.Fatalf("扫码之前关闭了登录会话：%v", err)
		default:
		}
		if c.snapshot().Phase != "ready" {
			return false
		}
		for _, response := range r.chrome.Capture.DoneAfter(0, browser.Match{URL: "/h5/mtop.fake.login/1.0/"}) {
			if strings.Contains(string(response.Body), "FAIL_SYS_SESSION_EXPIRED") {
				return true
			}
		}
		return false
	})
	if err := r.chrome.Navigate(c.fixture.Home()+"login-box", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	waitExperiment(t, "等待登录框出现", func() bool {
		for _, event := range c.snapshot().Events {
			if event.Event == "登录框已出现" {
				return true
			}
		}
		return false
	})
	button, err := r.runner.H.FirstVisible(ctx, "#finish-login", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.runner.H.Click(ctx, button); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	assertExperimentReleased(t, c, r)
	for _, event := range c.snapshot().Events {
		if event.Event == "登录完成" && event.Fields["昵称"] == "测试号" {
			return
		}
	}
	t.Fatal("扫码后没有读取我的主页昵称")
}

func TestControllerVerificationHoldsUntilManualConfirmation(t *testing.T) {
	c := experimentController(t, 442)
	c.fixture.SetMaskOnSearch(true)
	r, err := observeExperiment(t, c, c.run)
	if !errors.Is(err, web.ErrNeedVerify) {
		t.Fatalf("搜索遮罩应立即停止自动流程：%v", err)
	}
	c.journal.mu.Lock()
	inProgress, pending, ended := c.journal.state.InProgress, c.journal.state.VerifyPending, c.journal.state.EndedAt
	c.journal.mu.Unlock()
	if !inProgress || !pending || !ended.IsZero() || c.snapshot().Phase != "verification" {
		t.Fatalf("验证保留期间不能结算会话：运行中 %v，待验证 %v，结束 %s", inProgress, pending, ended)
	}
	queries := len(c.fixture.SearchQueries())
	if err := c.verify(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	retained := c.res == r && c.cancelJob == nil
	c.mu.Unlock()
	if !retained || len(experimentProcesses(t, c)) < 3 {
		t.Fatal("打开验证画面应保留原来的浏览器、屏幕和远程画面进程")
	}
	if err := c.confirm(context.Background()); !errors.Is(err, web.ErrNeedVerify) {
		t.Fatalf("遮罩仍在时不能确认成功：%v", err)
	}
	if len(c.fixture.SearchQueries()) != queries || c.fixture.DetailHits() != 0 {
		t.Fatal("风控停止后自动流程继续访问了页面")
	}
	// 在原浏览器打开本机正常页，模拟用户手动处理完验证。
	c.fixture.SetMaskOnSearch(false)
	if err := r.chrome.Navigate(c.fixture.Home(), 5*time.Second); err != nil {
		t.Fatal(err)
	}
	mark := r.chrome.Capture.Mark()
	if err := c.confirm(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.chrome.Capture.Mark() != mark {
		t.Fatal("人工确认不应再发起页面请求")
	}
	assertExperimentReleased(t, c, r)
	c.journal.mu.Lock()
	pending = c.journal.state.VerifyPending
	cooldown, manual := c.journal.state.CooldownUntil, c.journal.state.ManualUsed
	now := c.journal.effectiveNow()
	c.journal.mu.Unlock()
	if pending || manual || !cooldown.After(now) || c.snapshot().Phase != "completed" {
		t.Fatalf("验证完成应关闭会话并进入冷却：待验证 %v，人工额度 %v，冷却 %s", pending, manual, cooldown)
	}
	if c.fixture.DetailHits() != 0 || len(c.fixture.SearchQueries()) != queries {
		t.Fatal("人工确认后不应续跑原来的自动流程")
	}
}

func TestControllerExpiredVerificationReturnsToLogin(t *testing.T) {
	c := experimentController(t, 445)
	c.fixture.SetMaskOnSearch(true)
	r, err := observeExperiment(t, c, c.run)
	if !errors.Is(err, web.ErrNeedVerify) {
		t.Fatal(err)
	}
	if err := r.chrome.Navigate(c.fixture.Home()+"mini_login", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := c.confirm(context.Background()); !errors.Is(err, web.ErrLoginExpired) {
		t.Fatalf("登录失效不能被确认成验证成功：%v", err)
	}
	assertExperimentReleased(t, c, r)
	if c.snapshot().VerifyPending {
		t.Fatal("登录失效后仍残留待验证，挡住重新扫码")
	}
	advanceExperimentGap(t, c)
	c.fixture.SetMaskOnSearch(false)
	login, err := observeExperiment(t, c, c.login)
	if err != nil {
		t.Fatal(err)
	}
	assertExperimentReleased(t, c, login)
	if c.snapshot().VerifyPending {
		t.Fatal("重新登录后仍被旧待验证状态挡住")
	}
}

func TestControllerManualDenialClosesBrowserAndBlocksReentry(t *testing.T) {
	for i, mode := range []string{"status", "confirm"} {
		t.Run(mode, func(t *testing.T) {
			c := experimentController(t, 445+i)
			c.fixture.SetMaskOnSearch(true)
			r, err := observeExperiment(t, c, c.run)
			if !errors.Is(err, web.ErrNeedVerify) {
				t.Fatalf("应先进入人工验证：%v", err)
			}
			if err := r.chrome.Navigate(c.fixture.Home()+"punish?action=deny", 5*time.Second); err != nil {
				t.Fatal(err)
			}
			c.journal.mu.Lock()
			stoppedAt := c.journal.effectiveNow()
			c.journal.now = func() time.Time { return stoppedAt }
			c.journal.mu.Unlock()
			if mode == "status" {
				a := &access{used: true, session: "local-test", sessionExpires: time.Now().Add(time.Minute)}
				req := httptest.NewRequest("GET", "http://local/status", nil)
				req.AddCookie(&http.Cookie{Name: "kakatta_experiment", Value: a.session})
				response := httptest.NewRecorder()
				experimentHandler(c, a).ServeHTTP(response, req)
				var state status
				if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil || response.Code != http.StatusOK || state.Phase != "failed" || !strings.Contains(state.Error, web.ErrDenied.Error()) {
					t.Fatalf("状态查询没有发现人工阶段的直接拒绝：%d %s，%v", response.Code, response.Body, err)
				}
			} else if err := c.confirm(context.Background()); !errors.Is(err, web.ErrDenied) {
				t.Fatalf("人工确认没有发现新的直接拒绝：%v", err)
			}
			assertExperimentReleased(t, c, r)
			data, err := os.ReadFile(c.journal.path)
			if err != nil {
				t.Fatal(err)
			}
			var persisted visitState
			if err := json.Unmarshal(data, &persisted); err != nil {
				t.Fatal(err)
			}
			if !persisted.DeniedUntil.Equal(stoppedAt.Add(24*time.Hour)) || persisted.InProgress || !persisted.EndedAt.Equal(stoppedAt) {
				t.Fatalf("直接拒绝没有持久保存 24 小时暂停并结算会话：%+v", persisted)
			}
			advanceExperimentGap(t, c)
			if err := c.verify(context.Background()); err == nil || !strings.Contains(err.Error(), "没有待处理") {
				t.Fatalf("拒绝替换待验证后仍能重开旧验证：%v", err)
			}
			if err := c.login(context.Background()); err == nil || !strings.Contains(err.Error(), "拒绝访问") {
				t.Fatalf("账号间隔结束后登录绕过了拒绝暂停：%v", err)
			}
			assertExperimentReleased(t, c, r)
		})
	}
}

func TestControllerStartupFailureSettlesGapWithoutClosingOtherDisplay(t *testing.T) {
	c := experimentController(t, 443)
	occupied, err := browser.StartDisplay(context.Background(), c.opts.Account)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { occupied.Close() })
	if err := c.run(context.Background()); err == nil || !strings.Contains(err.Error(), "占用") {
		t.Fatalf("已有屏幕应使启动失败：%v", err)
	}
	assertExperimentSettled(t, c)
	if _, err := os.Stat(fmt.Sprintf("/proc/%d", occupied.PID())); err != nil {
		t.Fatalf("启动失败关掉了已有屏幕：%v", err)
	}
	if err := occupied.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.run(context.Background()); err == nil || !strings.Contains(err.Error(), "间隔") {
		t.Fatalf("启动失败也应记录结束时间和间隔：%v", err)
	}
	advanceExperimentGap(t, c)
	r, err := observeExperiment(t, c, c.run)
	if err != nil {
		t.Fatalf("间隔后应可重新启动，不应残留运行中状态：%v", err)
	}
	assertExperimentReleased(t, c, r)
}

func TestControllerCloseCancelsRunningFlowAndReleasesResources(t *testing.T) {
	c := experimentController(t, 444)
	c.fixture.SetFinalFilterDelay(time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.run(ctx) }()
	r := waitExperimentResource(t, c, done)
	waitExperiment(t, "等待筛选请求在途中", func() bool {
		for _, q := range c.fixture.SearchQueries() {
			if strings.Contains(q, "final=1") {
				return true
			}
		}
		return false
	})
	closed := make(chan struct{})
	go func() { c.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("关闭没有及时取消运行并等到资源退出")
	}
	if err := <-done; err == nil {
		t.Fatal("未完成的流程被关闭后不能报告成功")
	}
	assertExperimentReleased(t, c, r)
	if c.fixture.DetailHits() != 0 {
		t.Fatal("关闭后仍继续打开详情")
	}
	if err := c.run(context.Background()); err == nil || !strings.Contains(err.Error(), "已关闭") {
		t.Fatalf("已关闭的控制器不应再接受运行：%v", err)
	}
	c.Close()
}

func observeExperiment(t *testing.T, c *controller, run func(context.Context) error) (*resources, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx) }()
	r := waitExperimentResource(t, c, done)
	return r, <-done
}

func waitExperimentResource(t *testing.T, c *controller, done <-chan error) *resources {
	t.Helper()
	var r *resources
	waitExperiment(t, "等待浏览器资源启动", func() bool {
		c.mu.Lock()
		r = c.res
		c.mu.Unlock()
		if r == nil {
			select {
			case err := <-done:
				t.Fatalf("启动后未观察到活动资源：%v；状态 %+v", err, c.snapshot())
			default:
			}
		}
		return r != nil
	})
	if r.chrome.PID() <= 0 || r.display.PID() <= 0 {
		t.Fatal("没有真实启动浏览器和屏幕进程")
	}
	return r
}

func advanceExperimentGap(t *testing.T, c *controller) {
	t.Helper()
	c.journal.mu.Lock()
	defer c.journal.mu.Unlock()
	next := c.journal.state.EndedAt.Add(time.Duration(c.journal.state.GapSeconds)*time.Second + time.Second)
	c.journal.now = func() time.Time { return next }
	if _, err := c.journal.checkpoint(); err != nil {
		t.Fatal(err)
	}
}

func assertExperimentSettled(t *testing.T, c *controller) {
	t.Helper()
	c.mu.Lock()
	active := c.res != nil || c.cancelJob != nil || c.closing
	c.mu.Unlock()
	c.journal.mu.Lock()
	inProgress, ended := c.journal.state.InProgress, c.journal.state.EndedAt
	c.journal.mu.Unlock()
	if active || inProgress || ended.IsZero() {
		t.Fatalf("会话没有完整结算：资源活动 %v，运行中 %v，结束时间 %s", active, inProgress, ended)
	}
}

func assertExperimentReleased(t *testing.T, c *controller, r *resources) {
	t.Helper()
	assertExperimentSettled(t, c)
	waitExperiment(t, "等待账号全部进程退出", func() bool { return len(experimentProcesses(t, c)) == 0 })
	for _, pid := range []int{r.chrome.PID(), r.display.PID()} {
		if _, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); !os.IsNotExist(err) {
			t.Fatalf("进程 %d 仍然存在：%v", pid, err)
		}
	}
	for _, address := range []string{r.remote.Address(), fmt.Sprintf("127.0.0.1:%d", 20000+c.opts.Account)} {
		if conn, err := net.DialTimeout("tcp4", address, 100*time.Millisecond); err == nil {
			conn.Close()
			t.Fatalf("账号进程退出后端口仍可连接：%s", address)
		}
	}
	if conn, err := net.DialTimeout("unix", "/tmp/.X11-unix/X"+strconv.Itoa(100+c.opts.Account), 100*time.Millisecond); err == nil {
		conn.Close()
		t.Fatal("虚拟屏幕退出后仍可连接")
	}
}

func experimentProcesses(t *testing.T, c *controller) []int {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Fatal(err)
	}
	display := ":" + strconv.Itoa(100+c.opts.Account)
	profile := "--user-data-dir=" + filepath.Join(c.opts.Data, "browser-fixture", strconv.Itoa(c.opts.Account))
	var found []int
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		args, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if err != nil {
			continue
		}
		for _, arg := range strings.Split(string(args), "\x00") {
			if arg == display || arg == profile {
				found = append(found, pid)
				break
			}
		}
	}
	return found
}

func waitExperiment(t *testing.T, what string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatal(what + "超时")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
