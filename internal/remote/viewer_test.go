package remote

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestRequestCancellationClosesViewerAndJoins(t *testing.T) {
	s := testSession(t, echoVNC(t))
	cancels := make(chan context.CancelFunc, 1)
	finished := make(chan struct{}, 1)
	handler := s.Handler(func(*http.Request) bool { return true })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		cancels <- cancel
		handler.ServeHTTP(w, r.WithContext(ctx))
		finished <- struct{}{}
	}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/vnc", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {server.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	payload := []byte("viewer lease")
	if err := conn.Write(ctx, websocket.MessageBinary, payload); err != nil {
		t.Fatal(err)
	}
	stream := websocket.NetConn(ctx, conn, websocket.MessageBinary)
	received := make([]byte, len(payload))
	if _, err := io.ReadFull(stream, received); err != nil || !bytes.Equal(payload, received) {
		t.Fatalf("取消前没有连通真实回环流：%q %v", received, err)
	}
	(<-cancels)()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("请求取消后转发未关闭并等待两个复制协程退出")
	}
	if _, _, err := conn.Read(ctx); err == nil {
		t.Fatal("请求取消后仍可读取远程画面")
	}
	if s.HasViewer() || s.ctx.Err() != nil {
		t.Fatal("请求取消没有仅清理当前viewer")
	}
}

func loopbackVNC(t *testing.T, serve func(net.Conn)) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var workers sync.WaitGroup
	conns := make(map[net.Conn]struct{})
	closed := false
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			if closed {
				conn.Close()
				mu.Unlock()
				continue
			}
			conns[conn] = struct{}{}
			mu.Unlock()
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer conn.Close()
				serve(conn)
				mu.Lock()
				delete(conns, conn)
				mu.Unlock()
			}()
		}
	}()
	t.Cleanup(func() {
		mu.Lock()
		closed = true
		for conn := range conns {
			conn.Close()
		}
		mu.Unlock()
		listener.Close()
		workers.Wait()
	})
	return listener.Addr().String()
}

func connectViewer(t *testing.T, server *httptest.Server) (*websocket.Conn, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/vnc", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {server.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	return conn, ctx
}

type upgradeResponse struct {
	http.ResponseWriter
	beforeUpgrade func()
}

func (w upgradeResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w upgradeResponse) WriteHeader(status int) {
	if status == http.StatusSwitchingProtocols {
		w.beforeUpgrade()
	}
	w.ResponseWriter.WriteHeader(status)
}

func TestViewerRegistrationWaitsForSuccessfulUpgrade(t *testing.T) {
	s := testSession(t, loopbackVNC(t, func(conn net.Conn) { io.Copy(conn, conn) }))
	h := s.HandlerWithViewer(func(*http.Request) bool { return true }, func(context.Context) error {
		if !s.HasViewer() {
			t.Error("成功升级后的接入回调看不到当前viewer")
		}
		return nil
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(upgradeResponse{w, func() {
			if s.HasViewer() {
				t.Error("升级尚未成功，HasViewer已让待接手会话开始计时")
			}
		}}, r)
	}))
	t.Cleanup(server.Close)
	conn, ctx := connectViewer(t, server)
	if err := conn.Write(ctx, websocket.MessageBinary, []byte("accepted")); err != nil {
		t.Fatal(err)
	}
	stream := websocket.NetConn(ctx, conn, websocket.MessageBinary)
	got := make([]byte, len("accepted"))
	if _, err := io.ReadFull(stream, got); err != nil || string(got) != "accepted" {
		t.Fatalf("成功接入后没有转发：%q %v", got, err)
	}
}

func TestViewerAcceptedAfterUpgradeBeforeEitherVNCStream(t *testing.T) {
	greeting := []byte("RFB 003.008\n")
	payload := []byte{0, 1, 255, 0, 128}
	upstreamRead := make(chan []byte, 1)
	s := testSession(t, loopbackVNC(t, func(conn net.Conn) {
		if _, err := conn.Write(greeting); err != nil {
			return
		}
		got := make([]byte, len(payload))
		if _, err := io.ReadFull(conn, got); err == nil {
			upstreamRead <- got
			conn.Write(got)
		}
	}))
	accepted := make(chan bool, 1)
	release := make(chan struct{})
	defer close(release)
	type requestKey struct{}
	h := s.HandlerWithViewer(func(*http.Request) bool { return true }, func(ctx context.Context) error {
		accepted <- ctx.Value(requestKey{}) == "viewer"
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestKey{}, "viewer")))
	}))
	t.Cleanup(server.Close)
	conn, ctx := connectViewer(t, server)
	if !<-accepted {
		t.Fatal("接入回调丢失了请求上下文")
	}
	stream := websocket.NetConn(ctx, conn, websocket.MessageBinary)
	firstRead := make(chan error, 1)
	gotGreeting := make([]byte, len(greeting))
	go func() { _, err := io.ReadFull(stream, gotGreeting); firstRead <- err }()
	if err := conn.Write(ctx, websocket.MessageBinary, payload); err != nil {
		t.Fatal(err)
	}
	select {
	case <-firstRead:
		t.Fatal("接入回调完成前已向viewer发送VNC字节")
	case <-upstreamRead:
		t.Fatal("接入回调完成前已向VNC转发viewer字节")
	case <-time.After(100 * time.Millisecond):
	}
	release <- struct{}{}
	if err := <-firstRead; err != nil || !bytes.Equal(gotGreeting, greeting) {
		t.Fatalf("接入通过后没有收到原始VNC握手：%q %v", gotGreeting, err)
	}
	if got := <-upstreamRead; !bytes.Equal(got, payload) {
		t.Fatalf("接入通过后上游字节错误：%x", got)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(stream, got); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("接入通过后双向流不通：%x %v", got, err)
	}
}

func TestViewerCallbackFailureClosesOnlyThatViewer(t *testing.T) {
	s := testSession(t, loopbackVNC(t, func(conn net.Conn) { io.Copy(conn, conn) }))
	var calls atomic.Int32
	finished := make(chan struct{}, 2)
	h := s.HandlerWithViewer(func(*http.Request) bool { return true }, func(context.Context) error {
		if calls.Add(1) == 1 {
			return errors.New("接入状态已经改变")
		}
		return nil
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r)
		finished <- struct{}{}
	}))
	t.Cleanup(server.Close)
	conn, ctx := connectViewer(t, server)
	if _, _, err := conn.Read(ctx); err == nil {
		t.Fatal("接入失败仍向viewer提供字节")
	}
	select {
	case <-finished:
	case <-ctx.Done():
		t.Fatal("接入失败没有回收该viewer")
	}
	if s.HasViewer() || s.ctx.Err() != nil {
		t.Fatal("接入失败清理了整个会话或留下连接")
	}
	next, nextCtx := connectViewer(t, server)
	if err := next.Write(nextCtx, websocket.MessageBinary, []byte("retry")); err != nil {
		t.Fatal(err)
	}
	stream := websocket.NetConn(nextCtx, next, websocket.MessageBinary)
	got := make([]byte, 5)
	if _, err := io.ReadFull(stream, got); err != nil || string(got) != "retry" || calls.Load() != 2 {
		t.Fatalf("后续viewer不能复用仍然存活的会话：%q %v，回调%d次", got, err, calls.Load())
	}
}

func TestViewerCallbackDoesNotRunForHTTPOrFailedUpgrade(t *testing.T) {
	s := testSession(t, loopbackVNC(t, func(conn net.Conn) { io.Copy(conn, conn) }))
	var calls atomic.Int32
	server := httptest.NewServer(s.HandlerWithViewer(func(r *http.Request) bool {
		return r.Header.Get("Cookie") == "session=valid"
	}, func(context.Context) error { calls.Add(1); return nil }))
	t.Cleanup(server.Close)
	for _, tc := range []struct {
		name, path, cookie, origin string
		status                     int
	}{
		{"页面", "/", "session=valid", "", http.StatusOK},
		{"位置", "/viewport", "session=valid", "", http.StatusOK},
		{"普通GET", "/vnc", "session=valid", server.URL, http.StatusUpgradeRequired},
		{"未认证", "/vnc", "", server.URL, http.StatusUnauthorized},
		{"跨站", "/vnc", "session=valid", "https://other.test", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := http.NewRequest(http.MethodGet, server.URL+tc.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			r.Header.Set("Cookie", tc.cookie)
			r.Header.Set("Origin", tc.origin)
			response, err := server.Client().Do(r)
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if response.StatusCode != tc.status || calls.Load() != 0 {
				t.Fatalf("未升级请求触发接入或状态错误：%d，回调%d次", response.StatusCode, calls.Load())
			}
		})
	}
}

func TestCanceledViewerDoesNotDialVNC(t *testing.T) {
	for _, source := range []string{"request", "session"} {
		t.Run(source, func(t *testing.T) {
			var dials atomic.Int32
			s := testSession(t, loopbackVNC(t, func(net.Conn) { dials.Add(1) }))
			if source == "session" {
				s.cancel()
			}
			var accepted atomic.Int32
			h := s.HandlerWithViewer(func(*http.Request) bool { return true }, func(context.Context) error { accepted.Add(1); return nil })
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if source == "request" {
					ctx, cancel := context.WithCancel(r.Context())
					cancel()
					r = r.WithContext(ctx)
				}
				h.ServeHTTP(w, r)
			}))
			t.Cleanup(server.Close)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			conn, response, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/vnc", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {server.URL}}})
			if conn != nil {
				conn.CloseNow()
			}
			if err == nil || response == nil || response.StatusCode != http.StatusServiceUnavailable || dials.Load() != 0 || accepted.Load() != 0 {
				t.Fatalf("已取消上下文仍升级或拨号：err=%v，response=%v，dials=%d，accepted=%d", err, response, dials.Load(), accepted.Load())
			}
		})
	}
}

func TestViewerCancellationReachesCallback(t *testing.T) {
	for _, source := range []string{"request", "session"} {
		t.Run(source, func(t *testing.T) {
			s := testSession(t, loopbackVNC(t, func(conn net.Conn) { io.Copy(conn, conn) }))
			entered := make(chan struct{})
			finished := make(chan struct{})
			requestCancel := make(chan context.CancelFunc, 1)
			h := s.HandlerWithViewer(func(*http.Request) bool { return true }, func(ctx context.Context) error {
				close(entered)
				<-ctx.Done()
				return ctx.Err()
			})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ctx, cancel := context.WithCancel(r.Context())
				defer cancel()
				requestCancel <- cancel
				h.ServeHTTP(w, r.WithContext(ctx))
				close(finished)
			}))
			t.Cleanup(server.Close)
			conn, ctx := connectViewer(t, server)
			<-entered
			if source == "request" {
				(<-requestCancel)()
			} else {
				s.cancel()
			}
			select {
			case <-finished:
			case <-ctx.Done():
				t.Fatal("取消未传递到接入回调并结束viewer")
			}
			if _, _, err := conn.Read(ctx); err == nil || s.HasViewer() {
				t.Fatal("回调取消后仍保留viewer连接")
			}
		})
	}
}

func TestViewerCancellationJoinsWhenVNCStopsReading(t *testing.T) {
	for _, source := range []string{"request", "session"} {
		t.Run(source, func(t *testing.T) {
			peerReady := make(chan struct{})
			releasePeer := make(chan struct{})
			defer close(releasePeer)
			s := testSession(t, loopbackVNC(t, func(conn net.Conn) {
				if err := conn.(*net.TCPConn).SetReadBuffer(1024); err != nil {
					t.Error(err)
				}
				close(peerReady)
				<-releasePeer
			}))
			requestCancel := make(chan context.CancelFunc, 1)
			finished := make(chan struct{})
			h := s.Handler(func(*http.Request) bool { return true })
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ctx, cancel := context.WithCancel(r.Context())
				defer cancel()
				requestCancel <- cancel
				h.ServeHTTP(w, r.WithContext(ctx))
				close(finished)
			}))
			t.Cleanup(server.Close)
			conn, ctx := connectViewer(t, server)
			<-peerReady
			var writes atomic.Int64
			writerDone := make(chan struct{})
			go func() {
				defer close(writerDone)
				payload := make([]byte, 64*1024)
				for conn.Write(ctx, websocket.MessageBinary, payload) == nil {
					writes.Add(1)
				}
			}()
			t.Cleanup(func() {
				conn.CloseNow()
				select {
				case <-writerDone:
				case <-time.After(time.Second):
					t.Error("测试写入协程没有退出")
				}
			})
			deadline := time.Now().Add(2 * time.Second)
			last, unchangedSince := int64(0), time.Now()
			for {
				current := writes.Load()
				if current != last {
					last, unchangedSince = current, time.Now()
				}
				if current > 0 && time.Since(unchangedSince) >= 100*time.Millisecond {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("没有产生真实TCP背压")
				}
				time.Sleep(10 * time.Millisecond)
			}
			select {
			case <-writerDone:
				t.Fatal("取消前写入已结束，没有覆盖阻塞流")
			default:
			}
			if source == "request" {
				(<-requestCancel)()
			} else {
				s.cancel()
			}
			select {
			case <-finished:
			case <-time.After(time.Second):
				t.Fatal("VNC停止读取时取消没有关闭两端并等待复制结束")
			}
			if s.HasViewer() || source == "request" && s.ctx.Err() != nil {
				t.Fatal("取消没有只回收对应的viewer")
			}
		})
	}
}
