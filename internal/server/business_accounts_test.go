package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/service"
)

func createHTTPAccount(t *testing.T, f *businessFixture, name string, proxyID *int64) service.AccountView {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"name": name, "proxy_id": proxyID})
	result := f.request(http.MethodPost, "/api/v1/accounts", string(body), true)
	requireStatus(t, result, http.StatusCreated)
	return decodeBody[service.AccountView](t, result)
}

func createHTTPProxy(t *testing.T, f *businessFixture, name, address string) service.ProxyView {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"name": name, "url": address})
	result := f.request(http.MethodPost, "/api/v1/proxies", string(body), true)
	requireStatus(t, result, http.StatusCreated)
	for _, secret := range []string{address, "url_enc", "uuid"} {
		if strings.Contains(result.Body.String(), secret) {
			t.Fatal("代理响应不能包含地址、凭据或内部加密字段")
		}
	}
	return decodeBody[service.ProxyView](t, result)
}

func TestBusinessAccountsAndProxiesPersistPresenceWithoutSecrets(t *testing.T) {
	f := newBusinessFixture(t)
	proxy := createHTTPProxy(t, f, "本地代理", "http://private-user:private-password@127.0.0.1:18080")
	if proxy.ID < 1 || !proxy.Configured {
		t.Fatal("代理必须真实保存后才返回配置状态")
	}
	var encrypted []byte
	if err := f.store.DB().QueryRowContext(t.Context(), "SELECT url_enc FROM proxies WHERE id=?", proxy.ID).Scan(&encrypted); err != nil || len(encrypted) == 0 || bytes.Contains(encrypted, []byte("private-password")) {
		t.Fatal("代理必须使用密文持久化")
	}
	proxyPath := "/api/v1/proxies/" + strconv.FormatInt(proxy.ID, 10)
	requireStatus(t, f.request(http.MethodPatch, proxyPath, `{"name":"改名代理"}`, true), http.StatusOK)
	var after []byte
	if err := f.store.DB().QueryRowContext(t.Context(), "SELECT url_enc FROM proxies WHERE id=?", proxy.ID).Scan(&after); err != nil || !bytes.Equal(after, encrypted) {
		t.Fatal("没有传入地址时不能改写原代理密文")
	}
	account := createHTTPAccount(t, f, "账号一", &proxy.ID)
	if account.State != service.AccountSignedOut || account.ProxyID == nil || *account.ProxyID != proxy.ID || account.Session != nil {
		t.Fatal("新账号的状态或代理关联不正确")
	}
	path := "/api/v1/accounts/" + strconv.FormatInt(account.ID, 10)
	patch := f.request(http.MethodPatch, path, `{"name":"改名账号"}`, true)
	requireStatus(t, patch, http.StatusOK)
	updated := decodeBody[service.AccountView](t, patch)
	if updated.Name != "改名账号" || updated.ProxyID == nil || *updated.ProxyID != proxy.ID {
		t.Fatal("省略代理字段应保留原关联")
	}
	requireStatus(t, f.request(http.MethodDelete, proxyPath, "", true), http.StatusConflict)
	cleared := f.request(http.MethodPatch, path, `{"proxy_id":null}`, true)
	requireStatus(t, cleared, http.StatusOK)
	if decodeBody[service.AccountView](t, cleared).ProxyID != nil {
		t.Fatal("显式 null 应清空可空的代理关联")
	}
	for _, body := range []string{`{"name":null}`, `{"state":"active"}`, `{"cooldown_until":0}`, `{"proxy_id":999999}`} {
		requireStatus(t, f.request(http.MethodPatch, path, body, true), http.StatusUnprocessableEntity)
	}
	for _, body := range []string{`{"url":null}`, `{"uuid":"伪造"}`, `{"url":"https://秘密:敏感测试密码@127.0.0.1:443"}`} {
		result := f.request(http.MethodPatch, proxyPath, body, true)
		requireStatus(t, result, http.StatusUnprocessableEntity)
		if strings.Contains(result.Body.String(), "敏感测试密码") || strings.Contains(result.Body.String(), "秘密") {
			t.Fatal("代理校验错误不能回显输入凭据")
		}
	}
	if _, err := f.store.DB().ExecContext(t.Context(), "UPDATE accounts SET verify_url=? WHERE id=?", "https://private.invalid/verification-marker", account.ID); err != nil {
		t.Fatal(err)
	}
	get := f.request(http.MethodGet, path, "", true)
	requireStatus(t, get, http.StatusOK)
	if strings.Contains(get.Body.String(), "verification-marker") || strings.Contains(get.Body.String(), "verify_url") {
		t.Fatal("账号读取不能泄漏内部验证网址")
	}
	list := f.request(http.MethodGet, "/api/v1/accounts", "", true)
	requireStatus(t, list, http.StatusOK)
	if page := decodeBody[service.Page[service.AccountView]](t, list); page.Total != 1 || len(page.Items) != 1 || page.NextCursor != nil {
		t.Fatal("账号列表应来自真实数据库")
	}
	requireStatus(t, f.request(http.MethodDelete, path, "", true), http.StatusNoContent)
	requireStatus(t, f.request(http.MethodGet, path, "", true), http.StatusNotFound)
	requireStatus(t, f.request(http.MethodDelete, proxyPath, "", true), http.StatusNoContent)
	requireStatus(t, f.request(http.MethodPatch, proxyPath, `{"name":"已删除"}`, true), http.StatusNotFound)
}

func TestBusinessAccountLogoutKeepsQuotaAndProtection(t *testing.T) {
	f := newBusinessFixture(t)
	account := createHTTPAccount(t, f, "保留额度", nil)
	until := f.clock.Load() + int64(time.Hour/time.Millisecond)
	if _, err := f.store.DB().ExecContext(t.Context(), `UPDATE accounts SET state='needs_verification',verify_level=3,cooldown_until=?,verify_url=? WHERE id=?`, until, "https://private.invalid/verification-marker", account.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.DB().ExecContext(t.Context(), "INSERT INTO quota_events(account_id,kind,at) VALUES(?,'search',?)", account.ID, f.clock.Load()); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/accounts/" + strconv.FormatInt(account.ID, 10)
	result := f.request(http.MethodPost, path+"/logout", "", true)
	requireStatus(t, result, http.StatusOK)
	value := decodeBody[service.AccountView](t, result)
	if value.State != service.AccountCooling || value.Quota.HourlyUsed != 1 || value.Quota.DailySearchUsed != 1 || value.VerifyLevel != 3 || value.CooldownUntil == nil || *value.CooldownUntil != until {
		t.Fatal("退出账号不能恢复额度或清掉旧保护")
	}
	login := f.request(http.MethodPost, path+"/login", "", true)
	requireStatus(t, login, http.StatusConflict)
	if errorCode(login) != "run.account_cooling" {
		t.Fatal("退出后扫码没有按原冷却期限拦住")
	}
	var visits int
	if err := f.store.DB().QueryRowContext(t.Context(), "SELECT COUNT(*) FROM quota_events WHERE account_id=?", account.ID).Scan(&visits); err != nil || visits != 1 {
		t.Fatal("退出账号后额度记录丢失")
	}
}

func TestBusinessAccountAndProxyValidationAndPagination(t *testing.T) {
	f := newBusinessFixture(t)
	for _, body := range []string{`{"name":"重复","name":"另一个"}`, `null`, `[]`, `{"name":"非法\u0000"}{}`} {
		requireStatus(t, f.request(http.MethodPost, "/api/v1/accounts", body, true), http.StatusBadRequest)
	}
	requireStatus(t, f.request(http.MethodPost, "/api/v1/accounts", `{}`, true), http.StatusUnprocessableEntity)
	for _, path := range []string{"/api/v1/accounts?limit=0", "/api/v1/accounts?limit=201", "/api/v1/accounts?limit=1&limit=2", "/api/v1/accounts?unknown=1", "/api/v1/accounts?cursor=bad", "/api/v1/proxies?cursor=bad", "/api/v1/accounts/0", "/api/v1/accounts/-1", "/api/v1/accounts/9223372036854775808"} {
		requireStatus(t, f.request(http.MethodGet, path, "", true), http.StatusBadRequest)
	}
	first := createHTTPAccount(t, f, "第一", nil)
	second := createHTTPAccount(t, f, "第二", nil)
	page := decodeBody[service.Page[service.AccountView]](t, f.request(http.MethodGet, "/api/v1/accounts?limit=1", "", true))
	if page.Total != 2 || len(page.Items) != 1 || page.Items[0].ID != first.ID || page.NextCursor == nil {
		t.Fatal("账号分页顺序或游标不正确")
	}
	next := decodeBody[service.Page[service.AccountView]](t, f.request(http.MethodGet, "/api/v1/accounts?limit=1&cursor="+url.QueryEscape(*page.NextCursor), "", true))
	if next.Total != 2 || len(next.Items) != 1 || next.Items[0].ID != second.ID || next.NextCursor != nil {
		t.Fatal("后续页不能重复或丢掉账号")
	}
	for _, operation := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/accounts", `{"name":"匿名"}`},
		{http.MethodGet, "/api/v1/accounts", ""},
		{http.MethodGet, "/api/v1/accounts/1", ""},
		{http.MethodPatch, "/api/v1/accounts/1", `{"name":"匿名"}`},
		{http.MethodDelete, "/api/v1/accounts/1", ""},
		{http.MethodPost, "/api/v1/accounts/1/logout", ""},
		{http.MethodPost, "/api/v1/accounts/1/login", ""},
		{http.MethodPost, "/api/v1/accounts/1/verification", ""},
		{http.MethodPost, "/api/v1/accounts/1/verification/confirm", `{"session_id":"unknown"}`},
		{http.MethodGet, "/api/v1/accounts/1/sessions/unknown", ""},
		{http.MethodDelete, "/api/v1/accounts/1/sessions/unknown", ""},
		{http.MethodGet, "/api/v1/proxies", ""},
		{http.MethodPost, "/api/v1/proxies", `{"name":"匿名","url":"http://127.0.0.1:18080"}`},
		{http.MethodPatch, "/api/v1/proxies/1", `{"name":"匿名"}`},
		{http.MethodDelete, "/api/v1/proxies/1", ""},
		{http.MethodPost, "/api/v1/proxies/1/test", ""},
		{http.MethodGet, "/remote/sessions/unknown", ""},
		{http.MethodGet, "/remote/sessions/unknown/", ""},
		{http.MethodGet, "/remote/sessions/unknown/viewport", ""},
		{http.MethodGet, "/remote/sessions/unknown/viewer.js", ""},
		{http.MethodGet, "/remote/sessions/unknown/vnc", ""},
	} {
		requireStatus(t, f.request(operation.method, operation.path, operation.body, false), http.StatusUnauthorized)
	}
	blocked := f.request(http.MethodPost, "/api/v1/accounts", `{"name":"跨站"}`, true, func(r *http.Request) { r.Header.Set("Origin", "https://attacker.invalid") })
	requireStatus(t, blocked, http.StatusForbidden)
}

func TestBusinessProxyProbeOnlyConnectsToLocalProxy(t *testing.T) {
	f := newBusinessFixture(t)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	traffic := make(chan int, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			traffic <- -1
			return
		}
		defer connection.Close()
		_ = connection.SetReadDeadline(time.Now().Add(2 * time.Second))
		var one [1]byte
		n, _ := connection.Read(one[:])
		traffic <- n
	}()
	proxy := createHTTPProxy(t, f, "连通测试", "http://private-user:private-password@"+listener.Addr().String())
	server := httptest.NewServer(f.server)
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/api/v1/proxies/%d/test", server.URL, proxy.ID), nil)
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(&http.Cookie{Name: cookieName, Value: f.session.Token})
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var result service.ProxyTestResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil || response.StatusCode != http.StatusOK || !result.OK || result.Reason != nil {
		t.Fatal("本地代理连通测试没有返回真实成功结果")
	}
	select {
	case n := <-traffic:
		if n != 0 {
			t.Fatal("测试代理不能经过代理发起网站请求")
		}
	case <-ctx.Done():
		t.Fatal("没有观察到对本地代理的连接")
	}
	listener.Close()
	failed := f.request(http.MethodPost, fmt.Sprintf("/api/v1/proxies/%d/test", proxy.ID), "", true)
	requireStatus(t, failed, http.StatusOK)
	if value := decodeBody[service.ProxyTestResult](t, failed); value.OK || value.Reason == nil {
		t.Fatal("连通失败应返回明确的失败结果，不是假成功或内部错误")
	}
}

type heldAccountResources struct {
	started  chan struct{}
	canceled chan struct{}
	opens    atomic.Int32
}

func (f *heldAccountResources) Open(ctx context.Context, _ service.ResourceSpec) (service.SessionResource, error) {
	f.opens.Add(1)
	f.started <- struct{}{}
	<-ctx.Done()
	f.canceled <- struct{}{}
	return nil, ctx.Err()
}

func TestBusinessAccountLoginAcceptanceAndSessionReads(t *testing.T) {
	resources := &heldAccountResources{started: make(chan struct{}, 4), canceled: make(chan struct{}, 4)}
	f := newBusinessFixture(t, resources)
	account := createHTTPAccount(t, f, "人工登录", nil)
	path := fmt.Sprintf("/api/v1/accounts/%d", account.ID)
	requireStatus(t, f.request(http.MethodGet, path, "", true), http.StatusOK)
	requireStatus(t, f.request(http.MethodGet, "/api/v1/accounts", "", true), http.StatusOK)
	if resources.opens.Load() != 0 {
		t.Fatal("读取账号不能启动浏览器")
	}
	accepted := f.request(http.MethodPost, path+"/login", "", true)
	requireStatus(t, accepted, http.StatusAccepted)
	session := decodeBody[service.SessionView](t, accepted)
	if session.ID == "" || session.AccountID != account.ID || session.Kind != service.SessionLogin {
		t.Fatal("接纳登录必须返回真实会话编号")
	}
	select {
	case <-resources.started:
	case <-time.After(3 * time.Second):
		t.Fatal("已接纳登录没有进入真实资源工厂")
	}
	sessionPath := path + "/sessions/" + session.ID
	first := f.request(http.MethodGet, sessionPath, "", true)
	requireStatus(t, first, http.StatusOK)
	view := decodeBody[service.SessionView](t, first)
	f.advance(time.Minute)
	again := f.request(http.MethodGet, sessionPath, "", true)
	requireStatus(t, again, http.StatusOK)
	next := decodeBody[service.SessionView](t, again)
	if !equalOptionalTime(view.ExpiresAt, next.ExpiresAt) || resources.opens.Load() != 1 {
		t.Fatal("读取会话不能启动新资源或续期")
	}
	repeated := f.request(http.MethodPost, path+"/login", "", true)
	requireStatus(t, repeated, http.StatusAccepted)
	if same := decodeBody[service.SessionView](t, repeated); same.ID != session.ID || !equalOptionalTime(same.ExpiresAt, next.ExpiresAt) || resources.opens.Load() != 1 {
		t.Fatal("重复接纳登录不能续期或新开浏览器")
	}
	requireStatus(t, f.request(http.MethodGet, path+"/sessions/not-current", "", true), http.StatusNotFound)
	requireStatus(t, f.request(http.MethodDelete, sessionPath, "", true), http.StatusNoContent)
	select {
	case <-resources.canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("显式关闭会话没有取消其资源启动")
	}
	requireStatus(t, f.request(http.MethodGet, sessionPath, "", true), http.StatusNotFound)
	accountRead := decodeBody[service.AccountView](t, f.request(http.MethodGet, path, "", true))
	if accountRead.Session != nil {
		t.Fatal("关闭后账号不能残留假的活动会话")
	}
	f.advance(4 * time.Minute)
	reopened := f.request(http.MethodPost, path+"/login", "", true)
	requireStatus(t, reopened, http.StatusAccepted)
	fresh := decodeBody[service.SessionView](t, reopened)
	if fresh.ID == session.ID {
		t.Fatal("后来新建的会话不能复用旧编号")
	}
	select {
	case <-resources.started:
	case <-time.After(3 * time.Second):
		t.Fatal("新会话没有启动自己的资源")
	}
	requireStatus(t, f.request(http.MethodDelete, sessionPath, "", true), http.StatusNoContent)
	freshPath := path + "/sessions/" + fresh.ID
	requireStatus(t, f.request(http.MethodGet, freshPath, "", true), http.StatusOK)
	if resources.opens.Load() != 2 {
		t.Fatal("关闭旧编号误关了新会话或启动了额外资源")
	}
	requireStatus(t, f.request(http.MethodDelete, freshPath, "", true), http.StatusNoContent)
	select {
	case <-resources.canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("新会话关闭后没有回收启动中的资源")
	}
	requireStatus(t, f.request(http.MethodPost, path+"/verification", "", true), http.StatusConflict)
	requireStatus(t, f.request(http.MethodPost, path+"/verification/confirm", `{"session_id":"old"}`, true), http.StatusConflict)
}

func equalOptionalTime(left, right *int64) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func TestBusinessRemoteOriginUsesVerifiedPeer(t *testing.T) {
	for _, test := range []struct {
		origin string
		peer   requestPeer
		want   bool
	}{
		{"http://kakatta.test", requestPeer{Host: "kakatta.test"}, true},
		{"https://kakatta.test", requestPeer{Host: "kakatta.test", Secure: true}, true},
		{"https://KAKATTA.test:8443", requestPeer{Host: "kakatta.test:8443", Secure: true}, true},
		{"http://kakatta.test", requestPeer{Host: "kakatta.test", Secure: true}, false},
		{"https://attacker.invalid", requestPeer{Host: "kakatta.test", Secure: true}, false},
		{"https://kakatta.test/", requestPeer{Host: "kakatta.test", Secure: true}, false},
		{"https://user@kakatta.test", requestPeer{Host: "kakatta.test", Secure: true}, false},
		{"https://kakatta.test?q=1", requestPeer{Host: "kakatta.test", Secure: true}, false},
		{"", requestPeer{Host: "kakatta.test", Secure: true}, false},
	} {
		r := httptest.NewRequest(http.MethodGet, "http://internal.invalid/vnc", nil)
		r.Header.Set("Origin", test.origin)
		r.Header.Set("X-Forwarded-Host", "attacker.invalid")
		if got := sameOriginHost(r, test.peer) != ""; got != test.want {
			t.Fatalf("Origin校验不符合已核对来源：%v", got)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "http://internal.invalid/vnc", nil)
	r.Header.Add("Origin", "http://kakatta.test")
	r.Header.Add("Origin", "http://attacker.invalid")
	if sameOriginHost(r, requestPeer{Host: "kakatta.test"}) != "" {
		t.Fatal("多值Origin不能被当成同源")
	}
}

func TestBusinessRemoteLeaseStopsRevokedCredentials(t *testing.T) {
	f := newBusinessFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	business := &businessHandler{server: f.server, service: f.service}
	go business.watchAccountViewer(ctx, cancel, f.session.Identity, done)
	if err := f.auth.RevokeToken(t.Context(), f.session.Identity, f.session.Identity.TokenID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
		if ctx.Err() == nil {
			t.Fatal("吊销后必须取消该画面的请求上下文")
		}
	case <-time.After(6 * time.Second):
		t.Fatal("吊销后远程鉴权租约未及时退出")
	}
	if _, err := f.service.ListAccounts(t.Context(), service.ListQuery{}); err != nil {
		t.Fatal("吊销当前viewer不能关闭整个业务服务")
	}
}
