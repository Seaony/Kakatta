package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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

func TestExperimentControlOpensWithoutCredentialExchange(t *testing.T) {
	c, err := newController(options{Data: t.TempDir(), Account: 1, Details: 2, Keyword: "相机", Fixture: true}, testSettings())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	h := experimentHandler(c)
	status := httptest.NewRecorder()
	h.ServeHTTP(status, httptest.NewRequest(http.MethodGet, "http://local/status", nil))
	if status.Code != http.StatusOK || !bytes.Contains(status.Body.Bytes(), []byte("idle")) || len(status.Result().Cookies()) != 0 {
		t.Fatalf("直接打开应能读到状态且不换取网页会话：%d", status.Code)
	}
	page := httptest.NewRecorder()
	h.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "http://local/", nil))
	if page.Code != http.StatusOK || strings.Contains(page.Body.String(), "auth-form") || strings.Contains(page.Body.String(), `name="token"`) || !strings.Contains(page.Body.String(), `id="controls" aria-label="实验操作"`) {
		t.Fatal("实验页应直接显示操作，不再显示凭据输入")
	}
	close := httptest.NewRecorder()
	h.ServeHTTP(close, httptest.NewRequest(http.MethodPost, "http://local/close", nil))
	if close.Code != http.StatusNoContent || len(close.Result().Cookies()) != 0 {
		t.Fatalf("没有网页会话也应可操作控制页：%d", close.Code)
	}
}
func TestExperimentDirectControlKeepsCrossOriginWriteProtection(t *testing.T) {
	c, err := newController(options{Data: t.TempDir(), Account: 1, Details: 2, Keyword: "相机", Fixture: true}, testSettings())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	h := experimentHandler(c)
	for _, path := range []string{"/session", "/run", "/verify", "/confirm", "/close"} {
		r := httptest.NewRequest(http.MethodPost, "http://local"+path, nil)
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("跨站写入%s返回%d", path, w.Code)
		}
	}
	for _, path := range []string{"/remote/", "/remote/vnc", "/remote/novnc/core/rfb.js"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://local"+path, nil))
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "还没有打开浏览器") {
			t.Fatalf("画面应直接返回浏览器状态：%s返回%d", path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "http://local/auth", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("旧会话兑换入口仍然存在：%d", w.Code)
	}
}

func TestExperimentListenDefaultsToLoopbackAndKeepsContainerOverride(t *testing.T) {
	o, _, err := parseOptions(nil, io.Discard)
	if err != nil || o.Listen != "127.0.0.1:8080" {
		t.Fatalf("默认入口应只监听回环地址：%q，%v", o.Listen, err)
	}
	o, _, err = parseOptions([]string{"--listen", ":8080"}, io.Discard)
	if err != nil || o.Listen != ":8080" {
		t.Fatalf("容器显式监听地址被改变：%q，%v", o.Listen, err)
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
