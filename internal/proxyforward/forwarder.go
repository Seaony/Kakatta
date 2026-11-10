// Package proxyforward 把浏览器的本机无认证代理请求转交给账号绑定的代理。
package proxyforward

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/proxy"
)

var (
	ErrConfig   = errors.New("代理地址格式不对")
	ErrUpstream = errors.New("上游代理连接失败")
	ErrClosed   = errors.New("本机代理已关闭")
)

// Forwarder 和一个账号的浏览器一起启动、一起关闭，不会转成直连。
type Forwarder struct {
	ctx       context.Context
	cancel    context.CancelFunc
	listener  net.Listener
	server    *http.Server
	transport *http.Transport
	reverse   *httputil.ReverseProxy
	upstream  url.URL
	dialer    net.Dialer

	mu        sync.Mutex
	closed    bool
	conns     map[*trackedConn]struct{}
	handlers  sync.WaitGroup
	serveDone chan struct{}
	closeOnce sync.Once
}

// Start 只监听本机。目的地址保持域名形式，由上游代理解析。
func Start(ctx context.Context, upstream *url.URL) (*Forwarder, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if upstream == nil || (upstream.Scheme != "http" && upstream.Scheme != "socks5") ||
		!validAddress(upstream.Host) || (upstream.Path != "" && upstream.Path != "/") ||
		upstream.RawQuery != "" || upstream.Fragment != "" {
		return nil, ErrConfig
	}
	if upstream.Scheme == "socks5" && upstream.User != nil {
		password, _ := upstream.User.Password()
		if len(upstream.User.Username()) > 255 || len(password) > 255 {
			return nil, ErrConfig
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, errors.New("无法启动本机代理")
	}
	base, cancel := context.WithCancel(ctx)
	f := &Forwarder{
		ctx: base, cancel: cancel, listener: listener, upstream: *upstream,
		dialer: net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second},
		conns:  make(map[*trackedConn]struct{}), serveDone: make(chan struct{}),
	}
	f.transport = http.DefaultTransport.(*http.Transport).Clone()
	f.transport.Proxy = nil
	f.transport.DisableCompression = true
	f.transport.ForceAttemptHTTP2 = false
	f.transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return f.dialSOCKS(ctx, address)
	}
	if upstream.Scheme == "http" {
		f.transport.Proxy = http.ProxyURL(&f.upstream)
		f.transport.DialContext = f.dialSocket
	}
	f.reverse = &httputil.ReverseProxy{
		Transport: f.transport,
		Rewrite: func(r *httputil.ProxyRequest) {
			target := *r.In.URL
			r.Out.URL, r.Out.Host = &target, r.In.Host
			r.Out.Header.Del("Proxy-Authorization")
		},
		ModifyResponse: func(r *http.Response) error {
			if r.StatusCode == http.StatusProxyAuthRequired {
				return ErrUpstream
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, ErrUpstream.Error(), http.StatusBadGateway)
		},
		ErrorLog: log.New(io.Discard, "", 0),
	}
	f.server = &http.Server{
		Handler:           f,
		BaseContext:       func(net.Listener) context.Context { return f.ctx },
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	go func() {
		_ = f.server.Serve(trackedListener{Listener: listener, owner: f})
		close(f.serveDone)
		f.cancel()
	}()
	go func() {
		<-f.ctx.Done()
		_ = f.Close()
	}()
	return f, nil
}

func (f *Forwarder) URL() string { return "http://" + f.listener.Addr().String() }

// Close 关闭监听、普通请求和已交给隧道的连接，并等待转发结束。
func (f *Forwarder) Close() error {
	f.closeOnce.Do(func() {
		f.mu.Lock()
		f.closed = true
		connections := make([]*trackedConn, 0, len(f.conns))
		for c := range f.conns {
			connections = append(connections, c)
		}
		f.mu.Unlock()
		f.cancel()
		_ = f.server.Close()
		for _, c := range connections {
			_ = c.Close()
		}
		f.transport.CloseIdleConnections()
		<-f.serveDone
		f.handlers.Wait()
	})
	return nil
}

func (f *Forwarder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		http.Error(w, ErrClosed.Error(), http.StatusServiceUnavailable)
		return
	}
	f.handlers.Add(1)
	f.mu.Unlock()
	defer f.handlers.Done()

	if r.Method == http.MethodConnect {
		f.connect(w, r)
		return
	}
	if r.URL.User != nil || r.URL.Host == "" || (r.URL.Scheme != "http" && r.URL.Scheme != "https") {
		http.Error(w, "代理请求格式不对", http.StatusBadRequest)
		return
	}
	f.reverse.ServeHTTP(w, r)
}

func (f *Forwarder) connect(w http.ResponseWriter, r *http.Request) {
	if !validAddress(r.Host) {
		http.Error(w, "代理目的地址格式不对", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var upstream net.Conn
	var err error
	if f.upstream.Scheme == "http" {
		upstream, err = f.dialHTTPConnect(ctx, r.Host)
	} else {
		upstream, err = f.dialSOCKS(ctx, r.Host)
	}
	if err != nil {
		http.Error(w, ErrUpstream.Error(), http.StatusBadGateway)
		return
	}
	defer upstream.Close()
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "无法建立本机代理隧道", http.StatusInternalServerError)
		return
	}
	client, buffer, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer client.Close()
	if _, err := buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	if err := buffer.Flush(); err != nil {
		return
	}
	// 解析请求或代理响应时可能多读了隧道数据，不能丢掉缓冲里的字节。
	relay(client, buffer.Reader, upstream)
}

func (f *Forwarder) dialHTTPConnect(ctx context.Context, address string) (net.Conn, error) {
	conn, err := f.dialSocket(ctx, "tcp", f.upstream.Host)
	if err != nil {
		return nil, ErrUpstream
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: address}, Host: address, Header: make(http.Header)}
	if f.upstream.User != nil {
		password, _ := f.upstream.User.Password()
		auth := f.upstream.User.Username() + ":" + password
		req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(auth)))
	}
	if err := req.Write(conn); err != nil {
		_ = conn.Close()
		return nil, ErrUpstream
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, req)
	if err != nil || response.StatusCode != http.StatusOK {
		_ = conn.Close()
		return nil, ErrUpstream
	}
	_ = conn.SetDeadline(time.Time{})
	return &bufferedConn{Conn: conn, reader: reader, closeWrite: conn.(interface{ CloseWrite() error }).CloseWrite}, nil
}

func (f *Forwarder) dialSOCKS(ctx context.Context, address string) (net.Conn, error) {
	var socket net.Conn
	forward := contextDialer(func(ctx context.Context, network, address string) (net.Conn, error) {
		var err error
		socket, err = f.dialSocket(ctx, network, address)
		return socket, err
	})
	var auth *proxy.Auth
	if f.upstream.User != nil {
		password, _ := f.upstream.User.Password()
		auth = &proxy.Auth{User: f.upstream.User.Username(), Password: password}
	}
	dialer, err := proxy.SOCKS5("tcp", f.upstream.Host, auth, forward)
	if err != nil {
		return nil, ErrUpstream
	}
	conn, err := dialer.(proxy.ContextDialer).DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, ErrUpstream
	}
	return &bufferedConn{Conn: conn, reader: conn, closeWrite: socket.(interface{ CloseWrite() error }).CloseWrite}, nil
}

func (f *Forwarder) dialSocket(ctx context.Context, network, address string) (net.Conn, error) {
	conn, err := f.dialer.DialContext(ctx, network, address)
	if err != nil {
		return nil, ErrUpstream
	}
	return f.track(conn)
}

func (f *Forwarder) track(conn net.Conn) (net.Conn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		_ = conn.Close()
		return nil, ErrClosed
	}
	tracked := &trackedConn{Conn: conn, owner: f}
	f.conns[tracked] = struct{}{}
	return tracked, nil
}

func validAddress(address string) bool {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host == "" || strings.ContainsAny(host, "\r\n") {
		return false
	}
	n, err := strconv.Atoi(port)
	return err == nil && n > 0 && n <= 65535
}

func relay(client net.Conn, clientReader io.Reader, upstream net.Conn) {
	var copies sync.WaitGroup
	copies.Add(2)
	copyOne := func(dst net.Conn, src io.Reader) {
		defer copies.Done()
		if _, err := io.Copy(dst, src); err != nil {
			_ = client.Close()
			_ = upstream.Close()
			return
		}
		if half, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = half.CloseWrite()
		} else {
			_ = dst.Close()
		}
	}
	go copyOne(upstream, clientReader)
	go copyOne(client, upstream)
	copies.Wait()
}

type trackedListener struct {
	net.Listener
	owner *Forwarder
}

func (l trackedListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return l.owner.track(conn)
}

type trackedConn struct {
	net.Conn
	owner *Forwarder
	once  sync.Once
}

func (c *trackedConn) Close() error {
	var err error
	c.once.Do(func() {
		err = c.Conn.Close()
		c.owner.mu.Lock()
		delete(c.owner.conns, c)
		c.owner.mu.Unlock()
	})
	return err
}

func (c *trackedConn) CloseWrite() error { return c.Conn.(*net.TCPConn).CloseWrite() }

type bufferedConn struct {
	net.Conn
	reader     io.Reader
	closeWrite func() error
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }
func (c *bufferedConn) CloseWrite() error          { return c.closeWrite() }

type contextDialer func(context.Context, string, string) (net.Conn, error)

func (d contextDialer) Dial(network, address string) (net.Conn, error) {
	return d(context.Background(), network, address)
}
func (d contextDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return d(ctx, network, address)
}
