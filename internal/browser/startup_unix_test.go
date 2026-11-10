//go:build !windows

package browser

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func TestStartupFailureCleanupReturnsPromptly(t *testing.T) {
	const helper = "KAKATTA_BROWSER_STARTUP_FAILURE"
	if phase := os.Getenv(helper); phase != "" {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if phase == "before-allocation" {
			cancel()
		}
		allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, chromedp.ExecPath(filepath.Join(t.TempDir(), "missing-chrome")))
		tabCtx, cancelTab := chromedp.NewContext(allocCtx)
		allocatorClosed := false
		s := &Session{ctx: tabCtx, cancelTab: cancelTab, cancelAlloc: func() {
			cancelAlloc()
			allocatorClosed = true
		}}
		if err := chromedp.Listen(tabCtx, func(any) {}); err == nil {
			t.Fatal("没有启动浏览器却监听成功")
		} else if phase == "before-allocation" && !errors.Is(err, context.Canceled) {
			t.Fatalf("没有保留启动取消原因：%v", err)
		}
		if chromedp.FromContext(tabCtx).Browser != nil {
			t.Fatal("测试不应分配浏览器")
		}
		s.Close()
		s.Close()
		if !allocatorClosed || s.PID() != 0 {
			t.Fatal("启动失败后没有完成分配器清理")
		}
		return
	}
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"before-allocation", "missing-executable"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, path, "-test.run=^TestStartupFailureCleanupReturnsPromptly$", "-test.timeout=2s")
			cmd.Env = append(os.Environ(), helper+"="+phase)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("启动失败的清理没有及时结束：%v\n%s", err, out)
			}
		})
	}
}

func TestCancelDuringChromeStartupReturnsPromptly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "slow-chrome")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nsleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if s, err := Start(ctx, Options{ExecPath: path, ProfileDir: t.TempDir()}); err == nil {
		s.Close()
		t.Fatal("卡住的浏览器不应该启动成功")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("父上下文取消后仍在等浏览器启动")
	}
}
