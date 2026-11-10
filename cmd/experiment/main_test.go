package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestCommandRejectsUnattendedRealBrowsing(t *testing.T) {
	for _, args := range [][]string{{"--once"}, {"--details", "4"}, {"--keyword", " "}, {"--min-price", "20", "--max-price", "10"}, {"--account", "-1"}, {"--region", "浙江"}, {"--region", "浙江/"}, {"--region", "/杭州"}, {"--region", "浙江/杭州/"}, {"--webgl", "swiftshader"}} {
		if _, _, err := parseOptions(args, io.Discard); err == nil {
			t.Fatalf("应拒绝：%v", args)
		}
	}
	if err := runCommand([]string{"--help"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
}
func TestExperimentAuthenticationCoversControlAndRemote(t *testing.T) {
	c, err := newController(options{Data: t.TempDir(), Account: 1, Details: 2, Keyword: "相机", Fixture: true}, testSettings())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	a, err := newAccess()
	if err != nil {
		t.Fatal(err)
	}
	h := experimentHandler(c, a)
	for _, tc := range []struct{ method, path string }{{"GET", "/status"}, {"POST", "/session"}, {"POST", "/run"}, {"POST", "/verify"}, {"POST", "/confirm"}, {"POST", "/close"}, {"GET", "/remote/"}, {"GET", "/remote/vnc"}, {"GET", "/remote/novnc/core/rfb.js"}} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(tc.method, "http://local"+tc.path, nil))
		if w.Code != 401 {
			t.Fatalf("未登录的%s %s返回%d", tc.method, tc.path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "http://local/healthz", nil))
	if w.Code != 200 {
		t.Fatal("健康检查不能要求登录")
	}
	login := func(token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://local/auth", strings.NewReader(url.Values{"token": {token}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if got := login("wrong"); got.Code != 401 {
		t.Fatal("错误初始化令牌通过了")
	}
	token := a.token
	success := login(token)
	if success.Code != 204 {
		t.Fatalf("令牌登录失败：%d %s", success.Code, success.Body)
	}
	cookies := success.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("实验会话Cookie缺少保护")
	}
	if got := login(token); got.Code != 401 {
		t.Fatal("初始化令牌不能重用")
	}
	r := httptest.NewRequest("GET", "http://local/status", nil)
	r.AddCookie(cookies[0])
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte("idle")) {
		t.Fatal("登录后读不到状态")
	}
	r = httptest.NewRequest("POST", "http://local/close", nil)
	r.AddCookie(cookies[0])
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("跨站不能关闭会话")
	}
	a.sessionExpires = time.Now().Add(-time.Second)
	if a.authorized(r) {
		t.Fatal("过期会话仍然可用")
	}
}
func TestSettingsValidation(t *testing.T) {
	for _, change := range []func(*experimentSettings){func(s *experimentSettings) { s.HomeSeconds.Min = 0 }, func(s *experimentSettings) { s.AccountGapSeconds = -1 }, func(s *experimentSettings) { s.Timezone = "bad/zone" }, func(s *experimentSettings) { s.QuietStart = "1am" }, func(s *experimentSettings) { s.QuietStart = "1:00" }} {
		s := testSettings()
		change(&s)
		if s.validate() == nil {
			t.Fatal("无效设置通过了")
		}
	}
}

func TestAuthenticationStartsNewIdleWindow(t *testing.T) {
	a, err := newAccess()
	if err != nil {
		t.Fatal(err)
	}
	a.tokenExpires = time.Now().Add(time.Minute)
	r := httptest.NewRequest("POST", "http://local/auth", strings.NewReader(url.Values{"token": {a.token}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	a.login(w, r)
	if w.Code != 204 || time.Until(a.sessionExpires) < 29*time.Minute {
		t.Fatal("初始化令牌将要到期时，网页会话也提前到期")
	}
	a.sessionExpires = time.Now().Add(time.Minute)
	r = httptest.NewRequest("GET", "http://local/status", nil)
	r.AddCookie(w.Result().Cookies()[0])
	if !a.authorized(r) || time.Until(a.sessionExpires) < 29*time.Minute {
		t.Fatal("正在使用的人工会话没有续期")
	}
}
