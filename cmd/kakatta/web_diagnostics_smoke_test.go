package main

import (
	"bytes"
	"context"
	"image"
	"image/jpeg"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/Seaony/Kakatta/internal/diagnostics"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/chromedp/chromedp"
)

type webSmokeDiagnosticFiles struct {
	page, screenshot, loginScreenshot string
}

func seedWebSmokeDiagnostics(t *testing.T, data string) webSmokeDiagnosticFiles {
	t.Helper()
	root := filepath.Join(data, "diagnostics", "accounts", "12767")
	recorder, err := diagnostics.Open(root, webSmokeTimezone)
	if err != nil {
		t.Fatal(err)
	}
	defer recorder.Close()
	if err := recorder.Event("网页冒烟诊断样例", map[string]any{"source": "web-smoke-fixture", "真实页面捕获": false}); err != nil {
		t.Fatal(err)
	}
	// 只用样例像素验证截图的导出规则，不把它当成浏览器捕获的证据。
	var sample bytes.Buffer
	if err := jpeg.Encode(&sample, image.NewGray(image.Rect(0, 0, 2, 2)), &jpeg.Options{Quality: 60}); err != nil {
		t.Fatal(err)
	}
	page, err := recorder.Bundle("web-smoke-fixture", diagnostics.Page{
		Screenshot: sample.Bytes(), URL: "about:blank", Title: "网页冒烟诊断样例，非真实抓取", LoginPage: false,
		HTML: "<html><body><main>仅用于导出清单的本地样例</main></body></html>",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	login, err := recorder.Bundle("web-smoke-fixture", diagnostics.Page{
		Screenshot: sample.Bytes(), URL: "about:blank", Title: "登录截图排除规则样例，非真实登录", LoginPage: true,
		HTML: "<html><body><main>仅用于登录截图排除规则的本地样例</main></body></html>",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := recorder.Summary(map[string]any{"version": 1, "source": "web-smoke-fixture", "真实页面捕获": false}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	relative := func(name string) string {
		t.Helper()
		result, err := filepath.Rel(data, name)
		if err != nil {
			t.Fatal(err)
		}
		return filepath.ToSlash(result)
	}
	return webSmokeDiagnosticFiles{
		page: relative(filepath.Join(page, "page.html")), screenshot: relative(filepath.Join(page, "screenshot.jpg")),
		loginScreenshot: relative(filepath.Join(login, "screenshot.jpg")),
	}
}

func checkWebSmokeDiagnostics(t *testing.T, browser context.Context, events *webSmokeEvents, files webSmokeDiagnosticFiles, language locales.Language, shots string) {
	t.Helper()
	clickWebSmokeButton(t, browser, events, smokeTranslation(t, language, "admin.diagnostics.export"))
	waitWebSmoke(t, browser, events, `(page, screenshot, loginScreenshot, title, excludedLabel, excludedReason, download) => {
		const dialog = document.querySelector('[role=dialog]');
		const included = dialog?.querySelector('ul');
		const paths = [...included?.querySelectorAll('li > p:first-child') ?? []].map(node => node.textContent);
		const excluded = dialog?.querySelector('section');
		return dialog?.textContent.includes(title) && paths.includes(page) && paths.includes(screenshot) &&
			paths.some(path => path.endsWith('/summary.json')) && paths.some(path => /events-[0-9-]+\.jsonl$/.test(path)) &&
			!paths.includes(loginScreenshot) && !paths.some(path => /(?:^browser\/|secret\.key|kakatta\.db|Cookies)/.test(path)) &&
			excluded?.getAttribute('aria-label') === excludedLabel && excluded.textContent.includes(loginScreenshot) &&
			excluded.textContent.includes(excludedReason) && [...dialog.querySelectorAll('button')].some(button => button.textContent === download && !button.disabled);
	}`, files.page, files.screenshot, files.loginScreenshot,
		smokeTranslation(t, language, "admin.diagnostics.preview"), smokeTranslation(t, language, "admin.diagnostics.excluded"),
		smokeTranslation(t, language, "diagnostics.login_screenshot_excluded"), smokeTranslation(t, language, "admin.diagnostics.download"))
	waitWebSmoke(t, browser, events, `(path) => {
		const dialog = document.querySelector('[role=dialog]');
		const file = [...dialog.querySelectorAll('li > p:first-child')].find(node => node.textContent === path);
		const frame = dialog.getBoundingClientRect();
		return path.length >= 64 && !!file && frame.left >= 0 && frame.right <= innerWidth + 1 &&
			document.documentElement.scrollWidth <= innerWidth && dialog.scrollWidth <= dialog.clientWidth + 1 &&
			[...dialog.querySelectorAll('li,p')].every(node => node.scrollWidth <= node.clientWidth + 1);
	}`, files.page)
	writeWebSmokeScreenshot(t, browser, events, filepath.Join(shots, "diagnostics-export-"+string(language)+".png"))
	close := `//*[@role='dialog']//button[normalize-space(.)=` + strconv.Quote(smokeTranslation(t, language, "common.close")) + `]`
	runWebSmoke(t, browser, events, chromedp.Click(chromedp.Search(close)))
	waitWebSmoke(t, browser, events, `() => !document.querySelector('[role=dialog]')`)
}
