package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/diagnostics"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

func formalSetup(t *testing.T, browser context.Context, events *webSmokeEvents, m formalMetadata) {
	t.Helper()
	formalNetworkPhase(browser, "01初始化")
	formalSetupVisit(t, browser, events, "/", "web.setup.title")
	formalSetupInput(t, browser, events, `input[name=username]`, formalUsername)
	formalSetupInput(t, browser, events, `input[name=password]`, formalPassword)
	formalInitializationSubmission(browser)
	formalSetupButton(t, browser, events, "//main", "web.setup.submit")
	waitWebSmoke(t, browser, events, `() => location.pathname === '/onboarding' && !!document.querySelector('main input[name=name]')`)
	if formalScalar(t, "SELECT count(*) FROM admin WHERE username=?", formalUsername) != 1 {
		t.Fatal("网页初始化后没有建立管理员")
	}
	formalFastSettings(t, browser, events)
	formalSnapshot(t, browser, events, "01-onboarding")
	formalRecord(t, 1, "网页直接设置用户名和密码后建立管理员并进入引导；只在本测试目录通过设置页缩短等待")

	formalNetworkPhase(browser, "02账号扫码")
	formalSetupFillLabel(t, browser, events, "accounts.name", "本地扫码账号")
	formalSetupButton(t, browser, events, "//main", "accounts.save")
	waitWebSmoke(t, browser, events, `(name, login) => document.querySelector('main')?.innerText.includes(name) && [...document.querySelectorAll('main button')].some(node => node.textContent.trim() === login && node.getClientRects().length)`, "本地扫码账号", smokeTranslation(t, locales.ZhCN, "accounts.login"))
	accountID := formalScalar(t, "SELECT id FROM accounts WHERE name=?", "本地扫码账号")
	previous := formalSetupAccountSequence(t, accountID, "")
	formalSetupButton(t, browser, events, "//main", "accounts.login")
	waitWebSmoke(t, browser, events, `() => {
		const frame = [...document.querySelectorAll('main iframe')].find(node => node.getClientRects().length);
		const doc = frame?.contentDocument, canvas = doc?.querySelector('#screen canvas');
		return doc?.querySelector('#status')?.textContent === '已连接' && !!canvas?.getClientRects().length && canvas.width > 0 && canvas.height > 0;
	}`)
	formalWait(t, browser, "正式登录会话已观察到本次登录框", 45*time.Second, func() bool {
		return formalSetupAccountSequence(t, accountID, "登录框已出现") > previous
	})
	formalSnapshot(t, browser, events, "02-login-viewer")
	formalControl(t, "login_complete", nil)
	formalWait(t, browser, "扫码后账号就绪并结束登录会话", 45*time.Second, func() bool {
		return formalScalar(t, "SELECT count(*) FROM accounts WHERE id=? AND state='active'", accountID) == 1 &&
			formalScalar(t, "SELECT count(*) FROM meta WHERE key=?", fmt.Sprintf("runtime.session.%d", accountID)) == 0
	})
	formalSetupButton(t, browser, events, "//main", "web.onboarding.continue")
	waitWebSmoke(t, browser, events, `(label) => [...document.querySelectorAll('main label')].some(node => node.textContent.trim() === label && node.getClientRects().length)`, smokeTranslation(t, locales.ZhCN, "web.ai.base_url"))
	formalRecord(t, 2, "真实远程iframe和noVNC画布已连接；正式日志出现本次登录框后才模拟手机扫码，账号转为已登录且会话结束")

	formalNetworkPhase(browser, "03模型配置")
	formalSetupFillLabel(t, browser, events, "web.ai.name", "本地模型")
	formalSetupFillLabel(t, browser, events, "web.ai.base_url", m.ModelBaseURL)
	formalSetupFillLabel(t, browser, events, "web.ai.api_key", "wrong-local-formal-key")
	formalSetupFillLabel(t, browser, events, "web.ai.vision_model", "local-vision")
	formalSetupFillLabel(t, browser, events, "web.ai.text_model", "local-text")
	formalSetupButton(t, browser, events, "//main", "web.ai.save")
	formalSetupTryModel(t, browser, events, false)
	formalSetupButton(t, browser, events, "//*[@role='dialog']", "common.close")
	formalSetupButton(t, browser, events, "//main", "web.ai.edit_provider")
	formalSetupSelect(t, browser, events, "web.ai.api_key", "replace")
	formalSetupFillLabel(t, browser, events, "web.ai.api_key", m.ModelKey)
	formalSetupButton(t, browser, events, "//main", "web.ai.save")
	formalSetupTryModel(t, browser, events, true)
	formalSnapshot(t, browser, events, "03-model-tested")
	formalSetupButton(t, browser, events, "//*[@role='dialog']", "common.close")
	if formalScalar(t, "SELECT count(*) FROM ai_providers WHERE name=? AND enabled=1 AND error_state IS NULL", "本地模型") != 1 {
		t.Fatal("修正凭据并试分析后服务商仍不可用")
	}
	formalSetupButton(t, browser, events, "//main", "web.onboarding.continue")
	formalRecord(t, 3, "网页保存服务商，错误Key显示认证原因和修正办法，编辑为合成正确Key后文字与图片试分析均通过")

	formalNetworkPhase(browser, "04推送配置")
	formalSetupButton(t, browser, events, "//main", "web.notify.add_channel")
	formalSetupSelect(t, browser, events, "web.notify.channel_type", "webhook")
	formalSetupFillLabel(t, browser, events, "web.notify.channel_name", "本地Webhook")
	formalSetupFillLabel(t, browser, events, "web.notify.field.webhook_url", m.PushURL)
	formalSetupButton(t, browser, events, "//main", "web.notify.save_channel")
	formalControl(t, "push", map[string]any{"mode": "fail"})
	formalSetupButton(t, browser, events, "//main", "web.notify.send_test")
	formalWait(t, browser, "本地Webhook失败回执已保存", 20*time.Second, func() bool {
		return formalScalar(t, "SELECT count(*) FROM channels WHERE name=? AND last_test_ok=0 AND last_error IS NOT NULL", "本地Webhook") == 1
	})
	waitWebSmoke(t, browser, events, `() => document.querySelector('main')?.innerText.includes('HTTP 503')`)
	formalSnapshot(t, browser, events, "04-push-failure")
	formalControl(t, "push", map[string]any{"mode": "ok"})
	formalSetupButton(t, browser, events, "//main", "web.notify.send_test")
	formalWait(t, browser, "本地Webhook正常回执已保存", 20*time.Second, func() bool {
		return formalScalar(t, "SELECT count(*) FROM channels WHERE name=? AND enabled=1 AND last_test_ok=1", "本地Webhook") == 1
	})
	waitWebSmoke(t, browser, events, `(sent) => document.querySelector('main')?.innerText.includes(sent)`, smokeTranslation(t, locales.ZhCN, "web.notify.sent"))
	formalSnapshot(t, browser, events, "04-push-success")
	formalSetupButton(t, browser, events, "//main", "web.onboarding.continue")
	formalRecord(t, 4, "网页保存Webhook后显式发送测试；服务端503显示可读原因，恢复本地渠道后再次测试成功")

	formalNetworkPhase(browser, "05任务配置")
	formalSetupFillLabel(t, browser, events, "tasks.field.name", "正式流程相机监控")
	formalSetupFillLabel(t, browser, events, "tasks.field.keyword", "MacBook Pro")
	formalSetupFillLabel(t, browser, events, "tasks.field.min_price", "5000")
	formalSetupFillLabel(t, browser, events, "tasks.field.max_price", "15000")
	formalSetupFillLabel(t, browser, events, "tasks.field.max_pages", "2")
	formalSetupFillLabel(t, browser, events, "tasks.field.interval", "1")
	formalSetupFillLabel(t, browser, events, "tasks.field.requirement", "自用 MacBook Pro，型号一致、无拆修，价格合适时提醒我。")
	formalSetupButton(t, browser, events, "//main", "tasks.generate")
	waitWebSmoke(t, browser, events, `() => {
		const region = document.querySelector('#task-profile-specs')?.closest('section');
		return !!region?.querySelector('input')?.value.trim() && [...region.querySelectorAll('textarea')].some(node => node.value.includes('MacBook Pro'));
	}`)
	formalSnapshot(t, browser, events, "05-generated-profile")
	formalSetupButton(t, browser, events, "//main", "tasks.save")
	waitWebSmoke(t, browser, events, `(finish) => [...document.querySelectorAll('main button')].some(node => node.textContent.trim() === finish && node.getClientRects().length)`, smokeTranslation(t, locales.ZhCN, "web.onboarding.finish"))
	if formalScalar(t, `SELECT count(*) FROM tasks WHERE name=? AND keyword='MacBook Pro' AND judge_mode='ai' AND enabled=1 AND interval_minutes=1 AND min_price=500000 AND max_price=1500000 AND max_pages=2 AND requirement=? AND crawl_overrides_json='{}' AND json_array_length(profile_json,'$.specs')>0`, "正式流程相机监控", "自用 MacBook Pro，型号一致、无拆修，价格合适时提醒我。") != 1 {
		t.Fatal("网页保存的任务、价格筛选、频率或生成画像不完整")
	}
	formalSetupButton(t, browser, events, "//main", "web.onboarding.finish")
	formalWait(t, browser, "首次引导完成标记已保存", 10*time.Second, func() bool {
		return formalScalar(t, "SELECT count(*) FROM settings WHERE key='general.onboarding_done' AND value_json='true'") == 1
	})
	formalSetupVisit(t, browser, events, "/tasks", "tasks.title")
	waitWebSmoke(t, browser, events, `(name, next) => [...document.querySelectorAll('main article')].some(node => node.innerText.includes(name) && node.innerText.includes(next))`, "正式流程相机监控", strings.TrimSpace(smokeTranslationWithArgs(t, locales.ZhCN, "tasks.runtime.next", map[string]any{"time": ""})))
	formalSnapshot(t, browser, events, "05-task-saved")
	formalRecord(t, 5, "网页生成画像并保存已启用监控任务，关键词、5000–15000元筛选、两页和1分钟频率均落盘，任务列表显示下次运行")
}

func formalFastSettings(t *testing.T, browser context.Context, events *webSmokeEvents) {
	t.Helper()
	formalSetupVisit(t, browser, events, "/settings/about", "admin.about.title")
	formalSetupCheck(t, browser, events, `//button[@role='switch' and @id=//label[normalize-space(.)=`+strconv.Quote(smokeTranslation(t, locales.ZhCN, "admin.about.check_enabled"))+`]/@for]`, false)
	formalSetupButton(t, browser, events, "//main", "admin.save")
	formalSettingsSaved(t, browser, events, "admin.saved", "admin.save")
	formalWait(t, browser, "网页已关闭更新检查", 10*time.Second, func() bool {
		return formalScalar(t, "SELECT count(*) FROM settings WHERE key='general.update_check' AND value_json='false'") == 1
	})
	formalSetupVisit(t, browser, events, "/settings/general", "web.settings.general_title")
	formalSetupSelect(t, browser, events, "settings.general.language.label", "zh-CN")
	formalSetupCheck(t, browser, events, "#setting-crawl-quiet_hours button[role=switch]", false)
	formalSetupButton(t, browser, events, "//main", "web.settings.save")
	formalSettingsSaved(t, browser, events, "web.settings.saved", "web.settings.save")
	formalWait(t, browser, "网页已关闭安静时段并保存中文", 10*time.Second, func() bool {
		return formalScalar(t, "SELECT count(*) FROM settings WHERE key='crawl.quiet_hours' AND json_extract(value_json,'$.enabled')=0") == 1 &&
			formalScalar(t, `SELECT count(*) FROM settings WHERE key='general.language' AND value_json='"zh-CN"'`) == 1
	})
	formalSetupVisit(t, browser, events, "/settings/limits", "web.settings.limits_title")
	scalarFields := []struct {
		key   string
		value int64
	}{
		{"account_min_gap_seconds", 2}, {"account_min_gap_jitter_seconds", 0}, {"interval_jitter_percent", 0},
		{"account_hourly_visits", 200}, {"account_daily_search_visits", 1000}, {"account_daily_detail_visits", 1000},
		{"max_details_per_run", 10}, {"verify_cooldown_base_minutes", 1}, {"verify_cooldown_max_minutes", 1},
	}
	rangeFields := []struct {
		key   string
		value float64
	}{
		{"home_stay_seconds", 0.01}, {"page_wait_seconds", 0.01}, {"detail_pre_wait_seconds", 0.01},
		{"detail_stay_seconds", 0.01}, {"action_wait_seconds", 0.01}, {"typing_interval_ms", 1},
	}
	expected := make(map[string]string)
	for _, field := range scalarFields {
		selector := "#setting-crawl-" + field.key + " input"
		value := strconv.FormatInt(field.value, 10)
		expected[selector] = value
		formalSetupInput(t, browser, events, selector, value)
	}
	for _, field := range rangeFields {
		value := strconv.FormatFloat(field.value, 'f', -1, 64)
		for index := 1; index <= 2; index++ {
			selector := fmt.Sprintf(`(//*[@id="setting-crawl-%s"]//input)[%d]`, field.key, index)
			expected[selector] = value
			formalSetupInput(t, browser, events, selector, value)
		}
	}
	waitWebSmoke(t, browser, events, `(expected) => Object.entries(expected).every(([selector, value]) => {
		const node = selector.startsWith('(') ? document.evaluate(selector, document, null, XPathResult.FIRST_ORDERED_NODE_TYPE, null).singleNodeValue : document.querySelector(selector);
		return node?.value === value;
	})`, expected)
	formalSetupButton(t, browser, events, "//main", "web.settings.save")
	formalSettingsSaved(t, browser, events, "web.settings.saved", "web.settings.save")
	formalWait(t, browser, "网页加速设置已保存", 10*time.Second, func() bool {
		for _, field := range scalarFields {
			if formalScalar(t, "SELECT count(*) FROM settings WHERE key=? AND json_extract(value_json,'$')=?", "crawl."+field.key, field.value) != 1 {
				return false
			}
		}
		for _, field := range rangeFields {
			if formalScalar(t, "SELECT count(*) FROM settings WHERE key=? AND json_extract(value_json,'$.min')=? AND json_extract(value_json,'$.max')=?", "crawl."+field.key, field.value, field.value) != 1 {
				return false
			}
		}
		return formalScalar(t, "SELECT count(*) FROM settings WHERE key='general.update_check' AND value_json='false'") == 1
	})
	formalSetupVisit(t, browser, events, "/settings/ai", "web.ai.title")
	formalSetupFillLabel(t, browser, events, "settings.ai.request_timeout_seconds.label", "1")
	formalSetupFillLabel(t, browser, events, "settings.ai.image_count.label", "1")
	waitWebSmoke(t, browser, events, `(labels) => labels.every(label => {
		const node = [...document.querySelectorAll('main label')].find(node => node.textContent.trim() === label);
		return node && document.getElementById(node.htmlFor)?.value === '1';
	})`, []string{smokeTranslation(t, locales.ZhCN, "settings.ai.request_timeout_seconds.label"), smokeTranslation(t, locales.ZhCN, "settings.ai.image_count.label")})
	formalSetupButton(t, browser, events, "//main", "web.ai.save")
	formalSettingsSaved(t, browser, events, "web.ai.saved", "web.ai.save")
	formalWait(t, browser, "网页模型超时已保存", 10*time.Second, func() bool {
		return formalScalar(t, "SELECT count(*) FROM settings WHERE key IN ('ai.request_timeout_seconds','ai.image_count') AND value_json='1'") == 2
	})
	formalSetupVisit(t, browser, events, "/onboarding", "web.onboarding.title")
}

func formalSettingsSaved(t *testing.T, browser context.Context, events *webSmokeEvents, savedCode, saveCode string) {
	t.Helper()
	// 落盘可能早于网页收到回执，离开前要等页面结束保存并清掉草稿。
	waitWebSmoke(t, browser, events, `(saved, save, dirty) => {
		const main = document.querySelector('main');
		const feedback = [...(main?.querySelectorAll('[role=status]') ?? [])].some(node => node.textContent.trim() === saved);
		const button = [...(main?.querySelectorAll('button') ?? [])].find(node => node.textContent.trim() === save);
		return feedback && button?.disabled && button.getAttribute('aria-busy') !== 'true' && !main.innerText.includes(dirty);
	}`, smokeTranslation(t, locales.ZhCN, savedCode), smokeTranslation(t, locales.ZhCN, saveCode), smokeTranslation(t, locales.ZhCN, "web.settings.not_saved"))
}

func formalSetupVisit(t *testing.T, browser context.Context, events *webSmokeEvents, path, heading string) {
	t.Helper()
	events.mu.Lock()
	events.page = path
	events.mu.Unlock()
	runWebSmoke(t, browser, events, chromedp.Navigate(formalBaseURL+path))
	waitWebSmoke(t, browser, events, `(title) => document.querySelector('main h1')?.textContent.trim() === title`, smokeTranslation(t, locales.ZhCN, heading))
}

func formalSetupButton(t *testing.T, browser context.Context, events *webSmokeEvents, scope, code string) {
	t.Helper()
	text := smokeTranslation(t, locales.ZhCN, code)
	xpath := scope + `//button[normalize-space(.)=` + strconv.Quote(text) + ` and not(ancestor::*[@hidden])]`
	waitWebSmoke(t, browser, events, `(xpath) => {
		const node = document.evaluate(xpath, document, null, XPathResult.FIRST_ORDERED_NODE_TYPE, null).singleNodeValue;
		return !!node?.getClientRects().length && !node.disabled;
	}`, xpath)
	runWebSmoke(t, browser, events, chromedp.ScrollIntoView(chromedp.Search(xpath)))
	// 弹窗刚出现时仍可能挡住指针，不能只看按钮尺寸。
	waitWebSmoke(t, browser, events, `(xpath) => {
		const node = document.evaluate(xpath, document, null, XPathResult.FIRST_ORDERED_NODE_TYPE, null).singleNodeValue;
		if (!node || node.disabled) return false;
		const rect = node.getBoundingClientRect();
		const hit = document.elementFromPoint(rect.left + rect.width / 2, rect.top + rect.height / 2);
		return !!rect.width && !!rect.height && node.contains(hit);
	}`, xpath)
	runWebSmoke(t, browser, events, chromedp.Click(chromedp.Search(xpath)))
}

func formalSetupInput(t *testing.T, browser context.Context, events *webSmokeEvents, selector, value string) {
	t.Helper()
	target := `(selector => selector.startsWith('//') || selector.startsWith('(') ? document.evaluate(selector, document, null, XPathResult.FIRST_ORDERED_NODE_TYPE, null).singleNodeValue : document.querySelector(selector))(` + strconv.Quote(selector) + `)`
	// 滚到中间，避免输入框被底部固定的保存栏挡住。
	runWebSmoke(t, browser, events, chromedp.WaitVisible(chromedp.Search(selector)), chromedp.Evaluate[chromedp.Void](target+`.scrollIntoView({block:'center', inline:'nearest', behavior:'instant'})`))
	waitWebSmoke(t, browser, events, `() => {
		const node = `+target+`;
		if (!node || node.disabled) return false;
		const rect = node.getBoundingClientRect();
		return !!rect.width && !!rect.height && node.contains(document.elementFromPoint(rect.left + rect.width / 2, rect.top + rect.height / 2));
	}`)
	runWebSmoke(t, browser, events, chromedp.Click(chromedp.Search(selector)))
	oldLength := -1
	check := func(expected, stage string, exact bool) {
		t.Helper()
		ctx, cancel := context.WithTimeout(browser, time.Second)
		defer cancel()
		state, err := chromedp.Run(ctx, chromedp.Evaluate[struct {
			Length  int  `json:"length"`
			Focused bool `json:"focused"`
			Matches bool `json:"matches"`
		}](`(() => { const node = `+target+`; return {length: node?.value.length ?? -1, focused: node === document.activeElement, matches: node?.value === `+strconv.Quote(expected)+`}; })()`))
		if oldLength < 0 {
			oldLength = state.Length
		}
		if err != nil || !state.Focused || exact && !state.Matches {
			t.Fatalf("输入框%s阶段失败：%s，旧值长度%d，当前长度%d，仍聚焦%t：%v", stage, selector, oldLength, state.Length, state.Focused, err)
		}
	}
	check("", "聚焦", false)
	runWebSmoke(t, browser, events, chromedp.KeyEvent("a", chromedp.KeyModifiers(chromedp.ModifierCtrl)), chromedp.KeyEvent(kb.Backspace))
	check("", "清空", true)
	runWebSmoke(t, browser, events, chromedp.SendKeys(chromedp.Search(selector), value))
	check(value, "填写", true)
}

func formalSetupFillLabel(t *testing.T, browser context.Context, events *webSmokeEvents, code, value string) {
	t.Helper()
	selector := `//*[self::input or self::textarea][@id=//label[normalize-space(.)=` + strconv.Quote(smokeTranslation(t, locales.ZhCN, code)) + ` and not(ancestor::*[@hidden])]/@for]`
	formalSetupInput(t, browser, events, selector, value)
}

func formalSetupSelect(t *testing.T, browser context.Context, events *webSmokeEvents, code, value string) {
	t.Helper()
	label := smokeTranslation(t, locales.ZhCN, code)
	index, err := chromedp.Run(browser, chromedp.Evaluate[int](`((label, value) => {
		const node = [...document.querySelectorAll('label')].find(node => node.textContent.trim() === label && node.getClientRects().length);
		const select = node && document.getElementById(node.htmlFor);
		return select?.tagName === 'SELECT' ? [...select.options].findIndex(option => option.value === value) : -1;
	})(`+strconv.Quote(label)+`,`+strconv.Quote(value)+`)`))
	if err != nil || index < 0 {
		t.Fatal("页面没有指定的下拉选项", label, err)
	}
	selector := `//select[@id=//label[normalize-space(.)=` + strconv.Quote(label) + ` and not(ancestor::*[@hidden])]/@for]`
	runWebSmoke(t, browser, events, chromedp.SendKeys(chromedp.Search(selector), kb.Home+strings.Repeat(kb.ArrowDown, index)+kb.Enter))
}

func formalSetupCheck(t *testing.T, browser context.Context, events *webSmokeEvents, selector string, want bool) {
	t.Helper()
	checked, err := chromedp.Run(browser, chromedp.Evaluate[bool](`(selector => {
		const node = selector.startsWith('//') ? document.evaluate(selector, document, null, XPathResult.FIRST_ORDERED_NODE_TYPE, null).singleNodeValue : document.querySelector(selector);
		if (!node) throw Error('设置开关不可见');
		return node.getAttribute('aria-checked') === 'true' || node.checked === true;
	})(`+strconv.Quote(selector)+`)`))
	if err != nil {
		t.Fatal("读取页面开关失败", err)
	}
	if checked != want {
		runWebSmoke(t, browser, events, chromedp.Click(chromedp.Search(selector)))
	}
}

func formalSetupTryModel(t *testing.T, browser context.Context, events *webSmokeEvents, pass bool) {
	t.Helper()
	formalSetupButton(t, browser, events, "//main", "web.ai.test")
	formalSetupButton(t, browser, events, "//*[@role='dialog']", "web.ai.test")
	code := "web.ai.auth_help"
	if pass {
		code = "web.ai.test_passed"
	}
	waitWebSmoke(t, browser, events, `(text) => document.querySelector('[role=dialog]')?.innerText.includes(text)`, smokeTranslation(t, locales.ZhCN, code))
}

func formalSetupAccountSequence(t *testing.T, accountID int64, eventName string) uint64 {
	t.Helper()
	files, err := filepath.Glob(fmt.Sprintf("/data/diagnostics/accounts/%d/events-*.jsonl", accountID))
	if err != nil {
		t.Fatal("读取合成本地账号诊断索引失败", err)
	}
	var highest uint64
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal("读取合成本地账号诊断失败", err)
		}
		lines := bytes.Split(data, []byte{'\n'})
		for index, line := range lines {
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			var event diagnostics.Event
			if err := json.Unmarshal(line, &event); err != nil {
				if index == len(lines)-1 {
					continue
				}
				t.Fatal("本地账号诊断格式无效")
			}
			if event.Sequence > highest && (eventName == "" || event.Event == eventName) {
				highest = event.Sequence
			}
		}
	}
	return highest
}
