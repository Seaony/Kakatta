package main

import (
	"archive/zip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/locales"
	browserproto "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"
)

func formalFailures(t *testing.T, browser context.Context, events *webSmokeEvents, _ formalMetadata) {
	taskID := formalTaskID(t)
	formalControl(t, "model", map[string]any{"mode": "timeout"})
	formalSearchWithNewItem(t, "107", "MacBook Pro 模型超时样本")
	formalRunOnce(t, browser, events)
	if formalScalar(t, `SELECT count(*) FROM findings WHERE task_id=? AND item_id='107' AND decision='failed' AND retryable=1 AND json_extract(reason,'$.code')='analysis.client.timeout'`, taskID) != 1 {
		t.Fatal("模型超时没有保留可恢复的原发现")
	}
	findingID := formalScalar(t, "SELECT id FROM findings WHERE task_id=? AND item_id='107'", taskID)
	detailAt := formalScalar(t, "SELECT detail_fetched_at FROM listings WHERE item_id='107'")
	formalFinding(t, browser, events, findingID)
	waitWebSmoke(t, browser, events, `() => document.querySelector('main').innerText.includes('请求超时')`)
	formalSnapshot(t, browser, events, "07-model-timeout")
	formalControl(t, "model", map[string]any{"mode": "ok"})
	formalRunOnce(t, browser, events)
	if formalScalar(t, "SELECT count(*) FROM findings WHERE id=? AND decision='scored' AND retryable=0", findingID) != 1 ||
		formalScalar(t, "SELECT detail_fetched_at FROM listings WHERE item_id='107'") != detailAt {
		t.Fatal("模型恢复没有继续原发现或重复抓取了已缓存详情")
	}

	formalControl(t, "push", map[string]any{"mode": "fail"})
	formalSearchWithNewItem(t, "108", "MacBook Pro 推送失败样本")
	formalRunOnce(t, browser, events)
	formalWait(t, browser, "失败推送回执落盘", 30*time.Second, func() bool {
		return formalScalar(t, `SELECT count(*) FROM deliveries d JOIN delivery_items di ON di.delivery_id=d.id JOIN findings f ON f.id=di.finding_id WHERE f.item_id='108' AND d.ok=0`) > 0
	})
	formalVisit(t, browser, events, "/settings/notifications", "web.notify.title")
	clickWebSmokeButton(t, browser, events, smokeTranslation(t, locales.ZhCN, "web.notify.history_recent"))
	waitWebSmoke(t, browser, events, `() => document.querySelector('[role=dialog]')?.innerText.includes('503') && document.querySelector('[role=dialog]').innerText.includes('失败')`)
	formalSnapshot(t, browser, events, "07-push-failure")
	formalCloseDialog(t, browser, events)
	formalControl(t, "push", map[string]any{"mode": "ok"})
	formalRunOnce(t, browser, events)
	formalWait(t, browser, "原推送意图恢复送达", 90*time.Second, func() bool {
		return formalScalar(t, `SELECT count(*) FROM deliveries d JOIN delivery_items di ON di.delivery_id=d.id JOIN findings f ON f.id=di.finding_id WHERE f.item_id='108' AND d.ok=1`) == 1
	})

	formalControl(t, "risk", map[string]any{"mode": "expired"})
	runID := formalRunOnce(t, browser, events)
	if formalText(t, "SELECT failure_kind FROM run_records WHERE id=?", runID) != "login" {
		t.Fatal("登录失效没有记录为正确的运行失败")
	}
	formalVisit(t, browser, events, "/accounts?account_id=1", "accounts.title")
	waitWebSmoke(t, browser, events, `(label) => document.querySelector('main').innerText.includes('已失效') && [...document.querySelectorAll('main button')].some(button => button.textContent.trim() === label)`, smokeTranslation(t, locales.ZhCN, "accounts.relogin"))
	formalSnapshot(t, browser, events, "07-login-expired")
	formalControl(t, "risk", map[string]any{"mode": "none"})
	clickWebSmokeButton(t, browser, events, smokeTranslation(t, locales.ZhCN, "accounts.relogin"))
	formalWait(t, browser, "网页重新登录完成", 45*time.Second, func() bool {
		return formalScalar(t, "SELECT count(*) FROM accounts WHERE id=1 AND state='active'") == 1 &&
			formalScalar(t, "SELECT count(*) FROM meta WHERE key='runtime.session.1'") == 0
	})

	formalControl(t, "risk", map[string]any{"mode": "verify"})
	before := formalFixtureState(t)
	beforeModels := formalModelCalls(t, before)
	beforeDetails := formalCountFromState(t, before, "detail_count")
	runID = formalRunOnce(t, browser, events)
	if formalText(t, "SELECT failure_kind FROM run_records WHERE id=?", runID) != "risk_verify" ||
		formalScalar(t, "SELECT count(*) FROM accounts WHERE id=1 AND state='needs_verification'") != 1 {
		t.Fatal("风控没有停手并保留人工验证状态")
	}
	after := formalFixtureState(t)
	if formalModelCalls(t, after) != beforeModels || formalCountFromState(t, after, "detail_count") != beforeDetails {
		t.Fatal("风控停手后仍继续读取详情或请求模型")
	}
	formalShowRun(t, browser, events, runID)
	waitWebSmoke(t, browser, events, `() => document.querySelector('main').innerText.includes('需要验证')`)
	formalSnapshot(t, browser, events, "07-risk-stopped")
	formalVisit(t, browser, events, "/accounts?account_id=1", "accounts.title")
	clickWebSmokeButton(t, browser, events, smokeTranslation(t, locales.ZhCN, "accounts.verify"))
	formalRemoteConnected(t, browser, events)
	formalControl(t, "risk", map[string]any{"mode": "none"})
	searches := formalCountFromState(t, formalFixtureState(t), "search_count")
	formalRefreshRemote(t, browser, events)
	formalWait(t, browser, "用户通过远程画面刷新假验证页", 15*time.Second, func() bool {
		return formalCountFromState(t, formalFixtureState(t), "search_count") > searches
	})
	clickWebSmokeButton(t, browser, events, smokeTranslation(t, locales.ZhCN, "remote.confirm_verification"))
	formalWait(t, browser, "验证完成并保留冷却", 30*time.Second, func() bool {
		return formalScalar(t, "SELECT count(*) FROM accounts WHERE id=1 AND state='cooling'") == 1 &&
			formalScalar(t, "SELECT count(*) FROM meta WHERE key='runtime.session.1'") == 0
	})
	waitWebSmoke(t, browser, events, `() => document.querySelector('main').innerText.includes('冷却')`)
	formalSnapshot(t, browser, events, "07-verified-cooling")
	formalDiagnostics(t, browser, events)
	formalWait(t, browser, "保持真实时钟等待测试配置的一分钟冷却结束", 90*time.Second, func() bool {
		return formalScalar(t, "SELECT COALESCE(cooldown_until,0) FROM accounts WHERE id=1") <= time.Now().UnixMilli()
	})
	formalRecord(t, 7, "网页可定位登录失效、模型请求超时、推送503和风控停手；重新登录、缓存分析重试、原推送重试与远程手动验证走通，冷却未绕过，真实故障诊断ZIP可下载")
}

func formalModelCalls(t *testing.T, state map[string]any) int64 {
	t.Helper()
	counts, ok := state["model_counts"].(map[string]any)
	if !ok {
		t.Fatal("本地模型回执缺少用途计数")
	}
	return formalCountFromState(t, counts, "analysis") + formalCountFromState(t, counts, "title_screen")
}

func formalFinding(t *testing.T, browser context.Context, events *webSmokeEvents, id int64) {
	t.Helper()
	visitWebSmokePage(t, browser, events, webSmokePage{name: "formal-finding", path: fmt.Sprintf("/results?finding_id=%d", id), selector: "main h2"}, locales.ZhCN, 1280)
}

func formalCloseDialog(t *testing.T, browser context.Context, events *webSmokeEvents) {
	t.Helper()
	selector := `//*[@role='dialog']//button[normalize-space(.)=` + strconv.Quote(smokeTranslation(t, locales.ZhCN, "common.close")) + `]`
	runWebSmoke(t, browser, events, chromedp.Click(chromedp.Search(selector)))
	waitWebSmoke(t, browser, events, `() => !document.querySelector('[role=dialog]')`)
}

func formalRemoteConnected(t *testing.T, browser context.Context, events *webSmokeEvents) {
	t.Helper()
	waitWebSmoke(t, browser, events, `() => {
		const doc = document.querySelector('main iframe')?.contentDocument;
		return doc?.querySelector('#status')?.textContent === '已连接' && doc.querySelector('canvas')?.width > 0;
	}`)
}

func formalRefreshRemote(t *testing.T, browser context.Context, events *webSmokeEvents) {
	t.Helper()
	// 按键发给网页里的 noVNC，再由 X 服务器交给账号浏览器。
	runWebSmoke(t, browser, events, chromedp.Evaluate[chromedp.Void](`document.querySelector('main iframe').contentDocument.querySelector('canvas').focus()`),
		chromedp.Func(func(ctx context.Context, _ *chromedp.Target) error {
			for _, key := range []struct {
				key, code string
				vk, mods  int64
				down      bool
			}{{"Control", "ControlLeft", 17, 2, true}, {"r", "KeyR", 82, 2, true}, {"r", "KeyR", 82, 2, false}, {"Control", "ControlLeft", 17, 0, false}} {
				kind := input.DispatchKeyEventTypeKeyUp
				if key.down {
					kind = input.DispatchKeyEventTypeRawKeyDown
				}
				if _, err := chromedp.Call(ctx, input.DispatchKeyEvent, input.DispatchKeyEventParams{Type: kind, Key: key.key, Code: key.code, WindowsVirtualKeyCode: key.vk, NativeVirtualKeyCode: key.vk, Modifiers: key.mods}); err != nil {
					return err
				}
			}
			return nil
		}))
}

func formalDiagnostics(t *testing.T, browser context.Context, events *webSmokeEvents) {
	t.Helper()
	if err := os.MkdirAll("/work/downloads", 0700); err != nil {
		t.Fatal(err)
	}
	runWebSmoke(t, browser, events, chromedp.Func(func(ctx context.Context, _ *chromedp.Target) error {
		_, err := chromedp.Call(ctx, browserproto.SetDownloadBehavior, browserproto.SetDownloadBehaviorParams{Behavior: browserproto.SetDownloadBehaviorBehaviorAllow, DownloadPath: "/work/downloads"})
		return err
	}))
	formalVisit(t, browser, events, "/settings/system", "admin.system.title")
	clickWebSmokeButton(t, browser, events, smokeTranslation(t, locales.ZhCN, "admin.diagnostics.export"))
	waitWebSmoke(t, browser, events, `() => document.querySelector('[role=dialog]')?.querySelectorAll('li').length > 0 && [...document.querySelectorAll('[role=dialog] button')].some(button => button.textContent.includes('下载') && !button.disabled)`)
	formalSnapshot(t, browser, events, "07-diagnostic-preview")
	runWebSmoke(t, browser, events, chromedp.Click(chromedp.Search(`//*[@role='dialog']//button[normalize-space(.)=`+strconv.Quote(smokeTranslation(t, locales.ZhCN, "admin.diagnostics.download"))+`]`)))
	var downloaded string
	formalWait(t, browser, "浏览器实际保存诊断ZIP", 30*time.Second, func() bool {
		files, _ := filepath.Glob("/work/downloads/*.zip")
		if len(files) == 1 {
			downloaded = files[0]
			return true
		}
		return false
	})
	archive, err := zip.OpenReader(downloaded)
	if err != nil {
		t.Fatal("下载的诊断不是有效ZIP", err)
	}
	defer archive.Close()
	var page, summary, log bool
	for _, file := range archive.File {
		name := filepath.ToSlash(file.Name)
		if strings.Contains(name, "browser/") || strings.Contains(name, "Cookies") || filepath.Base(name) == "secret.key" || strings.HasSuffix(name, ".db") {
			t.Fatal("诊断ZIP混入浏览器登录资料或数据库")
		}
		page = page || strings.HasSuffix(name, "page.html")
		summary = summary || strings.HasSuffix(name, "summary.json")
		log = log || strings.HasSuffix(name, ".jsonl")
	}
	if !page || !summary || !log {
		t.Fatal("诊断ZIP缺少本轮真实假页故障现场、摘要或事件")
	}
}
