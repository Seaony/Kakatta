package web

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/Seaony/Kakatta/internal/browser"
	"github.com/Seaony/Kakatta/internal/pacer"
	"github.com/Seaony/Kakatta/internal/quota"
	"github.com/Seaony/Kakatta/internal/xianyu"
	"github.com/Seaony/Kakatta/internal/xianyu/fake"
)

func newFake(t *testing.T) *fake.Xianyu {
	f := fake.Start()
	t.Cleanup(f.Close)
	return f
}

// countingBudget 记下每次申请额度，用来核对计数口径。
type countingBudget struct{ got []string }

func (b *countingBudget) Acquire(kind quota.Kind, what string) error {
	b.got = append(b.got, string(kind)+":"+what)
	return nil
}

func fastPace() Pace {
	r := pacer.Between(5*time.Millisecond, 10*time.Millisecond)
	return Pace{HomeStay: r, Action: r, PageWait: r, PreDetail: r, Detail: r}
}

func startRunner(t *testing.T, f *fake.Xianyu) (*Runner, *countingBudget, context.Context) {
	t.Helper()
	if os.Getenv("KAKATTA_BROWSER_TESTS") != "1" {
		t.Skip("浏览器集成测试需设置 KAKATTA_BROWSER_TESTS=1，并在隔离的容器中运行")
	}
	if _, err := browser.FindChrome(); err != nil {
		t.Skip("本机没有 Chrome：", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	s, err := browser.Start(ctx, browser.Options{ProfileDir: t.TempDir(), Width: 1280, Height: 900})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	site := Site{Home: f.Home(), APIHost: f.APIHost()}
	b := &countingBudget{}
	r := NewRunner(s, pacer.New(nil), pacer.Between(5*time.Millisecond, 10*time.Millisecond), site, fastPace(), b, nil)
	return r, b, ctx
}

// 完整走一遍：首页 → 搜索框输入中文关键词 → 选「最新」→ 解析结果 → 打开详情。
func TestSearchAndDetailFlow(t *testing.T) {
	f := newFake(t)
	r, budget, ctx := startRunner(t, f)

	if err := r.OpenHome(ctx); err != nil {
		t.Fatal(err)
	}
	if y, err := chromedp.Run(r.S.Context(), chromedp.Evaluate[float64](`window.scrollY`)); err != nil || y <= 0 {
		t.Fatalf("首页应该被滚轮滚动过：scrollY=%v %v", y, err)
	}
	facts := r.InputFacts(ctx)
	if len(facts) == 0 || facts[0].Placeholder != "搜索闲鱼" {
		t.Fatalf("没有记录到首页搜索框：%+v", facts)
	}
	if _, err := r.Search(ctx, "MacBook 苹果"); err != nil {
		t.Fatal(err)
	}
	resp, err := r.SelectPublished(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.URL, "final=1") {
		t.Fatalf("应该取筛选之后的响应，拿到的是：%s", resp.URL)
	}
	items, err := xianyu.ParseSearch(resp.Body)
	if err != nil || len(items) != 2 {
		t.Fatalf("解析搜索结果：%v，%d 项", err, len(items))
	}
	if !strings.Contains(f.SearchBodies()[0], "MacBook%20") && !strings.Contains(f.SearchBodies()[0], "MacBook+") {
		t.Fatalf("页面收到的关键词不对：%q", f.SearchBodies()[0])
	}

	page2, ok, err := r.NextPage(ctx)
	if err != nil || !ok || !strings.Contains(page2.URL, "page=2") {
		t.Fatalf("翻到第 2 页：ok=%v err=%v", ok, err)
	}
	if _, ok, err := r.NextPage(ctx); ok || err != nil {
		t.Fatalf("没有下一页时应该返回 ok=false：ok=%v err=%v", ok, err)
	}

	detailURL := items[0].WebURL
	d, err := r.OpenDetail(ctx, detailURL)
	if err != nil {
		t.Fatal(err)
	}
	if d.SellerID != "998" || len(d.Images) != 1 {
		t.Fatalf("详情解析不对：%+v", d)
	}

	head, err := r.OwnProfile(ctx)
	if err != nil || head.Nick != "测试号" {
		t.Fatalf("自己的主页：%+v %v", head, err)
	}

	want := []string{"search:首页", "search:提交搜索", "search:发布时间筛选", "search:翻页", "detail:详情页", "detail:自己的主页"}
	if strings.Join(budget.got, ",") != strings.Join(want, ",") {
		t.Fatalf("额度申请不对：\n得到 %v\n应为 %v", budget.got, want)
	}
}

// 详情接口返回人机验证：必须作为风控错误返回，不能被当成普通失败吞掉（CRAWL-POLICY 第四节）。
func TestDetailVerificationStopsRun(t *testing.T) {
	f := newFake(t)
	const diagnostic = "仅供假服务端使用的诊断"
	f.SetDetailRet("FAIL_SYS_USER_VALIDATE::" + diagnostic)
	r, _, ctx := startRunner(t, f)
	started := 0
	_, err := r.OpenDetailWithStart(ctx, xianyu.WebLink("fleamarket://item?id=101"), func() { started++ })
	if !errors.Is(err, ErrNeedVerify) || !IsStop(err) || started != 1 {
		t.Fatalf("详情导航后应停止等待人机验证，导航 %d 次，错误 %v", started, err)
	}
	if strings.Contains(err.Error(), diagnostic) {
		t.Fatal("接口原始说明出现在诊断中")
	}
	_, err = r.OpenDetailWithStart(ctx, xianyu.WebLink("fleamarket://item?id=102"), func() { started++ })
	if !errors.Is(err, ErrNeedVerify) || started != 1 {
		t.Fatalf("旧风控应在打开下一件前拦住，导航 %d 次，错误 %v", started, err)
	}
}

// 令牌过期的那条响应要跳过，等页面重发后的结果。
func TestDetailSkipsExpiredToken(t *testing.T) {
	f := newFake(t)
	f.SetDetailRet("FAIL_SYS_TOKEN_EXOIRED::令牌过期")
	r, _, ctx := startRunner(t, f)
	var expired bool
	r.Log = func(event string, fields map[string]any) {
		if event == "接口返回" {
			if _, ok := fields["ret"]; ok {
				t.Fatal("日志保留了原始接口返回")
			}
			expired = expired || fields["结果"] == xianyu.RetTokenExpired.String()
		}
	}
	d, err := r.OpenDetail(ctx, xianyu.WebLink("fleamarket://item?id=101"))
	if err != nil {
		t.Fatal(err)
	}
	if d.SellerID != "998" {
		t.Fatalf("应该拿到重发后的详情：%+v", d)
	}
	if !expired {
		t.Fatal("日志没有记录已识别的令牌过期状态")
	}
}

// 搜索结果页弹出验证遮罩：搜索必须以风控错误结束。
func TestSearchStopsOnVisibleMask(t *testing.T) {
	f := newFake(t)
	f.SetMaskOnSearch(true)
	r, _, ctx := startRunner(t, f)
	if err := r.OpenHome(ctx); err != nil {
		t.Fatal(err)
	}
	_, err := r.Search(ctx, "相机")
	if !errors.Is(err, ErrNeedVerify) {
		t.Fatalf("应该返回人机验证错误，实际：%v", err)
	}
}

func TestSearchStopsOnRiskBeforeNavigation(t *testing.T) {
	f := newFake(t)
	f.SetSearchRiskWithoutNavigation(true)
	r, _, ctx := startRunner(t, f)
	if err := r.OpenHome(ctx); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err := r.Search(ctx, "相机")
	if !errors.Is(err, ErrNeedVerify) || time.Since(start) > 5*time.Second {
		t.Fatalf("没有跳转也必须即时处理后台风控：%v，用时 %s", err, time.Since(start))
	}
}

func TestLoggedInRequiresCurrentPageEvidence(t *testing.T) {
	f := newFake(t)
	r, _, ctx := startRunner(t, f)
	if err := r.S.Navigate(f.Home(), 15*time.Second); err != nil {
		t.Fatal(err)
	}
	if ok, err := r.LoggedIn(ctx); err != nil || ok {
		t.Fatalf("还没登录却判断为已登录：%v %v", ok, err)
	}
	if err := r.S.Navigate(f.LoginURL(), 15*time.Second); err != nil {
		t.Fatal(err)
	}
	if ok, err := r.LoggedIn(ctx); err != nil || ok {
		t.Fatalf("只有旧登录标记不能判断为已登录：%v %v", ok, err)
	}
	completeFakeLogin(t, r, ctx, f.Home())
	if ok, err := r.LoggedIn(ctx); err != nil || !ok {
		t.Fatalf("本次扫码有新页面证据后应为已登录：%v %v", ok, err)
	}
}

func TestLoginSuccessClearsHandledRiskBoundary(t *testing.T) {
	f := newFake(t)
	f.SetDetailRet("FAIL_SYS_USER_VALIDATE::需要验证")
	r, _, ctx := startRunner(t, f)
	if _, err := r.OpenDetail(ctx, xianyu.WebLink("fleamarket://item?id=101")); !errors.Is(err, ErrNeedVerify) {
		t.Fatalf("应先出现验证：%v", err)
	}
	r.BeginVerification()
	completeFakeLogin(t, r, ctx, f.Home())
	if head, err := r.OwnProfile(ctx); err != nil || head.Nick != "测试号" {
		t.Fatalf("人工处理后不能重放旧风控：%+v %v", head, err)
	}
}

// 接口数据里解析出的链接只能是闲鱼商品页，别的网址一律拒绝，浏览器不会被带去别的网站。
func TestItemURLRejectsOtherSites(t *testing.T) {
	site := Site{Home: "http://127.0.0.1:9/"}
	if got, err := site.itemURL("https://www.goofish.com/item?id=1&x=2"); err != nil || got != "http://127.0.0.1:9/item?id=1&x=2" {
		t.Fatalf("商品链接应该换到当前站点：%q %v", got, err)
	}
	for _, bad := range []string{"https://evil.example/item?id=1", "https://www.goofish.com/personal?userId=1", "https://www.goofish.com.evil/item?id=1", ""} {
		if _, err := site.itemURL(bad); err == nil {
			t.Fatalf("应该拒绝：%q", bad)
		}
	}
}

// 两步之间页面自己发出的接口被风控拦下：下一步开始时就要发现并停下，不能因为不是这一步发出的就漏掉。
func TestRiskBetweenStepsIsCaught(t *testing.T) {
	f := newFake(t)
	r, _, ctx := startRunner(t, f)
	if err := r.OpenHome(ctx); err != nil {
		t.Fatal(err)
	}
	js := `fetch('` + strings.TrimSuffix(f.Home(), "/") + `/h5/mtop.fake.risk/1.0/'); true`
	if _, err := chromedp.Run(r.S.Context(), chromedp.Evaluate[bool](js)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	if _, err := r.Search(ctx, "相机"); !errors.Is(err, ErrNeedVerify) {
		t.Fatalf("应该返回人机验证错误，实际：%v", err)
	}
}

// 搜索框里有预填文字时先清空，结果页的关键词和要搜的一致。
func TestSearchClearsPrefilledText(t *testing.T) {
	f := newFake(t)
	f.SetPrefill("推荐词 iPhone")
	r, _, ctx := startRunner(t, f)
	if err := r.OpenHome(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Search(ctx, "相机"); err != nil {
		t.Fatal(err)
	}
}

// 一次筛选连发的请求里最后一条失败：整个筛选算失败，不能拿前面成功的那条当结果。
func TestFilterFailsWhenFinalResponseFails(t *testing.T) {
	f := newFake(t)
	f.SetFinalFilterFails(true)
	r, _, ctx := startRunner(t, f)
	if err := r.OpenHome(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Search(ctx, "相机"); err != nil {
		t.Fatal(err)
	}
	resp, err := r.SelectPublished(ctx, 0)
	if err == nil || IsStop(err) {
		t.Fatalf("应该返回普通失败，实际：%v %v", resp, err)
	}
	if got := len(f.SearchQueries()); got != 3 {
		t.Fatalf("业务失败不能再次筛选：实际发出 %d 个搜索请求", got)
	}
}

func TestPublishedWindowKeepsLatestOrder(t *testing.T) {
	f := newFake(t)
	r, _, ctx := startRunner(t, f)
	if err := r.OpenHome(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Search(ctx, "相机"); err != nil {
		t.Fatal(err)
	}
	resp, err := r.SelectPublished(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.URL, "publish=%E6%9C%80%E6%96%B0") {
		t.Fatalf("7 天窗口也必须点最新：%s", resp.URL)
	}
}

func TestFilterWaitsForLastInFlightResponse(t *testing.T) {
	f := newFake(t)
	f.SetFinalFilterDelay(1800 * time.Millisecond)
	r, _, ctx := startRunner(t, f)
	if err := r.OpenHome(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Search(ctx, "相机"); err != nil {
		t.Fatal(err)
	}
	resp, err := r.SelectPublished(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.URL, "final=1") {
		t.Fatalf("最后一个请求仍在途中时不能用旧响应：%s", resp.URL)
	}
}

func TestMissingUnconfirmedCookieOnlyWarns(t *testing.T) {
	f := newFake(t)
	r, _, ctx := startRunner(t, f)
	warned := false
	r.Log = func(event string, fields map[string]any) {
		if event == "本地登录预检警告" {
			warned = true
		}
	}
	if err := r.OpenHome(ctx); err != nil {
		t.Fatalf("尚未确认的 Cookie 缺失不能拦住首页：%v", err)
	}
	if !warned {
		t.Fatal("应记录本地预检警告")
	}
	if err := r.CheckLogin(ctx); err != nil {
		t.Fatalf("首页正常、没有 Cookie 不能标记登录失效：%v", err)
	}
}

func TestMainDocumentPunishmentHeaderStops(t *testing.T) {
	f := newFake(t)
	r, _, ctx := startRunner(t, f)
	if err := r.S.Navigate(f.Home()+"document-block", 15*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := r.check(ctx, "主文档响应"); !errors.Is(err, ErrNeedVerify) {
		t.Fatalf("主文档 bxpunish 也必须停止：%v", err)
	}
}

func TestHiddenTextAndScriptsAreNotRisk(t *testing.T) {
	f := newFake(t)
	r, _, ctx := startRunner(t, f)
	if err := r.S.Navigate(f.Home()+"hidden-risk", 15*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := r.check(ctx, "正常页面"); err != nil {
		t.Fatalf("脚本、样式、隐藏元素不能触发风控：%v", err)
	}
}

func TestRiskInterruptsDetailStay(t *testing.T) {
	f := newFake(t)
	r, _, ctx := startRunner(t, f)
	if err := r.OpenHome(ctx); err != nil {
		t.Fatal(err)
	}
	js := `setTimeout(() => fetch('` + strings.TrimSuffix(f.Home(), "/") + `/h5/mtop.fake.risk/1.0/'), 300); true`
	if _, err := chromedp.Run(r.S.Context(), chromedp.Evaluate[bool](js)); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err := r.wait(ctx, "详情停留", pacer.Between(10*time.Second, 10*time.Second))
	if !errors.Is(err, ErrNeedVerify) || time.Since(start) > 3*time.Second {
		t.Fatalf("风控应打断停留，实际 %v，用时 %s", err, time.Since(start))
	}
}

func TestRiskInterruptionJoinsSlowCaptureWait(t *testing.T) {
	f := newFake(t)
	r, _, ctx := startRunner(t, f)
	if err := r.OpenHome(ctx); err != nil {
		t.Fatal(err)
	}
	mark := r.S.Capture.Mark()
	js := `setTimeout(() => fetch('` + strings.TrimSuffix(f.Home(), "/") + `/h5/mtop.fake.risk/1.0/'), 300); true`
	if _, err := chromedp.Run(r.S.Context(), chromedp.Evaluate[bool](js)); err != nil {
		t.Fatal(err)
	}
	workerFinished := false
	var captured *browser.Response
	err := r.watch(ctx, "等待慢接口", func(waitCtx context.Context) error {
		resp, err := r.S.Capture.WaitAfter(waitCtx, mark, browser.Match{URL: "/h5/mtop.never/"}, 30*time.Second)
		// 让取消后的退出有一个明确的尾部，外层必须等它结束才能读闭包写入的结果。
		time.Sleep(40 * time.Millisecond)
		captured = resp
		workerFinished = true
		return err
	})
	if !errors.Is(err, ErrNeedVerify) {
		t.Fatalf("应由风控打断慢接口等待：%v", err)
	}
	if !workerFinished || captured != nil {
		t.Fatalf("返回前必须回收等待协程：finished=%v，response=%v", workerFinished, captured)
	}
}

func TestCancelledFilterDoesNotRetry(t *testing.T) {
	f := newFake(t)
	r, _, ctx := startRunner(t, f)
	if err := r.OpenHome(ctx); err != nil {
		t.Fatal(err)
	}
	stepCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	calls := 0
	_, err := r.filterStep(stepCtx, "取消筛选", true, func() error {
		calls++
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("取消必须直接退出，不能重做筛选：err=%v，动作次数=%d", err, calls)
	}
}

// 当前页面已经出现验证遮罩：不能再去打开详情。
func TestDetailNotOpenedWhileMaskVisible(t *testing.T) {
	f := newFake(t)
	f.SetMaskOnSearch(true)
	r, _, ctx := startRunner(t, f)
	if err := r.S.Navigate(f.Home()+"search?q=x", 15*time.Second); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond) // 等结果页自己的搜索请求结束，免得和后面的计数混在一起
	_, err := r.OpenDetail(ctx, xianyu.WebLink("fleamarket://item?id=101"))
	if !errors.Is(err, ErrNeedVerify) {
		t.Fatalf("应该返回人机验证错误，实际：%v", err)
	}
	if n := f.DetailHits(); n != 0 {
		t.Fatalf("出现验证后不应再请求详情，实际请求了 %d 次", n)
	}
}

// 等登录时页面显示「非法访问」：用户处理不了，要立即结束，不能等满超时。
func TestWaitForLoginStopsWhenBlocked(t *testing.T) {
	f := newFake(t)
	r, _, ctx := startRunner(t, f)
	if err := r.S.Navigate(f.BlockedURL(), 15*time.Second); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err := r.WaitForLogin(ctx, 30*time.Second)
	if !errors.Is(err, ErrEnvBlocked) || time.Since(start) > 5*time.Second {
		t.Fatalf("应该立即返回环境异常，实际：%v，用时 %s", err, time.Since(start))
	}
}
