package browser

import (
	"context"
	"errors"
	"testing"

	"github.com/chromedp/cdproto"
	"github.com/chromedp/chromedp"
)

type viewportWire struct{ *runtimeWire }

func (w *viewportWire) Write(ctx context.Context, message *cdproto.Message) error {
	var result []byte
	switch message.Method {
	case "Page.getLayoutMetrics":
		result = []byte(`{"cssLayoutViewport":{"pageX":0,"pageY":0,"clientWidth":1200,"clientHeight":870}}`)
	case "Browser.getWindowForTarget":
		result = []byte(`{"windowId":1,"bounds":{"left":34,"top":18,"width":1200,"height":1000}}`)
	default:
		return w.runtimeWire.Write(ctx, message)
	}
	select {
	case w.in <- cdproto.Message{ID: message.ID, SessionID: message.SessionID, Result: result}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestContentScreenOriginUsesBrowserContextAndCallerCancellation(t *testing.T) {
	wire := &viewportWire{&runtimeWire{in: make(chan cdproto.Message, 128), closed: make(chan struct{})}}
	t.Cleanup(func() { _ = wire.Close() })
	connection, err := chromedp.NewBrowserTransport(t.Context(), wire)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := chromedp.NewContext(t.Context(), chromedp.WithTargetID("main"), func(c *chromedp.Context) { c.Browser = connection })
	defer cancel()
	if err := initializeTarget(ctx); err != nil {
		t.Fatal(err)
	}
	s := &Session{ctx: ctx}
	if x, y, err := s.ContentScreenOrigin(t.Context()); err != nil || x != 34 || y != 148 {
		t.Fatalf("内容区没有计入窗口位置和工具栏：%d, %d, %v", x, y, err)
	}
	caller, stop := context.WithCancel(t.Context())
	stop()
	if _, _, err := s.ContentScreenOrigin(caller); !errors.Is(err, context.Canceled) {
		t.Fatalf("已取消的调用不能继续定位：%v", err)
	}
}
