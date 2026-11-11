package modelservice

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis/client"
)

const modelResponse = `{"choices":[{"message":{"content":"本地回复"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3}}`

func networkProvider(t *testing.T, f *modelFixture, address string) ProviderView {
	t.Helper()
	input := modelInput()
	input.BaseURL = address
	return f.create(t, input)
}

func currentNetwork(s *Service) *networkClient {
	s.network.mu.Lock()
	defer s.network.mu.Unlock()
	return s.network.current
}

func completeLocal(t *testing.T, f *modelFixture) client.Result {
	t.Helper()
	result, err := f.s.Complete(t.Context(), client.Request{Kind: client.Market, User: "本地测试"})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func saveModelProxy(t *testing.T, f *modelFixture, id int64, address string) int64 {
	t.Helper()
	uuid := "local-model-proxy"
	if id != 0 {
		uuid = modelScalar[string](t, f, `SELECT uuid FROM proxies WHERE id=?`, id)
	}
	encrypted, err := f.cipher.Seal([]byte(address), "proxies", "url_enc", uuid)
	if err != nil {
		t.Fatal(err)
	}
	if id == 0 {
		result, err := f.db.ExecContext(t.Context(), `INSERT INTO proxies(uuid,name,url_enc,created_at) VALUES(?,?,?,?)`, uuid, "本地模型代理", encrypted, f.now.Load())
		if err != nil {
			t.Fatal(err)
		}
		id, err = result.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
	} else {
		f.exec(t, `UPDATE proxies SET url_enc=? WHERE id=?`, encrypted, id)
	}
	applyModelSettings(t, f, map[string]any{"ai.proxy_id": id})
	return id
}

func TestModelNetworkDirectIgnoresEnvironmentAndNeverKeepsCookies(t *testing.T) {
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY"} {
		t.Setenv(name, "http://127.0.0.1:1")
	}
	f := newModelFixture(t)
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/v1/chat/completions" || r.Method != http.MethodPost || r.Header.Get("Cookie") != "" || r.Header.Get("Proxy-Authorization") != "" {
			t.Errorf("模型直连请求带入其他会话或路径错误：%s %s %v", r.Method, r.URL, r.Header)
		}
		http.SetCookie(w, &http.Cookie{Name: "model-session", Value: "must-not-return"})
		_, _ = io.WriteString(w, modelResponse)
	}))
	defer server.Close()
	networkProvider(t, f, server.URL+"/v1")
	for range 2 {
		result := completeLocal(t, f)
		if result.Text != "本地回复" || len(result.Calls) != 1 {
			t.Fatalf("薄接线改变了client结果：%+v", result)
		}
	}
	if requests.Load() != 2 || currentNetwork(f.s).transport.Proxy != nil || modelScalar[int](t, f, `SELECT count(*) FROM ai_calls`) != 2 {
		t.Fatal("没有复用专用直连和逐次记账")
	}
}

func TestModelNetworkHTTPProxyUsesOnlySelectedCredentials(t *testing.T) {
	f := newModelFixture(t)
	var requests atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		want := "Basic " + base64.StdEncoding.EncodeToString([]byte("proxy-user:proxy-password"))
		if r.URL.String() != "http://model.invalid/v1/chat/completions" || r.Header.Get("Proxy-Authorization") != want || r.Header.Get("Authorization") != "Bearer provider-key" || r.Header.Get("Cookie") != "" {
			t.Errorf("代理认证、供应商认证或原始目的地址错误：%s %v", r.URL, r.Header)
		}
		_, _ = io.WriteString(w, modelResponse)
	}))
	defer upstream.Close()
	address, _ := url.Parse(upstream.URL)
	address.User = url.UserPassword("proxy-user", "proxy-password")
	saveModelProxy(t, f, 0, address.String())
	input := modelInput()
	input.BaseURL, input.APIKey = "http://model.invalid/v1", modelPointer("provider-key")
	f.create(t, input)
	completeLocal(t, f)
	if requests.Load() != 1 {
		t.Fatal("请求没有只走选定代理")
	}
}

func TestModelNetworkFailedOrInvalidProxyNeverFallsBack(t *testing.T) {
	f := newModelFixture(t)
	var direct atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { direct.Add(1); _, _ = io.WriteString(w, modelResponse) }))
	defer server.Close()
	networkProvider(t, f, server.URL)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	unavailable := "http://" + listener.Addr().String()
	_ = listener.Close()
	id := saveModelProxy(t, f, 0, unavailable)
	if _, err := f.s.Complete(t.Context(), client.Request{Kind: client.Market, User: "本地测试"}); err == nil || direct.Load() != 0 {
		t.Fatalf("失效代理回退了直连：%v", err)
	}
	old := currentNetwork(f.s)
	for _, damage := range []func(){
		func() { f.exec(t, `UPDATE proxies SET url_enc=x'00' WHERE id=?`, id) },
		func() { saveModelProxy(t, f, id, "http://user:secret@127.0.0.1:1/path") },
		func() {
			f.exec(t, `PRAGMA foreign_keys=OFF`)
			f.exec(t, `DELETE FROM proxies WHERE id=?`, id)
			f.exec(t, `PRAGMA foreign_keys=ON`)
		},
	} {
		damage()
		_, err := f.s.Complete(t.Context(), client.Request{Kind: client.Market, User: "本地测试"})
		if err == nil || direct.Load() != 0 || currentNetwork(f.s) != old {
			t.Fatalf("损坏代理借用旧客户端或发了直连：%v", err)
		}
	}
	if modelScalar[int](t, f, `SELECT count(*) FROM ai_calls`) != 1 {
		t.Fatal("发出前的代理配置失败不应伪造物理调用")
	}
}

func TestModelNetworkOnlyActualTimeoutOrProxyChangesReplaceClient(t *testing.T) {
	f := newModelFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, modelResponse) }))
	defer server.Close()
	networkProvider(t, f, server.URL)
	completeLocal(t, f)
	first := currentNetwork(f.s)
	applyModelSettings(t, f, map[string]any{"ai.request_timeout_seconds": 90, "ai.image_count": 3})
	completeLocal(t, f)
	if currentNetwork(f.s) != first {
		t.Fatal("同值保存或其他设置重建了网络客户端")
	}
	applyModelSettings(t, f, map[string]any{"ai.request_timeout_seconds": 91})
	completeLocal(t, f)
	second := currentNetwork(f.s)
	if second == first || !first.retired || first.users != 0 || second.config.timeout != 91*time.Second {
		t.Fatal("真正的超时变更没有替换并回收旧代")
	}
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, modelResponse) }))
	defer proxyServer.Close()
	id := saveModelProxy(t, f, 0, proxyServer.URL)
	completeLocal(t, f)
	third := currentNetwork(f.s)
	if third == second {
		t.Fatal("新增实际代理没有换代")
	}
	f.exec(t, `UPDATE proxies SET name='已改名' WHERE id=?`, id)
	saveModelProxy(t, f, id, proxyServer.URL)
	completeLocal(t, f)
	if currentNetwork(f.s) != third {
		t.Fatal("代理改名或同明文重加密不应换代")
	}
	applyModelSettings(t, f, map[string]any{"ai.proxy_id": nil})
	completeLocal(t, f)
	if currentNetwork(f.s) == third {
		t.Fatal("取消实际代理没有换代")
	}
}

func TestModelNetworkRetiredClientWaitsForOldCallAndAccountingReceipt(t *testing.T) {
	f := newModelFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var requests atomic.Int64
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		_, _ = io.WriteString(w, modelResponse)
	}))
	closed := make(chan struct{}, 4)
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateClosed {
			closed <- struct{}{}
		}
	}
	server.Start()
	defer server.Close()
	networkProvider(t, f, server.URL)
	firstDone := make(chan error, 1)
	go func() {
		_, err := f.s.Complete(t.Context(), client.Request{Kind: client.Market, User: "旧请求"})
		firstDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("旧请求没有发出")
	}
	old := currentNetwork(f.s)
	applyModelSettings(t, f, map[string]any{"ai.request_timeout_seconds": 91})
	completeLocal(t, f)
	f.s.network.mu.Lock()
	retired, users := old.retired, old.users
	f.s.network.mu.Unlock()
	if !retired || users != 1 {
		t.Fatal("热替换没有保留旧逻辑调用")
	}
	receipt, finishReceipt := make(chan struct{}), make(chan struct{})
	f.onPublish = func(kind string) error {
		if kind == "ai.usage.changed" {
			close(receipt)
			<-finishReceipt
		}
		return nil
	}
	once.Do(func() { close(release) })
	select {
	case <-receipt:
	case <-time.After(time.Second):
		t.Fatal("旧请求没有进入记账回执")
	}
	f.s.network.mu.Lock()
	users = old.users
	f.s.network.mu.Unlock()
	if users != 1 {
		t.Fatal("记账回执结束之前提前释放旧代引用")
	}
	select {
	case <-closed:
		t.Fatal("旧调用记账回执结束前关闭了旧代空闲连接")
	default:
	}
	close(finishReceipt)
	if err := <-firstDone; err != nil {
		t.Fatalf("新配置破坏了旧请求：%v", err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("旧调用收尾后没有关闭旧代空闲连接")
	}
	if modelScalar[int](t, f, `SELECT count(*) FROM ai_calls`) != 2 {
		t.Fatal("热切换丢失了用量")
	}
}

func TestModelNetworkCloseCancelsHTTPAndWaitsForAfter(t *testing.T) {
	f := newModelFixture(t)
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		r.Body.Close()
		close(entered)
		<-r.Context().Done()
	}))
	defer server.Close()
	networkProvider(t, f, server.URL)
	requestDone := make(chan error, 1)
	go func() {
		_, err := f.s.Complete(context.Background(), client.Request{Kind: client.Market, User: "取消请求"})
		requestDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("请求没有发出")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := f.s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-requestDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("关闭没有保留取消：%v", err)
	}
	if currentNetwork(f.s) != nil || modelScalar[int](t, f, `SELECT count(*) FROM ai_calls WHERE ok=0 AND error_kind='cancelled'`) != 1 || len(f.s.accounting.tickets) != 0 {
		t.Fatal("关闭没有回收网络和请求记账")
	}
	if _, err := f.s.Complete(t.Context(), client.Request{Kind: client.Market, User: "关闭后"}); err == nil {
		t.Fatal("关闭后还发模型请求")
	}
}

func TestModelNetworkTimeoutAndUserCancellationStillAccount(t *testing.T) {
	for _, userCancel := range []bool{false, true} {
		t.Run(fmt.Sprint(userCancel), func(t *testing.T) {
			f := newModelFixture(t)
			applyModelSettings(t, f, map[string]any{"ai.request_timeout_seconds": 1})
			entered := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				r.Body.Close()
				close(entered)
				<-r.Context().Done()
			}))
			defer server.Close()
			networkProvider(t, f, server.URL)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := f.s.Complete(ctx, client.Request{Kind: client.Market, User: "超时测试"})
				done <- err
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("请求未发出")
			}
			if userCancel {
				cancel()
			}
			var err error
			select {
			case err = <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("请求超时没有生效")
			}
			if err == nil || userCancel && !errors.Is(err, context.Canceled) {
				t.Fatalf("取消/超时错误丢失：%v", err)
			}
			if modelScalar[int](t, f, `SELECT count(*) FROM ai_calls WHERE ok=0`) != 1 {
				t.Fatal("取消或超时没有逐次记账")
			}
		})
	}
}

func TestModelNetworkHotSwapCannotBypassSharedFault(t *testing.T) {
	f := newModelFixture(t)
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests.Add(1); _, _ = io.WriteString(w, modelResponse) }))
	defer server.Close()
	networkProvider(t, f, server.URL)
	f.exec(t, `CREATE TRIGGER fail_model_call BEFORE INSERT ON ai_calls BEGIN SELECT RAISE(ABORT,'local failure'); END`)
	if _, err := f.s.Complete(t.Context(), client.Request{Kind: client.Market, User: "首次"}); err == nil {
		t.Fatal("记账失败仍成功")
	}
	first := currentNetwork(f.s)
	f.exec(t, `DROP TRIGGER fail_model_call`)
	applyModelSettings(t, f, map[string]any{"ai.request_timeout_seconds": 91})
	if _, err := f.s.Complete(t.Context(), client.Request{Kind: client.Market, User: "换代后"}); err == nil || requests.Load() != 1 || currentNetwork(f.s) == first {
		t.Fatalf("热切换绕共享故障锁：requests=%d %v", requests.Load(), err)
	}
}

func TestModelNetworkRedirectIsNotFollowed(t *testing.T) {
	f := newModelFixture(t)
	var redirected atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirected.Add(1)
		_, _ = io.WriteString(w, modelResponse)
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer origin.Close()
	networkProvider(t, f, origin.URL)
	if _, err := f.s.Complete(t.Context(), client.Request{Kind: client.Market, User: "跳转"}); err == nil || redirected.Load() != 0 || modelScalar[int](t, f, `SELECT http_status FROM ai_calls`) != 302 {
		t.Fatalf("模型请求自动跟随跳转：%v", err)
	}
}

func socksModelHandshake(conn net.Conn) error {
	var head [2]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		return err
	}
	methods := make([]byte, head[1])
	if _, err := io.ReadFull(conn, methods); err != nil {
		return err
	}
	if head[0] != 5 || !bytes.Contains(methods, []byte{2}) {
		return errors.New("缺SOCKS认证方法")
	}
	if _, err := conn.Write([]byte{5, 2}); err != nil {
		return err
	}
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		return err
	}
	username := make([]byte, head[1])
	if _, err := io.ReadFull(conn, username); err != nil {
		return err
	}
	var size [1]byte
	if _, err := io.ReadFull(conn, size[:]); err != nil {
		return err
	}
	password := make([]byte, size[0])
	if _, err := io.ReadFull(conn, password); err != nil {
		return err
	}
	if head[0] != 1 || string(username) != "socks-user" || string(password) != "socks-password" {
		return errors.New("SOCKS认证错误")
	}
	if _, err := conn.Write([]byte{1, 0}); err != nil {
		return err
	}
	var connect [4]byte
	if _, err := io.ReadFull(conn, connect[:]); err != nil {
		return err
	}
	if connect != [4]byte{5, 1, 0, 3} {
		return errors.New("目的域名被本机解析")
	}
	if _, err := io.ReadFull(conn, size[:]); err != nil {
		return err
	}
	host := make([]byte, size[0])
	if _, err := io.ReadFull(conn, host); err != nil {
		return err
	}
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		return err
	}
	if string(host) != "model.invalid" || binary.BigEndian.Uint16(head[:]) != 80 {
		return errors.New("SOCKS目的地址改变")
	}
	_, err := conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0})
	return err
}

func TestModelNetworkSOCKS5KeepsDomainAndDoesNotLeakProxyAuth(t *testing.T) {
	f := newModelFixture(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		if err := socksModelHandshake(conn); err != nil {
			done <- err
			return
		}
		request, err := http.ReadRequest(bufio.NewReader(conn))
		if err != nil {
			done <- err
			return
		}
		_, err = io.Copy(io.Discard, request.Body)
		request.Body.Close()
		if err != nil {
			done <- err
			return
		}
		if request.Header.Get("Proxy-Authorization") != "" || request.Header.Get("Cookie") != "" || request.URL.Path != "/v1/chat/completions" {
			done <- errors.New("SOCKS内部HTTP请求带入代理认证或路径错误")
			return
		}
		_, err = fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(modelResponse), modelResponse)
		done <- err
	}()
	saveModelProxy(t, f, 0, "socks5://socks-user:socks-password@"+listener.Addr().String())
	networkProvider(t, f, "http://model.invalid/v1")
	completeLocal(t, f)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestModelNetworkProxySecretsAndCompatibilityLogAreSafe(t *testing.T) {
	f := newModelFixture(t)
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	var requests atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"response_format json_schema is unsupported"}}`)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"proxy-password provider-key raw-user-input"}}`)
	}))
	defer upstream.Close()
	address, _ := url.Parse(upstream.URL)
	address.User = url.UserPassword("proxy-user", "proxy-password")
	saveModelProxy(t, f, 0, address.String())
	input := modelInput()
	input.BaseURL = "http://model.invalid"
	input.APIKey = modelPointer("provider-key")
	f.create(t, input)
	_, err := f.s.Complete(t.Context(), client.Request{Kind: client.TitleScreen, User: "raw-user-input", Schema: &client.Schema{Name: "title", JSON: json.RawMessage(`{"type":"object"}`)}, Validate: func(string) error { return nil }})
	if !errors.Is(err, client.ErrNoProvider) || requests.Load() != 2 {
		t.Fatalf("兼容/永久错误接线失效：%v", err)
	}
	reason := modelScalar[string](t, f, `SELECT error_state FROM ai_providers`)
	for _, secret := range []string{"proxy-password", "proxy-user", "provider-key"} {
		if strings.Contains(reason, secret) || strings.Contains(logs.String(), secret) {
			t.Fatalf("凭据进入安全原因或日志：%s %s", reason, logs.String())
		}
	}
	if !strings.Contains(logs.String(), "模型兼容参数已更新") || !strings.Contains(logs.String(), "model="+input.VisionModel) ||
		!strings.Contains(logs.String(), "from=schema") || !strings.Contains(logs.String(), "to=object") ||
		strings.Contains(logs.String(), "model.invalid") || strings.Contains(logs.String(), "raw-user-input") {
		t.Fatalf("兼容日志未记录模型和降级方向，或包含配置地址及正文：%s", logs.String())
	}
}

func TestModelNetworkHotSwapKeepsOneSharedSlotPool(t *testing.T) {
	f := newModelFixture(t)
	applyModelSettings(t, f, map[string]any{"ai.max_concurrent_requests": 1})
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		_, _ = io.WriteString(w, modelResponse)
	}))
	defer server.Close()
	networkProvider(t, f, server.URL)
	done := make(chan error, 2)
	go func() {
		_, err := f.s.Complete(t.Context(), client.Request{Kind: client.Market, User: "旧代"})
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("旧代请求未发出")
	}
	applyModelSettings(t, f, map[string]any{"ai.request_timeout_seconds": 91})
	go func() {
		_, err := f.s.Complete(t.Context(), client.Request{Kind: client.Market, User: "新代"})
		done <- err
	}()
	// 旧代外层/名额/票据三个工作，加新代外层/等名额两个工作。
	waitModelJobs(t, f.s, 5)
	if requests.Load() != 1 {
		t.Fatal("换代重建了并发池，绕过共享上限")
	}
	once.Do(func() { close(release) })
	for range 2 {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("共用名额未按释放顺序继续")
		}
	}
	if requests.Load() != 2 {
		t.Fatal("释放后新代请求丢失")
	}
}

func TestModelNetworkHTTPSProxyRefusalNeverConnectsDirectly(t *testing.T) {
	f := newModelFixture(t)
	var direct, connects atomic.Int64
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { direct.Add(1); _, _ = io.WriteString(w, modelResponse) }))
	defer target.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connects.Add(1)
		if r.Method != http.MethodConnect || r.Header.Get("Proxy-Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("proxy-user:proxy-password")) || r.Header.Get("Authorization") != "" {
			t.Errorf("CONNECT携带了供应商认证或遗漏代理认证：%s %v", r.Method, r.Header)
		}
		w.WriteHeader(http.StatusProxyAuthRequired)
	}))
	defer upstream.Close()
	address, _ := url.Parse(upstream.URL)
	address.User = url.UserPassword("proxy-user", "proxy-password")
	saveModelProxy(t, f, 0, address.String())
	input := modelInput()
	input.BaseURL, input.APIKey = target.URL, modelPointer("provider-key")
	f.create(t, input)
	if _, err := f.s.Complete(t.Context(), client.Request{Kind: client.Market, User: "CONNECT拒绝"}); err == nil || direct.Load() != 0 || connects.Load() != 1 {
		t.Fatalf("CONNECT拒绝后发生直连：direct=%d connect=%d %v", direct.Load(), connects.Load(), err)
	}
}

func TestModelNetworkCancelInterruptsSOCKSHandshake(t *testing.T) {
	f := newModelFixture(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	entered, ended := make(chan struct{}), make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			ended <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		var greeting [4]byte
		if _, err := io.ReadFull(conn, greeting[:]); err != nil {
			ended <- err
			return
		}
		close(entered)
		// 故意不回握手，等调用方取消后断开。
		var remaining [1]byte
		_, err = conn.Read(remaining[:])
		ended <- err
	}()
	saveModelProxy(t, f, 0, "socks5://socks-user:socks-password@"+listener.Addr().String())
	networkProvider(t, f, "http://model.invalid")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := f.s.Complete(ctx, client.Request{Kind: client.Market, User: "取消握手"})
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("SOCKS握手未开始")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("SOCKS取消丢失：%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("SOCKS握手无法取消")
	}
	select {
	case err := <-ended:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("取消后没有及时关闭SOCKS连接：%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("SOCKS连接未回收")
	}
	if modelScalar[int](t, f, `SELECT count(*) FROM ai_calls WHERE error_kind='cancelled'`) != 1 {
		t.Fatal("SOCKS取消没有完成记账")
	}
}

func TestModelNetworkCancelInterruptsConnectAndTLSHandshake(t *testing.T) {
	for _, protocol := range []string{"connect", "tls"} {
		t.Run(protocol, func(t *testing.T) {
			f := newModelFixture(t)
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			entered, ended := make(chan struct{}), make(chan error, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					ended <- err
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
				reader := bufio.NewReader(conn)
				if protocol == "connect" {
					request, err := http.ReadRequest(reader)
					if err != nil {
						ended <- err
						return
					}
					request.Body.Close()
					if request.Method != http.MethodConnect {
						ended <- errors.New("未发CONNECT")
						return
					}
				} else {
					var record [5]byte
					if _, err := io.ReadFull(reader, record[:]); err != nil {
						ended <- err
						return
					}
					if record[0] != 22 {
						ended <- errors.New("未发TLS握手")
						return
					}
					if _, err := io.CopyN(io.Discard, reader, int64(binary.BigEndian.Uint16(record[3:]))); err != nil {
						ended <- err
						return
					}
				}
				close(entered)
				var next [1]byte
				_, err = reader.Read(next[:])
				ended <- err
			}()
			address := "https://" + listener.Addr().String()
			if protocol == "connect" {
				saveModelProxy(t, f, 0, "http://"+listener.Addr().String())
				address = "https://model.invalid"
			}
			networkProvider(t, f, address)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := f.s.Complete(ctx, client.Request{Kind: client.Market, User: "取消握手"})
				done <- err
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("握手未开始")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("取消丢失：%v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("握手取消没有完成")
			}
			select {
			case err := <-ended:
				if !errors.Is(err, io.EOF) {
					t.Fatalf("取消后连接未关闭：%v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("握手连接仍被占用")
			}
			if modelScalar[int](t, f, `SELECT count(*) FROM ai_calls WHERE error_kind='cancelled'`) != 1 {
				t.Fatal("握手取消丢失记账")
			}
		})
	}
}

type observedModelConn struct {
	net.Conn
	closed chan struct{}
	once   sync.Once
}

func (c *observedModelConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

func TestModelNetworkLateDialTransferredToAnotherRequestStaysOpen(t *testing.T) {
	for _, encrypted := range []bool{false, true} {
		for _, cancelOwner := range []bool{false, true} {
			t.Run(fmt.Sprintf("tls=%v_cancel=%v", encrypted, cancelOwner), func(t *testing.T) { testModelNetworkLateDialTransfer(t, encrypted, cancelOwner) })
		}
	}
}

func testModelNetworkLateDialTransfer(t *testing.T, encrypted, cancelOwner bool) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ownerCtx, cancelOwnerRequest := context.WithCancel(ctx)
	defer cancelOwnerRequest()
	enteredB, enteredA, enteredC := make(chan struct{}), make(chan struct{}), make(chan struct{})
	releaseB, releaseA, releaseC := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce [3]sync.Once
	defer func() {
		for i, release := range []chan struct{}{releaseB, releaseA, releaseC} {
			releaseOnce[i].Do(func() { close(release) })
		}
	}()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		r.Body.Close()
		var entered, release chan struct{}
		switch r.URL.Path {
		case "/b":
			entered, release = enteredB, releaseB
		case "/a":
			entered, release = enteredA, releaseA
		case "/c":
			entered, release = enteredC, releaseC
		default:
			t.Errorf("未知测试路径：%s", r.URL.Path)
			return
		}
		close(entered)
		select {
		case <-release:
			_, _ = io.WriteString(w, "ok")
		case <-r.Context().Done():
		}
	}))
	if encrypted {
		server.StartTLS()
	} else {
		server.Start()
	}
	defer server.Close()
	lateDial := make(chan *observedModelConn, 1)
	thirdDial := make(chan struct{})
	releaseLate := make(chan struct{})
	var lateOnce sync.Once
	defer lateOnce.Do(func() { close(releaseLate) })
	var calls atomic.Int64
	dialer := &net.Dialer{}
	controlledDial := func(ctx context.Context, network, address string) (net.Conn, error) {
		switch calls.Add(1) {
		case 2:
			conn, err := dialer.DialContext(ctx, network, address)
			if err != nil {
				return nil, err
			}
			observed := &observedModelConn{Conn: conn, closed: make(chan struct{})}
			lateDial <- observed
			select {
			case <-releaseLate:
				return observed, nil
			case <-ctx.Done():
				_ = observed.Close()
				return nil, ctx.Err()
			}
		case 3:
			close(thirdDial)
			<-ctx.Done()
			return nil, ctx.Err()
		default:
			return dialer.DialContext(ctx, network, address)
		}
	}
	transport := &http.Transport{DialContext: requestDialer(controlledDial), TLSHandshakeTimeout: 10 * time.Second}
	if encrypted {
		transport.TLSClientConfig = server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
		transport.DialTLSContext = requestDialer(func(ctx context.Context, network, address string) (net.Conn, error) {
			return dialModelTLS(ctx, network, address, controlledDial, nil, transport)
		})
	}
	defer transport.CloseIdleConnections()
	httpClient := &http.Client{Transport: modelRoundTripper{transport}}
	type requestResult struct {
		err  error
		text string
	}
	start := func(path string) (<-chan net.Conn, <-chan requestResult) {
		got, result := make(chan net.Conn, 1), make(chan requestResult, 1)
		go func() {
			trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { got <- info.Conn }}
			requestCtx := ctx
			if path == "/a" {
				requestCtx = ownerCtx
			}
			request, err := http.NewRequestWithContext(httptrace.WithClientTrace(requestCtx, trace), http.MethodPost, server.URL+path, strings.NewReader("本地测试"))
			if err != nil {
				result <- requestResult{err: err}
				return
			}
			response, err := httpClient.Do(request)
			if err != nil {
				result <- requestResult{err: err}
				return
			}
			body, err := io.ReadAll(response.Body)
			response.Body.Close()
			result <- requestResult{err: err, text: string(body)}
		}()
		return got, result
	}
	wait := func(signal <-chan struct{}, name string) {
		t.Helper()
		select {
		case <-signal:
		case <-ctx.Done():
			t.Fatalf("时序未到达%s：%v", name, ctx.Err())
		}
	}
	gotB, doneB := start("/b")
	wait(enteredB, "B占用暖连接")
	bConn := <-gotB
	gotA, doneA := start("/a")
	var late *observedModelConn
	select {
	case late = <-lateDial:
	case <-ctx.Done():
		t.Fatal("A的晚拨号未开始")
	}
	releaseOnce[0].Do(func() { close(releaseB) })
	if result := <-doneB; result.err != nil || result.text != "ok" {
		t.Fatalf("B失败：%+v", result)
	}
	wait(enteredA, "A拿到B归还的暖连接")
	if conn := <-gotA; conn != bConn {
		t.Fatal("未走A复用B连接的目标路径")
	}
	gotC, doneC := start("/c")
	wait(thirdDial, "C等待独立拨号")
	lateOnce.Do(func() { close(releaseLate) })
	wait(enteredC, "A的晚拨号交给C")
	conn := <-gotC
	if secure, ok := conn.(interface{ NetConn() net.Conn }); ok {
		conn = secure.NetConn()
	}
	if conn != late {
		t.Fatal("未走C复用A晚拨号的目标路径，或TLS底层连接身份不一致")
	}
	if cancelOwner {
		cancelOwnerRequest()
	} else {
		releaseOnce[1].Do(func() { close(releaseA) })
	}
	if result := <-doneA; cancelOwner && !errors.Is(result.err, context.Canceled) || !cancelOwner && (result.err != nil || result.text != "ok") {
		t.Fatalf("A结束结果错误：err=%v text=%q", result.err, result.text)
	}
	select {
	case <-late.closed:
		t.Error("A结束误关了已经交给C使用的晚拨号连接")
	default:
	}
	releaseOnce[2].Do(func() { close(releaseC) })
	if result := <-doneC; result.err != nil || result.text != "ok" {
		t.Errorf("C被其他请求的收尾打断：err=%v text=%q", result.err, result.text)
	}
	transport.CloseIdleConnections()
	select {
	case <-late.closed:
	case <-ctx.Done():
		t.Error("回归未观测到真实连接的关闭，负断言无效")
	}
}

func trustLocalModelTLS(t *testing.T, f *modelFixture, target *httptest.Server) {
	t.Helper()
	_, release, err := f.s.borrowClient(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	transport := currentNetwork(f.s).transport
	transport.TLSClientConfig.RootCAs = target.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	if transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("测试不能关闭证书验证")
	}
}

func TestModelNetworkTLSVerifiesCertificatesAndNegotiatesHTTP2(t *testing.T) {
	f := newModelFixture(t)
	var requests atomic.Int64
	target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.ProtoMajor != 2 || r.TLS == nil || r.TLS.NegotiatedProtocol != "h2" {
			t.Errorf("TLS没有正常协商HTTP2：%s %+v", r.Proto, r.TLS)
		}
		_, _ = io.WriteString(w, modelResponse)
	}))
	target.EnableHTTP2 = true
	target.StartTLS()
	defer target.Close()
	networkProvider(t, f, target.URL)
	if _, err := f.s.Complete(t.Context(), client.Request{Kind: client.Market, User: "未信任证书"}); err == nil || requests.Load() != 0 {
		t.Fatalf("未信任证书没有阻止模型请求：%v", err)
	}
	applyModelSettings(t, f, map[string]any{"ai.request_timeout_seconds": 91})
	trustLocalModelTLS(t, f, target)
	completeLocal(t, f)
	if requests.Load() != 1 || currentNetwork(f.s).transport.TLSHandshakeTimeout != 10*time.Second {
		t.Fatal("可信TLS请求或原握手超时改变")
	}
}

func TestModelNetworkHTTPSProxyPreservesSNIAuthAndHTTP2(t *testing.T) {
	f := newModelFixture(t)
	target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || r.TLS.ServerName != "example.com" || r.ProtoMajor != 2 || r.Header.Get("Authorization") != "Bearer provider-key" || r.Header.Get("Proxy-Authorization") != "" {
			t.Errorf("隧道内SNI、HTTP2或认证隔离失败：%s %v %+v", r.Proto, r.Header, r.TLS)
		}
		_, _ = io.WriteString(w, modelResponse)
	}))
	target.EnableHTTP2 = true
	target.StartTLS()
	defer target.Close()
	_, port, _ := net.SplitHostPort(target.Listener.Addr().String())
	destination := net.JoinHostPort("example.com", port)
	relayDone := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := "Basic " + base64.StdEncoding.EncodeToString([]byte("proxy-user:proxy-password"))
		if r.Method != http.MethodConnect || r.Host != destination || r.Header.Get("Proxy-Authorization") != want || r.Header.Get("Authorization") != "" {
			t.Errorf("CONNECT目的或认证错误：%s %s %v", r.Method, r.Host, r.Header)
		}
		conn, buffer, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			close(relayDone)
			return
		}
		defer conn.Close()
		defer close(relayDone)
		remote, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", target.Listener.Addr().String())
		if err != nil {
			t.Error(err)
			return
		}
		defer remote.Close()
		if _, err := buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			t.Error(err)
			return
		}
		if err := buffer.Flush(); err != nil {
			t.Error(err)
			return
		}
		copied := make(chan struct{}, 2)
		go func() { _, _ = io.Copy(remote, buffer.Reader); copied <- struct{}{} }()
		go func() { _, _ = io.Copy(conn, remote); copied <- struct{}{} }()
		<-copied
		_ = remote.Close()
		_ = conn.Close()
		<-copied
	}))
	defer upstream.Close()
	address, _ := url.Parse(upstream.URL)
	address.User = url.UserPassword("proxy-user", "proxy-password")
	saveModelProxy(t, f, 0, address.String())
	input := modelInput()
	input.BaseURL, input.APIKey = "https://"+destination, modelPointer("provider-key")
	f.create(t, input)
	trustLocalModelTLS(t, f, target)
	completeLocal(t, f)
	if err := f.s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-relayDone:
	case <-time.After(time.Second):
		t.Fatal("HTTPS代理隧道未随关闭回收")
	}
}

func TestModelNetworkConnectPreservesReadAhead(t *testing.T) {
	clientSide, proxySide := net.Pipe()
	defer clientSide.Close()
	defer proxySide.Close()
	_ = clientSide.SetDeadline(time.Now().Add(time.Second))
	_ = proxySide.SetDeadline(time.Now().Add(time.Second))
	done := make(chan error, 1)
	go func() {
		request, err := http.ReadRequest(bufio.NewReader(proxySide))
		if err == nil {
			request.Body.Close()
			_, err = io.WriteString(proxySide, "HTTP/1.1 200 Connection Established\r\n\r\nbuffered-bytes")
		}
		done <- err
	}()
	address, _ := url.Parse("http://127.0.0.1:1")
	conn, err := connectModelProxy(clientSide, "example.invalid:443", address)
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, len("buffered-bytes"))
	if _, err := io.ReadFull(conn, data); err != nil || string(data) != "buffered-bytes" {
		t.Fatalf("CONNECT解析多读的隧道字节丢失：%q %v", data, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
