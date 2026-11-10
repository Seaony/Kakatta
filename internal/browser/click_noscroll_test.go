package browser

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"

	"github.com/Seaony/Kakatta/internal/pacer"
)

type clickWire struct {
	*runtimeWire
	quad        []float64
	viewport    any
	metricsFail bool
	holdMethod  string
	entered     chan struct{}
	once        sync.Once
}

func (w *clickWire) Write(ctx context.Context, msg *cdproto.Message) error {
	method := string(msg.Method)
	if method != "DOM.getBoxModel" && method != "Page.getLayoutMetrics" && method != w.holdMethod {
		return w.runtimeWire.Write(ctx, msg)
	}
	w.mu.Lock()
	copy := *msg
	copy.Params = append(copy.Params[:0:0], msg.Params...)
	w.seen = append(w.seen, copy)
	w.mu.Unlock()
	if method == w.holdMethod {
		w.once.Do(func() { close(w.entered) })
		return nil
	}
	response := cdproto.Message{ID: msg.ID, SessionID: msg.SessionID}
	if method == "DOM.getBoxModel" {
		response.Result, _ = json.Marshal(map[string]any{"model": map[string]any{"width": 60, "height": 20, "content": w.quad}})
	} else if w.metricsFail {
		response.Error = &cdproto.Error{Code: -32000, Message: "画面信息不可用"}
	} else {
		response.Result, _ = json.Marshal(map[string]any{"cssVisualViewport": w.viewport})
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-w.closed:
		return context.Canceled
	case w.in <- response:
		return nil
	}
}

type immediateClickClock struct{}

func (immediateClickClock) Sleep(ctx context.Context, _ time.Duration) error { return ctx.Err() }
func (immediateClickClock) Float64() float64                                 { return 0.5 }

func clickProtocol(t *testing.T) (*Human, *chromedp.Node, *clickWire) {
	t.Helper()
	lifetime, cancel := context.WithCancel(t.Context())
	wire := &clickWire{
		runtimeWire: &runtimeWire{in: make(chan cdproto.Message, 128), closed: make(chan struct{})},
		quad:        []float64{20, 25, 80, 25, 80, 45, 20, 45},
		viewport:    map[string]any{"offsetX": 10, "offsetY": 20, "clientWidth": 300, "clientHeight": 200, "scale": 1},
		entered:     make(chan struct{}),
	}
	browser, err := chromedp.NewBrowserTransport(lifetime, wire)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancelTarget := chromedp.NewContext(lifetime, chromedp.WithTargetID("main"), func(c *chromedp.Context) { c.Browser = browser })
	t.Cleanup(func() { cancelTarget(); cancel(); wire.Close() })
	if err := initializeTarget(ctx); err != nil {
		t.Fatal(err)
	}
	human := NewHuman(&Session{ctx: ctx}, pacer.New(immediateClickClock{}), pacer.Between(0, 0))
	node := &chromedp.Node{Node: &cdp.Node{BackendNodeID: 2, NodeID: 2, NodeType: 1, NodeName: "BUTTON"}}
	return human, node, wire
}

func mouseCommands(t *testing.T, wire *clickWire) []input.DispatchMouseEventParams {
	t.Helper()
	result := []input.DispatchMouseEventParams{}
	for _, cmd := range wire.commands() {
		if string(cmd.Method) != "Input.dispatchMouseEvent" {
			continue
		}
		var params input.DispatchMouseEventParams
		if err := json.Unmarshal(cmd.Params, &params); err != nil {
			t.Fatal(err)
		}
		result = append(result, params)
	}
	return result
}

func requireNoScrollCommands(t *testing.T, wire *clickWire) {
	t.Helper()
	for _, cmd := range wire.commands() {
		if string(cmd.Method) == "DOM.scrollIntoViewIfNeeded" || string(cmd.Method) == "Input.synthesizeScrollGesture" {
			t.Fatalf("不滚动的点击仍发出滚动命令：%s", cmd.Method)
		}
	}
	for _, mouse := range mouseCommands(t, wire) {
		if mouse.Type == input.DispatchMouseEventTypeMouseWheel {
			t.Fatal("不滚动的点击发出了滚轮事件")
		}
	}
}

func TestClickNoScrollUsesMouseSequenceOnly(t *testing.T) {
	human, node, wire := clickProtocol(t)
	if err := human.ClickNoScroll(t.Context(), node); err != nil {
		t.Fatal(err)
	}
	requireNoScrollCommands(t, wire)
	mouse := mouseCommands(t, wire)
	if len(mouse) < 8 || len(mouse) > 15 {
		t.Fatalf("没有复用分步移动和按下抬起：%d", len(mouse))
	}
	for _, step := range mouse[:len(mouse)-2] {
		if step.Type != input.DispatchMouseEventTypeMouseMoved {
			t.Fatal("鼠标移动步骤混入了其他动作")
		}
	}
	pressed, released := mouse[len(mouse)-2], mouse[len(mouse)-1]
	if pressed.Type != input.DispatchMouseEventTypeMousePressed || released.Type != input.DispatchMouseEventTypeMouseReleased || pressed.X != 50 || pressed.Y != 35 || released.X != pressed.X || released.Y != pressed.Y || pressed.Button != input.MouseButtonLeft || pressed.ClickCount != 1 {
		t.Fatalf("点击落点或按下抬起不符：%+v %+v", pressed, released)
	}
}

func TestClickNoScrollRejectsOffscreenAndUnknownViewport(t *testing.T) {
	for _, mode := range []string{"左侧超出", "下方超出", "部分超出", "空面积", "画面缺失", "画面失败"} {
		t.Run(mode, func(t *testing.T) {
			human, node, wire := clickProtocol(t)
			switch mode {
			case "左侧超出":
				wire.quad = []float64{5, 25, 65, 25, 65, 45, 5, 45}
			case "下方超出":
				wire.quad = []float64{20, 230, 80, 230, 80, 250, 20, 250}
			case "部分超出":
				wire.quad = []float64{20, 210, 80, 210, 80, 230, 20, 230}
			case "空面积":
				wire.quad = []float64{20, 25, 20, 25, 20, 45, 20, 45}
			case "画面缺失":
				wire.viewport = nil
			case "画面失败":
				wire.metricsFail = true
			}
			err := human.ClickNoScroll(t.Context(), node)
			if err == nil {
				t.Fatal("未确认视口内元素仍允许点击")
			}
			if mode != "画面缺失" && mode != "画面失败" && !errors.Is(err, ErrNotFound) {
				t.Fatal("视口外未报告找不到可点击元素", err)
			}
			if len(mouseCommands(t, wire)) != 0 {
				t.Fatal("视口校验失败仍发出鼠标动作")
			}
			requireNoScrollCommands(t, wire)
		})
	}
}

func TestClickNoScrollCancellationStopsPendingProtocolCommand(t *testing.T) {
	for _, method := range []string{"DOM.getBoxModel", "Page.getLayoutMetrics", "Input.dispatchMouseEvent"} {
		t.Run(method, func(t *testing.T) {
			human, node, wire := clickProtocol(t)
			wire.holdMethod = method
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			finished := make(chan error, 1)
			go func() { finished <- human.ClickNoScroll(ctx, node) }()
			<-wire.entered
			cause := errors.New("停止本次点击")
			cancel(cause)
			select {
			case err := <-finished:
				if !errors.Is(err, context.Canceled) || !errors.Is(err, cause) {
					t.Fatal("取消点击分类或原因丢失", err)
				}
			case <-time.After(time.Second):
				t.Fatal("取消后仍等点击协议命令")
			}
			if human.s.ctx.Err() != nil {
				t.Fatal("取消点击关闭了浏览器")
			}
			requireNoScrollCommands(t, wire)
		})
	}
}

func TestClickKeepsExistingScrollBehavior(t *testing.T) {
	human, node, wire := clickProtocol(t)
	if err := human.Click(t.Context(), node); err != nil {
		t.Fatal(err)
	}
	scrolls := 0
	for _, cmd := range wire.commands() {
		if string(cmd.Method) == "DOM.scrollIntoViewIfNeeded" {
			scrolls++
		}
	}
	if scrolls != 1 || len(mouseCommands(t, wire)) < 8 {
		t.Fatal("旧点击入口的滚动和鼠标行为被改变")
	}
}
