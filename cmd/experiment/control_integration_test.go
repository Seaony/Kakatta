package main

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/browser"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/remote"
)

func TestExperimentDirectControlRunsFakePageAndKeepsProtection(t *testing.T) {
	c := experimentController(t, 550)
	c.fixture.SetMaskOnSearch(true)
	server := httptest.NewServer(experimentHandler(c))
	defer server.Close()
	chromePath, err := browser.FindChrome()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	options := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	options = append(options, chromedp.ExecPath(chromePath), chromedp.Headless, chromedp.UserDataDir(filepath.Join(t.TempDir(), "observer")), chromedp.WindowSize(1280, 900), chromedp.Flag("remote-debugging-port", "0"), remote.WebSocket)
	allocator, stopAllocator := chromedp.NewExecAllocator(ctx, options...)
	defer stopAllocator()
	observer, stopObserver := chromedp.NewContext(allocator)
	defer stopObserver()
	if _, err := chromedp.Run(observer, chromedp.Navigate(server.URL)); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(observer, 5*time.Second)
		defer cancel()
		if err := chromedp.Cancel(cleanup); err != nil {
			t.Error("关闭无头观察浏览器失败", err)
		}
	}()
	wait := func(what, expression string) {
		t.Helper()
		waitExperiment(t, what, func() bool {
			ok, err := chromedp.Run(observer, chromedp.Evaluate[bool](expression))
			if err != nil {
				t.Fatal(err)
			}
			return ok
		})
	}
	screenshot := func(name string) {
		t.Helper()
		directory := os.Getenv("KAKATTA_EXPERIMENT_SCREEN_DIR")
		if directory == "" {
			return
		}
		data, err := chromedp.Run(observer, chromedp.CaptureScreenshot())
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, name+".png"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	wait("等待实验操作直接可用", `document.querySelector('#phase').textContent === '还未开始' && !document.querySelector('#auth-form') && !document.querySelector('#controls').hidden && !document.querySelector('#run').disabled && document.cookie === ''`)
	screenshot("experiment-direct-idle")
	if _, err := chromedp.Run(observer, chromedp.Click(chromedp.CSS("#run"))); err != nil {
		t.Fatal(err)
	}
	r := waitExperimentResource(t, c, make(chan error))
	wait("等待假页面风控停手和远程画面", `document.querySelector('#phase').textContent === '请手动完成验证' && !document.querySelector('#remote').hidden && !!document.querySelector('#remote iframe').contentDocument?.querySelector('#screen canvas') && document.querySelector('#remote iframe').contentDocument.querySelector('#actual').disabled === false`)
	if c.fixture.DetailHits() != 0 || c.snapshot().Phase != "verification" {
		t.Fatal("验证码出现后仍继续抓取详情")
	}
	screenshot("experiment-direct-verification")
	queries := len(c.fixture.SearchQueries())
	if _, err := chromedp.Run(observer, chromedp.Click(chromedp.CSS("#close"))); err != nil {
		t.Fatal(err)
	}
	wait("等待结束后直接返回操作页", `document.querySelector('#phase').textContent === '浏览器已关闭' && document.querySelector('#remote').hidden && !document.querySelector('#run').disabled`)
	assertExperimentReleased(t, c, r)
	if _, err := chromedp.Run(observer, chromedp.Click(chromedp.CSS("#run"))); err != nil {
		t.Fatal(err)
	}
	wait("等待访问保护提示", `!document.querySelector('#error').hidden && document.querySelector('#error').textContent.includes('验证')`)
	if len(c.fixture.SearchQueries()) != queries || c.fixture.DetailHits() != 0 || !c.snapshot().VerifyPending {
		t.Fatal("网页直接操作绕过了待验证保护")
	}
	screenshot("experiment-direct-protected")
	t.Log("无会话直接打开实验页、真实点击运行、本地假页面验证码停手、远程画面加载、关闭和再次运行保护均通过")
}
