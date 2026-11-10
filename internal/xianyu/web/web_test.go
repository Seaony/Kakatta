package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/Seaony/Kakatta/internal/browser"
	"github.com/Seaony/Kakatta/internal/pacer"
	"github.com/Seaony/Kakatta/internal/xianyu"
)

// 本地的假闲鱼：页面结构和接口格式照 docs/XIANYU.md，只在本机运行，测试不访问真实闲鱼。

const fakeHome = `<!doctype html><meta charset="utf-8"><title>首页</title>
<div style="height:80px"><input placeholder="搜索闲鱼" style="width:420px;height:32px;margin:20px">
<input type="text" style="width:60px"></div>
<div style="height:3000px">首页内容</div>
<script>
document.querySelector('input[placeholder="搜索闲鱼"]').addEventListener('keydown', e => {
  if (e.key === 'Enter') location.href = '/search?q=' + encodeURIComponent(e.target.value);
});
</script>`

const fakeSearch = `<!doctype html><meta charset="utf-8"><title>搜索</title>
<div><span id="pub" style="padding:8px">新发布</span>
<div id="menu" style="display:none"><div style="padding:6px">最新</div><div style="padding:6px">1天内</div><div style="padding:6px">7天内</div></div>
<span style="padding:8px">个人闲置</span></div>
<div class="closeIconBg--a1b2" style="width:30px;height:30px;position:fixed;right:10px;top:10px">×</div>
<div class="baxia-dialog-mask" style="width:200px;height:200px;MASKSTYLE">验证</div>
<div id="list">商品列表</div>
<script>
function search(extra) {
  return fetch(API + '/h5/mtop.taobao.idlemtopsearch.pc.search/1.0/?' + extra, {method: 'POST', body: 'q=' + encodeURIComponent(new URLSearchParams(location.search).get('q'))});
}
search('first=1');
document.getElementById('pub').addEventListener('click', () => { document.getElementById('menu').style.display = 'block'; });
document.querySelectorAll('#menu div').forEach(d => d.addEventListener('click', () => {
  document.getElementById('menu').style.display = 'none';
  search('publish=' + encodeURIComponent(d.textContent));
}));
document.querySelector('[class*=closeIconBg]').addEventListener('click', e => e.target.remove());
</script>`

const fakeItem = `<!doctype html><meta charset="utf-8"><title>商品</title><div>商品详情</div>
<script>
function load(n) {
  fetch(API + '/h5/mtop.taobao.idle.pc.detail/1.0/?id=' + new URLSearchParams(location.search).get('id') + '&n=' + n)
    .then(r => r.json()).then(j => { if (String(j.ret).includes('TOKEN_EXOIRED') && n < 2) load(n + 1); });
}
load(1);
</script>`

type fakeXianyu struct {
	srv *httptest.Server
	mu  sync.Mutex
	// 风控场景
	maskOnSearch  bool   // 搜索结果页显示验证遮罩
	detailRet     string // 详情接口第一次返回的 ret，空表示成功
	searchBodies  []string
	detailQueries int
}

func newFakeXianyu(t *testing.T) *fakeXianyu {
	f := &fakeXianyu{}
	mux := http.NewServeMux()
	page := func(html string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			f.mu.Lock()
			mask := "display:none"
			if f.maskOnSearch {
				mask = "display:block"
			}
			f.mu.Unlock()
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			out := strings.ReplaceAll(html, "MASKSTYLE", mask)
			out = strings.Replace(out, "<script>", "<script>const API = '"+f.srv.URL+"';", 1)
			fmt.Fprint(w, out)
		}
	}
	mux.HandleFunc("/{$}", page(fakeHome))
	mux.HandleFunc("/search", page(fakeSearch))
	mux.HandleFunc("/item", page(fakeItem))
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "unb", Value: "1", Path: "/"})
		fmt.Fprint(w, "ok")
	})
	mux.HandleFunc("/h5/mtop.taobao.idlemtopsearch.pc.search/1.0/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.searchBodies = append(f.searchBodies, string(body))
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(searchJSON(f.srv.URL))
	})
	mux.HandleFunc("/h5/mtop.taobao.idle.pc.detail/1.0/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.detailQueries++
		ret := f.detailRet
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if ret != "" && r.URL.Query().Get("n") == "1" {
			json.NewEncoder(w).Encode(map[string]any{"ret": []string{ret}, "data": map[string]any{}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"ret": []string{"SUCCESS::调用成功"},
			"data": map[string]any{
				"itemDO":   map[string]any{"imageInfos": []any{map[string]any{"url": "//img.example.com/1.jpg"}}, "wantCnt": 3, "browseCnt": 50, "desc": "电池 92%，无拆修"},
				"sellerDO": map[string]any{"sellerId": "998", "userRegDay": 800, "zhimaLevelInfo": map[string]any{"levelName": "信用极好"}},
			},
		})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func searchJSON(base string) map[string]any {
	item := func(id, title, price string) map[string]any {
		return map[string]any{"data": map[string]any{"item": map[string]any{"main": map[string]any{
			"exContent":  map[string]any{"itemId": id, "title": title, "price": []any{map[string]any{"text": "¥"}, map[string]any{"text": price}}, "area": "上海", "userNickName": "卖家" + id, "picUrl": "//img.example.com/" + id + ".jpg"},
			"clickParam": map[string]any{"args": map[string]any{"publishTime": fmt.Sprint(time.Now().Add(-time.Hour).UnixMilli()), "wantNum": 2}},
			"targetUrl":  "fleamarket://item?id=" + id,
		}}}}
	}
	return map[string]any{"ret": []string{"SUCCESS::调用成功"}, "data": map[string]any{"resultList": []any{
		item("101", "MacBook Pro M3", "8999"), item("102", "MacBook Air", "5200"),
	}}}
}

// countingBudget 记下每次申请额度，用来核对计数口径。
type countingBudget struct{ got []string }

func (b *countingBudget) Acquire(kind VisitKind, what string) error {
	b.got = append(b.got, string(kind)+":"+what)
	return nil
}

func fastPace() Pace {
	r := pacer.Between(5*time.Millisecond, 10*time.Millisecond)
	return Pace{HomeStay: r, Action: r, PageWait: r, PreDetail: r, Detail: r}
}

func startRunner(t *testing.T, f *fakeXianyu) (*Runner, *countingBudget, context.Context) {
	t.Helper()
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
	site := Site{Home: f.srv.URL + "/", APIHost: strings.TrimPrefix(f.srv.URL, "http://")}
	b := &countingBudget{}
	r := NewRunner(s, pacer.New(nil), pacer.Between(5*time.Millisecond, 10*time.Millisecond), site, fastPace(), b, nil)
	return r, b, ctx
}

// 完整走一遍：首页 → 搜索框输入中文关键词 → 选「最新」→ 解析结果 → 打开详情。
func TestSearchAndDetailFlow(t *testing.T) {
	f := newFakeXianyu(t)
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
	if !strings.Contains(resp.URL, "publish=") {
		t.Fatalf("应该取筛选之后的响应，拿到的是：%s", resp.URL)
	}
	items, err := xianyu.ParseSearch(resp.Body)
	if err != nil || len(items) != 2 {
		t.Fatalf("解析搜索结果：%v，%d 项", err, len(items))
	}
	if !strings.Contains(f.searchBodies[0], "MacBook%20") && !strings.Contains(f.searchBodies[0], "MacBook+") {
		t.Fatalf("页面收到的关键词不对：%q", f.searchBodies[0])
	}

	detailURL := f.srv.URL + "/item?id=" + items[0].ID
	d, err := r.OpenDetail(ctx, detailURL)
	if err != nil {
		t.Fatal(err)
	}
	if d.SellerID != "998" || len(d.Images) != 1 {
		t.Fatalf("详情解析不对：%+v", d)
	}

	want := []string{"search:首页", "search:提交搜索", "search:发布时间筛选", "detail:详情页"}
	if strings.Join(budget.got, ",") != strings.Join(want, ",") {
		t.Fatalf("额度申请不对：\n得到 %v\n应为 %v", budget.got, want)
	}
}

// 详情接口返回人机验证：必须作为风控错误返回，不能被当成普通失败吞掉（CRAWL-POLICY 第四节）。
func TestDetailVerificationStopsRun(t *testing.T) {
	f := newFakeXianyu(t)
	f.detailRet = "FAIL_SYS_USER_VALIDATE::哎哟喂,被挤爆啦"
	r, _, ctx := startRunner(t, f)
	_, err := r.OpenDetail(ctx, f.srv.URL+"/item?id=101")
	if !errors.Is(err, ErrNeedVerify) || !IsStop(err) {
		t.Fatalf("应该返回人机验证错误，实际：%v", err)
	}
}

// 令牌过期的那条响应要跳过，等页面重发后的结果。
func TestDetailSkipsExpiredToken(t *testing.T) {
	f := newFakeXianyu(t)
	f.detailRet = "FAIL_SYS_TOKEN_EXOIRED::令牌过期"
	r, _, ctx := startRunner(t, f)
	d, err := r.OpenDetail(ctx, f.srv.URL+"/item?id=101")
	if err != nil {
		t.Fatal(err)
	}
	if d.SellerID != "998" {
		t.Fatalf("应该拿到重发后的详情：%+v", d)
	}
}

// 搜索结果页弹出验证遮罩：搜索必须以风控错误结束。
func TestSearchStopsOnVisibleMask(t *testing.T) {
	f := newFakeXianyu(t)
	f.maskOnSearch = true
	r, _, ctx := startRunner(t, f)
	if err := r.OpenHome(ctx); err != nil {
		t.Fatal(err)
	}
	_, err := r.Search(ctx, "相机")
	if !errors.Is(err, ErrNeedVerify) {
		t.Fatalf("应该返回人机验证错误，实际：%v", err)
	}
}

// 登录判断只看本地状态：没有登录 Cookie 时为假，有了以后为真。
func TestLoggedInChecksCookieOnly(t *testing.T) {
	f := newFakeXianyu(t)
	r, _, ctx := startRunner(t, f)
	if err := r.S.Navigate(f.srv.URL+"/", 15*time.Second); err != nil {
		t.Fatal(err)
	}
	if ok, err := r.LoggedIn(ctx); err != nil || ok {
		t.Fatalf("还没登录却判断为已登录：%v %v", ok, err)
	}
	if err := r.S.Navigate(f.srv.URL+"/login", 15*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := r.WaitForLogin(ctx, 5*time.Second); err != nil {
		t.Fatal(err)
	}
}
