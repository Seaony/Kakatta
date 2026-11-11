package fake

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

const delayedLoginPage = `<!doctype html><meta charset="utf-8"><title>延迟登录框</title>
<div id="login-waiting">正在准备登录框</div>
<button id="finish-login" hidden style="width:120px;height:40px">完成扫码</button>
<button id="continue-login" hidden style="width:180px;height:40px">触发登录后的页面变化</button>
<script>
const completion = LOGIN_COMPLETION;
setTimeout(() => {
  document.getElementById('login-waiting').remove();
  const box = document.createElement('iframe');
  box.id = 'alibaba-login-box';
  box.style.cssText = 'width:220px;height:120px;border:0';
  document.body.prepend(box);
  document.getElementById('finish-login').hidden = false;
  fetch('/h5/mtop.taobao.idlemessage.pc.loginuser.get/1.0/');
  fetch('/h5/mtop.idle.web.user.page.nav/1.0/');
}, 1000);
document.getElementById('finish-login').addEventListener('click', () => {
  document.getElementById('alibaba-login-box').style.display = 'none';
  document.getElementById('finish-login').hidden = true;
  document.getElementById('continue-login').hidden = false;
});
document.getElementById('continue-login').addEventListener('click', () => {
  if (completion === 'navigation') location.href = '/fixture/login-evidence-complete';
  if (completion === 'mtop') fetch('/h5/mtop.fake.login/1.0/?state=success&after_scan=1');
});
</script>`

func (f *Xianyu) registerLoginEvidence(mux *http.ServeMux, page func(string) http.HandlerFunc) {
	setMarker := func(w http.ResponseWriter, expired bool) {
		expires := time.Now().Add(30 * 24 * time.Hour)
		if expired {
			expires = time.Now().Add(-time.Hour)
		}
		http.SetCookie(w, &http.Cookie{Name: "unb", Value: "old-marker", Path: "/", Expires: expires})
	}
	mux.HandleFunc("/fixture/login-delayed", func(w http.ResponseWriter, r *http.Request) {
		completion := r.URL.Query().Get("completion")
		if completion == "" {
			completion = "none"
		}
		if completion != "none" && completion != "mtop" && completion != "navigation" {
			http.Error(w, "登录夹具参数无效", http.StatusBadRequest)
			return
		}
		setMarker(w, r.URL.Query().Get("cookie") == "expired")
		page(strings.ReplaceAll(delayedLoginPage, "LOGIN_COMPLETION", strconv.Quote(completion)))(w, r)
	})
	mux.HandleFunc("/fixture/login-cookie-only", func(w http.ResponseWriter, r *http.Request) {
		setMarker(w, r.URL.Query().Get("cookie") == "expired")
		page(`<!doctype html><meta charset="utf-8"><title>只有旧登录标记</title><div>没有新的登录页面证据</div>`)(w, r)
	})
	mux.HandleFunc("/fixture/login-evidence-complete", page(`<!doctype html><meta charset="utf-8"><title>扫码后页面</title><div>已完成扫码后的主框架导航</div>`))
	mux.HandleFunc("/fixture/login-gate", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseUint(r.URL.Query().Get("id"), 10, 64)
		f.mu.Lock()
		gate := f.loginGates[id]
		f.mu.Unlock()
		if err != nil || gate == nil {
			http.Error(w, "登录夹具不存在", http.StatusNotFound)
			return
		}
		defer func() {
			f.mu.Lock()
			delete(f.loginGates, id)
			f.mu.Unlock()
		}()
		select {
		case <-gate:
			http.SetCookie(w, &http.Cookie{Name: "fixture_login_completed", Value: "1", Path: "/", HttpOnly: true, Expires: time.Now().Add(30 * 24 * time.Hour)})
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"completed":true}`))
		case <-r.Context().Done():
		}
	})
}

func (f *Xianyu) automaticLoginPage() string {
	f.mu.Lock()
	f.loginGateNext++
	id := f.loginGateNext
	if f.loginGates == nil {
		f.loginGates = map[uint64]chan struct{}{}
	}
	f.loginGates[id] = make(chan struct{})
	f.mu.Unlock()
	frame := `<iframe id="alibaba-login-box" style="width:220px;height:120px;border:0"></iframe>`
	source := strings.Replace(fakeHome, `<div style="height:80px">`, frame+`<div style="height:80px">`, 1)
	return source + `<script>fetch('/fixture/login-gate?id=` + strconv.FormatUint(id, 10) + `').then(r => r.json()).then(() => {
document.getElementById('alibaba-login-box').style.display = 'none';
fetch('/h5/mtop.fake.login/1.0/?state=success&after_scan=1');
});</script>`
}

// CompleteAutomaticLogin 只完成当前已打开的假扫码，不提前放行之后的新窗口。
func (f *Xianyu) CompleteAutomaticLogin() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, gate := range f.loginGates {
		select {
		case <-gate:
		default:
			close(gate)
		}
	}
}
