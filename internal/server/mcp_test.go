package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/auth"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func newMCPFixture(t *testing.T) (*businessFixture, auth.IssuedToken) {
	t.Helper()
	f := newBusinessFixture(t)
	if err := f.server.RegisterMCP(f.service); err != nil {
		t.Fatal(err)
	}
	token, err := f.auth.CreateToken(t.Context(), f.session.Identity, "本地工具测试")
	if err != nil {
		t.Fatal(err)
	}
	return f, token
}

func mcpBody(t *testing.T, method string, params any) string {
	t.Helper()
	data, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func mcpRequest(t *testing.T, f *businessFixture, token, body string, mutate ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	changes := []func(*http.Request){func(r *http.Request) {
		r.Header.Set("Accept", "application/json, text/event-stream")
		r.Header.Set("MCP-Protocol-Version", "2025-11-25")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
	}}
	return f.request(http.MethodPost, "/mcp", body, false, append(changes, mutate...)...)
}

func mcpToolResult(t *testing.T, response *httptest.ResponseRecorder) *mcp.CallToolResult {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("工具 HTTP 响应错误：%d %s", response.Code, response.Body.String())
	}
	var envelope struct {
		Result *mcp.CallToolResult `json:"result"`
		Error  *jsonrpc.Error      `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error != nil || envelope.Result == nil {
		t.Fatalf("工具协议响应错误：%s", response.Body.String())
	}
	if len(envelope.Result.Content) != 1 {
		t.Fatal("工具必须带一个 JSON 文本后备")
	}
	content, ok := envelope.Result.Content[0].(*mcp.TextContent)
	structured, err := json.Marshal(envelope.Result.StructuredContent)
	var textValue, structuredValue any
	if !ok || err != nil || json.Unmarshal([]byte(content.Text), &textValue) != nil || json.Unmarshal(structured, &structuredValue) != nil || !reflect.DeepEqual(textValue, structuredValue) {
		t.Fatal("文本后备与结构化结果不一致")
	}
	return envelope.Result
}

func mcpOutput[T any](t *testing.T, result *mcp.CallToolResult) T {
	t.Helper()
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var output T
	if err := json.Unmarshal(data, &output); err != nil {
		t.Fatal(err)
	}
	return output
}

func TestMCPRegistrationAndBearerOnly(t *testing.T) {
	f, token := newMCPFixture(t)
	if err := f.server.RegisterMCP(nil); err == nil {
		t.Fatal("不能注册空服务")
	}
	if err := f.server.RegisterMCP(f.service); err == nil {
		t.Fatal("不能重复注册工具接口")
	}
	body := mcpBody(t, "tools/list", map[string]any{})
	for _, test := range []struct {
		name, bearer string
		mutate       func(*http.Request)
	}{
		{"无凭据", "", func(*http.Request) {}},
		{"错令牌", "wrong", func(*http.Request) {}},
		{"网页令牌当Bearer", f.session.Token, func(*http.Request) {}},
		{"只给Cookie", "", func(r *http.Request) { r.AddCookie(&http.Cookie{Name: cookieName, Value: f.session.Token}) }},
		{"混合凭据", token.Token, func(r *http.Request) { r.AddCookie(&http.Cookie{Name: cookieName, Value: f.session.Token}) }},
		{"重复凭据", token.Token, func(r *http.Request) { r.Header.Add("Authorization", "Bearer "+token.Token) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			requireStatus(t, mcpRequest(t, f, test.bearer, body, test.mutate), http.StatusUnauthorized)
		})
	}
	requireStatus(t, mcpRequest(t, f, token.Token, body), http.StatusOK)
	if err := f.auth.RevokeToken(t.Context(), f.session.Identity, token.ID); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, mcpRequest(t, f, token.Token, body), http.StatusUnauthorized)
}

func TestMCPOriginAndTrustedProxyLocalhost(t *testing.T) {
	f, token := newMCPFixture(t)
	body := mcpBody(t, "tools/list", map[string]any{})
	for _, test := range []struct {
		origin []string
		status int
	}{
		{nil, 200}, {[]string{"http://kakatta.test"}, 200},
		{[]string{"null"}, 403}, {[]string{"https://kakatta.test"}, 403},
		{[]string{"http://other.test"}, 403}, {[]string{"http://kakatta.test/path"}, 403},
		{[]string{"http://user@kakatta.test"}, 403}, {[]string{"http://kakatta.test", "http://kakatta.test"}, 403},
	} {
		requireStatus(t, mcpRequest(t, f, token.Token, body, func(r *http.Request) {
			if test.origin != nil {
				r.Header["Origin"] = test.origin
			}
		}), test.status)
	}
	proxyRequest := func(trusted bool) *httptest.ResponseRecorder {
		return mcpRequest(t, f, token.Token, body, func(r *http.Request) {
			r.RemoteAddr = "127.0.0.1:3456"
			ctx := context.WithValue(r.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8080})
			*r = *r.WithContext(ctx)
			r.Host = "public.test"
			r.Header.Set("Origin", "https://public.test")
			r.Header.Set("X-Forwarded-Host", "public.test")
			r.Header.Set("X-Forwarded-Proto", "https")
			if !trusted {
				r.Header.Set("Origin", "http://public.test")
			}
		})
	}
	requireStatus(t, proxyRequest(false), http.StatusForbidden)
	if _, err := f.settings.Apply(t.Context(), settings.Values{"security.trusted_proxies": json.RawMessage(`["127.0.0.1"]`)}); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, proxyRequest(true), http.StatusOK)
	requireStatus(t, mcpRequest(t, f, token.Token, body, func(r *http.Request) {
		r.Header.Set("Origin", "https://public.test")
		r.Header.Set("X-Forwarded-Host", "public.test")
		r.Header.Set("X-Forwarded-Proto", "https")
	}), http.StatusForbidden)
}

func TestMCPBodyAndTransportBoundaries(t *testing.T) {
	f, token := newMCPFixture(t)
	body := mcpBody(t, "tools/list", map[string]any{})
	for _, test := range []struct {
		body   string
		status int
		mutate func(*http.Request)
	}{
		{body, 415, func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }},
		{body, 400, func(r *http.Request) { r.Header.Add("Content-Type", "application/json") }},
		{body, 400, func(r *http.Request) { r.Header.Set("Accept", "application/json") }},
		{body, 400, func(r *http.Request) { r.Header.Set("MCP-Protocol-Version", "2025-01-01") }},
		{body, 400, func(r *http.Request) { r.Header.Add("MCP-Protocol-Version", "2025-11-25") }},
		{`{"jsonrpc":"2.0","id":1,"method":"tools/list","method":"tools/list"}`, 400, nil},
		{`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"pause_task","arguments":{"task_id":1,"task_id":2}}}`, 400, nil},
		{body + `{}`, 400, nil},
		{string([]byte{0xff}), 400, nil},
		{strings.Repeat("[", 101) + "0" + strings.Repeat("]", 101), 400, nil},
		{strings.Repeat(" ", maxBody) + body, 413, nil},
		{strings.Repeat(" ", maxBody) + body, 413, func(r *http.Request) { r.ContentLength = -1; r.TransferEncoding = []string{"chunked"} }},
	} {
		var mutate []func(*http.Request)
		if test.mutate != nil {
			mutate = append(mutate, test.mutate)
		}
		requireStatus(t, mcpRequest(t, f, token.Token, test.body, mutate...), test.status)
	}
	for _, method := range []string{http.MethodGet, http.MethodDelete, http.MethodHead, http.MethodOptions} {
		response := f.request(method, "/mcp", "", false, func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token.Token) })
		requireStatus(t, response, http.StatusMethodNotAllowed)
		if response.Header().Get("Allow") != "POST" {
			t.Fatal("无状态接口只接受POST")
		}
	}
	requireStatus(t, mcpRequest(t, f, token.Token, `{"jsonrpc":"2.0","method":"notifications/initialized"}`), http.StatusAccepted)
	padding := strings.Repeat(" ", maxBody-len(body)) + body
	requireStatus(t, mcpRequest(t, f, token.Token, padding), http.StatusOK)
}

func TestMCPCurrentProtocolMetadata(t *testing.T) {
	f, token := newMCPFixture(t)
	params := map[string]any{"_meta": map[string]any{
		mcp.MetaKeyProtocolVersion:    "2026-07-28",
		mcp.MetaKeyClientInfo:         map[string]any{"name": "本地测试", "version": "1"},
		mcp.MetaKeyClientCapabilities: map[string]any{},
	}}
	body := mcpBody(t, "tools/list", params)
	setHeaders := func(r *http.Request) {
		r.Header.Set("MCP-Protocol-Version", "2026-07-28")
		r.Header.Set("Mcp-Method", "tools/list")
	}
	requireStatus(t, mcpRequest(t, f, token.Token, body, setHeaders), http.StatusOK)
	requireStatus(t, mcpRequest(t, f, token.Token, body, setHeaders, func(r *http.Request) { r.Header.Set("Mcp-Method", "tools/call") }), http.StatusBadRequest)
}

func TestMCPMiddlewareRechecksIdentityAndPreservesCancellation(t *testing.T) {
	f, token := newMCPFixture(t)
	identity, err := f.auth.Authenticate(t.Context(), token.Token, auth.APIToken)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(t.Context(), identityKey, identity)
	if err := f.auth.RevokeToken(t.Context(), f.session.Identity, token.ID); err != nil {
		t.Fatal(err)
	}
	called := false
	handler := f.server.mcpMiddleware(func(context.Context, string, mcp.Request) (mcp.Result, error) {
		called = true
		return nil, nil
	})
	result, err := handler(ctx, "tools/call", nil)
	if err != nil || called || mcpOutput[ErrorEnvelope](t, result.(*mcp.CallToolResult)).Error.Code != "auth.unauthenticated" {
		t.Fatal("工具入口必须重新检查已吊销身份")
	}
	original, cancel := context.WithCancel(t.Context())
	stripped := context.WithValue(context.Background(), mcpHTTPContextKey{}, original)
	started, finished := make(chan struct{}), make(chan struct{})
	handler = f.server.mcpMiddleware(func(ctx context.Context, _ string, _ mcp.Request) (mcp.Result, error) {
		close(started)
		<-ctx.Done()
		close(finished)
		return nil, ctx.Err()
	})
	go func() { _, _ = handler(stripped, "ping", nil) }()
	<-started
	cancel()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("旧协议剥离取消后仍应响应原HTTP取消")
	}
}

func TestMCPMiddlewareMasksInternalErrors(t *testing.T) {
	f, _ := newMCPFixture(t)
	for _, failure := range []error{errors.New("数据库密码秘密"), &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "数据库密码秘密"}} {
		handler := f.server.mcpMiddleware(func(context.Context, string, mcp.Request) (mcp.Result, error) { return nil, failure })
		_, err := handler(t.Context(), "ping", nil)
		if err == nil || strings.Contains(err.Error(), "秘密") || !strings.Contains(err.Error(), "http.internal") {
			t.Fatal("内部错误必须脱敏")
		}
	}
}

func TestMCPAuthStorageFailureStopsBeforeBusiness(t *testing.T) {
	f, token := newMCPFixture(t)
	identity, err := f.auth.Authenticate(t.Context(), token.Token, auth.APIToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.DB().ExecContext(t.Context(), `ALTER TABLE auth_tokens RENAME TO unavailable_tokens`); err != nil {
		t.Fatal(err)
	}
	called := false
	handler := f.server.mcpMiddleware(func(context.Context, string, mcp.Request) (mcp.Result, error) { called = true; return nil, nil })
	result, err := handler(context.WithValue(t.Context(), identityKey, identity), "tools/call", nil)
	if err != nil || called || mcpOutput[ErrorEnvelope](t, result.(*mcp.CallToolResult)).Error.Code != "http.unavailable" {
		t.Fatal("认证存储失败必须拦住，不能进入业务或透传SQL错误")
	}
}

type mcpBearerTransport struct {
	base  http.RoundTripper
	token string
}

func (transport mcpBearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header.Set("Authorization", "Bearer "+transport.token)
	return transport.base.RoundTrip(clone)
}

func TestMCPOfficialSDKOverLocalHTTP(t *testing.T) {
	for _, version := range []string{"2025-11-25", "2026-07-28"} {
		t.Run(version, func(t *testing.T) { testMCPOfficialSDK(t, version) })
	}
}

func testMCPOfficialSDK(t *testing.T, version string) {
	t.Helper()
	f, token := newMCPFixture(t)
	host := httptest.NewServer(f.server)
	defer host.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "本地回归", Version: "1"}, nil)
	httpClient := &http.Client{Transport: mcpBearerTransport{base: host.Client().Transport, token: token.Token}}
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: host.URL + "/mcp", HTTPClient: httpClient, DisableStandaloneSSE: true, MaxRetries: -1}, &mcp.ClientSessionOptions{ProtocolVersion: version})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(t.Context(), nil)
	if err != nil || len(tools.Tools) != 6 {
		t.Fatalf("官方客户端不能发现六个工具：%v", err)
	}
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "list_tasks", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatalf("官方客户端不能调用列表：%v", err)
	}
	if err := f.auth.RevokeToken(t.Context(), f.session.Identity, token.ID); err != nil {
		t.Fatal(err)
	}
	_, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: "list_tasks", Arguments: map[string]any{}})
	if err == nil {
		t.Fatal("已建客户端不能继续使用被吊销的Bearer")
	}
	response, err := host.Client().Get(host.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
}

func TestMCPSubscriptionsEndWithoutKeepingIdleSession(t *testing.T) {
	f, token := newMCPFixture(t)
	params := map[string]any{
		"notifications": map[string]any{"toolsListChanged": true, "resourcesListChanged": true},
		"_meta": map[string]any{
			mcp.MetaKeyProtocolVersion: "2026-07-28", mcp.MetaKeyClientInfo: map[string]any{"name": "本地测试", "version": "1"}, mcp.MetaKeyClientCapabilities: map[string]any{},
		},
	}
	body := mcpBody(t, "subscriptions/listen", params)
	host := httptest.NewServer(f.server)
	defer host.Close()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, host.URL+"/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token.Token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2026-07-28")
	request.Header.Set("Mcp-Method", "subscriptions/listen")
	client := host.Client()
	client.Timeout = 2 * time.Second
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK || !strings.Contains(string(data), "notifications/subscriptions/acknowledged") || strings.Contains(string(data), `"toolsListChanged":true`) {
		t.Fatal("未声明订阅能力时必须立即结束流，不能留闲置会话")
	}
}
