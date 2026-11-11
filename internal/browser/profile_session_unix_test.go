//go:build !windows

package browser

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

func TestPrepareProfileOccupiedDoesNotChangeSessionData(t *testing.T) {
	const helper = "KAKATTA_PROFILE_OCCUPIED_HELPER"
	if os.Getenv(helper) == "1" {
		fmt.Println("ready")
		io.Copy(io.Discard, os.Stdin)
		return
	}
	dir := t.TempDir()
	preferences := []byte(`{"session":{"restore_on_startup":5},"keep":true}`)
	writeProfileFixture(t, dir, "Default/Preferences", preferences)
	writeProfileFixture(t, dir, "Default/Sessions/Session_local", []byte("原有恢复日志"))
	writeProfileFixture(t, dir, "Default/Sessions_Encrypted/Session_local", []byte("原有恢复日志"))
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestPrepareProfileOccupiedDoesNotChangeSessionData$", "--", "--user-data-dir="+dir)
	cmd.Env = append(os.Environ(), helper+"=1")
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { in.Close(); cmd.Wait() })
	ready := make(chan string, 1)
	go func() { line, _ := bufio.NewReader(out).ReadString('\n'); ready <- line }()
	select {
	case line := <-ready:
		if line != "ready\n" {
			t.Fatal("占用检查进程没有就绪", line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("占用检查进程启动超时")
	}
	if err := prepareProfile(dir); err == nil || !strings.Contains(err.Error(), "还在运行") {
		t.Fatal("占用中的配置不能修改或启动", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "Default", "Preferences"))
	if err != nil || !bytes.Equal(data, preferences) {
		t.Fatal("占用检查前就修改了配置", err)
	}
	for _, sub := range []string{"Sessions", "Sessions_Encrypted"} {
		data, err := os.ReadFile(filepath.Join(dir, "Default", sub, "Session_local"))
		if err != nil || string(data) != "原有恢复日志" {
			t.Fatal("占用检查前就清理了恢复日志", sub, err)
		}
	}
}

func TestSessionCookieSurvivesRestartsWithoutRestoringPages(t *testing.T) {
	requireBrowser(t)
	if _, err := FindChrome(); err != nil {
		t.Skip("本机没有 Chrome：", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path == "/login" {
			http.SetCookie(w, &http.Cookie{Name: "session_probe", Value: "local-fixture", Path: "/", HttpOnly: true})
		}
		fmt.Fprint(w, `<html><head><title>本地会话测试</title><link rel="icon" href="data:,"></head><body>本地页</body></html>`)
	}))
	defer srv.Close()
	dir := t.TempDir()
	start := func() *Session {
		t.Helper()
		s, err := Start(ctx, Options{ProfileDir: dir, AccountID: 543})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(s.Close)
		return s
	}
	s := start()
	if err := s.Navigate(srv.URL+"/login", 15*time.Second); err != nil {
		t.Fatal(err)
	}
	if requests.Load() == 0 {
		t.Fatal("请求计数器没有观察到主动访问")
	}
	assertSessionProbeCookie(t, ctx, s, srv.URL)
	s.Close()
	for restart := 1; restart <= 2; restart++ {
		before := requests.Load()
		s = start()
		assertBlankSessionRestart(t, ctx, s, &requests, before)
		assertSessionProbeCookie(t, ctx, s, srv.URL)
		if err := s.Navigate(srv.URL+"/old", 15*time.Second); err != nil {
			t.Fatal(err)
		}
		if requests.Load() <= before {
			t.Fatal("旧页面计数器没有观察到主动访问")
		}
		if restart == 1 {
			s.Close()
		}
	}
	deadline := time.Now().Add(20 * time.Second)
	for !hasRecoverySnapshot(t, dir) {
		if time.Now().After(deadline) {
			t.Fatal("强杀前没有生成旧页面恢复日志")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := syscall.Kill(-s.PID(), syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if !hasRecoverySnapshot(t, dir) {
		t.Fatal("强杀后没有保留待清理的恢复日志")
	}
	before := requests.Load()
	s = start()
	assertBlankSessionRestart(t, ctx, s, &requests, before)
	assertSessionProbeCookie(t, ctx, s, srv.URL)
	s.Close()
}

func hasRecoverySnapshot(t *testing.T, dir string) bool {
	t.Helper()
	for _, sub := range []string{"Sessions", "Sessions_Encrypted"} {
		entries, err := os.ReadDir(filepath.Join(dir, "Default", sub))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "Session_") {
				info, err := entry.Info()
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode().IsRegular() && info.Size() > 0 {
					return true
				}
			}
		}
	}
	return false
}

func assertSessionProbeCookie(t *testing.T, ctx context.Context, s *Session, url string) {
	t.Helper()
	if present, err := s.HasCookieContext(ctx, []string{url}, "session_probe"); err != nil || !present {
		t.Fatalf("重启后会话 Cookie 必须仍在：存在=%v，错误=%v", present, err)
	}
	metadata, err := s.CookieMetadata(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, cookie := range metadata {
		if cookie.Name == "session_probe" {
			if !cookie.Session || cookie.Expires != -1 || cookie.Domain != "127.0.0.1" {
				t.Fatal("测试必须使用真正的会话 Cookie", cookie)
			}
			return
		}
	}
	t.Fatal("会话 Cookie 元数据没有找到")
}

func assertBlankSessionRestart(t *testing.T, ctx context.Context, s *Session, requests *atomic.Int64, before int64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		result, err := chromedp.CallBrowser(s.ctx, target.GetTargets, target.GetTargetsParams{})
		if err != nil {
			t.Fatal(err)
		}
		pages := 0
		for _, info := range result.TargetInfos {
			if info.Type == "page" {
				pages++
				if info.URL != "about:blank" {
					t.Fatal("启动时恢复了旧页面", info.URL)
				}
			}
		}
		if pages != 1 || requests.Load() != before {
			t.Fatalf("启动后只能有一个空白页且不能访问旧页面：页面=%d，新增请求=%d", pages, requests.Load()-before)
		}
		if !time.Now().Before(deadline) {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}
