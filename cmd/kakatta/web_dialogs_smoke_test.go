package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

const webSmokeBackupPassword = "local-web-smoke-backup"

func seedWebSmokeConfigurations(t *testing.T, base, username, password string) string {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	request := func(path string, body any, want int) []byte {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Post(base+path, "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatal("准备本地网页样例失败", path, err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != want {
			t.Fatalf("准备本地网页样例%s失败：HTTP%d %v", path, response.StatusCode, err)
		}
		return data
	}
	request("/api/v1/auth/login", map[string]string{"username": username, "password": password}, 200)
	request("/api/v1/accounts", map[string]string{"name": "网页冒烟假账号"}, 201)
	request("/api/v1/proxies", map[string]string{"name": "网页冒烟假代理", "url": "http://127.0.0.1:1"}, 201)
	request("/api/v1/ai/providers", map[string]any{"name": "网页冒烟假模型", "preset": "custom", "base_url": "http://127.0.0.1:1/v1", "vision_model": "web-smoke-only", "text_model": nil, "enabled": false}, 201)
	request("/api/v1/channels", map[string]any{"name": "网页冒烟假渠道", "type": "webhook", "enabled": false, "secrets": map[string]string{"url": "http://127.0.0.1:1"}}, 201)
	// 只验证临时库的备份预览，不确认恢复，也不启用账号、模型和渠道。
	archive := request("/api/v1/backup/export", map[string]string{"password": webSmokeBackupPassword}, 200)
	path := filepath.Join(t.TempDir(), "web-smoke-backup.zip")
	if err := os.WriteFile(path, archive, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func checkWebSmokeDialogs(t *testing.T, browser context.Context, events *webSmokeEvents, page webSmokePage, language locales.Language, shots, backup string) {
	t.Helper()
	type dialog struct {
		name, trigger, title string
		args                 map[string]any
	}
	var dialogs []dialog
	switch page.name {
	case "login":
		dialogs = []dialog{{"recover-password", "web.auth.forgot_password", "web.auth.forgot_password", nil}}
	case "tasks":
		dialogs = []dialog{{"task-import", "tasks.import", "tasks.import", nil}, {"task-export", "tasks.export", "tasks.export", nil}, {"task-delete", "tasks.delete", "tasks.delete.title", nil}}
	case "task-editor":
		dialogs = []dialog{{"risk-dictionary", "tasks.profile.choose_dictionary", "tasks.profile.dictionary_title", nil}}
	case "result-detail":
		dialogs = []dialog{{"edit-specs", "web.detail.edit_specs", "web.detail.edit_specs", nil}, {"dismiss", "web.detail.dismiss", "web.detail.dismiss", nil}, {"block-seller", "web.detail.block", "web.detail.block", nil}, {"reanalyze", "web.detail.reanalyze", "web.detail.reanalyze", nil}}
	case "accounts":
		dialogs = []dialog{{"account-add", "accounts.add", "accounts.add", nil}, {"proxy-add", "proxies.add", "proxies.add", nil}, {"proxy-edit", "proxies.edit", "proxies.edit", nil}, {"proxy-delete", "proxies.delete", "proxies.delete", nil}}
	case "account-detail":
		dialogs = []dialog{{"account-edit", "accounts.edit", "accounts.edit", nil}, {"account-delete", "accounts.delete", "accounts.delete", nil}}
	case "settings-ai":
		dialogs = []dialog{{"provider-add", "web.ai.add_provider", "web.ai.add_provider", nil}, {"provider-edit", "web.ai.edit_provider", "web.ai.edit_provider", nil}, {"provider-delete", "web.ai.delete", "web.ai.delete_provider_title", nil}, {"provider-trial", "web.ai.test", "web.ai.test", nil}}
	case "settings-notifications":
		dialogs = []dialog{{"delivery-history", "web.notify.history_recent", "web.notify.history_named", map[string]any{"name": "网页冒烟假渠道"}}, {"channel-delete", "web.notify.delete", "web.notify.delete_named", map[string]any{"name": "网页冒烟假渠道"}}}
	case "settings-security":
		dialogs = []dialog{{"password", "admin.password.title", "admin.password.title", nil}, {"create-token", "admin.tokens.create", "admin.tokens.create", nil}, {"totp", "admin.totp.start", "admin.totp.start", nil}, {"revoke", "admin.revoke", "", nil}}
	}
	for _, item := range dialogs {
		trigger := smokeTranslationWithArgs(t, language, item.trigger, item.args)
		selector := `(//main//button[normalize-space(.)=` + strconv.Quote(trigger) + `])[1]`
		waitWebSmoke(t, browser, events, `(xpath) => {
			const node = document.evaluate(xpath, document, null, XPathResult.FIRST_ORDERED_NODE_TYPE, null).singleNodeValue;
			return !!node?.getClientRects().length && !node.disabled;
		}`, selector)
		runWebSmoke(t, browser, events, chromedp.Click(chromedp.Search(selector)))
		title := ""
		if item.title != "" {
			title = smokeTranslationWithArgs(t, language, item.title, item.args)
		}
		checkWebSmokeDialog(t, browser, events, language, shots, item.name, title, selector)
	}
	if page.name == "settings-prompts" {
		original, err := chromedp.Run(browser, chromedp.Value(chromedp.CSS(`main textarea`)))
		if err != nil {
			t.Fatal(err)
		}
		runWebSmoke(t, browser, events, chromedp.Click(chromedp.CSS(`main textarea`)),
			chromedp.KeyEvent(kb.End, chromedp.KeyModifiers(chromedp.ModifierCtrl)),
			chromedp.SendKeys(chromedp.CSS(`main textarea`), " "))
		clickWebSmokeButton(t, browser, events, smokeTranslation(t, language, "web.ai.restore"))
		checkWebSmokeDialog(t, browser, events, language, shots, "prompt-restore", smokeTranslation(t, language, "web.ai.restore"), `//main//button[normalize-space(.)=`+strconv.Quote(smokeTranslation(t, language, "web.ai.restore"))+`]`)
		runWebSmoke(t, browser, events, chromedp.Click(chromedp.CSS(`main [role=tab][aria-selected=false]`)))
		checkWebSmokeDialog(t, browser, events, language, shots, "prompt-discard", smokeTranslation(t, language, "web.ai.discard_title"), `main [role=tab][aria-selected=true]`)
		runWebSmoke(t, browser, events, chromedp.Click(chromedp.CSS(`main textarea`)), chromedp.KeyEvent("z", chromedp.KeyModifiers(chromedp.ModifierCtrl)))
		func() {
			started, released := time.Now(), false
			defer func() {
				events.mu.Lock()
				events.cleanGuards = append(events.cleanGuards, webSmokeCleanGuard{Language: language, ElapsedMS: time.Since(started).Milliseconds(), Released: released})
				events.mu.Unlock()
			}()
			waitWebSmoke(t, browser, events, `(original) => {
				if (document.querySelector('main textarea')?.value !== original || !document.querySelector('main form button[type=submit]')?.disabled) return false;
				const event = new Event('beforeunload', {cancelable: true});
				window.dispatchEvent(event);
				return !event.defaultPrevented;
			}`, original)
			released = true
		}()
	}
	if page.name == "settings-backup" {
		if backup == "" {
			t.Fatal("备份预览缺少本地样例")
		}
		runWebSmoke(t, browser, events, chromedp.SetUploadFiles(chromedp.CSS(`main input[type=file]`), []string{backup}))
		waitWebSmoke(t, browser, events, `() => document.querySelector('main input[type=file]')?.files[0]?.name === 'web-smoke-backup.zip'`)
		fillWebSmokeInput(t, browser, events, `main form:has(input[type=file]) input[type=password]`, webSmokeBackupPassword)
		waitWebSmoke(t, browser, events, `(label) => [...document.querySelectorAll('main button')].some(node => node.textContent === label && !node.disabled)`, smokeTranslation(t, language, "admin.backup.validate"))
		clickWebSmokeButton(t, browser, events, smokeTranslation(t, language, "admin.backup.validate"))
		checkWebSmokeDialog(t, browser, events, language, shots, "backup-preview", smokeTranslation(t, language, "admin.backup.preview_title"), `//main//button[normalize-space(.)=`+strconv.Quote(smokeTranslation(t, language, "admin.backup.validate"))+`]`)
	}
}

func checkWebSmokeDialog(t *testing.T, browser context.Context, events *webSmokeEvents, language locales.Language, shots, name, title, focus string) {
	t.Helper()
	events.mu.Lock()
	previous := events.page
	events.page += " [弹窗:" + name + "]"
	events.mu.Unlock()
	defer func() { events.mu.Lock(); events.page = previous; events.mu.Unlock() }()
	waitWebSmoke(t, browser, events, `(title) => {
		const dialog = document.querySelector('[role=dialog],[role=alertdialog]');
		return !!dialog?.getClientRects().length && !!dialog.querySelector('h2')?.textContent &&
			(!title || dialog.querySelector('h2').textContent.trim() === title) &&
			![...dialog.querySelectorAll('[aria-busy=true]')].some(node => node.getClientRects().length);
	}`, title)
	writeWebSmokeScreenshot(t, browser, events, filepath.Join(shots, "dialog-"+name+"-"+string(language)+".png"))
	assertWebSmokeLayout(t, browser, events)
	close := smokeTranslation(t, language, "common.close")
	cancel := smokeTranslation(t, language, "common.cancel")
	selector := `((//*[@role='dialog' or @role='alertdialog'])//button[normalize-space(.)=` + strconv.Quote(close) + ` or normalize-space(.)=` + strconv.Quote(cancel) + `])[1]`
	runWebSmoke(t, browser, events, chromedp.Click(chromedp.Search(selector)))
	waitWebSmokeFocus(t, browser, events, focus)
}

func waitWebSmokeFocus(t *testing.T, browser context.Context, events *webSmokeEvents, selector string) {
	t.Helper()
	waitWebSmoke(t, browser, events, `(selector) => {
		const node = selector.startsWith('//') || selector.startsWith('(')
			? document.evaluate(selector, document, null, XPathResult.FIRST_ORDERED_NODE_TYPE, null).singleNodeValue
			: document.querySelector(selector);
		return !document.querySelector('[role=dialog],[role=alertdialog]') && !!node &&
			(document.activeElement === node || node.matches(':disabled') && document.activeElement === document.body);
	}`, selector)
}

func assertWebSmokeLayout(t *testing.T, browser context.Context, events *webSmokeEvents) {
	t.Helper()
	raw, err := locales.JSON(locales.ZhCN)
	if err != nil {
		t.Fatal(err)
	}
	var dictionary map[string]string
	if err := json.Unmarshal(raw, &dictionary); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(dictionary))
	for code := range dictionary {
		keys = append(keys, code)
	}
	issues, err := chromedp.Run(browser, chromedp.Evaluate[[]string](`(keys => {
		const issues = [];
		if (document.documentElement.scrollWidth > innerWidth + 1) issues.push('页面横向溢出');
		for (const dialog of document.querySelectorAll('[role=dialog],[role=alertdialog]')) {
			const rect = dialog.getBoundingClientRect();
			if (rect.left < -1 || rect.right > innerWidth + 1 || rect.top < -1 || rect.bottom > innerHeight + 1 || dialog.scrollWidth > dialog.clientWidth + 1) issues.push('弹窗超出画面');
		}
		for (const node of document.querySelectorAll('main button,main h1,[role=dialog] button,[role=alertdialog] button')) {
			if (node.getClientRects().length && node.clientWidth > 0 && node.scrollWidth > node.clientWidth + 1) issues.push('控件文字被截断：' + node.textContent.trim().slice(0, 100));
		}
		const codes = new Set(keys);
		const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
		for (let node; node = walker.nextNode();) {
			const parent = node.parentElement;
			if (parent?.getClientRects().length && !parent.closest('pre,code,textarea,script,style') && codes.has(node.textContent.trim())) issues.push('缺失文案键：' + node.textContent.trim());
		}
		return issues;
	})(`+string(mustWebSmokeJSON(t, keys))+`)`))
	events.assert(t)
	if err != nil || len(issues) > 0 {
		t.Fatalf("%s 布局或文案错误：%s %v", events.page, strings.Join(issues, "；"), err)
	}
}

func mustWebSmokeJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
