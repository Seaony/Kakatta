package proxyforward

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func startForwarder(t *testing.T, ctx context.Context, rawURL string) *Forwarder {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	f, err := Start(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	u, err = url.Parse(f.URL())
	if err != nil || !net.ParseIP(u.Hostname()).IsLoopback() || u.User != nil {
		t.Fatalf("本机代理必须只监听回环地址且不包含认证信息：%s", f.URL())
	}
	return f
}

func proxyClient(t *testing.T, f *Forwarder) *http.Client {
	t.Helper()
	u, _ := url.Parse(f.URL())
	transport := &http.Transport{Proxy: http.ProxyURL(u), DisableCompression: true}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 3 * time.Second}
}

func TestHTTPProxyPreservesRemoteDomainAndSeparatesAuthentication(t *testing.T) {
	var originHits atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originHits.Add(1)
		if r.Header.Get("Proxy-Authorization") != "" || r.Header.Get("X-Hop") != "" || r.Header.Get("X-Forwarded-For") != "" {
			t.Error("逐跳头或代理认证泄露给目的服务")
		}
		if r.Header.Get("Authorization") != "Bearer target-only" {
			t.Error("目的服务自己的认证不应被改变")
		}
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("X-Origin", "yes")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, string(body)+":"+r.URL.RequestURI())
	}))
	defer origin.Close()
	originURL, _ := url.Parse(origin.URL)
	transport := &http.Transport{DisableCompression: true}
	defer transport.CloseIdleConnections()
	var proxyHits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
		wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("upstream-user:upstream-pass"))
		if r.Header.Get("Proxy-Authorization") != wantAuth || r.URL.Host != "not-resolved.invalid:80" || r.URL.Scheme != "http" {
			t.Error("上游没有收到原始目的域名或自己的认证")
		}
		out := r.Clone(r.Context())
		out.RequestURI = ""
		out.URL.Host = originURL.Host
		out.Header.Del("Proxy-Authorization")
		resp, err := transport.RoundTrip(out)
		if err != nil {
			t.Error(err)
			return
		}
		defer resp.Body.Close()
		for key, values := range resp.Header {
			w.Header()[key] = values
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	defer upstream.Close()
	u, _ := url.Parse(upstream.URL)
	u.User = url.UserPassword("upstream-user", "upstream-pass")
	f := startForwarder(t, context.Background(), u.String())
	req, _ := http.NewRequest(http.MethodPost, "http://not-resolved.invalid:80/原样?q=1", strings.NewReader("request-body"))
	req.Header.Set("Authorization", "Bearer target-only")
	req.Header.Set("Proxy-Authorization", "client-untrusted")
	req.Header.Set("Connection", "keep-alive, X-Hop")
	req.Header.Set("X-Hop", "remove-me")
	resp, err := proxyClient(t, f).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated || resp.Header.Get("X-Origin") != "yes" || string(body) != "request-body:/%E5%8E%9F%E6%A0%B7?q=1" {
		t.Fatalf("请求或响应被改变：%d %s", resp.StatusCode, body)
	}
	if proxyHits.Load() != 1 || originHits.Load() != 1 {
		t.Fatal("没有完整经过上游代理和本地目的服务")
	}
}

type tcpFixture struct {
	listener net.Listener
	mu       sync.Mutex
	conn     net.Conn
	done     chan struct{}
	err      error
}

func newTCPFixture(t *testing.T, handle func(net.Conn) error) *tcpFixture {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &tcpFixture{listener: listener, done: make(chan struct{})}
	go func() {
		defer close(f.done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		f.mu.Lock()
		f.conn = conn
		f.mu.Unlock()
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		f.err = handle(conn)
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		f.mu.Lock()
		if f.conn != nil {
			_ = f.conn.Close()
		}
		f.mu.Unlock()
		<-f.done
		if f.err != nil {
			t.Error(f.err)
		}
	})
	return f
}

func (f *tcpFixture) address() string { return f.listener.Addr().String() }

func tunnelClient(t *testing.T, f *Forwarder, address, initial string) (*net.TCPConn, *bufio.Reader) {
	t.Helper()
	u, _ := url.Parse(f.URL())
	conn, err := net.DialTimeout("tcp", u.Host, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	_, err = fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: client-untrusted\r\n\r\n%s", address, address, initial)
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("隧道没有连通：%v %v", resp, err)
	}
	return conn.(*net.TCPConn), reader
}

func TestHTTPConnectPreservesBufferedBytesAndHalfClose(t *testing.T) {
	upstream := newTCPFixture(t, func(conn net.Conn) error {
		reader := bufio.NewReader(conn)
		req, err := http.ReadRequest(reader)
		if err != nil {
			return err
		}
		wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("tunnel-user:tunnel-pass"))
		if req.Method != http.MethodConnect || req.Host != "tunnel.invalid:443" || req.Header.Get("Proxy-Authorization") != wantAuth {
			return fmt.Errorf("隧道没有把目的域名和认证交给上游")
		}
		if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\ngreeting:"); err != nil {
			return err
		}
		body, err := io.ReadAll(reader)
		if err != nil || string(body) != "buffered-payload" {
			return fmt.Errorf("请求头之后的缓冲字节丢失：%s %v", body, err)
		}
		_, err = conn.Write(append([]byte("echo:"), body...))
		return err
	})
	f := startForwarder(t, context.Background(), "http://tunnel-user:tunnel-pass@"+upstream.address())
	client, reader := tunnelClient(t, f, "tunnel.invalid:443", "buffered-payload")
	greeting := make([]byte, len("greeting:"))
	if _, err := io.ReadFull(reader, greeting); err != nil || string(greeting) != "greeting:" {
		t.Fatalf("上游响应头后面的缓冲字节丢失：%s %v", greeting, err)
	}
	if err := client.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(reader)
	if err != nil || string(body) != "echo:buffered-payload" {
		t.Fatalf("半关闭后仍应收到完整回复：%s %v", body, err)
	}
	<-upstream.done
}

func socksHandshake(conn net.Conn, wantAddress string) error {
	var head [2]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		return err
	}
	methods := make([]byte, head[1])
	if _, err := io.ReadFull(conn, methods); err != nil {
		return err
	}
	if head[0] != 5 || !strings.ContainsRune(string(methods), 2) {
		return fmt.Errorf("没有协商用户名密码认证")
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
	if head[0] != 1 || string(username) != "socks-user" || string(password) != "socks-pass" {
		return fmt.Errorf("SOCKS5 上游认证不正确")
	}
	if _, err := conn.Write([]byte{1, 0}); err != nil {
		return err
	}
	var request [4]byte
	if _, err := io.ReadFull(conn, request[:]); err != nil {
		return err
	}
	if request != [4]byte{5, 1, 0, 3} {
		return fmt.Errorf("目的地址必须以域名交给 SOCKS5，不能在本机解析：%v", request)
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
	address := net.JoinHostPort(string(host), fmt.Sprint(binary.BigEndian.Uint16(head[:])))
	if address != wantAddress {
		return fmt.Errorf("SOCKS5 目的地址改变：%s", address)
	}
	_, err := conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0})
	return err
}

func TestSOCKS5HTTPKeepsDomainAndDoesNotLeakAuthentication(t *testing.T) {
	upstream := newTCPFixture(t, func(conn net.Conn) error {
		if err := socksHandshake(conn, "not-resolved.invalid:80"); err != nil {
			return err
		}
		req, err := http.ReadRequest(bufio.NewReader(conn))
		if err != nil {
			return err
		}
		defer req.Body.Close()
		body, err := io.ReadAll(req.Body)
		if err != nil || req.Method != http.MethodPost || req.RequestURI != "/path?q=1" || string(body) != "payload" {
			return fmt.Errorf("SOCKS5 里的普通请求改变了：%v %s %v", req.URL, body, err)
		}
		if req.Header.Get("Proxy-Authorization") != "" {
			return fmt.Errorf("代理认证头泄露给目的服务")
		}
		_, err = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
		return err
	})
	f := startForwarder(t, context.Background(), "socks5://socks-user:socks-pass@"+upstream.address())
	req, _ := http.NewRequest(http.MethodPost, "http://not-resolved.invalid:80/path?q=1", strings.NewReader("payload"))
	req.Header.Set("Proxy-Authorization", "client-untrusted")
	resp, err := proxyClient(t, f).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("SOCKS5 普通请求失败：%d %s", resp.StatusCode, body)
	}
	<-upstream.done
}

func TestSOCKS5ConnectSupportsHalfClose(t *testing.T) {
	upstream := newTCPFixture(t, func(conn net.Conn) error {
		if err := socksHandshake(conn, "tunnel.invalid:443"); err != nil {
			return err
		}
		if _, err := io.WriteString(conn, "ready:"); err != nil {
			return err
		}
		body, err := io.ReadAll(conn)
		if err != nil {
			return err
		}
		_, err = conn.Write(append([]byte("echo:"), body...))
		return err
	})
	f := startForwarder(t, context.Background(), "socks5://socks-user:socks-pass@"+upstream.address())
	client, reader := tunnelClient(t, f, "tunnel.invalid:443", "payload")
	ready := make([]byte, len("ready:"))
	if _, err := io.ReadFull(reader, ready); err != nil || string(ready) != "ready:" {
		t.Fatalf("隧道没有返回准备消息：%s %v", ready, err)
	}
	if err := client.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(reader)
	if err != nil || string(body) != "echo:payload" {
		t.Fatalf("SOCKS5 半关闭丢失回复：%s %v", body, err)
	}
	<-upstream.done
}

func TestUnavailableUpstreamNeverFallsBackToDirect(t *testing.T) {
	var hits atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, "direct")
	}))
	defer origin.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	unavailable := listener.Addr().String()
	_ = listener.Close()
	for _, scheme := range []string{"http", "socks5"} {
		t.Run(scheme, func(t *testing.T) {
			f := startForwarder(t, context.Background(), scheme+"://private-user:private-password@"+unavailable)
			resp, err := proxyClient(t, f).Get(origin.URL)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusBadGateway || string(body) != ErrUpstream.Error()+"\n" || strings.Contains(string(body), "private-") {
				t.Fatalf("失败必须拦住且不显示认证信息：%d %s", resp.StatusCode, body)
			}
			u, _ := url.Parse(f.URL())
			conn, err := net.DialTimeout("tcp", u.Host, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			originURL, _ := url.Parse(origin.URL)
			_, _ = fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", originURL.Host, originURL.Host)
			response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
			if err != nil || response.StatusCode != http.StatusBadGateway {
				t.Fatalf("隧道失败后不能改成直连：%v %v", response, err)
			}
			response.Body.Close()
		})
	}
	if hits.Load() != 0 {
		t.Fatal("代理失败后悄悄直连了目的服务")
	}
}

func TestHTTPAuthenticationFailureDoesNotExposeUpstreamBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusProxyAuthRequired)
		fmt.Fprint(w, "sensitive upstream-user:upstream-pass")
	}))
	defer upstream.Close()
	f := startForwarder(t, context.Background(), upstream.URL)
	resp, err := proxyClient(t, f).Get("http://not-resolved.invalid:80/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadGateway || string(body) != ErrUpstream.Error()+"\n" {
		t.Fatalf("上游认证失败正文不应暴露：%d %s", resp.StatusCode, body)
	}
}

func TestParentCancelClosesActiveTunnelAndListener(t *testing.T) {
	upstream := newTCPFixture(t, func(conn net.Conn) error {
		reader := bufio.NewReader(conn)
		if _, err := http.ReadRequest(reader); err != nil {
			return err
		}
		if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			return err
		}
		_, err := io.Copy(io.Discard, reader)
		return err
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := startForwarder(t, ctx, "http://"+upstream.address())
	client, _ := tunnelClient(t, f, "tunnel.invalid:443", "")
	start := time.Now()
	cancel()
	var one [1]byte
	if n, err := client.Read(one[:]); n != 0 || err == nil {
		t.Fatal("取消后本机隧道仍然连着")
	}
	if time.Since(start) > time.Second {
		t.Fatal("父上下文取消后没有及时自动关闭隧道")
	}
	if err := f.Close(); err != nil {
		t.Fatal("自动关闭后再关闭不应出错", err)
	}
	<-upstream.done
	u, _ := url.Parse(f.URL())
	if conn, err := net.DialTimeout("tcp", u.Host, time.Second); err == nil {
		conn.Close()
		t.Fatal("取消后本机代理仍然监听")
	}
	if err := f.Close(); err != nil {
		t.Fatal("重复关闭不应出错", err)
	}
	f.mu.Lock()
	left := len(f.conns)
	f.mu.Unlock()
	if left != 0 {
		t.Fatalf("关闭后还有 %d 个连接没有回收", left)
	}
}

func TestSOCKS5AuthenticationFailureDoesNotExposeCredentials(t *testing.T) {
	upstream := newTCPFixture(t, func(conn net.Conn) error {
		var head [2]byte
		if _, err := io.ReadFull(conn, head[:]); err != nil {
			return err
		}
		if _, err := io.CopyN(io.Discard, conn, int64(head[1])); err != nil {
			return err
		}
		_, err := conn.Write([]byte{5, 255})
		return err
	})
	f := startForwarder(t, context.Background(), "socks5://private-user:private-password@"+upstream.address())
	response, err := proxyClient(t, f).Get("http://not-resolved.invalid:80/")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusBadGateway || string(body) != ErrUpstream.Error()+"\n" {
		t.Fatalf("认证协商失败应拦住且不显示凭据：%d %s", response.StatusCode, body)
	}
	<-upstream.done
}

func TestCloseInterruptsPendingSOCKS5Handshake(t *testing.T) {
	started := make(chan struct{})
	upstream := newTCPFixture(t, func(conn net.Conn) error {
		var head [2]byte
		if _, err := io.ReadFull(conn, head[:]); err != nil {
			return err
		}
		if _, err := io.CopyN(io.Discard, conn, int64(head[1])); err != nil {
			return err
		}
		close(started)
		_, err := io.Copy(io.Discard, conn)
		return err
	})
	f := startForwarder(t, context.Background(), "socks5://socks-user:socks-pass@"+upstream.address())
	client := proxyClient(t, f)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		response, _ := client.Get("http://not-resolved.invalid:80/")
		if response != nil {
			response.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("上游没有收到 SOCKS5 握手")
	}
	start := time.Now()
	_ = f.Close()
	if time.Since(start) > time.Second {
		t.Fatal("SOCKS5 握手挂起时无法及时关闭")
	}
	<-finished
	<-upstream.done
}

func TestCloseInterruptsPendingConnectHandshake(t *testing.T) {
	started := make(chan struct{})
	upstream := newTCPFixture(t, func(conn net.Conn) error {
		reader := bufio.NewReader(conn)
		if _, err := http.ReadRequest(reader); err != nil {
			return err
		}
		close(started)
		_, err := io.Copy(io.Discard, reader)
		return err
	})
	f := startForwarder(t, context.Background(), "http://"+upstream.address())
	u, _ := url.Parse(f.URL())
	client, err := net.DialTimeout("tcp", u.Host, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_, _ = io.WriteString(client, "CONNECT tunnel.invalid:443 HTTP/1.1\r\nHost: tunnel.invalid:443\r\n\r\n")
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("上游没有收到隧道申请")
	}
	start := time.Now()
	_ = f.Close()
	if time.Since(start) > time.Second {
		t.Fatal("上游不回复时无法及时关闭")
	}
	<-upstream.done
}

func TestInvalidConfigurationAndCancelledStart(t *testing.T) {
	for _, raw := range []string{"ftp://private-user:secret@127.0.0.1:1", "http://127.0.0.1", "http://127.0.0.1:0", "http://127.0.0.1:65536", "socks5://127.0.0.1:1/path"} {
		u, _ := url.Parse(raw)
		if f, err := Start(context.Background(), u); err != ErrConfig || f != nil {
			t.Fatalf("非法配置没有被拒绝：%v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	u, _ := url.Parse("http://127.0.0.1:1")
	if f, err := Start(ctx, u); err != context.Canceled || f != nil {
		t.Fatalf("取消后不应开监听：%v", err)
	}
}
