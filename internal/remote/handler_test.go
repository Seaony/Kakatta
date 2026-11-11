package remote

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"path"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func testSession(t *testing.T, address string) *Session {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s := &Session{ctx: ctx, cancel: cancel, address: address, conns: map[net.Conn]struct{}{}}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestHandlerRequiresAuthenticationForEveryRoute(t *testing.T) {
	s := testSession(t, "127.0.0.1:1")
	for _, check := range []func(*http.Request) bool{nil, func(*http.Request) bool { return false }} {
		for _, path := range []string{"/", "/viewer.js", "/novnc/core/rfb.js", "/viewport", "/vnc"} {
			r := httptest.NewRequest(http.MethodGet, path, nil)
			w := httptest.NewRecorder()
			s.Handler(check).ServeHTTP(w, r)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("%s 未拒绝未认证请求：%d", path, w.Code)
			}
			if strings.Contains(w.Body.String(), "127.0.0.1") {
				t.Fatal("错误内容泄露了内部地址")
			}
		}
	}
}

func TestStaticAndViewportUnderPrefix(t *testing.T) {
	s := testSession(t, "127.0.0.1:1")
	view := &Viewport{X: 800, Y: 300, Width: 400, Height: 200}
	if err := s.SetViewport(view); err != nil {
		t.Fatal(err)
	}
	view.X = 0
	h := http.StripPrefix("/remote", s.Handler(func(r *http.Request) bool { return r.Header.Get("Cookie") == "session=valid" }))
	for _, path := range []string{"/remote/", "/remote/viewer.js", "/remote/viewer.css", "/remote/novnc/core/rfb.js", "/remote/novnc/vendor/pako/lib/zlib/inflate.js", "/remote/viewport"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("Cookie", "session=valid")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK || w.Body.Len() == 0 {
			t.Fatalf("%s 未正确提供：%d", path, w.Code)
		}
		if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Frame-Options") != "SAMEORIGIN" || strings.Contains(w.Header().Get("Content-Security-Policy"), "unsafe-inline") {
			t.Fatalf("%s 缺少安全响应头", path)
		}
		if path == "/remote/viewport" {
			var got Viewport
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.X != 800 {
				t.Fatalf("验证码坐标被调用方改动：%s", w.Body.String())
			}
		}
	}
}

func TestOriginRejectedBeforeUpstreamDial(t *testing.T) {
	s := testSession(t, "127.0.0.1:1")
	for _, origin := range []string{"", "null", "http://other.test", "http://tool.test.evil", "http://tool.test/path", "http://tool.test?query=1", "http://user@tool.test", "ws://tool.test"} {
		r := httptest.NewRequest(http.MethodGet, "http://tool.test/vnc", nil)
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		s.Handler(func(*http.Request) bool { return true }).ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("来源 %q 未被拒绝：%d", origin, w.Code)
		}
	}
}

func echoVNC(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() { defer conn.Close(); io.Copy(conn, conn) }()
		}
	}()
	return l.Addr().String()
}

func dialViewer(t *testing.T, s *Session) (*websocket.Conn, context.Context) {
	t.Helper()
	server := httptest.NewServer(s.Handler(func(*http.Request) bool { return true }))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/vnc", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {server.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	return conn, ctx
}

func TestBinaryStreamAndSessionClose(t *testing.T) {
	s := testSession(t, echoVNC(t))
	conn, ctx := dialViewer(t, s)
	payload := []byte{0, 1, 2, 255, '\n', 0, 128}
	if err := conn.Write(ctx, websocket.MessageBinary, payload); err != nil {
		t.Fatal(err)
	}
	var received []byte
	for len(received) < len(payload) {
		kind, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if kind != websocket.MessageBinary {
			t.Fatal("转发改变了二进制消息类型")
		}
		received = append(received, data...)
	}
	if !bytes.Equal(received, payload) {
		t.Fatalf("转发改变了内容：%x", received)
	}
	closed := make(chan struct{})
	go func() { s.Close(); s.Close(); close(closed) }()
	if _, _, err := conn.Read(ctx); websocket.CloseStatus(err) != websocket.StatusNormalClosure {
		t.Fatal("正常会话结束没有完成关闭握手", err)
	}
	select {
	case <-closed:
	case <-ctx.Done():
		t.Fatal("关闭会话没有回收活动连接")
	}
	if s.register(nil) {
		t.Fatal("关闭会话后仍能注册连接")
	}
}

func TestTextFramesAreRejected(t *testing.T) {
	s := testSession(t, echoVNC(t))
	conn, ctx := dialViewer(t, s)
	if err := conn.Write(ctx, websocket.MessageText, []byte("not an RFB stream")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := conn.Read(ctx); err == nil {
		t.Fatal("文本消息被当成 VNC 流转发")
	}
}

func TestSessionCancellationClosesStream(t *testing.T) {
	s := testSession(t, echoVNC(t))
	conn, ctx := dialViewer(t, s)
	s.cancel()
	if _, _, err := conn.Read(ctx); err == nil {
		t.Fatal("上下文取消后仍保持远程连接")
	}
}

func TestUpstreamFailureDoesNotExposeAddress(t *testing.T) {
	s := testSession(t, "127.0.0.1:1")
	r := httptest.NewRequest(http.MethodGet, "http://tool.test/vnc", nil)
	r.Header.Set("Origin", "http://tool.test")
	w := httptest.NewRecorder()
	s.Handler(func(*http.Request) bool { return true }).ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), s.Address()) {
		t.Fatalf("远程画面关闭时响应不正确：%d %s", w.Code, w.Body.String())
	}
}

func TestControlPageContainsFixedContentHashes(t *testing.T) {
	page := string(ControlPage())
	policy := ControlPagePolicy()
	if strings.Contains(page, "{{") || strings.Contains(policy, "unsafe-inline") {
		t.Fatal("控制页模板或安全策略未完成")
	}
	for _, asset := range []string{"control.css", "control.js"} {
		data, err := assets.ReadFile("assets/" + asset)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		if !strings.Contains(page, string(data)) || !strings.Contains(policy, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'") {
			t.Fatalf("%s 的内容或哈希不一致", asset)
		}
	}
}

func TestVendoredModuleImportsResolve(t *testing.T) {
	imports := regexp.MustCompile(`(?:\bfrom\s*|\bimport\s*)['"](\.[^'"]+)['"]`)
	err := fs.WalkDir(assets, "assets/novnc", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(name, ".js") {
			return err
		}
		data, err := assets.ReadFile(name)
		if err != nil {
			return err
		}
		for _, match := range imports.FindAllSubmatch(data, -1) {
			dependency := path.Join(path.Dir(name), string(match[1]))
			if _, err := fs.Stat(assets, dependency); err != nil {
				t.Errorf("%s 缺少依赖 %s", name, dependency)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
