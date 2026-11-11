package remote

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestExplicitSessionCloseMarksEndBeforeRFBHandshake(t *testing.T) {
	s := testSession(t, echoVNC(t))
	accepted := make(chan struct{})
	server := httptest.NewServer(s.HandlerWithViewer(func(*http.Request) bool { return true }, func(ctx context.Context) error {
		close(accepted)
		<-ctx.Done()
		return ctx.Err()
	}))
	defer server.Close()
	conn, ctx := connectViewer(t, server)
	<-accepted
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	_, _, err := conn.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusNormalClosure || !strings.Contains(err.Error(), "kakatta-session-ended") {
		t.Fatal("正常会话结束缺少固定标记", err)
	}
	select {
	case err := <-closed:
		if err != nil || s.HasViewer() {
			t.Fatal("正常结束后仍有viewer或清理失败", err)
		}
	case <-time.After(time.Second):
		t.Fatal("正常结束未回收viewer")
	}
}

func TestEarlierFailureCannotBecomeNormalDuringExplicitCleanup(t *testing.T) {
	for _, cause := range []string{"parent", "process", "earlier_failure"} {
		t.Run(cause, func(t *testing.T) {
			s := testSession(t, echoVNC(t))
			accepted := make(chan struct{})
			server := httptest.NewServer(s.HandlerWithViewer(func(*http.Request) bool { return true }, func(ctx context.Context) error {
				close(accepted)
				<-ctx.Done()
				return ctx.Err()
			}))
			defer server.Close()
			conn, ctx := connectViewer(t, server)
			<-accepted
			switch cause {
			case "parent":
				parent, cancel := context.WithCancel(context.Background())
				s.parent = parent
				cancel()
			case "process":
				s.done = make(chan struct{})
				close(s.done)
			case "earlier_failure":
				if err := s.close(false); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			_, _, err := conn.Read(ctx)
			if err == nil || websocket.CloseStatus(err) == websocket.StatusNormalClosure || strings.Contains(err.Error(), "kakatta-session-ended") || s.normalEnd || s.HasViewer() {
				t.Fatal("先发生的故障被清理动作伪装为正常结束", err)
			}
		})
	}
}

func TestUpstreamLossClosesAndJoinsWithoutNormalEndMarker(t *testing.T) {
	release := make(chan struct{})
	s := testSession(t, loopbackVNC(t, func(conn net.Conn) { <-release }))
	finished := make(chan struct{})
	handler := s.Handler(func(*http.Request) bool { return true })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
		close(finished)
	}))
	defer server.Close()
	conn, ctx := connectViewer(t, server)
	close(release)
	_, _, err := conn.Read(ctx)
	if err == nil || websocket.CloseStatus(err) == websocket.StatusNormalClosure || strings.Contains(err.Error(), "kakatta-session-ended") {
		t.Fatal("上游丢失被标成正常会话结束", err)
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("上游丢失后未等待转发协程退出")
	}
	if s.HasViewer() || s.ctx.Err() != nil {
		t.Fatal("上游丢失未回收viewer或关闭了整个会话")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := conn.Read(ctx); err == nil || strings.Contains(err.Error(), "kakatta-session-ended") {
		t.Fatal("稍后的资源清理改写了异常关闭", err)
	}
}
