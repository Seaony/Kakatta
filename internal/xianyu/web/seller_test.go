package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/browser"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/pacer"
	"github.com/Seaony/Kakatta/internal/quota"
)

func TestSellerURLOnlyChangesQueryOnFixedProfilePath(t *testing.T) {
	for _, test := range []struct{ home, seller string }{
		{"https://www.goofish.com/", "998"},
		{"http://127.0.0.1:18080/nested/", "中文 &x=1?/#"},
		{"http://[::1]:18080/", "https://elsewhere.invalid/not-a-target"},
	} {
		got, err := (Site{Home: test.home}).sellerURL(test.seller)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := url.Parse(got)
		base, _ := url.Parse(test.home)
		if err != nil || parsed.Host != base.Host || parsed.Scheme != base.Scheme || parsed.Path != "/personal" || parsed.Query().Get("userId") != test.seller || len(parsed.Query()) != 1 || parsed.Fragment != "" {
			t.Fatalf("卖家编号只能作为查询值，不能改导航位置：%q，%v", got, err)
		}
	}
	for _, test := range []struct{ home, seller string }{
		{"https://www.goofish.com/", ""}, {"https://www.goofish.com/", " \t"},
		{"https://www.goofish.com/", "a\nb"}, {"https://www.goofish.com/", "a\x00b"},
		{"file:///tmp/", "1"}, {"https://user:secret@example.invalid/", "1"},
		{"https://example.invalid/?token=secret", "1"}, {"https://example.invalid/#secret", "1"},
		{"/relative/", "1"}, {"https://", "1"},
	} {
		if got, err := (Site{Home: test.home}).sellerURL(test.seller); err == nil || got != "" || strings.Contains(err.Error(), "secret") {
			t.Fatalf("非法站点或编号应在浏览器前拒绝且不回显凭据：%q，%v", got, err)
		}
	}
}

func TestOpenSellerRejectsInvalidInputAndCancellationBeforeBrowser(t *testing.T) {
	budget := &countingBudget{}
	r := &Runner{Site: Site{Home: "https://www.goofish.com/"}, Budget: budget}
	for _, id := range []string{"", " \t", "a\nb"} {
		result, err := r.OpenSeller(t.Context(), id)
		if err == nil || result.Opened || result.Head != nil || result.Items != nil || result.Ratings != nil || len(result.Warnings) != 0 {
			t.Fatalf("非法编号不能触碰浏览器或伪造部分资料：%+v %v", result, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := r.OpenSeller(ctx, "998")
	if !errors.Is(err, context.Canceled) || result.Opened || len(budget.got) != 0 {
		t.Fatalf("取消不能扣额度或打开页面：%+v %v %v", result, err, budget.got)
	}
}

func TestSellerWarningsPreserveStopAndCancellationCause(t *testing.T) {
	for _, kind := range []error{ErrNeedVerify, ErrDenied, ErrEnvBlocked, ErrLoginExpired, context.Canceled} {
		err := fmt.Errorf("步骤失败：%w", kind)
		result := SellerPage{}
		if got := result.recordFailure(t.Context(), "run.seller_head_failed", err); !errors.Is(got, kind) || len(result.Warnings) != 0 {
			t.Fatalf("停止错误不能改成卖家普通警告：%v，%+v", got, result)
		}
	}
	cause := errors.New("搜索代次已经改变")
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(cause)
	result := SellerPage{}
	if got := result.recordFailure(ctx, "run.seller_page_failed", errors.New("连接已关闭")); !errors.Is(got, context.Canceled) || !errors.Is(got, cause) || len(result.Warnings) != 0 {
		t.Fatalf("取消应保留业务原因，不能继续正常结算：%v，%+v", got, result)
	}
	deadline, cancelDeadline := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancelDeadline()
	if got := result.recordFailure(deadline, "run.seller_page_failed", errors.New("读取失败")); !errors.Is(got, context.DeadlineExceeded) || len(result.Warnings) != 0 {
		t.Fatalf("整体超时不能降级：%v", got)
	}
}

func TestSellerWarningsDoNotPersistRawErrorOrPretendKnownData(t *testing.T) {
	for _, code := range []string{"run.seller_page_failed", "run.seller_head_failed", "run.seller_items_failed", "run.seller_ratings_failed"} {
		result := SellerPage{Opened: true}
		if err := result.recordFailure(t.Context(), code, errors.New("https://user:secret@example.invalid/?token=private")); err != nil {
			t.Fatal(err)
		}
		if len(result.Warnings) != 1 || result.Warnings[0].Code != code || len(result.Warnings[0].Args) != 0 || result.Head != nil || result.Items != nil || result.Ratings != nil || !result.Opened {
			t.Fatalf("普通错误只能留下固定原因，不能伪造资料：%+v", result)
		}
		for _, language := range []locales.Language{locales.ZhCN, locales.En} {
			text, err := locales.Format(language, result.Warnings[0])
			if err != nil || strings.Contains(text, "secret") || strings.Contains(text, "private") {
				t.Fatalf("警告必须可双语显示且不泄漏：%q %v", text, err)
			}
		}
	}
}

type sellerTestBudget struct {
	got       []string
	denyAfter int
	err       error
}

func (b *sellerTestBudget) Acquire(kind quota.Kind, what string) error {
	b.got = append(b.got, string(kind)+":"+what)
	if b.err != nil && len(b.got) > b.denyAfter {
		return b.err
	}
	return nil
}

type sellerTestSite struct {
	server  *httptest.Server
	mode    string
	mu      sync.Mutex
	hits    map[string]int
	userIDs []string
}

func (f *sellerTestSite) count(kind string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[kind]
}

func sellerTestAPIPath(fragment string) string {
	return "/h5/" + strings.Trim(fragment, "/") + "/1.0/"
}

func sellerBrowserRunner(t *testing.T, mode string, budget *sellerTestBudget) (*Runner, *sellerTestSite, context.Context) {
	t.Helper()
	if os.Getenv("KAKATTA_BROWSER_TESTS") != "1" {
		t.Skip("卖家完整浏览回归须在断网容器显式开启")
	}
	original := rules
	rules.Timeouts = make(map[string]int, len(original.Timeouts))
	for key, value := range original.Timeouts {
		rules.Timeouts[key] = value
	}
	for _, key := range []string{"profile_response", "seller_items_response", "seller_ratings_response", "element"} {
		rules.Timeouts[key] = 400
	}
	t.Cleanup(func() { rules = original })
	f := &sellerTestSite{mode: mode, hits: map[string]int{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	t.Cleanup(cancel)
	session, err := browser.Start(ctx, browser.Options{ProfileDir: t.TempDir(), Width: 1280, Height: 900})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(session.Close)
	pace := fastPace()
	r := NewRunner(session, pacer.New(nil), pacer.Between(time.Millisecond, time.Millisecond), Site{Home: f.server.URL + "/", APIHost: strings.TrimPrefix(f.server.URL, "http://")}, pace, budget, nil)
	return r, f, ctx
}

func (f *sellerTestSite) serve(w http.ResponseWriter, r *http.Request) {
	kind := "other"
	switch r.URL.Path {
	case "/personal", "/preload":
		kind = "page"
	case sellerTestAPIPath(rules.API.UserHead):
		kind = "head"
	case sellerTestAPIPath(rules.API.UserItems):
		kind = "items"
	case sellerTestAPIPath(rules.API.UserRatings):
		kind = "ratings"
	case "/unexpected-scroll":
		kind = "scroll"
	}
	f.mu.Lock()
	f.hits[kind]++
	if kind == "page" {
		f.userIDs = append(f.userIDs, r.URL.Query().Get("userId"))
	}
	f.mu.Unlock()
	if kind == "page" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; connect-src 'self'")
		tab := `<ul><li id="ratings" style="width:180px;height:40px"><div>信用及评价</div></li></ul>`
		if f.mode == "no_tab" {
			tab = ""
		}
		if f.mode == "offscreen_tab" {
			tab = `<div style="height:1600px"></div>` + tab
		}
		if f.mode == "hidden_tab" {
			tab = `<div style="display:none">` + tab + `</div>`
		}
		if f.mode == "page_env" {
			tab += `<div>请使用正常浏览器访问</div>`
		}
		extra := ""
		if r.URL.Path == "/preload" {
			extra = "?old=1"
		}
		fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>卖家夹具</title>%s<div style="height:2400px">本地卖家</div>
<script>
fetch(%q);
setTimeout(() => fetch(%q), 30);
document.querySelector('#ratings')?.addEventListener('click', () => fetch(%q));
addEventListener('scroll', () => fetch('/unexpected-scroll'));
</script>`, tab, sellerTestAPIPath(rules.API.UserItems)+extra, sellerTestAPIPath(rules.API.UserHead)+extra, sellerTestAPIPath(rules.API.UserRatings))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if f.mode == kind+"_http" {
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprint(w, `{"ret":["FAIL_BIZ_ERROR"],"data":{}}`)
		return
	}
	if f.mode == kind+"_json" {
		fmt.Fprint(w, `not-json`)
		return
	}
	if strings.HasPrefix(f.mode, kind+"_risk_") {
		mode := strings.TrimPrefix(f.mode, kind+"_risk_")
		if mode == "deny" {
			fmt.Fprint(w, `{"ret":["FAIL_BIZ_ERROR"],"data":{"url":"/punish?action=deny"}}`)
			return
		}
		ret := map[string]string{"verify": "FAIL_SYS_USER_VALIDATE", "deny": "FAIL_SYS_ILLEGAL_ACCESS", "login": "FAIL_SYS_SESSION_EXPIRED"}[mode]
		fmt.Fprintf(w, `{"ret":[%q],"data":{}}`, ret)
		return
	}
	if f.mode == kind+"_missing" {
		fmt.Fprint(w, `{"ret":["SUCCESS::调用成功"],"data":{}}`)
		return
	}
	if f.mode == "ratings_delay" && kind == "ratings" {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
	switch kind {
	case "head":
		nickname := "本地卖家"
		if r.URL.Query().Get("old") == "1" {
			nickname = "旧页面的卖家"
		}
		fmt.Fprintf(w, `{"ret":["SUCCESS::调用成功"],"data":{"module":{"base":{"displayName":%q},"tabs":{"item":{"number":2},"rate":{"number":1}}}}}`, nickname)
	case "items":
		if r.URL.Query().Get("old") == "1" {
			fmt.Fprint(w, `{"ret":["SUCCESS::调用成功"],"data":{"cardList":[{"cardData":{"id":"old","title":"旧页面商品"}}]}}`)
			return
		}
		if f.mode == "empty_items" {
			fmt.Fprint(w, `{"ret":["SUCCESS::调用成功"],"data":{"nextPage":true,"cardList":[]}}`)
			return
		}
		fmt.Fprint(w, `{"ret":["SUCCESS::调用成功"],"data":{"nextPage":true,"cardList":[{"cardData":{"id":"a","title":"镜头","itemStatus":0,"priceInfo":{"price":"100"}}},{"cardData":{"id":"b","title":"三脚架","itemStatus":1}}]}}`)
	case "ratings":
		if f.mode == "empty_ratings" {
			fmt.Fprint(w, `{"ret":["SUCCESS::调用成功"],"data":{"nextPage":true,"cardList":[]}}`)
		} else {
			fmt.Fprint(w, `{"ret":["SUCCESS::调用成功"],"data":{"nextPage":true,"cardList":[{"cardData":{"rateId":"r1","feedback":"描述符合","rate":1,"rateTagList":[{"text":"卖家"}],"gmtCreate":"2026-11-01"}}]}}`)
		}
	default:
		fmt.Fprint(w, `{}`)
	}
}

func sellerHasWarning(page SellerPage, code string) bool {
	for _, warning := range page.Warnings {
		if warning.Code == code {
			return true
		}
	}
	return false
}

func TestOpenSellerReadsOnePageAndKeepsEarlierItemsResponse(t *testing.T) {
	budget := &sellerTestBudget{}
	r, site, ctx := sellerBrowserRunner(t, "normal", budget)
	page, err := r.OpenSeller(ctx, "998 & literal")
	if err != nil || !page.Opened || page.Head == nil || page.Items == nil || page.Ratings == nil || len(page.Warnings) != 0 {
		t.Fatalf("应取得完整卖家资料：%+v，%v", page, err)
	}
	if page.Head.Nick != "本地卖家" || !page.Head.ItemCountKnown || !page.Head.RateCountKnown || len(page.Items.Items) != 2 || page.Items.OnSale != 1 || page.Items.Sold != 1 || len(page.Ratings.Recent) != 1 || page.Ratings.AsSeller.Total != 1 {
		t.Fatalf("第一页原始资料不对：头部%+v，商品%+v，评价%+v", page.Head, page.Items, page.Ratings)
	}
	if got := strings.Join(budget.got, ","); got != "detail:卖家主页,detail:卖家评价" {
		t.Fatalf("卖家页和评价应各计一次访问：%s", got)
	}
	for _, kind := range []string{"page", "head", "items", "ratings"} {
		if got := site.count(kind); got != 1 {
			t.Fatalf("%s只读第一页，不因nextPage=true再加载：%d", kind, got)
		}
	}
	if site.count("scroll") != 0 {
		t.Fatal("卖家主页不应滚动加载")
	}
	site.mu.Lock()
	defer site.mu.Unlock()
	if len(site.userIDs) != 1 || site.userIDs[0] != "998 & literal" {
		t.Fatalf("卖家编号必须被完整编码：%q", site.userIDs)
	}
}

func TestOpenSellerSuccessfulEmptyListsRemainKnown(t *testing.T) {
	for _, mode := range []string{"empty_items", "empty_ratings"} {
		t.Run(mode, func(t *testing.T) {
			r, _, ctx := sellerBrowserRunner(t, mode, &sellerTestBudget{})
			page, err := r.OpenSeller(ctx, "998")
			if err != nil || page.Items == nil || page.Ratings == nil || len(page.Warnings) != 0 {
				t.Fatalf("成功空列表不能变成未取得：%+v，%v", page, err)
			}
			if mode == "empty_items" && (page.Items.Items == nil || len(page.Items.Items) != 0) || mode == "empty_ratings" && (page.Ratings.Recent == nil || len(page.Ratings.Recent) != 0 || page.Ratings.AsSeller.Total != 0) {
				t.Fatalf("成功空列表应明确是空数组：%+v", page)
			}
		})
	}
}

func TestOpenSellerDoesNotBorrowResponsesBeforeNavigation(t *testing.T) {
	r, site, ctx := sellerBrowserRunner(t, "normal", &sellerTestBudget{})
	mark := r.S.Capture.Mark()
	if _, err := r.S.NavigateContext(ctx, site.server.URL+"/preload", time.Second); err != nil {
		t.Fatal(err)
	}
	for _, match := range []browser.Match{r.Site.userHeadAPI(), r.Site.userItemsAPI()} {
		if _, err := r.S.Capture.WaitAfter(ctx, mark, match, time.Second); err != nil {
			t.Fatal(err)
		}
	}
	page, err := r.OpenSeller(ctx, "998")
	if err != nil || page.Head == nil || page.Items == nil || page.Head.Nick != "本地卖家" || len(page.Items.Items) != 2 || page.Items.Items[0].ID == "old" {
		t.Fatalf("不能把旧页面响应当成本次卖家资料：%+v，%v", page, err)
	}
}

func TestOpenSellerNavigationFailureKeepsWarningWithoutFreshData(t *testing.T) {
	r, site, ctx := sellerBrowserRunner(t, "normal", &sellerTestBudget{})
	site.server.Close()
	page, err := r.OpenSeller(ctx, "998")
	if err != nil || !page.Opened || page.Head != nil || page.Items != nil || page.Ratings != nil || !sellerHasWarning(page, "run.seller_page_failed") {
		t.Fatalf("页面普通失败必须返回警告，不能生成空的新鲜资料：%+v，%v", page, err)
	}
}

func TestOpenSellerOrdinaryFailuresKeepOtherAcquiredParts(t *testing.T) {
	for _, test := range []struct {
		mode, warning        string
		head, items, ratings bool
	}{
		{"head_http", "run.seller_head_failed", false, true, true},
		{"head_json", "run.seller_head_failed", false, true, true},
		{"items_http", "run.seller_items_failed", true, false, true},
		{"items_missing", "run.seller_items_failed", true, false, true},
		{"ratings_http", "run.seller_ratings_failed", true, true, false},
		{"ratings_json", "run.seller_ratings_failed", true, true, false},
		{"ratings_missing", "run.seller_ratings_failed", true, true, false},
		{"no_tab", "run.seller_ratings_missing", true, true, false},
		{"offscreen_tab", "run.seller_ratings_missing", true, true, false},
		{"hidden_tab", "run.seller_ratings_missing", true, true, false},
	} {
		t.Run(test.mode, func(t *testing.T) {
			budget := &sellerTestBudget{}
			r, site, ctx := sellerBrowserRunner(t, test.mode, budget)
			page, err := r.OpenSeller(ctx, "998")
			if err != nil || !page.Opened || (page.Head != nil) != test.head || (page.Items != nil) != test.items || (page.Ratings != nil) != test.ratings || !sellerHasWarning(page, test.warning) {
				t.Fatalf("普通失败应保留有效部分并给固定警告：%+v，%v", page, err)
			}
			if strings.HasSuffix(test.mode, "_tab") && (site.count("ratings") != 0 || site.count("scroll") != 0 || len(budget.got) != 1) {
				t.Fatal("缺少可见评价标签时不能滚动、扣评价额度或请求接口")
			}
		})
	}
}

func TestOpenSellerQuotaAndStorageFailuresDoNotSendUnapprovedRequests(t *testing.T) {
	storage := errors.New("额度记录写入失败")
	for _, test := range []struct {
		name    string
		after   int
		failure error
		wantErr error
		opened  bool
	}{
		{"主页额度不足", 0, ErrQuota, ErrQuota, false},
		{"评价额度不足", 1, ErrQuota, nil, true},
		{"主页记账失败", 0, storage, storage, false},
		{"评价记账失败", 1, storage, storage, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			budget := &sellerTestBudget{denyAfter: test.after, err: test.failure}
			r, site, ctx := sellerBrowserRunner(t, "normal", budget)
			page, err := r.OpenSeller(ctx, "998")
			if !errors.Is(err, test.wantErr) || page.Opened != test.opened || page.Ratings != nil || site.count("ratings") != 0 {
				t.Fatalf("预算拒绝后不能继续访问：%+v，%v，次数%v", page, err, budget.got)
			}
			if test.after == 0 && site.count("page") != 0 {
				t.Fatal("主页预算未提交不能导航")
			}
			if test.after == 1 && test.failure == ErrQuota && (page.Head == nil || page.Items == nil || !sellerHasWarning(page, "run.seller_ratings_quota")) {
				t.Fatal("评价额度不足应保留其他资料")
			}
		})
	}
}

func TestOpenSellerStopsForRiskAndLoginAtEveryAPI(t *testing.T) {
	for _, stage := range []string{"head", "items", "ratings"} {
		for _, risk := range []struct {
			mode string
			err  error
		}{{"verify", ErrNeedVerify}, {"deny", ErrDenied}, {"login", ErrLoginExpired}} {
			t.Run(stage+"_"+risk.mode, func(t *testing.T) {
				budget := &sellerTestBudget{}
				r, site, ctx := sellerBrowserRunner(t, stage+"_risk_"+risk.mode, budget)
				page, err := r.OpenSeller(ctx, "998")
				if !errors.Is(err, risk.err) || !page.Opened || len(page.Warnings) != 0 {
					t.Fatalf("风控和失效不能转为普通缺资料：%+v，%v", page, err)
				}
				if stage != "ratings" && (site.count("ratings") != 0 || len(budget.got) != 1) {
					t.Fatalf("遇风控后不能继续点评价：%v", budget.got)
				}
			})
		}
	}
}

func TestOpenSellerVisibleEnvironmentBlockStopsBeforeRatings(t *testing.T) {
	budget := &sellerTestBudget{}
	r, site, ctx := sellerBrowserRunner(t, "page_env", budget)
	page, err := r.OpenSeller(ctx, "998")
	if !errors.Is(err, ErrEnvBlocked) || !page.Opened || len(page.Warnings) != 0 || len(budget.got) != 1 || site.count("ratings") != 0 {
		t.Fatalf("可见环境拒绝不能被部分卖家资料遮住：%+v，%v，%v", page, err, budget.got)
	}
}

func TestOpenSellerCancellationRetainsCauseAndLeavesSessionOwnedByCaller(t *testing.T) {
	r, site, lifetime := sellerBrowserRunner(t, "ratings_delay", &sellerTestBudget{})
	ctx, cancel := context.WithCancelCause(lifetime)
	cause := errors.New("当前任务已删除")
	defer cancel(cause)
	type result struct {
		page SellerPage
		err  error
	}
	done := make(chan result, 1)
	go func() { page, err := r.OpenSeller(ctx, "998"); done <- result{page, err} }()
	deadline := time.Now().Add(5 * time.Second)
	for site.count("ratings") == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if site.count("ratings") == 0 {
		t.Fatal("本地页面未发出评价请求")
	}
	cancel(cause)
	select {
	case got := <-done:
		if !errors.Is(got.err, context.Canceled) || !errors.Is(got.err, cause) || got.page.Head == nil || got.page.Items == nil || got.page.Ratings != nil || len(got.page.Warnings) != 0 {
			t.Fatalf("取消应保留来源与已取得部分：%+v，%v", got.page, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("取消后没有及时停止卖家流程")
	}
	select {
	case <-r.S.Context().Done():
		t.Fatal("单次卖家流程取消不能擅自关闭账号浏览器")
	default:
	}
}
