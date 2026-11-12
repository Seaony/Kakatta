package server

import (
	"net/http"
	"testing"
	"time"
)

func TestFreshInstanceDirectAccountSetupAndLogin(t *testing.T) {
	f := newServerFixture(t, false)
	f.advance(2 * time.Hour)
	body := `{"username":"本地用户","password":"` + testPassword + `"}`
	setup := f.request(http.MethodPost, "/api/v1/setup", body, false)
	requireStatus(t, setup, http.StatusOK)
	f.session.Token = activeCookie(t, setup).Value
	status := decodeBody[map[string]bool](t, f.request(http.MethodGet, "/api/v1/setup", "", false))
	if len(status) != 1 || !status["initialized"] {
		t.Fatal("初始化状态应只包含是否已有管理员")
	}
	requireStatus(t, f.request(http.MethodPost, "/api/v1/setup", body, false), http.StatusConflict)
	requireStatus(t, f.request(http.MethodPost, "/api/v1/auth/logout", `{}`, true), http.StatusNoContent)
	f.advance(time.Second)
	login := f.request(http.MethodPost, "/api/v1/auth/login", body, false)
	requireStatus(t, login, http.StatusOK)
	if activeCookie(t, login).Value == f.session.Token {
		t.Fatal("再次登录应建立新会话")
	}
}
