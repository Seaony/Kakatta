package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/auth"
	"github.com/Seaony/Kakatta/internal/settings"
)

func TestSecurityActionsKeepLoginFailureEventUntilFullLogin(t *testing.T) {
	for _, name := range []string{"password", "setup", "enable", "disable"} {
		t.Run(name, func(t *testing.T) {
			f := newServerFixture(t, true)
			binding := prepareSecurityAction(t, f, name)
			events := observeLoginFailureEvent(t, f)
			path, body, status := securityActionRequest(f, name, binding, false)
			requireStatus(t, f.request(http.MethodPost, path, body, true), status)
			if len(*events) != 1 || !(*events)[0] {
				t.Fatalf("安全设置不是新登录，不能解决登录失败事件：%v", *events)
			}

			password := testPassword
			if name == "password" {
				password = "更新后的本地密码"
			}
			bodyBytes, _ := json.Marshal(map[string]string{"username": "本地用户", "password": password})
			login := f.request(http.MethodPost, "/api/v1/auth/login", string(bodyBytes), false)
			requireStatus(t, login, http.StatusOK)
			if name == "enable" {
				challenge := decodeBody[struct {
					Status    string `json:"status"`
					Challenge string `json:"challenge"`
				}](t, login)
				if challenge.Status != "totp_required" || len(*events) != 1 {
					t.Fatalf("密码通过但还没完成验证码，不算完整登录：%v", *events)
				}
				f.advance(30 * time.Second)
				verify, _ := json.Marshal(map[string]string{"challenge": challenge.Challenge, "code": totpCode(binding.Secret, time.UnixMilli(f.clock.Load()))})
				requireStatus(t, f.request(http.MethodPost, "/api/v1/auth/totp", string(verify), false), http.StatusOK)
			}
			if len(*events) != 2 || !(*events)[0] || (*events)[1] {
				t.Fatalf("下一次完整登录必须解决同一个失败事件：%v", *events)
			}
		})
	}
}

func TestSecurityActionFailuresStillShareLoginProtection(t *testing.T) {
	for _, name := range []string{"password", "setup", "enable", "disable"} {
		t.Run(name, func(t *testing.T) {
			f := newServerFixture(t, true)
			binding := prepareSecurityAction(t, f, name)
			events := observeLoginFailureEvent(t, f)
			path, body, _ := securityActionRequest(f, name, binding, true)
			requireStatus(t, f.request(http.MethodPost, path, body, true), http.StatusUnauthorized)
			blocked := f.request(http.MethodPost, path, body, true)
			requireStatus(t, blocked, http.StatusTooManyRequests)
			if blocked.Header().Get("Retry-After") != "1" || len(*events) != 1 || !(*events)[0] {
				t.Fatalf("安全设置失败必须保留事件并使用同一保护：%v", *events)
			}
		})
	}
}

func TestInitialSetupCompletesLoginAndResolvesFailureEvent(t *testing.T) {
	f := newServerFixture(t, false)
	events := observeLoginFailureEvent(t, f)
	body, _ := json.Marshal(map[string]string{"username": "本地用户", "password": testPassword})
	result := f.request(http.MethodPost, "/api/v1/setup", string(body), false)
	requireStatus(t, result, http.StatusOK)
	if len(*events) != 2 || !(*events)[0] || (*events)[1] || activeCookie(t, result).Value == "" {
		t.Fatalf("首会话建立后应解决登录失败事件：%v", *events)
	}
}

func TestLoginFailureEventCapturesResolvedTrustedClientIP(t *testing.T) {
	f := newServerFixture(t, true)
	if _, err := f.settings.Apply(t.Context(), settings.Values{"security.trusted_proxies": json.RawMessage(`["10.0.0.0/8"]`)}); err != nil {
		t.Fatal(err)
	}
	var events []LoginFailureEvent
	f.server.limiter = newLoginLimiter(func() time.Time { return time.UnixMilli(f.clock.Load()) }, func(_ context.Context, event LoginFailureEvent) error {
		events = append(events, event)
		return nil
	})
	for index := 1; index <= 20; index++ {
		result := f.request(http.MethodPost, "/api/v1/auth/login", `{"username":"本地用户","password":"错误的本地密码"}`, false, func(r *http.Request) {
			r.RemoteAddr = "10.0.0.1:3456"
			r.Header.Set("X-Forwarded-For", fmt.Sprintf("192.0.2.99, 203.0.113.%d, 10.0.0.2", index))
		})
		requireStatus(t, result, http.StatusUnauthorized)
	}
	if len(events) != 1 || events[0] != (LoginFailureEvent{Active: true, Limit: 20, IP: "203.0.113.20"}) {
		t.Fatalf("系统事件必须保存达到上限时已经解析好的来源事实：%+v", events)
	}
}

func observeLoginFailureEvent(t *testing.T, f *serverFixture) *[]bool {
	t.Helper()
	events := []bool{}
	f.server.limiter = newLoginLimiter(func() time.Time { return time.UnixMilli(f.clock.Load()) }, func(_ context.Context, event LoginFailureEvent) error {
		events = append(events, event.Active)
		return nil
	})
	failDifferentIPs(t, f.server.limiter, 20)
	if len(events) != 1 || !events[0] {
		t.Fatalf("测试必须先触发真实的失败事件：%v", events)
	}
	f.advance(61 * time.Second)
	return &events
}

func prepareSecurityAction(t *testing.T, f *serverFixture, name string) auth.TOTPSetup {
	t.Helper()
	if name != "enable" && name != "disable" {
		return auth.TOTPSetup{}
	}
	binding, err := f.auth.BeginTOTP(t.Context(), f.session.Identity, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if name == "disable" {
		if err := f.auth.EnableTOTP(t.Context(), f.session.Identity, binding.SetupToken, totpCode(binding.Secret, time.UnixMilli(f.clock.Load()))); err != nil {
			t.Fatal(err)
		}
	}
	return binding
}

func securityActionRequest(f *serverFixture, name string, binding auth.TOTPSetup, invalid bool) (string, string, int) {
	password := testPassword
	if invalid {
		password = "错误的本地密码"
	}
	code := ""
	if binding.Secret != "" {
		now := time.UnixMilli(f.clock.Load())
		code = totpCode(binding.Secret, now)
		if invalid {
			for number := 0; ; number++ {
				code = fmt.Sprintf("%06d", number)
				if code != totpCode(binding.Secret, now.Add(-30*time.Second)) && code != totpCode(binding.Secret, now) && code != totpCode(binding.Secret, now.Add(30*time.Second)) {
					break
				}
			}
		}
	}
	var path string
	var value map[string]string
	status := http.StatusNoContent
	switch name {
	case "password":
		path, value = "/api/v1/security/password", map[string]string{"old_password": password, "new_password": "更新后的本地密码"}
	case "setup":
		path, value, status = "/api/v1/security/totp/setup", map[string]string{"password": password}, http.StatusOK
	case "enable":
		path, value = "/api/v1/security/totp/enable", map[string]string{"setup_token": binding.SetupToken, "code": code}
	case "disable":
		path, value = "/api/v1/security/totp/disable", map[string]string{"password": testPassword, "code": code}
	}
	body, _ := json.Marshal(value)
	return path, string(body), status
}
