// Package fake 是一个本地的假闲鱼网站，给浏览器流程的测试用：页面结构和接口格式照 docs/XIANYU.md 和上游源码构造，
// 只在本机随机端口运行。测试不访问真实闲鱼。
package fake

import (
	"encoding/json"
	"fmt"
	"html"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const fakeHome = `<!doctype html><meta charset="utf-8"><title>首页</title>
<a href="/fixture/login-delayed?completion=mtop">模拟扫码登录</a>
<div style="height:80px"><input placeholder="搜索闲鱼" value="PREFILL" style="width:420px;height:32px;margin:20px">
<input type="text" style="width:60px"></div>
<div style="height:3000px">首页内容</div>
<script>
document.querySelector('input[placeholder="搜索闲鱼"]').addEventListener('keydown', e => {
  if (e.key === 'Enter') {
    if (STAYRISK) { fetch('/h5/mtop.fake.risk/1.0/'); return; }
    location.href = '/search?q=' + encodeURIComponent(e.target.value);
  }
});
</script>`

const fakeSearch = `<!doctype html><meta charset="utf-8"><title>搜索</title>
<div><span id="pub" style="padding:8px">新发布</span>
<div id="menu" style="display:none"><div style="padding:6px">最新</div><div style="padding:6px">1天内</div><div style="padding:6px">7天内</div></div>
<span id="personal" style="padding:8px">个人闲置</span>FILTERMARKUP</div>
<div class="closeIconBg--a1b2" style="width:30px;height:30px;position:fixed;right:10px;top:10px">×</div>
<div class="baxia-dialog-mask" style="width:200px;height:200px;MASKSTYLE">验证</div>
<div id="list">商品列表</div>
<button class="search-pagination-arrow-container--x1" id="next"><span class="search-pagination-arrow-right--y2">下一页</span></button>
<script>
function search(extra) {
  return fetch(API + '/h5/mtop.taobao.idlemtopsearch.pc.search/1.0/?' + extra, {method: 'POST', body: 'q=' + encodeURIComponent(new URLSearchParams(location.search).get('q'))});
}
search('first=1');
document.getElementById('pub').addEventListener('click', () => { document.getElementById('menu').style.display = 'block'; });
document.querySelectorAll('#menu div').forEach(d => d.addEventListener('click', () => {
  document.getElementById('menu').style.display = 'none';
  search('publish=' + encodeURIComponent(d.textContent));
  // 一次筛选可能连发两个请求，最后一条才是筛选后的结果。
  search('publish=' + encodeURIComponent(d.textContent) + '&final=1');
}));
document.querySelector('[class*=closeIconBg]').addEventListener('click', e => e.target.remove());
// 只有两页：翻到第 2 页后按钮变成不可点。
document.getElementById('next').addEventListener('click', e => { e.currentTarget.disabled = true; search('page=2'); });
</script>`

const fakePersonal = `<!doctype html><meta charset="utf-8"><title>我的主页</title><div>个人主页</div>
<script>fetch(API + '/h5/mtop.idle.web.user.page.head/1.0/');</script>`

const fakeItem = `<!doctype html><meta charset="utf-8"><title>商品</title><div>商品详情</div>
<script>
function load(n) {
  fetch(API + '/h5/mtop.taobao.idle.pc.detail/1.0/?id=' + new URLSearchParams(location.search).get('id') + '&n=' + n)
    .then(r => r.json()).then(j => { if (String(j.ret).includes('TOKEN_EXOIRED') && n < 2) load(n + 1); });
}
load(1);
</script>`

// Xianyu 是一个本地的假闲鱼网站。
type Xianyu struct {
	loginHold             chan struct{}
	loginRelease          sync.Once
	filters               filterState
	srv                   *httptest.Server
	mu                    sync.Mutex
	maskOnSearch          bool   // 搜索结果页显示验证遮罩
	detailRet             string // 详情接口第一次返回的 ret，空表示成功
	prefill               string // 首页搜索框里预填的文字
	finalFails            bool   // 筛选后的最后一条搜索响应返回业务失败
	finalDelay            time.Duration
	detailHits            int // 详情接口被请求的次数
	searchBodies          []string
	searchQueries         []string
	riskWithoutNavigation bool
	loginRequired         bool
	automaticLogin        bool
	loginGateNext         uint64
	loginGates            map[uint64]chan struct{}
	sellerProfile         bool
	sellerHits            SellerVisitCounts
	searchPages           *[2][]SearchItem
	searchFaults          map[string]searchFaultState
	faultGates            []*SearchFaultGate
	requests              atomic.Int64
	sequences             map[string]*searchSequence
	sequenceSellers       map[string]string
}

// Start 在本机随机端口启动假闲鱼，用完调用 Close。
func Start() *Xianyu {
	f := &Xianyu{loginHold: make(chan struct{})}
	mux := http.NewServeMux()
	page := func(source string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			f.mu.Lock()
			mask := "display:none"
			if f.maskOnSearch {
				mask = "display:block"
			}
			prefill := f.prefill
			stayRisk := f.riskWithoutNavigation
			f.mu.Unlock()
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'")
			out := strings.ReplaceAll(source, "MASKSTYLE", mask)
			switch r.URL.Path {
			case "/", "/search", "/item", "/personal":
				nick := "login"
				if fixtureLoginCompleted(r) {
					nick = "valid"
				}
				out = pageWithLoginHeader(out, nick)
			}
			if strings.Contains(out, "FILTERMARKUP") {
				f.filters.Lock()
				behavior, _ := json.Marshal(f.filters.behavior)
				emptyDistrict := f.filters.emptyDistrict
				f.filters.Unlock()
				out = strings.ReplaceAll(out, "FILTERMARKUP", filterMarkup)
				out += strings.ReplaceAll(strings.ReplaceAll(filterScript, "FILTERBEHAVIOR", string(behavior)), "EMPTYDISTRICT", fmt.Sprint(emptyDistrict))
			}
			out = strings.ReplaceAll(out, "PREFILL", html.EscapeString(prefill))
			out = strings.ReplaceAll(out, "STAYRISK", fmt.Sprint(stayRisk))
			out = strings.Replace(out, "<script>", "<script>const API = '"+f.srv.URL+"';", 1)
			fmt.Fprint(w, out)
		}
	}
	home := page(fakeHome)
	mux.HandleFunc("/{$}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		loginRequired := f.loginRequired
		automaticLogin := f.automaticLogin
		f.mu.Unlock()
		if loginRequired {
			http.Redirect(w, r, "/mini_login", http.StatusFound)
			return
		}
		if automaticLogin {
			http.SetCookie(w, &http.Cookie{Name: "unb", Value: "1", Path: "/", Expires: time.Now().Add(30 * 24 * time.Hour)})
			if !fixtureLoginCompleted(r) {
				page(f.automaticLoginPage())(w, r)
				return
			}
		}
		home(w, r)
	})
	mux.HandleFunc("/mini_login", page(`<!doctype html><meta charset="utf-8"><div>请登录</div><script>fetch('/h5/mtop.fake.login/1.0/?state=expired');</script>`))
	for _, api := range []string{"mtop.taobao.idlemessage.pc.loginuser.get", "mtop.idle.web.user.page.nav"} {
		mux.HandleFunc("/h5/"+api+"/1.0/", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"ret":["FAIL_SYS_SESSION_EXPIRED::登录失效"],"data":{}}`)
		})
	}
	mux.HandleFunc("/search", page(fakeSearch))
	mux.HandleFunc("/item", page(fakeItem))
	mux.HandleFunc("/personal", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		seller := f.sellerProfile && r.URL.Query().Get("userId") != ""
		if seller {
			f.sellerHits.Pages++
		}
		f.mu.Unlock()
		if seller {
			page(fakeSeller)(w, r)
			return
		}
		page(fakePersonal)(w, r)
	})
	mux.HandleFunc("/blocked", page(`<!doctype html><meta charset="utf-8"><div>非法访问，请使用正常浏览器访问</div>`))
	mux.HandleFunc("/hidden-risk", page(`<!doctype html><meta charset="utf-8"><script>const message = '非法访问';</script><style>body::before { content: '请使用正常浏览器访问'; display: none; }</style><div style="display:none">非法访问</div><div class="baxia-dialog-mask" style="display:none">验证</div><div data-message="非法访问">正常页面</div>`))
	mux.HandleFunc("/document-block", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("bxpunish", "1")
		fmt.Fprint(w, "<!doctype html><div>需要处理</div>")
	})
	mux.HandleFunc("/punish", page(`<!doctype html><div>需要处理</div>`))
	mux.HandleFunc("/_____tmd_____", page(`<!doctype html><div>需要处理</div>`))
	mux.HandleFunc("/images/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		img := image.NewRGBA(image.Rect(0, 0, 16, 16))
		for y := range 16 {
			for x := range 16 {
				img.Set(x, y, color.RGBA{R: 90, G: 140, B: 170, A: 255})
			}
		}
		jpeg.Encode(w, img, &jpeg.Options{Quality: 80})
	})
	mux.HandleFunc("/h5/mtop.idle.web.user.page.head/1.0/", func(w http.ResponseWriter, r *http.Request) {
		if f.serveSellerAPI(w, r, "head") {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"ret": []string{"SUCCESS::调用成功"}, "data": map[string]any{"module": map[string]any{"base": map[string]any{"displayName": "测试号"}}}})
	})
	for kind, endpoint := range map[string]string{"items": "mtop.idle.web.xyh.item.list", "ratings": "mtop.idle.web.trade.rate.list"} {
		mux.HandleFunc("/h5/"+endpoint+"/1.0/", func(w http.ResponseWriter, r *http.Request) {
			if !f.serveSellerAPI(w, r, kind) {
				http.NotFound(w, r)
			}
		})
	}
	// 一个总是要求验证的接口，用来模拟页面在两步之间自己发出、却被风控拦下的请求。
	mux.HandleFunc("/h5/mtop.fake.risk/1.0/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"ret": []string{"RGV587_ERROR::SM::哎哟喂,被挤爆啦"}, "data": map[string]any{}})
	})
	mux.HandleFunc("/h5/mtop.fake.redirect/1.0/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/fixture/punish?action=deny&"+r.URL.RawQuery, http.StatusFound)
	})
	mux.HandleFunc("/fixture/punish", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Query().Get("state"), "header") {
			w.Header().Set("bxpunish", "1")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
		}
		if r.URL.Query().Get("hold") == "1" {
			select {
			case <-f.loginHold:
			case <-r.Context().Done():
				return
			}
		}
		data := map[string]any{}
		if strings.Contains(r.URL.Query().Get("state"), "denied") {
			data["url"] = f.srv.URL + "/punish?action=deny"
		}
		json.NewEncoder(w).Encode(map[string]any{"ret": []string{"SUCCESS::调用成功"}, "data": data})
	})
	mux.HandleFunc("/blocked-mask", page(`<!doctype html><meta charset="utf-8"><div class="baxia-dialog-mask" style="width:200px;height:200px">验证</div><div>非法访问</div>`))
	mux.HandleFunc("/h5/mtop.fake.login/1.0/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("punish_header") == "1" {
			w.Header().Set("bxpunish", "1")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
		}
		if r.URL.Query().Get("hold") == "1" {
			select {
			case <-f.loginHold:
			case <-r.Context().Done():
				return
			}
		}
		if r.URL.Query().Get("delayed") == "1" {
			select {
			case <-time.After(300 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
		}
		ret := "SUCCESS::调用成功"
		data := map[string]any{}
		switch r.URL.Query().Get("state") {
		case "verify":
			ret = "RGV587::需要验证"
		case "expired":
			ret = "FAIL_SYS_SESSION_EXPIRED::登录失效"
		case "token_expired":
			ret = "FAIL_SYS_TOKEN_EXPIRED::令牌过期"
		case "denied":
			data["url"] = f.srv.URL + "/punish?action=deny"
		}
		if r.URL.Query().Get("state") == "success" && r.URL.Query().Get("after_scan") == "1" {
			if nick, ok := loginHeaderState(r); ok && nick == "valid" {
				markFixtureLoginCompleted(w)
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"ret": []string{ret}, "data": data})
	})
	mux.HandleFunc("/login-box", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "unb", Value: "1", Path: "/", Expires: time.Now().Add(30 * 24 * time.Hour)})
		source := `<!doctype html><meta charset="utf-8"><iframe id="alibaba-login-box" style="width:220px;height:120px;border:0"></iframe><button id="finish-login" style="width:120px;height:40px">完成扫码</button><script>document.getElementById('finish-login').addEventListener('click', () => { document.getElementById('alibaba-login-box').style.display = 'none'; fetch('/h5/mtop.fake.login/1.0/?state=success&after_scan=1').then(() => { document.getElementById('header').outerHTML = LOGIN_HEADER; }); });</script>`
		page(pageWithLoginHeader(strings.ReplaceAll(source, "LOGIN_HEADER", strconv.Quote(loginHeader("valid"))), "login"))(w, r)
	})
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		// 带过期时间，浏览器重启后还在，和真实的登录状态一样。
		http.SetCookie(w, &http.Cookie{Name: "unb", Value: "1", Path: "/", Expires: time.Now().Add(30 * 24 * time.Hour)})
		fmt.Fprint(w, "ok")
	})
	f.registerLoginEvidence(mux, page)
	mux.HandleFunc("/fixture/action", func(w http.ResponseWriter, r *http.Request) {
		f.filters.Lock()
		f.filters.actions = append(f.filters.actions, r.URL.RawQuery)
		f.filters.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/h5/mtop.taobao.idlemtopsearch.pc.search/1.0/", func(w http.ResponseWriter, r *http.Request) {
		f.filters.Lock()
		behavior := f.filters.behavior[r.URL.Query().Get("step")]
		f.filters.Unlock()
		if behavior.NoResponse {
			<-r.Context().Done()
			return
		}
		if behavior.Delay > 0 {
			select {
			case <-time.After(behavior.Delay):
			case <-r.Context().Done():
				return
			}
		}
		if behavior.Ret != "" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"ret": []string{behavior.Ret}, "data": map[string]any{}})
			return
		}
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.searchBodies = append(f.searchBodies, string(body))
		f.searchQueries = append(f.searchQueries, r.URL.RawQuery)
		finalFails := f.finalFails
		finalDelay := f.finalDelay
		pages := f.searchPages
		f.mu.Unlock()
		if f.serveSearchFault(w, r, body) {
			return
		}
		if f.serveSearchSequence(w, r, body) {
			return
		}
		if finalDelay > 0 && r.URL.Query().Get("final") == "1" {
			select {
			case <-time.After(finalDelay):
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if finalFails && r.URL.Query().Get("final") == "1" {
			json.NewEncoder(w).Encode(map[string]any{"ret": []string{"FAIL_BIZ_SEARCH::搜索失败"}, "data": map[string]any{}})
			return
		}
		page2 := r.URL.Query().Get("page") == "2"
		if pages != nil {
			index := 0
			if page2 {
				index = 1
			}
			json.NewEncoder(w).Encode(searchItemsJSON(f.srv.URL, pages[index]))
			return
		}
		json.NewEncoder(w).Encode(searchJSON(f.srv.URL, page2))
	})
	mux.HandleFunc("/h5/mtop.taobao.idle.pc.detail/1.0/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.detailHits++
		ret := f.detailRet
		sellerID := "998"
		if f.searchPages != nil {
		findSeller:
			for _, items := range *f.searchPages {
				for _, item := range items {
					if item.ID == r.URL.Query().Get("id") && item.SellerID != "" {
						sellerID = item.SellerID
						break findSeller
					}
				}
			}
		}
		if seller := f.sequenceSellerLocked(r.URL.Query().Get("id")); seller != "" {
			sellerID = seller
		}
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if ret != "" && r.URL.Query().Get("n") == "1" {
			json.NewEncoder(w).Encode(map[string]any{"ret": []string{ret}, "data": map[string]any{}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"ret": []string{"SUCCESS::调用成功"},
			"data": map[string]any{
				"itemDO":   map[string]any{"imageInfos": []any{map[string]any{"url": f.srv.URL + "/images/1.jpg"}}, "wantCnt": 3, "browseCnt": 50, "desc": "电池 92%，无拆修"},
				"sellerDO": map[string]any{"sellerId": sellerID, "userRegDay": 800, "zhimaLevelInfo": map[string]any{"levelName": "信用极好"}},
			},
		})
	})
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		mux.ServeHTTP(w, r)
	}))
	return f
}

// Close 关闭假闲鱼。
func (f *Xianyu) Close() {
	f.CompleteAutomaticLogin()
	f.ReleaseLoginResponses()
	f.releaseSearchFaults()
	f.srv.Close()
}

// ReleaseLoginResponses 让测试控制在途登录响应何时返回。
func (f *Xianyu) ReleaseLoginResponses() { f.loginRelease.Do(func() { close(f.loginHold) }) }

// Home 是首页网址，以 / 结尾。
func (f *Xianyu) Home() string { return f.srv.URL + "/" }

// APIHost 是接口所在的主机和端口。
func (f *Xianyu) APIHost() string { return strings.TrimPrefix(f.srv.URL, "http://") }

// LoginURL 只写入旧的登录标记，不代表本次已完成扫码。
func (f *Xianyu) LoginURL() string { return f.srv.URL + "/login" }

// EnableAutomaticLogin 打开可控扫码框，等测试确认程序已看见后再放行。
func (f *Xianyu) EnableAutomaticLogin() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.automaticLogin = true
}

// RequireLoginOnHome 让首页跳到登录页并报告会话失效。
func (f *Xianyu) RequireLoginOnHome() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loginRequired = true
}

// SetMaskOnSearch 让搜索结果页显示验证遮罩。
func (f *Xianyu) SetMaskOnSearch(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.maskOnSearch = on
}

// SetDetailRet 让详情接口第一次返回指定的 ret；页面遇到令牌过期会自己重发一次。
func (f *Xianyu) SetDetailRet(ret string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.detailRet = ret
}

// SetPrefill 让首页搜索框里预先带着文字，像推荐词那样。
func (f *Xianyu) SetPrefill(text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prefill = text
}

// SetFinalFilterFails 让一次筛选连发的两个请求里，最后一个返回业务失败。
func (f *Xianyu) SetFinalFilterFails(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finalFails = on
}

// SetFinalFilterDelay 模拟最后一条筛选请求还在途中，不能提早用前一条响应。
func (f *Xianyu) SetFinalFilterDelay(delay time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finalDelay = delay
}

// SetSearchRiskWithoutNavigation 模拟提交后没有跳转，但后台接口要求验证。
func (f *Xianyu) SetSearchRiskWithoutNavigation(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.riskWithoutNavigation = on
}

// DetailHits 返回详情接口被请求的次数。
func (f *Xianyu) DetailHits() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.detailHits
}

// BlockedURL 是一个显示「非法访问」的页面。
func (f *Xianyu) BlockedURL() string { return f.srv.URL + "/blocked" }

// SearchBodies 返回搜索接口收到的请求正文。
func (f *Xianyu) SearchBodies() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.searchBodies...)
}

// SearchQueries 返回页面自己发出的搜索请求参数。
func (f *Xianyu) SearchQueries() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.searchQueries...)
}

// searchJSON 构造一页搜索结果。第 2 页和第 1 页有一个商品重复，和真实翻页时新商品把列表往后挤的情况一样。
func searchJSON(baseURL string, page2 bool) map[string]any {
	list := []any{searchItemJSON(baseURL, "101", "MacBook Pro M3", "8999", time.Hour), searchItemJSON(baseURL, "102", "MacBook Air", "5200", 2*time.Hour)}
	if page2 {
		list = []any{searchItemJSON(baseURL, "102", "MacBook Air", "5200", 2*time.Hour), searchItemJSON(baseURL, "103", "MacBook Pro M4", "11999", 30*time.Minute)}
	}
	return map[string]any{"ret": []string{"SUCCESS::调用成功"}, "data": map[string]any{"resultList": list}}
}
