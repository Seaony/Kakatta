package modelservice

import (
	"bufio"
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis/client"
	"golang.org/x/net/proxy"
)

type networkState struct {
	mu      sync.Mutex
	current *networkClient
	closed  bool
}

type networkConfig struct {
	timeout time.Duration
	proxy   string
}

type networkClient struct {
	config    networkConfig
	client    *client.Client
	transport *http.Transport
	users     int
	retired   bool
}

func (s *Service) Complete(ctx context.Context, request client.Request) (client.Result, error) {
	ctx, finish, err := s.beginJob(ctx)
	if err != nil {
		return client.Result{}, err
	}
	defer finish()
	current, release, err := s.borrowClient(ctx)
	if err != nil {
		return client.Result{}, err
	}
	defer release()
	return current.Complete(ctx, request)
}

func (s *Service) borrowClient(ctx context.Context) (*client.Client, func(), error) {
	s.network.mu.Lock()
	defer s.network.mu.Unlock()
	if s.network.closed {
		return nil, nil, closedService()
	}
	config, err := s.networkConfig(ctx)
	if err != nil {
		return nil, nil, storageFailure(err)
	}
	current := s.network.current
	if current == nil || current.config != config {
		transport, secrets, err := modelTransport(config)
		if err != nil {
			return nil, nil, storageFailure(err)
		}
		next, err := client.New(client.Dependencies{Repository: s, Accounting: s, Slots: s,
			HTTP: &http.Client{Transport: modelRoundTripper{transport}}, Timeout: config.timeout, Secrets: secrets, CompatLog: logCompatibility})
		if err != nil {
			transport.CloseIdleConnections()
			return nil, nil, storageFailure(err)
		}
		if err := ctx.Err(); err != nil {
			transport.CloseIdleConnections()
			return nil, nil, err
		}
		if current != nil {
			retireClient(current)
		}
		current = &networkClient{config: config, client: next, transport: transport}
		s.network.current = current
	}
	current.users++
	var once sync.Once
	return current.client, func() {
		once.Do(func() {
			s.network.mu.Lock()
			defer s.network.mu.Unlock()
			current.users--
			if current.retired && current.users == 0 {
				current.transport.CloseIdleConnections()
			}
		})
	}, nil
}

// 调用方持网络锁；旧调用仍用旧配置，最后一个调用退出才收空闲连接。
func retireClient(current *networkClient) {
	current.retired = true
	if current.users == 0 {
		current.transport.CloseIdleConnections()
	}
}

func (s *Service) closeNetwork() {
	s.network.mu.Lock()
	defer s.network.mu.Unlock()
	s.network.closed = true
	if s.network.current != nil {
		retireClient(s.network.current)
		s.network.current = nil
	}
}

func (s *Service) networkConfig(ctx context.Context) (networkConfig, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return networkConfig{}, err
	}
	defer tx.Rollback()
	snapshot, err := s.settings.SnapshotTx(ctx, tx)
	if err != nil {
		return networkConfig{}, err
	}
	var seconds int64
	if err := json.Unmarshal(snapshot.Values["ai.request_timeout_seconds"], &seconds); err != nil || seconds < 1 || seconds > int64((1<<63-1)/time.Second) {
		return networkConfig{}, errors.New("模型超时设置无效")
	}
	config := networkConfig{timeout: time.Duration(seconds) * time.Second}
	var proxyID *int64
	if err := json.Unmarshal(snapshot.Values["ai.proxy_id"], &proxyID); err != nil {
		return networkConfig{}, err
	}
	if proxyID != nil {
		var uuid string
		var encrypted []byte
		if err := tx.QueryRowContext(ctx, `SELECT uuid,url_enc FROM proxies WHERE id=?`, *proxyID).Scan(&uuid, &encrypted); err != nil {
			return networkConfig{}, err
		}
		plain, err := s.cipher.Open(encrypted, "proxies", "url_enc", uuid)
		if err != nil {
			return networkConfig{}, err
		}
		config.proxy = string(plain)
		clear(plain)
		if _, err := modelProxyURL(config.proxy); err != nil {
			return networkConfig{}, err
		}
	}
	return config, nil
}

func modelProxyURL(raw string) (*url.URL, error) {
	address, err := url.Parse(raw)
	if err != nil || address.Opaque != "" || address.RawQuery != "" || address.ForceQuery || address.Fragment != "" ||
		(address.Scheme != "http" && address.Scheme != "socks5") || address.Path != "" && address.Path != "/" || strings.ContainsAny(raw, "\r\n") {
		return nil, errors.New("模型代理地址无效")
	}
	host, port, err := net.SplitHostPort(address.Host)
	number, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || host == "" || strings.ContainsAny(host, "\r\n\t ") || number < 1 || number > 65535 {
		return nil, errors.New("模型代理地址无效")
	}
	if address.Scheme == "socks5" && address.User != nil {
		password, _ := address.User.Password()
		if len(address.User.Username()) > 255 || len(password) > 255 {
			return nil, errors.New("模型代理认证无效")
		}
	}
	return address, nil
}

func modelTransport(config networkConfig) (*http.Transport, []string, error) {
	dialer := &net.Dialer{Timeout: config.timeout, KeepAlive: 30 * time.Second}
	transport := &http.Transport{ForceAttemptHTTP2: true, TLSClientConfig: &tls.Config{},
		IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 10 * time.Second, ExpectContinueTimeout: time.Second}
	dial := dialer.DialContext
	var httpProxy *url.URL
	var secrets []string
	if config.proxy != "" {
		address, err := modelProxyURL(config.proxy)
		if err != nil {
			return nil, nil, err
		}
		secrets = []string{config.proxy}
		if address.User != nil {
			password, _ := address.User.Password()
			secrets = append(secrets, address.User.Username(), password)
		}
		if address.Scheme == "http" {
			httpProxy = address
			proxyURL := http.ProxyURL(address)
			transport.Proxy = func(request *http.Request) (*url.URL, error) {
				// HTTPS先在私有拨号里完成CONNECT和TLS，再交连接池。
				if request.URL.Scheme == "https" {
					return nil, nil
				}
				return proxyURL(request)
			}
		} else {
			upstream, err := proxy.FromURL(address, dialer)
			if err != nil {
				return nil, nil, errors.New("模型代理配置无效")
			}
			contextDialer, ok := upstream.(proxy.ContextDialer)
			if !ok {
				return nil, nil, errors.New("模型代理不支持取消")
			}
			dial = contextDialer.DialContext
		}
	}
	transport.DialContext = requestDialer(dial)
	transport.DialTLSContext = requestDialer(func(ctx context.Context, network, address string) (net.Conn, error) {
		return dialModelTLS(ctx, network, address, dial, httpProxy, transport)
	})
	return transport, secrets, nil
}

func dialModelTLS(ctx context.Context, network, address string, dial func(context.Context, string, string) (net.Conn, error), httpProxy *url.URL, transport *http.Transport) (_ net.Conn, err error) {
	target := address
	if httpProxy != nil {
		target = httpProxy.Host
	}
	raw, err := dial(ctx, network, target)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = raw.Close()
		}
	}()
	closed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = raw.Close(); close(closed) })
	defer func() {
		// 就绪连接交池前，不能再有原请求的关连接回调留在后台。
		if !stop() {
			<-closed
		}
	}()
	var conn net.Conn = raw
	if httpProxy != nil {
		conn, err = connectModelProxy(raw, address, httpProxy)
		if err != nil {
			return nil, err
		}
	}
	tlsConfig := transport.TLSClientConfig.Clone()
	if tlsConfig.ServerName == "" {
		tlsConfig.ServerName, _, err = net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
	}
	secure := tls.Client(conn, tlsConfig)
	handshake, cancel := context.WithTimeout(ctx, transport.TLSHandshakeTimeout)
	defer cancel()
	if err := secure.HandshakeContext(handshake); err != nil {
		return nil, err
	}
	return secure, nil
}

type modelBufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *modelBufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

func connectModelProxy(conn net.Conn, address string, upstream *url.URL) (net.Conn, error) {
	request := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: address}, Host: address, Header: make(http.Header)}
	if upstream.User != nil {
		password, _ := upstream.User.Password()
		auth := upstream.User.Username() + ":" + password
		request.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(auth)))
	}
	if err := request.Write(conn); err != nil {
		return nil, fmt.Errorf("proxy CONNECT: %w", err)
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, request)
	if err != nil {
		return nil, fmt.Errorf("proxy CONNECT: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("proxy CONNECT rejected")
	}
	return &modelBufferedConn{Conn: conn, reader: reader}, nil
}

type modelRequestKey struct{}

type modelRoundTripper struct{ transport *http.Transport }

type modelDialRequest struct {
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	done   bool
	dials  sync.WaitGroup
}

func (t modelRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithCancel(request.Context())
	dial := &modelDialRequest{ctx: ctx, cancel: cancel}
	defer dial.finish()
	requestCtx := context.WithValue(request.Context(), modelRequestKey{}, dial)
	return t.transport.RoundTrip(request.WithContext(requestCtx))
}

func (d *modelDialRequest) finish() {
	d.mu.Lock()
	d.done = true
	d.cancel()
	d.mu.Unlock()
	d.dials.Wait()
}

// Transport 会分离拨号与请求的取消；把原请求带回来，握手不能留在后台。
func requestDialer(dial func(context.Context, string, string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		request, ok := ctx.Value(modelRequestKey{}).(*modelDialRequest)
		if !ok {
			return dial(ctx, network, address)
		}
		request.mu.Lock()
		if request.done {
			request.mu.Unlock()
			return nil, context.Canceled
		}
		request.dials.Add(1)
		request.mu.Unlock()
		defer request.dials.Done()
		dialCtx, cancel := context.WithCancel(request.ctx)
		stop := context.AfterFunc(ctx, cancel)
		defer stop()
		defer cancel()
		conn, err := dial(dialCtx, network, address)
		if err != nil {
			return nil, err
		}
		request.mu.Lock()
		defer request.mu.Unlock()
		if request.done || request.ctx.Err() != nil {
			_ = conn.Close()
			return nil, context.Canceled
		}
		return conn, nil
	}
}

func logCompatibility(ctx context.Context, key client.CompatKey, before, after client.Compat) error {
	slog.InfoContext(ctx, "模型兼容参数已更新", "provider_id", key.Provider.ID, "before", before, "after", after)
	return nil
}
