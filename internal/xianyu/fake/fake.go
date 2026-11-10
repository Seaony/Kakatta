// Package fake 是一个本地的假闲鱼网站，给浏览器流程的测试用：页面结构和接口格式照 docs/XIANYU.md 和上游源码构造，
// 只在本机随机端口运行。测试不访问真实闲鱼。
package fake

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
)

const fakeHome = `<!doctype html><meta charset="utf-8"><title>首页</title>
<div style="height:80px"><input placeholder="搜索闲鱼" value="PREFILL" style="width:420px;height:32px;margin:20px">
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
	srv          *httptest.Server
	mu           sync.Mutex
	maskOnSearch bool   // 搜索结果页显示验证遮罩
	detailRet    string // 详情接口第一次返回的 ret，空表示成功
	prefill      string // 首页搜索框里预填的文字
	finalFails   bool   // 筛选后的最后一条搜索响应返回业务失败
	detailHits   int    // 详情接口被请求的次数
	searchBodies []string
}

// Start 在本机随机端口启动假闲鱼，用完调用 Close。
func Start() *Xianyu {
	f := &Xianyu{}
	mux := http.NewServeMux()
	page := func(html string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			f.mu.Lock()
			mask := "display:none"
			if f.maskOnSearch {
				mask = "display:block"
			}
			prefill := f.prefill
			f.mu.Unlock()
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			out := strings.ReplaceAll(html, "MASKSTYLE", mask)
			out = strings.ReplaceAll(out, "PREFILL", prefill)
			out = strings.Replace(out, "<script>", "<script>const API = '"+f.srv.URL+"';", 1)
			fmt.Fprint(w, out)
		}
	}
	mux.HandleFunc("/{$}", page(fakeHome))
	mux.HandleFunc("/search", page(fakeSearch))
	mux.HandleFunc("/item", page(fakeItem))
	mux.HandleFunc("/personal", page(fakePersonal))
	mux.HandleFunc("/blocked", page(`<!doctype html><meta charset="utf-8"><div>非法访问，请使用正常浏览器访问</div>`))
	mux.HandleFunc("/h5/mtop.idle.web.user.page.head/1.0/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"ret": []string{"SUCCESS::调用成功"}, "data": map[string]any{"module": map[string]any{"base": map[string]any{"displayName": "测试号"}}}})
	})
	// 一个总是要求验证的接口，用来模拟页面在两步之间自己发出、却被风控拦下的请求。
	mux.HandleFunc("/h5/mtop.fake.risk/1.0/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"ret": []string{"RGV587_ERROR::SM::哎哟喂,被挤爆啦"}, "data": map[string]any{}})
	})
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		// 带过期时间，浏览器重启后还在，和真实的登录状态一样。
		http.SetCookie(w, &http.Cookie{Name: "unb", Value: "1", Path: "/", Expires: time.Now().Add(30 * 24 * time.Hour)})
		fmt.Fprint(w, "ok")
	})
	mux.HandleFunc("/h5/mtop.taobao.idlemtopsearch.pc.search/1.0/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.searchBodies = append(f.searchBodies, string(body))
		finalFails := f.finalFails
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if finalFails && r.URL.Query().Get("final") == "1" {
			json.NewEncoder(w).Encode(map[string]any{"ret": []string{"FAIL_BIZ_SEARCH::搜索失败"}, "data": map[string]any{}})
			return
		}
		json.NewEncoder(w).Encode(searchJSON(r.URL.Query().Get("page") == "2"))
	})
	mux.HandleFunc("/h5/mtop.taobao.idle.pc.detail/1.0/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.detailHits++
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
	return f
}

// Close 关闭假闲鱼。
func (f *Xianyu) Close() { f.srv.Close() }

// Home 是首页网址，以 / 结尾。
func (f *Xianyu) Home() string { return f.srv.URL + "/" }

// APIHost 是接口所在的主机和端口。
func (f *Xianyu) APIHost() string { return strings.TrimPrefix(f.srv.URL, "http://") }

// LoginURL 打开后会写入登录 Cookie，模拟扫码登录成功。
func (f *Xianyu) LoginURL() string { return f.srv.URL + "/login" }

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

// searchJSON 构造一页搜索结果。第 2 页和第 1 页有一个商品重复，和真实翻页时新商品把列表往后挤的情况一样。
func searchJSON(page2 bool) map[string]any {
	item := func(id, title, price string, age time.Duration) map[string]any {
		return map[string]any{"data": map[string]any{"item": map[string]any{"main": map[string]any{
			"exContent":  map[string]any{"itemId": id, "title": title, "price": []any{map[string]any{"text": "¥"}, map[string]any{"text": price}}, "area": "上海", "userNickName": "卖家" + id, "picUrl": "//img.example.com/" + id + ".jpg"},
			"clickParam": map[string]any{"args": map[string]any{"publishTime": fmt.Sprint(time.Now().Add(-age).UnixMilli()), "wantNum": 2}},
			"targetUrl":  "fleamarket://item?id=" + id,
		}}}}
	}
	list := []any{item("101", "MacBook Pro M3", "8999", time.Hour), item("102", "MacBook Air", "5200", 2*time.Hour)}
	if page2 {
		list = []any{item("102", "MacBook Air", "5200", 2*time.Hour), item("103", "MacBook Pro M4", "11999", 30*time.Minute)}
	}
	return map[string]any{"ret": []string{"SUCCESS::调用成功"}, "data": map[string]any{"resultList": list}}
}
