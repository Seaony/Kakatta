package main

import (
	"context"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

func TestWebSmokeFailureStateDeadline(t *testing.T) {
	if os.Getenv("KAKATTA_WEB_SMOKE") != "1" {
		t.Skip("用断网网页测试容器运行")
	}
	interfaces, err := net.Interfaces()
	if err != nil || len(interfaces) != 1 || interfaces[0].Flags&net.FlagLoopback == 0 {
		t.Fatal("只允许断网回环测试")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	allocator, stopAllocator := chromedp.NewExecAllocator(ctx, append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath("/usr/bin/google-chrome-stable"), chromedp.UserDataDir(t.TempDir()))...)
	browser, stopBrowser := chromedp.NewContext(allocator)
	defer func() { cancel(); stopBrowser(); stopAllocator() }()
	entered := make(chan struct{}, 1)
	if err := chromedp.Listen(browser, func(event any) {
		if e, ok := event.(*runtime.EventConsoleAPICalled); ok {
			for _, arg := range e.Args {
				if string(arg.Value) == `"deadline-test-entered"` {
					select {
					case entered <- struct{}{}:
					default:
					}
				}
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := chromedp.Run(browser, chromedp.Navigate("data:text/html,<title>本地截止测试</title>")); err != nil {
		t.Fatal(err)
	}
	busy, stopBusy := context.WithCancel(browser)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = chromedp.Run(busy, chromedp.Evaluate[chromedp.Void](`console.log('deadline-test-entered'); for (;;) {}`))
	}()
	defer func() { stopBusy(); <-done }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("页面没有进入本地忙循环", ctx.Err())
	}
	before := time.Now()
	_, err = webSmokeFailureState(browser)
	elapsed := time.Since(before)
	if !errors.Is(err, context.DeadlineExceeded) || elapsed > 2*time.Second {
		t.Fatal("忙页面的失败取证没有独立短截止", err, elapsed)
	}
	t.Log("忙页面的取证已按独立截止返回", elapsed)
}
