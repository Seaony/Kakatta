//go:build kakatta_dev

package remote

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestDevelopmentNativePreviewRejectsMissingCaptureAndCancellation(t *testing.T) {
	if view, err := StartDevelopmentNativePreview(t.Context(), nil); err == nil || view != nil {
		t.Fatal("没有截图来源时不应返回空画面")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if view, err := StartDevelopmentNativePreview(ctx, func(context.Context) ([]byte, error) { return []byte{1}, nil }); !errors.Is(err, context.Canceled) || view != nil {
		t.Fatal("已取消的会话不应重新启动画面", err)
	}
}

func TestDevelopmentNativePreviewUsesNativeNoticeAndRejectsInput(t *testing.T) {
	frame := []byte{0xff, 0xd8, 0xff, 0xd9}
	var captures atomic.Int64
	view, err := StartDevelopmentNativePreview(t.Context(), func(context.Context) ([]byte, error) {
		captures.Add(1)
		return frame, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = view.Close() })
	handler := view.Handler(func(r *http.Request) bool { return r.Header.Get("X-Development-Test") == "yes" })
	for _, route := range []string{"/", "/vnc", "/preview.js"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, route, nil))
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("本机开发画面仍必须验证登录：%s %d", route, response.Code)
		}
	}
	for _, lang := range []string{"zh-CN", "en", "ja"} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.Header.Set("X-Development-Test", "yes")
		request.Header.Set("Accept-Language", lang)
		handler.ServeHTTP(response, request)
		body := response.Body.String()
		if response.Code != http.StatusOK || !strings.Contains(body, "本机假页请在独立的 Chrome 窗口操作") || !strings.Contains(body, "This web view is read-only.") || !strings.Contains(body, "この画面は閲覧専用です。") || strings.Contains(body, "重启容器") || strings.Contains(body, "restarting the container") || strings.Contains(body, "<button") || captures.Load() != 0 {
			t.Fatal("本机画面不得显示容器恢复指引、提供输入或提前截图", body)
		}
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	connection, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/vnc", &websocket.DialOptions{HTTPHeader: http.Header{"X-Development-Test": {"yes"}, "Origin": {server.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.CloseNow() })
	kind, data, err := connection.Read(ctx)
	if err != nil || kind != websocket.MessageBinary || !bytes.Equal(data, frame) || captures.Load() != 1 || view.Address() != "" {
		t.Fatal("本机画面只能通过现有只读协议发送截图", err)
	}
	if err := connection.Write(ctx, websocket.MessageText, []byte("key input")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := connection.Read(ctx); websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatal("只读画面不能接收客户端按键", err)
	}
	if err := view.Close(); err != nil || view.HasViewer() {
		t.Fatal("本机画面关闭后还有读取者", err)
	}
}
