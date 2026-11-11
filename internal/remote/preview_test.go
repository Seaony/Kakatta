package remote

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestPreviewStartsOnlyForAnEligibleVNCFailure(t *testing.T) {
	started := &Session{}
	failure := &vncStartError{"reason_missing", errors.New("未公开的路径")}
	other := errors.New("资源已关")
	for _, tc := range []struct {
		name     string
		view     *Session
		err      error
		canceled bool
		wantView bool
		wantErr  error
	}{
		{"成功时保留原画面", started, nil, false, false, nil},
		{"x11vnc 失败时使用备用", nil, failure, false, true, nil},
		{"取消时不切换", nil, failure, true, false, context.Canceled},
		{"其他错误不切换", nil, other, false, false, other},
		{"部分资源交由调用方回收", started, failure, false, false, failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			view, err := startWithFallback(ctx, ":107", func(context.Context) ([]byte, error) {
				t.Fatal("没有用户接入时发起了截图")
				return nil, nil
			}, func(context.Context, string) (*Session, error) {
				if tc.canceled {
					cancel()
				}
				return tc.view, tc.err
			})
			if !errors.Is(err, tc.wantErr) || (view != nil && view.preview != nil) != tc.wantView {
				t.Fatalf("备用启动口径错误：%v", err)
			}
			if tc.wantView {
				defer view.Close()
				if view.Address() != "" || view.HasViewer() || view.preview.reason != failure.reason {
					t.Fatal("备用开启额外端口或未使用安全原因")
				}
			} else if view != tc.view {
				t.Fatal("启动返回的原资源被替换")
			}
		})
	}
}

func previewFixture(t *testing.T, capture func(context.Context) ([]byte, error)) *Session {
	t.Helper()
	view, err := startWithFallback(t.Context(), ":107", capture, func(context.Context, string) (*Session, error) {
		return nil, &vncStartError{"reason_timeout", errors.New("<script>不公开的文件路径</script>")}
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = view.Close() })
	return view
}

func TestPreviewReusesAuthorizationAndRejectsClientData(t *testing.T) {
	jpeg := []byte{0xff, 0xd8, 0xff, 0xd9}
	var captures, accepted atomic.Int64
	view := previewFixture(t, func(context.Context) ([]byte, error) {
		captures.Add(1)
		return jpeg, nil
	})
	handler := view.HandlerWithViewer(func(r *http.Request) bool { return r.Header.Get("X-Test-Viewer") == "yes" }, func(context.Context) error {
		accepted.Add(1)
		return nil
	})
	for _, route := range []string{"/", "/vnc", "/preview.js"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, route, nil))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("匿名请求没有被拦截：%s %d", route, w.Code)
		}
	}
	w := httptest.NewRecorder()
	page := httptest.NewRequest(http.MethodGet, "/", nil)
	page.Header.Set("X-Test-Viewer", "yes")
	page.Header.Set("Accept-Language", "zh-CN")
	handler.ServeHTTP(w, page)
	body := w.Body.String()
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(body, "等待 x11vnc 就绪超时") ||
		!strings.Contains(body, "仅查看，无法操作") || !strings.Contains(body, "重启容器") || !strings.Contains(body, "查看容器日志") ||
		strings.Contains(body, "不公开的文件路径") || strings.Contains(body, "<button") || captures.Load() != 0 {
		t.Fatalf("备用页面内容、安全标头或惰性启动错误：%d %s", w.Code, body)
	}
	w = httptest.NewRecorder()
	page.Header.Set("Accept-Language", "en-US,zh-CN;q=0.8")
	handler.ServeHTTP(w, page)
	body = w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, `<html lang="en">`) || !strings.Contains(body, "Fallback view only. Controls are unavailable.") ||
		!strings.Contains(body, "Timed out waiting for x11vnc") || !strings.Contains(body, "check the container logs") || !strings.Contains(body, `data-disconnected="The fallback view disconnected"`) {
		t.Fatalf("实验页面没有按请求语言返回完整的英文文案：%d %s", w.Code, body)
	}
	w = httptest.NewRecorder()
	page.Header.Set("Accept-Language", "en;q=0.2,ja-JP;q=0.9,zh;q=0")
	handler.ServeHTTP(w, page)
	body = w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, `<html lang="ja">`) || !strings.Contains(body, "予備の画面です。閲覧のみで操作はできません。") ||
		!strings.Contains(body, "x11vncの起動待ちがタイムアウトしました") || !strings.Contains(body, "コンテナのログを確認してください") ||
		!strings.Contains(body, `data-disconnected="予備の画面が切断されました。"`) || strings.Contains(body, "不公开的文件路径") {
		t.Fatalf("备用页面没有完整使用日语或泄露原始原因：%d %s", w.Code, body)
	}
	for _, lang := range []string{"zh-CN", "en", "ja"} {
		if strings.Count(body, `<span lang="`+lang+`"`) != 1 {
			t.Fatalf("远程画面缺少或重复语言副本：%s", lang)
		}
	}
	w = httptest.NewRecorder()
	page.Header.Set("Accept-Language", "fr-FR")
	handler.ServeHTTP(w, page)
	if !strings.Contains(w.Body.String(), `<html lang="en">`) {
		t.Fatal("未知语言未回退英文")
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	address := "ws" + strings.TrimPrefix(server.URL, "http") + "/vnc"
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	headers := http.Header{"X-Test-Viewer": {"yes"}, "Origin": {"https://other.invalid"}}
	conn, response, err := websocket.Dial(ctx, address, &websocket.DialOptions{HTTPHeader: headers})
	if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
		if conn != nil {
			conn.CloseNow()
		}
		t.Fatalf("接受了其他站点的请求：%v", err)
	}
	headers.Set("Origin", server.URL)
	conn, _, err = websocket.Dial(ctx, address, &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	kind, frame, err := conn.Read(ctx)
	if err != nil || kind != websocket.MessageBinary || !bytes.Equal(frame, jpeg) || accepted.Load() != 1 || !view.HasViewer() {
		t.Fatalf("没有向已认证连接交付截图：%x %v", frame, err)
	}
	if err := conn.Write(ctx, websocket.MessageText, []byte("不允许的输入")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := conn.Read(ctx); websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("客户端数据没有导致只读连接关闭：%v", err)
	}
	if err := view.Close(); err != nil || view.HasViewer() || captures.Load() != 1 {
		t.Fatalf("关闭后还有读取者或抓取未受限：%v", err)
	}
}

func TestPreviewLimitsCaptureAcrossConnections(t *testing.T) {
	var captures atomic.Int64
	preview := &readOnlyPreview{capture: func(context.Context) ([]byte, error) {
		captures.Add(1)
		return []byte{1}, nil
	}}
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			frame, err := preview.image(t.Context())
			if err != nil || !bytes.Equal(frame, []byte{1}) {
				t.Errorf("并发读取没有复用截图：%v %v", frame, err)
			}
		})
	}
	workers.Wait()
	if captures.Load() != 1 {
		t.Fatalf("同一会话为多个读取者重复截图：%d", captures.Load())
	}
}

func TestPreviewCloseCancelsAndWaitsForScreenshot(t *testing.T) {
	entered := make(chan struct{})
	left := make(chan struct{})
	view := previewFixture(t, func(ctx context.Context) ([]byte, error) {
		close(entered)
		<-ctx.Done()
		close(left)
		return nil, ctx.Err()
	})
	server := httptest.NewServer(view.Handler(func(*http.Request) bool { return true }))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/vnc", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {server.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("没有开始读取截图")
	}
	closed := make(chan error, 1)
	go func() { closed <- view.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("关闭没有取消正在读取的截图")
	}
	select {
	case <-left:
	default:
		t.Fatal("关闭未等待读取协程退出")
	}
	if _, _, err := ws.Read(ctx); err == nil || ctx.Err() != nil {
		t.Fatalf("关闭后连接仍可用：%v", err)
	}
}
