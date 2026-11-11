package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/auth"
	"github.com/Seaony/Kakatta/internal/security"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/store"
)

const testPassword = "仅供本地测试的密码"

type testSettingsStore struct {
	base *appsettings.Store
	mu   sync.Mutex
	err  error
}

func (s *testSettingsStore) Get(ctx context.Context) (settings.Snapshot, error) {
	s.mu.Lock()
	err := s.err
	s.mu.Unlock()
	if err != nil {
		return settings.Snapshot{}, err
	}
	return s.base.Get(ctx)
}

func (s *testSettingsStore) Apply(ctx context.Context, patch settings.Values) (settings.Snapshot, error) {
	return s.base.Apply(ctx, patch)
}

func (s *testSettingsStore) Definitions() []settings.Definition { return s.base.Definitions() }

type serverFixture struct {
	t        *testing.T
	server   *Server
	store    *store.Store
	auth     *auth.Service
	settings *testSettingsStore
	clock    atomic.Int64
	initial  string
	session  auth.Session
}

func newServerFixture(t *testing.T, initialized bool) *serverFixture {
	t.Helper()
	f := &serverFixture{t: t}
	f.clock.Store(time.Date(2026, 11, 10, 4, 0, 0, 0, time.UTC).UnixMilli())
	now := func() time.Time { return time.UnixMilli(f.clock.Load()) }
	var err error
	f.store, err = store.Open(t.Context(), filepath.Join(t.TempDir(), "kakatta.db"))
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := security.NewCipher(bytes.Repeat([]byte{4}, 32))
	if err != nil {
		t.Fatal(err)
	}
	var bootstrap auth.Bootstrap
	f.auth, bootstrap, err = auth.New(t.Context(), auth.Dependencies{DB: f.store.DB(), Cipher: cipher, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	f.initial = bootstrap.Token
	base, err := appsettings.New(t.Context(), f.store.DB(), settings.Environment{}, now)
	if err != nil {
		t.Fatal(err)
	}
	f.settings = &testSettingsStore{base: base}
	f.server, err = New(Dependencies{
		Auth: f.auth, Settings: f.settings, Now: now, Health: f.store.DB().PingContext,
		LoginFailure: func(context.Context, LoginFailureEvent) error { return nil },
		Web: fstest.MapFS{
			"index.html":           {Data: []byte("<!doctype html><title>Kakatta</title>")},
			"assets/a.js":          {Data: []byte("export {}")},
			"favicon.svg":          {Data: []byte("<svg></svg>")},
			"manifest.webmanifest": {Data: []byte("{}")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.server.Close(); f.store.Close() })
	if initialized {
		f.session, err = f.auth.Initialize(t.Context(), f.initial, "本地用户", testPassword, "本地测试浏览器")
		if err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f *serverFixture) advance(value time.Duration) { f.clock.Add(value.Milliseconds()) }

func (f *serverFixture) request(method, path string, body string, authenticated bool, mutate ...func(*http.Request)) *httptest.ResponseRecorder {
	f.t.Helper()
	r := httptest.NewRequest(method, "http://kakatta.test"+path, strings.NewReader(body))
	r.RemoteAddr = "203.0.113.9:3456"
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if authenticated {
		r.AddCookie(&http.Cookie{Name: cookieName, Value: f.session.Token})
	}
	for _, change := range mutate {
		change(r)
	}
	w := httptest.NewRecorder()
	f.server.ServeHTTP(w, r)
	return w
}

func requireStatus(t *testing.T, result *httptest.ResponseRecorder, status int) {
	t.Helper()
	if result.Code != status {
		t.Fatalf("响应状态为 %d，预期 %d，错误代码：%s", result.Code, status, errorCode(result))
	}
}

func errorCode(result *httptest.ResponseRecorder) string {
	var value ErrorEnvelope
	_ = json.Unmarshal(result.Body.Bytes(), &value)
	return value.Error.Code
}

func decodeBody[T any](t *testing.T, result *httptest.ResponseRecorder) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(result.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func activeCookie(t *testing.T, result *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, cookie := range result.Result().Cookies() {
		if cookie.MaxAge >= 0 && cookie.Value != "" {
			return cookie
		}
	}
	t.Fatal("没有发放会话 Cookie")
	return nil
}

func TestAllDeclaredBusinessEndpointsRequireAuthentication(t *testing.T) {
	f := newBusinessFixture(t)
	for _, operation := range testAPIOperations(t) {
		if len(operation.Security) == 0 {
			continue
		}
		path := sampleRoutePath(operation.Path)
		t.Run(operation.Method+" "+path, func(t *testing.T) {
			result := f.request(operation.Method, path, `{}`, false)
			requireStatus(t, result, http.StatusUnauthorized)
			if errorCode(result) != "auth.unauthenticated" {
				t.Fatal(errorCode(result))
			}
		})
	}
	for _, path := range []string{"/api/v1/missing", "/mcp", "/remote/viewer.js", "/openapi.json", "/docs/index.html"} {
		requireStatus(t, f.request(http.MethodGet, path, "", false), http.StatusUnauthorized)
	}
}

func TestSetupLoginAndSlidingCookie(t *testing.T) {
	f := newServerFixture(t, false)
	status := f.request(http.MethodGet, "/api/v1/setup", "", false)
	requireStatus(t, status, http.StatusOK)
	if decodeBody[auth.SetupStatus](t, status).Initialized {
		t.Fatal("新数据目录不能已经初始化")
	}
	body, _ := json.Marshal(map[string]string{"token": f.initial, "username": "本地用户", "password": testPassword})
	setup := f.request(http.MethodPost, "/api/v1/setup", string(body), false)
	requireStatus(t, setup, http.StatusOK)
	cookie := activeCookie(t, setup)
	if cookie.Name != cookieName || !cookie.HttpOnly || cookie.Secure || cookie.Domain != "" || cookie.Path != "/" || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatal("明文入口的 Cookie 属性不符合约定")
	}
	if cookie.Expires.UnixMilli() != f.clock.Load()+30*24*time.Hour.Milliseconds() {
		t.Fatal("首次会话期限不正确")
	}
	f.session.Token = cookie.Value
	me := decodeBody[meResponse](t, setup)
	f.session.Identity.TokenID = me.TokenID
	if me.Username != "本地用户" || me.Language != "system" || me.Appearance != "system" || me.TOTPEnabled {
		t.Fatal("初始化身份不完整")
	}
	status = f.request(http.MethodGet, "/api/v1/setup", "", false)
	if value := decodeBody[auth.SetupStatus](t, status); !value.Initialized || value.TokenExpiresAt != nil {
		t.Fatal("初始化成功后必须清掉初始化令牌有效期")
	}
	requireStatus(t, f.request(http.MethodPost, "/api/v1/setup", string(body), false), http.StatusConflict)
	f.advance(time.Second)
	f.advance(29 * 24 * time.Hour)
	get := f.request(http.MethodGet, "/api/v1/me", "", true)
	requireStatus(t, get, http.StatusOK)
	if renewed := activeCookie(t, get); renewed.Value != cookie.Value || !renewed.Expires.After(cookie.Expires) {
		t.Fatal("使用会话后应同步滑动浏览器 Cookie 期限")
	}
	login, _ := json.Marshal(map[string]string{"username": "本地用户", "password": testPassword})
	result := f.request(http.MethodPost, "/api/v1/auth/login", string(login), false)
	requireStatus(t, result, http.StatusOK)
	if next := activeCookie(t, result); next.Value == cookie.Value {
		t.Fatal("重新登录必须换会话 ID")
	}
	if value := decodeBody[map[string]json.RawMessage](t, result); string(value["status"]) != `"authenticated"` || value["user"] == nil {
		t.Fatal("密码登录响应不符合描述")
	}
}

func TestLoginFailureRateAndSecretRedaction(t *testing.T) {
	f := newServerFixture(t, true)
	first := f.request(http.MethodPost, "/api/v1/auth/login", `{"username":"不存在的用户","password":"敏感测试标记"}`, false, func(r *http.Request) {
		r.Header.Set("X-Request-Id", "请求头敏感标记")
	})
	requireStatus(t, first, http.StatusUnauthorized)
	var envelope ErrorEnvelope
	if err := json.Unmarshal(first.Body.Bytes(), &envelope); err != nil || envelope.Error.Code != "auth.invalid_credentials" || envelope.RequestID == "" || envelope.Fields == nil || !strings.Contains(first.Body.String(), `"args":{}`) {
		t.Fatal("错误格式不完整")
	}
	for _, secret := range []string{"不存在的用户", "敏感测试标记", "请求头敏感标记", testPassword} {
		if strings.Contains(first.Body.String(), secret) {
			t.Fatal("错误响应回显了敏感输入")
		}
	}
	blocked := f.request(http.MethodPost, "/api/v1/auth/login", `{"username":"本地用户","password":"错误"}`, false)
	requireStatus(t, blocked, http.StatusTooManyRequests)
	if blocked.Header().Get("Retry-After") != "1" || errorCode(blocked) != "auth.rate_limited" {
		t.Fatal("登录保护缺少重试时间")
	}
	f.advance(time.Second)
	body, _ := json.Marshal(map[string]string{"username": "本地用户", "password": testPassword})
	requireStatus(t, f.request(http.MethodPost, "/api/v1/auth/login", string(body), false), http.StatusOK)
}

func TestStrictJSONAndBodyLimit(t *testing.T) {
	f := newServerFixture(t, true)
	for name, body := range map[string]string{
		"未知字段":  `{"name":"设备","other":"秘密"}`,
		"重复字段":  `{"name":"设备","name":"秘密"}`,
		"转义重复":  `{"name":"设备","na\u006de":"秘密"}`,
		"大小写不符": `{"Name":"设备"}`,
		"尾部对象":  `{"name":"设备"}{}`,
		"数组":    `[]`, "空值": `null`, "缺少": `{}`, "类型错误": `{"name":3}`,
		"非法编码": "{\"name\":\"\xff\"}",
	} {
		t.Run(name, func(t *testing.T) {
			result := f.request(http.MethodPost, "/api/v1/security/tokens", body, true)
			requireStatus(t, result, http.StatusBadRequest)
			if strings.Contains(result.Body.String(), "秘密") {
				t.Fatal("JSON 错误不应回显值")
			}
		})
	}
	tooLarge := `{"name":"` + strings.Repeat("x", maxBody) + `"}`
	requireStatus(t, f.request(http.MethodPost, "/api/v1/security/tokens", tooLarge, true), http.StatusRequestEntityTooLarge)
	requireStatus(t, f.request(http.MethodPost, "/api/v1/security/tokens", `{"name":"设备"}`, true, func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }), http.StatusUnsupportedMediaType)
	requireStatus(t, f.request(http.MethodPost, "/api/v1/security/tokens", `{"name":"设备"}`, true), http.StatusCreated)
}

func TestSettingsAndTokenLifecycle(t *testing.T) {
	f := newServerFixture(t, true)
	get := f.request(http.MethodGet, "/api/v1/settings", "", true)
	requireStatus(t, get, http.StatusOK)
	if len(decodeBody[settings.Snapshot](t, get).Values) != 50 {
		t.Fatal("设置必须覆盖全部 50 项")
	}
	definitions := f.request(http.MethodGet, "/api/v1/settings/definitions", "", true)
	requireStatus(t, definitions, http.StatusOK)
	if len(decodeBody[[]settings.Definition](t, definitions)) != 50 {
		t.Fatal("设置元数据不完整")
	}
	requireStatus(t, f.request(http.MethodPatch, "/api/v1/settings", `{"values":{"general.language":"en","general.appearance":"dark"}}`, true), http.StatusOK)
	me := decodeBody[meResponse](t, f.request(http.MethodGet, "/api/v1/me", "", true))
	if me.Language != "en" || me.Appearance != "dark" {
		t.Fatal("身份响应应使用当前界面设置")
	}
	invalid := f.request(http.MethodPatch, "/api/v1/settings", `{"values":{"general.language":"zh-CN","crawl.account_hourly_visits":0}}`, true)
	requireStatus(t, invalid, http.StatusUnprocessableEntity)
	if len(decodeBody[ErrorEnvelope](t, invalid).Fields) == 0 {
		t.Fatal("设置校验应返回字段原因")
	}
	if decodeBody[meResponse](t, f.request(http.MethodGet, "/api/v1/me", "", true)).Language != "en" {
		t.Fatal("非法设置不能部分保存")
	}
	created := f.request(http.MethodPost, "/api/v1/security/tokens", `{"name":"本地脚本"}`, true)
	requireStatus(t, created, http.StatusCreated)
	issued := decodeBody[struct {
		ID    int64  `json:"id"`
		Token string `json:"token"`
	}](t, created)
	if len(issued.Token) != 43 {
		t.Fatal("新令牌缺少一次性明文")
	}
	useAPI := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+issued.Token) }
	apiMe := f.request(http.MethodGet, "/api/v1/me", "", false, useAPI)
	requireStatus(t, apiMe, http.StatusOK)
	if len(apiMe.Result().Cookies()) != 0 || decodeBody[meResponse](t, apiMe).AuthKind != auth.APIToken {
		t.Fatal("令牌鉴权不能发网页会话")
	}
	list := f.request(http.MethodGet, "/api/v1/security/tokens", "", true)
	requireStatus(t, list, http.StatusOK)
	if strings.Contains(list.Body.String(), issued.Token) || strings.Contains(list.Body.String(), f.session.Token) || strings.Contains(list.Body.String(), "password_hash") {
		t.Fatal("列表只能返回令牌元数据")
	}
	requireStatus(t, f.request(http.MethodDelete, "/api/v1/security/tokens/"+strconv.FormatInt(issued.ID, 10), "", true), http.StatusNoContent)
	requireStatus(t, f.request(http.MethodGet, "/api/v1/me", "", false, useAPI), http.StatusUnauthorized)
	requireStatus(t, f.request(http.MethodDelete, "/api/v1/security/tokens/0", "", true), http.StatusBadRequest)
	requireStatus(t, f.request(http.MethodDelete, "/api/v1/security/tokens/999999", "", true), http.StatusNotFound)
	requireStatus(t, f.request(http.MethodPost, "/api/v1/auth/logout", "", true), http.StatusNoContent)
	requireStatus(t, f.request(http.MethodGet, "/api/v1/me", "", true), http.StatusUnauthorized)
}

func TestCookieAndCredentialAmbiguity(t *testing.T) {
	f := newServerFixture(t, true)
	token, err := f.auth.CreateToken(t.Context(), f.session.Identity, "脚本")
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*http.Request){
		"网页和脚本":       func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token.Token) },
		"重复Cookie":    func(r *http.Request) { r.AddCookie(&http.Cookie{Name: cookieName, Value: f.session.Token}) },
		"两种Cookie":    func(r *http.Request) { r.AddCookie(&http.Cookie{Name: secureCookieName, Value: f.session.Token}) },
		"加密入口弱Cookie": func(r *http.Request) { r.TLS = &tls.ConnectionState{} },
		"多鉴权头": func(r *http.Request) {
			r.Header.Add("Authorization", "Bearer "+token.Token)
			r.Header.Add("Authorization", "Bearer "+token.Token)
		},
	} {
		t.Run(name, func(t *testing.T) {
			requireStatus(t, f.request(http.MethodGet, "/api/v1/me", "", true, mutate), http.StatusUnauthorized)
		})
	}
	login, _ := json.Marshal(map[string]string{"username": "本地用户", "password": testPassword})
	secure := f.request(http.MethodPost, "/api/v1/auth/login", string(login), false, func(r *http.Request) { r.TLS = &tls.ConnectionState{} })
	requireStatus(t, secure, http.StatusOK)
	cookie := activeCookie(t, secure)
	if cookie.Name != secureCookieName || !cookie.Secure || !cookie.HttpOnly || cookie.Domain != "" || cookie.Path != "/" || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatal("加密入口的 Cookie 属性不符合约定")
	}
	requireStatus(t, f.request(http.MethodGet, "/api/v1/me", "", false, func(r *http.Request) { r.TLS = &tls.ConnectionState{}; r.AddCookie(cookie) }), http.StatusOK)
}

func TestCrossOriginAndTrustedProxy(t *testing.T) {
	f := newServerFixture(t, true)
	for _, path := range []string{"/api/v1/auth/login", "/api/v1/setup", "/api/v1/security/tokens", "/api/v1/settings"} {
		requireStatus(t, f.request(http.MethodPost, path, `{}`, true, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }), http.StatusForbidden)
	}
	requireStatus(t, f.request(http.MethodPost, "/api/v1/security/tokens", `{"name":"脚本"}`, true, func(r *http.Request) { r.Header.Set("Origin", "https://evil.invalid") }), http.StatusForbidden)
	requireStatus(t, f.request(http.MethodPost, "/api/v1/security/tokens", `{"name":"脚本"}`, true, func(r *http.Request) { r.Header.Set("Origin", "http://kakatta.test") }), http.StatusCreated)
	spoofed := f.request(http.MethodGet, "/api/v1/me", "", true, func(r *http.Request) {
		r.Header.Set("X-Forwarded-Proto", "https")
		r.Header.Set("X-Forwarded-Host", "evil.invalid")
	})
	requireStatus(t, spoofed, http.StatusOK)
	if activeCookie(t, spoofed).Secure {
		t.Fatal("非可信请求头不能改变 Cookie 安全属性")
	}
	_, err := f.settings.Apply(t.Context(), settings.Values{"security.trusted_proxies": json.RawMessage(`["10.0.0.1"]`)})
	if err != nil {
		t.Fatal(err)
	}
	proxy := func(r *http.Request) {
		r.RemoteAddr = "10.0.0.1:4567"
		r.Header.Set("X-Forwarded-For", "203.0.113.66")
		r.Header.Set("X-Forwarded-Proto", "https")
		r.Header.Set("X-Forwarded-Host", "kakatta.example")
		r.Header.Set("Origin", "https://kakatta.example")
	}
	body, _ := json.Marshal(map[string]string{"username": "本地用户", "password": testPassword})
	login := f.request(http.MethodPost, "/api/v1/auth/login", string(body), false, proxy)
	requireStatus(t, login, http.StatusOK)
	cookie := activeCookie(t, login)
	if cookie.Name != secureCookieName || !cookie.Secure {
		t.Fatal("可信代理的加密入口必须发安全 Cookie")
	}
	requireStatus(t, f.request(http.MethodPatch, "/api/v1/settings", `{"values":{"general.language":"en"}}`, false, proxy, func(r *http.Request) { r.AddCookie(cookie) }), http.StatusOK)
	bad := f.request(http.MethodGet, "/api/v1/me", "", false, proxy, func(r *http.Request) { r.Header.Set("X-Forwarded-For", "敏感代理头标记"); r.AddCookie(cookie) })
	requireStatus(t, bad, http.StatusBadRequest)
	if strings.Contains(bad.Body.String(), "敏感代理头标记") || errorCode(bad) != "http.invalid_forwarded" {
		t.Fatal("非法代理头不能被回显或忽略")
	}
}

func TestWriteOriginMatchesExternalSchemeAndHost(t *testing.T) {
	f := newServerFixture(t, true)
	for _, test := range []struct {
		name, host, origin string
		status             int
	}{
		{"无来源头", "kakatta.test", "", http.StatusUnauthorized},
		{"同源", "kakatta.test", "https://kakatta.test", http.StatusUnauthorized},
		{"同源的默认端口", "kakatta.test:443", "https://kakatta.test", http.StatusUnauthorized},
		{"协议不同", "kakatta.test", "http://kakatta.test", http.StatusForbidden},
		{"端口不同", "kakatta.test:8080", "https://kakatta.test", http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := f.request(http.MethodPatch, "/api/v1/settings", "", false, func(r *http.Request) {
				r.TLS = &tls.ConnectionState{}
				r.Host = test.host
				if test.origin != "" {
					r.Header.Set("Origin", test.origin)
				}
			})
			requireStatus(t, result, test.status)
		})
	}
}

func TestTOTPAndPasswordFlows(t *testing.T) {
	f := newServerFixture(t, true)
	body, _ := json.Marshal(map[string]string{"password": testPassword})
	setup := f.request(http.MethodPost, "/api/v1/security/totp/setup", string(body), true)
	requireStatus(t, setup, http.StatusOK)
	binding := decodeBody[struct {
		SetupToken string `json:"setup_token"`
		Secret     string `json:"secret"`
		URI        string `json:"uri"`
	}](t, setup)
	if binding.Secret == "" || !strings.HasPrefix(binding.URI, "otpauth://") {
		t.Fatal("绑定响应不完整")
	}
	code := totpCode(binding.Secret, time.UnixMilli(f.clock.Load()))
	enable, _ := json.Marshal(map[string]string{"setup_token": binding.SetupToken, "code": code})
	requireStatus(t, f.request(http.MethodPost, "/api/v1/security/totp/enable", string(enable), true), http.StatusNoContent)
	if !decodeBody[meResponse](t, f.request(http.MethodGet, "/api/v1/me", "", true)).TOTPEnabled {
		t.Fatal("验证码通过后应启用两步验证")
	}
	var encrypted []byte
	if err := f.store.DB().QueryRow("SELECT totp_secret_enc FROM admin").Scan(&encrypted); err != nil || bytes.Contains(encrypted, []byte(binding.Secret)) {
		t.Fatal("绑定密钥必须加密存储")
	}
	f.advance(30 * time.Second)
	loginBody, _ := json.Marshal(map[string]string{"username": "本地用户", "password": testPassword})
	login := f.request(http.MethodPost, "/api/v1/auth/login", string(loginBody), false)
	requireStatus(t, login, http.StatusOK)
	challenge := decodeBody[struct {
		Status    string `json:"status"`
		Challenge string `json:"challenge"`
	}](t, login)
	if challenge.Status != "totp_required" || challenge.Challenge == "" || len(login.Result().Cookies()) != 0 {
		t.Fatal("第二步验证前不能发会话")
	}
	verifyBody, _ := json.Marshal(map[string]string{"challenge": challenge.Challenge, "code": totpCode(binding.Secret, time.UnixMilli(f.clock.Load()))})
	verified := f.request(http.MethodPost, "/api/v1/auth/totp", string(verifyBody), false)
	requireStatus(t, verified, http.StatusOK)
	if activeCookie(t, verified).Value == f.session.Token {
		t.Fatal("第二步通过后必须发新会话")
	}
	f.advance(30 * time.Second)
	disable, _ := json.Marshal(map[string]string{"password": testPassword, "code": totpCode(binding.Secret, time.UnixMilli(f.clock.Load()))})
	requireStatus(t, f.request(http.MethodPost, "/api/v1/security/totp/disable", string(disable), true), http.StatusNoContent)
	if decodeBody[meResponse](t, f.request(http.MethodGet, "/api/v1/me", "", true)).TOTPEnabled {
		t.Fatal("关闭两步验证后身份状态应更新")
	}
	token, err := f.auth.CreateToken(t.Context(), f.session.Identity, "旧脚本")
	if err != nil {
		t.Fatal(err)
	}
	change, _ := json.Marshal(map[string]string{"old_password": testPassword, "new_password": "新的本地密码"})
	requireStatus(t, f.request(http.MethodPost, "/api/v1/security/password", string(change), true), http.StatusNoContent)
	requireStatus(t, f.request(http.MethodGet, "/api/v1/me", "", true), http.StatusOK)
	requireStatus(t, f.request(http.MethodGet, "/api/v1/me", "", false, func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token.Token) }), http.StatusUnauthorized)
	newBody, _ := json.Marshal(map[string]string{"username": "本地用户", "password": "新的本地密码"})
	requireStatus(t, f.request(http.MethodPost, "/api/v1/auth/login", string(newBody), false), http.StatusOK)
}

func totpCode(secret string, at time.Time) string {
	key, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	var data [8]byte
	binary.BigEndian.PutUint64(data[:], uint64(at.Unix()/30))
	h := hmac.New(sha1.New, key)
	h.Write(data[:])
	digest := h.Sum(nil)
	index := digest[len(digest)-1] & 15
	number := binary.BigEndian.Uint32(digest[index:index+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", number%1000000)
}

func TestHealthStaticAndExtensionBoundaries(t *testing.T) {
	f := newServerFixture(t, true)
	for _, item := range []struct {
		path, accept string
		status       int
		html         bool
	}{
		{"/", "", 200, true}, {"/assets/a.js", "", 200, false}, {"/results/42", "text/html", 200, true},
		{"/results/42", "application/json", 404, false}, {"/results/42", "text/html;q=0", 404, false},
		{"/assets/missing.js", "text/html", 404, false}, {"/missing.svg", "text/html", 404, false},
		{"/api/v1/missing", "text/html", 404, false}, {"/remote/viewer.js", "text/html", 404, false}, {"/mcp", "text/html", 404, false},
	} {
		t.Run(item.path+item.accept, func(t *testing.T) {
			result := f.request(http.MethodGet, item.path, "", true, func(r *http.Request) { r.Header.Set("Accept", item.accept) })
			requireStatus(t, result, item.status)
			if strings.Contains(result.Body.String(), "<!doctype html>") != item.html {
				t.Fatal("静态回退边界不正确")
			}
		})
	}
	requireStatus(t, f.request(http.MethodGet, "/api/v1/openapi.json", "", true), http.StatusOK)
	requireStatus(t, f.request(http.MethodPut, "/api/v1/settings", "{}", true), http.StatusMethodNotAllowed)
	f.server.RegisterProtected("POST /api/v1/extension", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if identity, ok := IdentityFromContext(r.Context()); !ok || identity.TokenID != f.session.Identity.TokenID {
			t.Error("扩展接口缺少当前身份")
		}
		WriteJSON(w, r, http.StatusOK, map[string]bool{"ok": true})
	}))
	requireStatus(t, f.request(http.MethodPost, "/api/v1/extension", "", false), http.StatusUnauthorized)
	requireStatus(t, f.request(http.MethodPost, "/api/v1/extension", "", true), http.StatusOK)
	f.settings.mu.Lock()
	f.settings.err = errors.New("底层敏感数据库标记")
	f.settings.mu.Unlock()
	response := f.request(http.MethodGet, "/api/v1/me", "", true)
	requireStatus(t, response, http.StatusServiceUnavailable)
	if strings.Contains(response.Body.String(), "底层敏感数据库标记") {
		t.Fatal("不能泄露底层错误")
	}
	ready := f.request(http.MethodGet, "/healthz", "", false)
	requireStatus(t, ready, http.StatusOK)
	f.server.health = func(ctx context.Context) error {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("健康检查缺少超时")
		}
		return errors.New("底层敏感健康标记")
	}
	unavailable := f.request(http.MethodGet, "/healthz", "", false)
	requireStatus(t, unavailable, http.StatusServiceUnavailable)
	if strings.TrimSpace(unavailable.Body.String()) != `{"status":"unavailable"}` {
		t.Fatal("健康状态不应公开内部原因")
	}
	requireStatus(t, f.request(http.MethodPost, "/healthz", "", false), http.StatusMethodNotAllowed)
	f.server.Close()
	requireStatus(t, f.request(http.MethodGet, "/healthz", "", false), http.StatusServiceUnavailable)
}

func TestSlowJSONBodyReleasesLoginSlot(t *testing.T) {
	f := newServerFixture(t, true)
	server := httptest.NewServer(f.server)
	defer server.Close()
	connection, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(13 * time.Second)); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = io.WriteString(connection, "POST /api/v1/auth/login HTTP/1.1\r\nHost: kakatta.test\r\nContent-Type: application/json\r\nContent-Length: 100\r\nConnection: close\r\n\r\n{")
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(connection), &http.Request{Method: http.MethodPost})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest || time.Since(start) < 8*time.Second || time.Since(start) > 12*time.Second {
		t.Fatal("慢请求没有在有界时间内被拒绝")
	}
	f.server.limiter.mu.Lock()
	active := f.server.limiter.inflight
	f.server.limiter.mu.Unlock()
	if active != 0 {
		t.Fatal("慢请求结束后没有归还登录名额")
	}
}
