package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/log"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

func TestWebTasksSearchSmoke(t *testing.T) {
	if os.Getenv("KAKATTA_WEB_SMOKE") != "1" {
		t.Skip("用 deploy/test-web-smoke.sh 在断网容器中定向运行")
	}
	interfaces, err := net.Interfaces()
	if err != nil || len(interfaces) != 1 || interfaces[0].Flags&net.FlagLoopback == 0 {
		t.Fatal("任务搜索框验收只能在只有回环网卡的容器中运行")
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
				if _, err := g.preferences.Apply(t.Context(), settings.Values{"general.language": json.RawMessage(strconv.Quote(string(language)))}); err != nil {
					t.Fatal(err)
				}
				host := httptest.NewServer(g.handler)
				defer host.Close()
				allocator, closeAllocator := chromedp.NewExecAllocator(t.Context(), append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(chrome))...)
				defer closeAllocator()
				browser, closeBrowser := chromedp.NewContext(allocator)
				defer closeBrowser()
				events := webSmokeEvents{base: host.URL, page: "/tasks"}
				if _, err := chromedp.Run(browser, chromedp.Evaluate[string](`navigator.userAgent`)); err != nil {
					t.Fatal(err)
				}
				defer func() {
					closing, cancel := context.WithTimeout(browser, 5*time.Second)
					defer cancel()
					if err := chromedp.Cancel(closing); err != nil {
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
				visitWebSmokePage(t, browser, &events, webSmokePage{"tasks", "/tasks", "tasks.title", "", ""}, language, viewport.width)
				directory := filepath.Join("/screens", viewport.name)
				if err := os.MkdirAll(directory, 0755); err != nil {
					t.Fatal(err)
				}
				writeWebSmokeScreenshot(t, browser, &events, filepath.Join(directory, "tasks-width-"+string(language)+".png"))
				assertWebSmokeTaskSearch(t, browser, &events, language, viewport.width)
			})
		}
	}
}

func assertWebSmokeTaskSearch(t *testing.T, browser context.Context, events *webSmokeEvents, language locales.Language, viewport int64) {
	t.Helper()
	label := smokeTranslation(t, language, "tasks.list.search")
	measurement, err := chromedp.Run(browser, chromedp.Evaluate[struct {
		Width       float64   `json:"width"`
		LabelHeight float64   `json:"label_height"`
		LineHeight  float64   `json:"line_height"`
		Bottoms     []float64 `json:"bottoms"`
		RowDelta    float64   `json:"row_delta"`
	}](`(text => {
		const label = [...document.querySelectorAll('main label')].find(node => node.textContent.trim() === text);
		const input = label && document.getElementById(label.htmlFor);
		const form = input?.closest('form');
		const controls = [input, form?.querySelector('select'), form?.querySelector('button[type=submit]'), form?.querySelector('button:not([type=submit])')];
		const bottoms = controls.map(node => node?.getBoundingClientRect().bottom ?? 0);
		return {width: input?.getBoundingClientRect().width ?? 0, label_height: label?.getBoundingClientRect().height ?? 0,
			line_height: label ? parseFloat(getComputedStyle(label).lineHeight) || parseFloat(getComputedStyle(label).fontSize) * 1.2 : 0,
			bottoms, row_delta: Math.max(...bottoms) - Math.min(...bottoms)};
	})(`+strconv.Quote(label)+`)`))
	events.assert(t)
	if err != nil {
		t.Fatal(err)
	}
	minimum := 200.0
	if viewport < 640 {
		minimum = 160
	}
	t.Logf("%s %d视窗：任务搜索输入宽%.2fpx（至少%.0fpx），标签高%.2fpx、行高%.2fpx，四控件bottom=%v、行差%.2fpx", language, viewport, measurement.Width, minimum, measurement.LabelHeight, measurement.LineHeight, measurement.Bottoms, measurement.RowDelta)
	if measurement.Width < minimum || measurement.LineHeight <= 0 || measurement.LabelHeight > 2*measurement.LineHeight+1 || viewport >= 640 && measurement.RowDelta > 1 {
		t.Fatal("任务搜索框不可读、标签逐字换行，或桌面搜索筛选控件没有同行")
	}
}
