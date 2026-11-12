package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/analysis/prompts"
	"github.com/Seaony/Kakatta/internal/config"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/store"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/log"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

type webSmokePage struct {
	name, path, heading, selector, text string
}

const webSmokeTimezone = "Asia/Shanghai"
const webSmokeUsername = "smoke-local"
const webSmokePassword = "local-smoke-only-password"

var webSmokeLanguages = []locales.Language{locales.ZhCN, locales.En, locales.Ja}

var webSmokePages = []webSmokePage{
	{"onboarding", "/onboarding", "web.onboarding.title", "main h2", ""},
	{"overview", "/overview", "overview.title", "main h2", "示例 · 轻薄本"},
	{"tasks", "/tasks", "tasks.title", "", "示例 · 轻薄本"},
	{"task-editor", "/tasks?task_id=2", "tasks.edit", "main form", ""},
	{"results", "/results", "results.title", "", "【示例】黑色定焦相机，箱说齐全"},
	{"result-detail", "/results?finding_id=4", "", "main h2", "【示例】轻薄本，毕业转让"},
	{"market", "/results?task_id=2", "results.title", "main h2", "@web.market.title"},
	{"favorites", "/results?collection=favorites", "results.title", "", "【示例】轻薄本，毕业转让"},
	{"compare", "/results/compare?ids=4,5", "web.compare.title", "main table", "【示例】轻薄本，正常使用痕迹"},
	{"logs", "/logs?run_id=2", "logs.title", "#logs-list-title", "示例 · 轻薄本"},
	{"accounts", "/accounts", "accounts.title", "main h2", ""},
	{"account-detail", "/accounts?account_id=1", "accounts.title", "main h2", "网页冒烟假账号"},
	{"settings-general", "/settings/general", "web.settings.general_title", "#setting-general-language", ""},
	{"settings-ai", "/settings/ai", "web.ai.title", "main h2", ""},
	{"settings-notifications", "/settings/notifications", "web.notify.title", "main h2", ""},
	{"settings-limits", "/settings/limits", "web.settings.limits_title", "#setting-crawl-global_pages", ""},
	{"settings-prompts", "/settings/prompts", "web.ai.prompts_title", "main textarea", ""},
	{"settings-security", "/settings/security", "admin.security.title", "main form", ""},
	{"settings-system", "/settings/system", "admin.system.title", "main h2", ""},
	{"settings-backup", "/settings/backup", "admin.backup.title", "main form", ""},
	{"settings-about", "/settings/about", "admin.about.title", "main h2", ""},
}

func TestWebSmoke(t *testing.T) {
	if os.Getenv("KAKATTA_WEB_SMOKE") != "1" {
		t.Skip("用 deploy/test-web-smoke.sh 在断网容器中运行")
	}
	interfaces, err := net.Interfaces()
	if err != nil || len(interfaces) != 1 || interfaces[0].Flags&net.FlagLoopback == 0 {
		t.Fatalf("网页冒烟只能在只有回环网卡的断网容器中运行：%v %v", interfaces, err)
	}
	chrome, err := exec.LookPath("google-chrome-stable")
	if err != nil {
		t.Fatal(err)
	}
	for _, viewport := range []struct {
		name          string
		width, height int64
		mobile        bool
	}{{"desktop", 1280, 900, false}, {"mobile", 393, 852, true}} {
		t.Run(viewport.name, func(t *testing.T) {
			base, diagnosticFiles := startWebSmokeServer(t)
			options := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(chrome))
			allocator, stopAllocator := chromedp.NewExecAllocator(t.Context(), options...)
			defer stopAllocator()
			browser, stopBrowser := chromedp.NewContext(allocator)
			defer stopBrowser()
			var events webSmokeEvents
			events.base = base
			events.checkSmokeAuth = true
			t.Cleanup(func() {
				events.mu.Lock()
				defer events.mu.Unlock()
				raw, err := json.MarshalIndent(map[string]any{"requests": events.authChecks, "inputs": events.inputChecks, "javascript_dialogs": events.jsDialogs, "clean_guards": events.cleanGuards}, "", "  ")
				if err == nil {
					err = os.WriteFile(filepath.Join("/screens", viewport.name, "authentication.json"), append(raw, '\n'), 0600)
				}
				if err != nil {
					t.Error("不能保存合成账号的认证检查", err)
				}
			})
			if _, err := chromedp.Run(browser, chromedp.Evaluate[string](`navigator.userAgent`)); err != nil {
				t.Fatal(err)
			}
			defer func() {
				ctx, cancel := context.WithTimeout(browser, 5*time.Second)
				defer cancel()
				if err := chromedp.Cancel(ctx); err != nil {
					t.Errorf("浏览器关闭失败：%v", err)
				}
			}()
			if err := chromedp.Listen(browser, events.receive); err != nil {
				t.Fatal(err)
			}
			runWebSmoke(t, browser, &events, chromedp.Func(func(ctx context.Context, _ *chromedp.Target) error {
				postSize := int64(4096)
				if _, err := chromedp.Call(ctx, network.Enable, network.EnableParams{MaxPostDataSize: &postSize}); err != nil {
					return err
				}
				if _, err := chromedp.Call(ctx, log.Enable, cdp.Empty{}); err != nil {
					return err
				}
				if _, err := chromedp.Call(ctx, emulation.SetTimezoneOverride, emulation.SetTimezoneOverrideParams{TimezoneID: "UTC"}); err != nil {
					return err
				}
				_, err := chromedp.Call(ctx, emulation.SetDeviceMetricsOverride, emulation.SetDeviceMetricsOverrideParams{
					Width: viewport.width, Height: viewport.height, ScreenWidth: viewport.width, ScreenHeight: viewport.height,
					DeviceScaleFactor: 1, Mobile: viewport.mobile,
				})
				return err
			}))
			runWebSmoke(t, browser, &events, smokeBrowserLanguage("zh-CN"))
			shots := filepath.Join("/screens", viewport.name)
			if err := os.MkdirAll(shots, 0755); err != nil {
				t.Fatal(err)
			}
			backup := ""
			checkPage := func(page webSmokePage, lang locales.Language, takeScreenshot bool) {
				t.Helper()
				t.Logf("%s %s", lang, page.path)
				visitWebSmokePage(t, browser, &events, page, lang, viewport.width)
				if page.name == "results" {
					found := smokeTranslationWithArgs(t, lang, "results.card.found", map[string]any{"time": ""})
					waitWebSmoke(t, browser, &events, `(label, zone) => [...document.querySelectorAll('main article span')].some(node =>
						node.textContent.startsWith(label) && node.textContent.endsWith(' (' + zone + ')'))`, found, webSmokeTimezone)
				}
				if page.name == "settings-prompts" && lang == locales.ZhCN {
					waitWebSmoke(t, browser, &events, `() => document.querySelector('nav a[href="/settings/prompts"]')?.textContent.trim() === '提示词' &&
						document.querySelector('main h1')?.textContent.trim() === '提示词模板'`)
				}
				if page.name == "settings-prompts" {
					waitWebSmoke(t, browser, &events, `(suffix, hint) => {
						const main = document.querySelector('main');
						return main.textContent.includes(suffix) && main.textContent.includes(hint) &&
							main.querySelectorAll('textarea').length === 1 && !main.querySelector('textarea').value.includes(suffix);
					}`, prompts.SellerDataSystemSuffix, smokeTranslation(t, lang, "web.ai.prompt_system_suffix_hint"))
				}
				if page.name == "result-detail" {
					waitWebSmoke(t, browser, &events, `(heading, status) => {
						const node = [...document.querySelectorAll('main h3')].find(node => node.textContent === heading);
						return node?.parentElement.querySelector('li')?.textContent.includes(status) &&
							node.parentElement.querySelector('li').textContent.includes('日常办公用轻薄本');
					}`, smokeTranslation(t, lang, "tasks.profile.musts"), smokeTranslation(t, lang, "web.detail.requirement.met"))
					waitWebSmoke(t, browser, &events, `(heading, note, falseLabel) => {
						const node = [...document.querySelectorAll('main h3')].find(node => node.textContent === heading);
						const section = node?.parentElement;
						return section?.textContent.includes(note) && [...section.querySelectorAll('dd')].some(x => x.textContent === '0') &&
							[...section.querySelectorAll('dd')].some(x => x.textContent === falseLabel) && section.textContent.includes('98%（样例原文）');
					}`, smokeTranslation(t, lang, "web.detail.seller_detail.title"), smokeTranslation(t, lang, "web.detail.seller_detail.note"), smokeTranslation(t, lang, "web.detail.seller_detail.auth_false"))
				}
				if takeScreenshot {
					writeWebSmokeScreenshot(t, browser, &events, filepath.Join(shots, page.name+"-"+string(lang)+".png"))
				}
				if page.name == "tasks" {
					assertWebSmokeTaskSearch(t, browser, &events, lang, viewport.width)
				}
				if page.name == "settings-prompts" || page.name == "result-detail" {
					heading := "web.ai.prompt_system_suffix"
					if page.name == "result-detail" {
						heading = "tasks.profile.musts"
					}
					runWebSmoke(t, browser, &events, chromedp.Evaluate[chromedp.Void](`(heading => [...document.querySelectorAll('main h2,main h3')].find(node => node.textContent === heading)?.scrollIntoView())(`+strconv.Quote(smokeTranslation(t, lang, heading))+`)`))
					writeWebSmokeScreenshot(t, browser, &events, filepath.Join(shots, page.name+"-evidence-"+string(lang)+".png"))
				}
				if page.name == "result-detail" {
					runWebSmoke(t, browser, &events, chromedp.Evaluate[chromedp.Void](`(heading => [...document.querySelectorAll('main h3')].find(node => node.textContent === heading)?.scrollIntoView())(`+strconv.Quote(smokeTranslation(t, lang, "web.detail.seller_detail.title"))+`)`))
					writeWebSmokeScreenshot(t, browser, &events, filepath.Join(shots, "seller-metrics-"+string(lang)+".png"))
				}
				if page.name == "settings-system" {
					checkWebSmokeDiagnostics(t, browser, &events, diagnosticFiles, lang, shots)
				}
				checkWebSmokeDialogs(t, browser, &events, page, lang, shots, backup)
				assertWebSmokeLayout(t, browser, &events)
			}
			const username, password = webSmokeUsername, webSmokePassword
			setup := webSmokePage{"setup", "/setup", "web.setup.title", "main input[name=username]", ""}
			for _, language := range webSmokeLanguages {
				browserLanguage := string(language)
				if language == locales.Ja {
					browserLanguage = "ja-JP"
				}
				runWebSmoke(t, browser, &events, smokeBrowserLanguage(browserLanguage))
				checkPage(setup, language, true)
			}
			runWebSmoke(t, browser, &events, smokeBrowserLanguage("fr-FR"))
			checkPage(setup, locales.En, false)
			runWebSmoke(t, browser, &events, smokeBrowserLanguage("zh-CN"))
			checkPage(setup, locales.ZhCN, false)
			fillWebSmokeInput(t, browser, &events, `input[name=username]`, username)
			fillWebSmokeInput(t, browser, &events, `input[name=password]`, password)
			runWebSmoke(t, browser, &events,
				chromedp.Click(chromedp.CSS(`main button[type=submit]`)),
				chromedp.WaitVisible(chromedp.CSS(`main h2`)),
			)
			runWebSmoke(t, browser, &events, chromedp.Func(func(ctx context.Context, _ *chromedp.Target) error {
				_, err := chromedp.Call(ctx, network.ClearBrowserCookies, cdp.Empty{})
				return err
			}))
			login := webSmokePage{"login", "/login", "web.auth.sign_in", "main input[name=username]", ""}
			for _, language := range webSmokeLanguages {
				browserLanguage := string(language)
				if language == locales.Ja {
					browserLanguage = "ja-JP"
				}
				runWebSmoke(t, browser, &events, smokeBrowserLanguage(browserLanguage))
				checkPage(login, language, true)
			}
			runWebSmoke(t, browser, &events, smokeBrowserLanguage("zh-CN"))
			checkPage(login, locales.ZhCN, false)
			fillWebSmokeInput(t, browser, &events, `input[name=username]`, username)
			fillWebSmokeInput(t, browser, &events, `input[name=password]`, password)
			runWebSmoke(t, browser, &events,
				chromedp.Click(chromedp.CSS(`main button[type=submit]`)),
				chromedp.WaitVisible(chromedp.CSS(`main h2`)),
			)
			backup = seedWebSmokeConfigurations(t, base, username, password)
			for _, language := range webSmokeLanguages {
				switchWebSmokeLanguage(t, browser, &events, language)
				for _, page := range webSmokePages {
					checkPage(page, language, true)
				}
				clickWebSmokeButton(t, browser, &events, smokeTranslation(t, language, "admin.about.show_docs"))
				waitWebSmoke(t, browser, &events, `() => [...document.querySelectorAll('main summary code')].some(node => node.textContent === '/api/v1/setup')`)
				writeWebSmokeScreenshot(t, browser, &events, filepath.Join(shots, "openapi-"+string(language)+".png"))
				assertWebSmokeLayout(t, browser, &events)
			}
		})
	}
}

func startWebSmokeServer(t *testing.T) (string, webSmokeDiagnosticFiles) {
	t.Helper()
	data := t.TempDir()
	seed := exec.CommandContext(t.Context(), "/tests/seed", "--data", data)
	seed.Env = append(seed.Environ(), "TZ="+webSmokeTimezone)
	if output, err := seed.CombinedOutput(); err != nil {
		t.Fatalf("样例数据库生成失败：%v %s", err, output)
	}
	seedWebSmokeSellerMetrics(t, data)
	diagnosticFiles := seedWebSmokeDiagnostics(t, data)
	environment := settings.Environment{TZ: webSmokeTimezone}
	prepareOfflineRuntime(t, data, environment)
	ctx, cancel := context.WithCancel(t.Context())
	logs := &observedLog{address: make(chan string, 1)}
	finished := make(chan error, 1)
	go func() {
		finished <- serve(ctx, config.Config{DataDir: data, Listen: "127.0.0.1:0", Environment: environment},
			func(string) (string, bool) { return "", false }, slog.New(slog.NewJSONHandler(logs, nil)))
	}()
	var address string
	select {
	case address = <-logs.address:
	case err := <-finished:
		cancel()
		t.Fatalf("网页测试服务未启动：%v", err)
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("网页测试服务启动超时")
	}
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-finished:
			if err != nil {
				t.Errorf("网页测试服务关闭失败：%v", err)
			}
		case <-time.After(35 * time.Second):
			t.Error("网页测试服务关闭超时")
		}
	})
	return "http://" + address, diagnosticFiles
}

func seedWebSmokeSellerMetrics(t *testing.T, data string) {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(data, "kakatta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.DB().ExecContext(t.Context(), `UPDATE listings SET seller_id='web-smoke-seller',seller_detail_json=? WHERE item_id=(SELECT item_id FROM findings WHERE id=4)`, `{"item_count":0,"has_sold_num_integer":8,"avg_reply_30d_long":1.25,"new_good_ratio_rate":"98%（样例原文）","last_visit_time":"刚刚来过（样例原文）","idle_fish_credit_level":"L4（未确认）","zhima_auth":false}`)
	if err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := db.DB().QueryRowContext(t.Context(), `SELECT profile_json FROM tasks WHERE id=(SELECT task_id FROM findings WHERE id=4)`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var profile analysis.Profile
	if err := json.Unmarshal([]byte(raw), &profile); err != nil {
		t.Fatal(err)
	}
	profile.Specs = []analysis.ProfileSpec{{Name: "内存（网页样例）", Options: []string{"16GB"}, Values: []string{"16GB"}, Source: analysis.SourceUser, Edited: true}}
	encoded, err := json.Marshal(profile)
	if err == nil {
		_, err = db.DB().ExecContext(t.Context(), `UPDATE tasks SET profile_json=? WHERE id=(SELECT task_id FROM findings WHERE id=4)`, string(encoded))
	}
	if err != nil {
		t.Fatal(err)
	}
}

type webSmokeEvents struct {
	mu             sync.Mutex
	base, page     string
	allowSignedOut bool
	allowedHTTP    map[string]int
	readOnly       bool
	issues         []string
	pending        map[network.RequestID]struct{}
	lastActivity   time.Time
	authChecks     []webSmokeAuthCheck
	inputChecks    []webSmokeInputCheck
	checkSmokeAuth bool
	jsDialogs      []string
	cleanGuards    []webSmokeCleanGuard
}

type webSmokeCleanGuard struct {
	Language  locales.Language `json:"language"`
	ElapsedMS int64            `json:"elapsed_ms"`
	Released  bool             `json:"released"`
}

type webSmokeInputCheck struct {
	Selector     string `json:"selector"`
	BeforeLength int    `json:"before_length"`
	AfterLength  int    `json:"after_length"`
	Matches      bool   `json:"matches"`
	Appended     bool   `json:"appended"`
	MissingFirst bool   `json:"missing_first"`
	BeforeFocus  string `json:"before_focus"`
	AfterFocus   string `json:"after_focus"`
	SameNode     bool   `json:"same_node"`
	TargetFocus  bool   `json:"target_focused"`
}

type webSmokeAuthCheck struct {
	Path            string `json:"path"`
	Observed        bool   `json:"observed"`
	UsernameMatches bool   `json:"username_matches"`
	PasswordMatches bool   `json:"password_matches"`
	UsernameLength  int    `json:"username_length"`
	PasswordLength  int    `json:"password_length"`
}

func (events *webSmokeEvents) receive(event any) {
	events.mu.Lock()
	defer events.mu.Unlock()
	var issue string
	switch value := event.(type) {
	case *network.EventRequestWillBeSent:
		if events.checkSmokeAuth && value.Request.Method == http.MethodPost && (value.Request.URL == events.base+"/api/v1/setup" || value.Request.URL == events.base+"/api/v1/auth/login") {
			var raw []byte
			for _, entry := range value.Request.PostDataEntries {
				raw = append(raw, entry.Bytes...)
			}
			var submitted struct{ Username, Password string }
			observed := json.Unmarshal(raw, &submitted) == nil
			check := webSmokeAuthCheck{Path: strings.TrimPrefix(value.Request.URL, events.base), Observed: observed,
				UsernameMatches: submitted.Username == webSmokeUsername, PasswordMatches: submitted.Password == webSmokePassword,
				UsernameLength: len(submitted.Username), PasswordLength: len(submitted.Password)}
			events.authChecks = append(events.authChecks, check)
			if observed && (!check.UsernameMatches || !check.PasswordMatches) {
				issue = fmt.Sprintf("合成账号认证请求与测试输入不符：用户名匹配=%t，密码匹配=%t", check.UsernameMatches, check.PasswordMatches)
			}
		}
		if events.readOnly && value.Request.Method != http.MethodGet && value.Request.Method != http.MethodHead {
			issue = "诊断页面发起了写请求"
		}
		address, err := url.Parse(value.Request.URL)
		if err != nil || ((address.Scheme == "http" || address.Scheme == "https") && !strings.HasPrefix(value.Request.URL, events.base+"/")) {
			issue = "请求离开本地测试服务"
		} else if strings.HasPrefix(value.Request.URL, events.base+"/") && address.Path != "/api/v1/events" {
			if events.pending == nil {
				events.pending = make(map[network.RequestID]struct{})
			}
			events.pending[value.RequestID] = struct{}{}
			events.lastActivity = time.Now()
		}
	case *network.EventResponseReceived:
		response := value.Response
		if response.Status >= 400 && events.allowedHTTP[response.URL] != int(response.Status) && !(events.allowSignedOut && response.Status == 401 && response.URL == events.base+"/api/v1/me") {
			issue = fmt.Sprintf("HTTP %d %s", int(response.Status), strings.TrimPrefix(response.URL, events.base))
		}
	case *network.EventLoadingFinished:
		events.finish(value.RequestID)
	case *network.EventLoadingFailed:
		if events.finish(value.RequestID) && !value.Canceled {
			issue = "页面请求未完成：" + value.ErrorText
		}
	case *runtime.EventExceptionThrown:
		issue = value.ExceptionDetails.Text
		if exception := value.ExceptionDetails.Exception; exception != nil {
			issue += ": " + exception.Description
		}
	case *runtime.EventConsoleAPICalled:
		if value.Type == runtime.ConsoleAPICalledTypeError || value.Type == runtime.ConsoleAPICalledTypeAssert {
			issue = "控制台 " + string(value.Type)
			for _, argument := range value.Args {
				issue += " " + string(argument.Value) + argument.Description
			}
		}
	case *log.EventEntryAdded:
		entry := value.Entry
		if entry.Level == log.EntryLevelError && !(entry.Source == log.EntrySourceNetwork && events.allowedHTTP[entry.URL] != 0 &&
			strings.Contains(entry.Text, strconv.Itoa(events.allowedHTTP[entry.URL]))) && !(events.allowSignedOut && entry.Source == log.EntrySourceNetwork &&
			entry.URL == events.base+"/api/v1/me" && strings.Contains(entry.Text, "401")) {
			issue = entry.Text
		}
	case *page.EventJavascriptDialogOpening:
		events.jsDialogs = append(events.jsDialogs, string(value.Type)+" "+strings.TrimPrefix(value.URL, events.base))
	}
	if issue != "" {
		events.issues = append(events.issues, events.page+"："+issue)
	}
}

func (events *webSmokeEvents) finish(id network.RequestID) bool {
	if _, pending := events.pending[id]; !pending {
		return false
	}
	delete(events.pending, id)
	events.lastActivity = time.Now()
	return true
}

func (events *webSmokeEvents) assert(t *testing.T) {
	t.Helper()
	events.mu.Lock()
	defer events.mu.Unlock()
	if len(events.issues) != 0 {
		t.Fatalf("网页请求或控制台错误：\n%s", strings.Join(events.issues, "\n"))
	}
}

func visitWebSmokePage(t *testing.T, browser context.Context, events *webSmokeEvents, page webSmokePage, lang locales.Language, width int64) {
	t.Helper()
	events.assert(t)
	events.mu.Lock()
	events.page, events.allowSignedOut = page.path, page.path == "/login"
	events.mu.Unlock()
	runWebSmoke(t, browser, events, chromedp.Navigate(events.base+page.path))
	heading := smokeTranslation(t, lang, page.heading)
	content := page.text
	if code, translated := strings.CutPrefix(content, "@"); translated {
		content = smokeTranslation(t, lang, code)
	}
	path, _, _ := strings.Cut(page.path, "?")
	waitWebSmoke(t, browser, events, `(path, lang, width, heading, selector, content) => {
		const main = document.querySelector('main');
		const title = main?.querySelector('h1');
		const marker = selector && main && document.querySelector(selector);
		return location.pathname === path && document.documentElement.lang === lang && screen.width === width &&
			!!title?.getClientRects().length && (!heading || title.textContent.trim() === heading) &&
			(!selector || !!marker?.getClientRects().length) && (!content || main.innerText.includes(content)) &&
			![...main.querySelectorAll('section[aria-busy=true]')].some(node => node.getClientRects().length);
	}`, path, string(lang), width, heading, page.selector, content)
}

func switchWebSmokeLanguage(t *testing.T, browser context.Context, events *webSmokeEvents, language locales.Language) {
	t.Helper()
	events.mu.Lock()
	events.page = "/settings/general"
	events.mu.Unlock()
	runWebSmoke(t, browser, events, chromedp.Navigate(events.base+"/settings/general"), chromedp.WaitVisible(chromedp.CSS(`#setting-general-language select`)))
	index, err := chromedp.Run(browser, chromedp.Evaluate[int](`(language => [...document.querySelector('#setting-general-language select').options].findIndex(option => option.value === language))(`+strconv.Quote(string(language))+`)`))
	if err != nil || index < 0 {
		t.Fatalf("语言选项缺失：%s %v", language, err)
	}
	runWebSmoke(t, browser, events,
		chromedp.SendKeys(chromedp.CSS(`#setting-general-language select`), kb.Home+strings.Repeat(kb.ArrowDown, index)+kb.Enter),
		chromedp.Click(chromedp.CSS(`main form button[type=submit]:not(:disabled)`)),
	)
	waitWebSmoke(t, browser, events, `(language) => document.documentElement.lang === language && document.querySelector('#setting-general-language select')?.value === language`, string(language))
}

func smokeBrowserLanguage(lang string) chromedp.Action[chromedp.Void] {
	return chromedp.Func(func(ctx context.Context, _ *chromedp.Target) error {
		agent, err := chromedp.Run(ctx, chromedp.Evaluate[string](`navigator.userAgent`))
		if err != nil {
			return err
		}
		_, err = chromedp.Call(ctx, emulation.SetUserAgentOverride, emulation.SetUserAgentOverrideParams{UserAgent: agent, AcceptLanguage: lang})
		return err
	})
}

func smokeTranslation(t *testing.T, lang locales.Language, code string) string {
	t.Helper()
	if code == "" {
		return ""
	}
	return smokeTranslationWithArgs(t, lang, code, nil)
}

func smokeTranslationWithArgs(t *testing.T, lang locales.Language, code string, args map[string]any) string {
	t.Helper()
	result, err := locales.Format(lang, locales.Message{Code: code, Args: args})
	if err != nil {
		t.Fatalf("%s %s：%v", lang, code, err)
	}
	return result
}

func clickWebSmokeButton(t *testing.T, browser context.Context, events *webSmokeEvents, text string) {
	t.Helper()
	selector := `//main//button[normalize-space(.)=` + strconv.Quote(text) + `]`
	runWebSmoke(t, browser, events, chromedp.Click(chromedp.Search(selector)))
}

func fillWebSmokeInput(t *testing.T, browser context.Context, events *webSmokeEvents, selector, value string) {
	t.Helper()
	beforeFocus, err := chromedp.Run(browser, chromedp.Evaluate[string](`(selector => {
		window.__webSmokeInputNode = document.querySelector(selector);
		const node = document.activeElement;
		return node ? node.tagName + '#' + node.id + '[name=' + (node.getAttribute('name') ?? '') + ']' : '';
	})(`+strconv.Quote(selector)+`)`))
	if err != nil {
		t.Fatal(err)
	}
	before, err := chromedp.Run(browser, chromedp.Value(chromedp.CSS(selector)))
	if err != nil {
		t.Fatal(err)
	}
	runWebSmoke(t, browser, events, chromedp.SendKeys(chromedp.CSS(selector), value))
	after, err := chromedp.Run(browser, chromedp.Value(chromedp.CSS(selector)))
	if err != nil {
		t.Fatal(err)
	}
	check := webSmokeInputCheck{Selector: selector, BeforeLength: len(before), AfterLength: len(after), Matches: after == value,
		Appended: before != "" && after == before+value, MissingFirst: len(value) > 0 && after == value[1:], BeforeFocus: beforeFocus}
	focus, err := chromedp.Run(browser, chromedp.Evaluate[struct {
		Name    string `json:"name"`
		Same    bool   `json:"same"`
		Focused bool   `json:"focused"`
	}](`(selector => {
		const active = document.activeElement;
		return {name: active ? active.tagName + '#' + active.id + '[name=' + (active.getAttribute('name') ?? '') + ']' : '',
			same: document.querySelector(selector) === window.__webSmokeInputNode, focused: active === window.__webSmokeInputNode};
	})(`+strconv.Quote(selector)+`)`))
	if err != nil {
		t.Fatal(err)
	}
	check.AfterFocus, check.SameNode, check.TargetFocus = focus.Name, focus.Same, focus.Focused
	events.mu.Lock()
	events.inputChecks = append(events.inputChecks, check)
	events.mu.Unlock()
	if !check.Matches {
		t.Fatalf("合成账号输入不符：%s 输入前%d字节、输入后%d字节、预期%d字节，追加已有内容=%t、缺首字符=%t、同一节点=%t、目标仍聚焦=%t、前=%s、后=%s", selector, len(before), len(after), len(value), check.Appended, check.MissingFirst, check.SameNode, check.TargetFocus, check.BeforeFocus, check.AfterFocus)
	}
}

func runWebSmoke(t *testing.T, browser context.Context, events *webSmokeEvents, actions ...chromedp.Action[chromedp.Void]) {
	t.Helper()
	ctx, cancel := context.WithTimeout(browser, 12*time.Second)
	defer cancel()
	for _, action := range actions {
		_, err := chromedp.Run(ctx, action)
		events.assert(t)
		if err != nil {
			capture, stop := context.WithTimeout(browser, time.Second)
			if shot, shotErr := chromedp.Run(capture, chromedp.CaptureScreenshot()); shotErr == nil {
				path := filepath.Join("/screens", "failure-"+strconv.FormatInt(time.Now().UnixNano(), 10)+".png")
				if os.WriteFile(path, shot, 0644) == nil {
					t.Log("失败截图：", path)
				}
			}
			stop()
			events.mu.Lock()
			dialogs := append([]string{}, events.jsDialogs...)
			events.mu.Unlock()
			t.Log("原生弹窗记录：", dialogs)
			t.Fatalf("%s 操作失败：%v", events.page, err)
		}
	}
}

func waitWebSmoke(t *testing.T, browser context.Context, events *webSmokeEvents, predicate string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(browser, 10*time.Second)
	defer cancel()
	_, err := chromedp.Run(ctx, chromedp.PollFunction[bool](predicate, chromedp.WithPollingArgs(args...), chromedp.WithPollingInterval(40*time.Millisecond)))
	events.assert(t)
	if err != nil {
		state, captureErr := webSmokeFailureState(browser)
		t.Fatalf("%s 等待条件未满足：%v；现场读取：%v %s", events.page, err, captureErr, state)
	}
	// 等本页请求完成，别在切到下一页时把慢响应取消掉。
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		events.assert(t)
		events.mu.Lock()
		idle := len(events.pending) == 0 && time.Since(events.lastActivity) >= 75*time.Millisecond
		events.mu.Unlock()
		if idle {
			return
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			events.mu.Lock()
			pending := make([]string, 0, len(events.pending))
			for id := range events.pending {
				pending = append(pending, string(id))
			}
			idleFor := time.Since(events.lastActivity)
			events.mu.Unlock()
			sort.Strings(pending)
			t.Fatalf("%s 页面请求未在时限内结束，未结束请求：%v，距最后请求活动：%s", events.page, pending, idleFor)
		}
	}
}

func webSmokeFailureState(browser context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(browser, time.Second)
	defer cancel()
	return chromedp.Run(ctx, chromedp.Evaluate[string](`JSON.stringify({path:location.pathname+location.search,lang:document.documentElement.lang,width:screen.width,title:document.querySelector('main h1')?.textContent,text:document.querySelector('main')?.innerText.slice(0, 250),active:{tag:document.activeElement?.tagName,id:document.activeElement?.id,name:document.activeElement?.getAttribute('name'),text:document.activeElement?.tagName === 'BUTTON' ? document.activeElement.textContent.trim().slice(0,80) : undefined},dialogs:[...document.querySelectorAll('[role=dialog],[role=alertdialog]')].map(node => ({role:node.getAttribute('role'),title:node.querySelector('h2')?.textContent}))})`))
}

func writeWebSmokeScreenshot(t *testing.T, browser context.Context, events *webSmokeEvents, path string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(browser, 5*time.Second)
	defer cancel()
	shot, err := chromedp.Run(ctx, chromedp.CaptureScreenshot())
	events.assert(t)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, shot, 0644); err != nil {
		t.Fatal(err)
	}
}
