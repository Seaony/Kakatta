package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/log"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

func TestWebStorageFailureSmoke(t *testing.T) {
	if os.Getenv("KAKATTA_WEB_SMOKE") != "1" {
		t.Skip("用 deploy/test-web-smoke.sh 在断网容器中运行")
	}
	interfaces, err := net.Interfaces()
	if err != nil || len(interfaces) != 1 || interfaces[0].Flags&net.FlagLoopback == 0 {
		t.Fatalf("故障网页冒烟只能在断网容器中运行：%v %v", interfaces, err)
	}
	chrome, err := exec.LookPath("google-chrome-stable")
	if err != nil {
		t.Fatal(err)
	}
	for _, viewport := range []struct {
		name          string
		width, height int64
	}{{"desktop", 1280, 900}, {"mobile", 393, 852}} {
		for _, language := range webSmokeLanguages {
			t.Run(viewport.name+"/"+string(language), func(t *testing.T) {
				g := graphFixture(t)
				session := graphAdmin(t, g)
				if _, err := g.preferences.Apply(t.Context(), settings.Values{"general.language": json.RawMessage(`"` + string(language) + `"`)}); err != nil {
					t.Fatal(err)
				}
				if _, err := g.database.DB().Exec(`CREATE TRIGGER smoke_storage BEFORE UPDATE ON meta WHEN OLD.key='quota.clock' BEGIN SELECT RAISE(ABORT,'local storage failure'); END`); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _, _ = g.database.DB().Exec(`DROP TRIGGER IF EXISTS smoke_storage`) })
				if err := g.core.Prepare(t.Context()); err == nil {
					t.Fatal("没有触发真实业务准备失败")
				}
				if _, err := g.database.DB().Exec(`CREATE TRIGGER smoke_auth_readonly BEFORE UPDATE ON auth_tokens BEGIN SELECT RAISE(ABORT,'diagnostic read must not write'); END`); err != nil {
					t.Fatal(err)
				}
				host := httptest.NewServer(g.handler)
				defer host.Close()
				options := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(chrome))
				allocator, stopAllocator := chromedp.NewExecAllocator(t.Context(), options...)
				defer stopAllocator()
				browser, stopBrowser := chromedp.NewContext(allocator)
				defer stopBrowser()
				events := webSmokeEvents{base: host.URL, page: "/settings/system", allowedHTTP: map[string]int{}, readOnly: true}
				for _, path := range []string{"/healthz", "/api/v1/system/status", "/api/v1/system/events?resolved=false&limit=50", "/api/v1/events"} {
					events.allowedHTTP[host.URL+path] = http.StatusServiceUnavailable
				}
				if _, err := chromedp.Run(browser, chromedp.Evaluate[string](`navigator.userAgent`)); err != nil {
					t.Fatal(err)
				}
				defer func() {
					ctx, cancel := context.WithTimeout(browser, 5*time.Second)
					defer cancel()
					if err := chromedp.Cancel(ctx); err != nil {
						t.Error(err)
					}
				}()
				if err := chromedp.Listen(browser, events.receive); err != nil {
					t.Fatal(err)
				}
				runWebSmoke(t, browser, &events, chromedp.Func(func(ctx context.Context, _ *chromedp.Target) error {
					if _, err := chromedp.Call(ctx, network.Enable, network.EnableParams{}); err != nil {
						return err
					}
					if _, err := chromedp.Call(ctx, log.Enable, cdp.Empty{}); err != nil {
						return err
					}
					httpOnly := true
					if _, err := chromedp.Call(ctx, network.SetCookie, network.SetCookieParams{Name: "kakatta_session", Value: session.Token, URL: host.URL, HTTPOnly: &httpOnly}); err != nil {
						return err
					}
					_, err := chromedp.Call(ctx, emulation.SetDeviceMetricsOverride, emulation.SetDeviceMetricsOverrideParams{Width: viewport.width, Height: viewport.height, ScreenWidth: viewport.width, ScreenHeight: viewport.height, DeviceScaleFactor: 1, Mobile: viewport.name == "mobile"})
					return err
				}))
				visitWebSmokePage(t, browser, &events, webSmokePage{"storage-failure", "/settings/system", "admin.system.title", "", "@runtime.storage_unavailable"}, language, viewport.width)
				waitWebSmoke(t, browser, &events, `(text) => document.querySelector('main')?.innerText.includes(text)`, smokeTranslation(t, language, "admin.system.health_unavailable"))
				waitWebSmoke(t, browser, &events, `() => document.documentElement.scrollWidth <= innerWidth && !document.querySelector('input[name=password]')`)
				assertWebSmokeLayout(t, browser, &events)
				directory := filepath.Join("/screens", viewport.name)
				if err := os.MkdirAll(directory, 0755); err != nil {
					t.Fatal(err)
				}
				writeWebSmokeScreenshot(t, browser, &events, filepath.Join(directory, "storage-failure-"+string(language)+".png"))
			})
		}
	}
}
