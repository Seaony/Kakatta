package browser

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/chromedp"

	"github.com/Seaony/Kakatta/internal/pacer"
)

type readWire struct {
	*lookupWire
	cookieValue string
}

func (w *readWire) Write(ctx context.Context, msg *cdproto.Message) error {
	method := string(msg.Method)
	var result any
	switch method {
	case "DOM.getBoxModel":
		result = map[string]any{"model": map[string]any{"width": 60, "height": 20, "content": []int{20, 25, 80, 25, 80, 45, 20, 45}}}
	case "Accessibility.getPartialAXTree":
		result = map[string]any{"nodes": []any{map[string]any{"nodeId": "input", "ignored": false, "value": map[string]any{"type": "string", "value": "本地文字"}}}}
	case "Network.getCookies":
		result = map[string]any{"cookies": []any{map[string]any{"name": "unb", "value": w.cookieValue}}}
	default:
		return w.lookupWire.Write(ctx, msg)
	}
	w.mu.Lock()
	copy := *msg
	copy.Params = append(copy.Params[:0:0], msg.Params...)
	w.seen = append(w.seen, copy)
	w.mu.Unlock()
	if method == w.hold {
		w.once.Do(func() { close(w.entered) })
		return nil
	}
	response := cdproto.Message{ID: msg.ID, SessionID: msg.SessionID}
	if method == w.fail {
		response.Error = &cdproto.Error{Code: -32000, Message: "本地读取失败"}
	} else {
		response.Result, _ = json.Marshal(result)
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

func readProtocol(t *testing.T) (*Human, *chromedp.Node, *readWire, context.CancelCauseFunc) {
	t.Helper()
	lifetime, cancelTransport := context.WithCancel(t.Context())
	wire := &readWire{lookupWire: &lookupWire{runtimeWire: &runtimeWire{in: make(chan cdproto.Message, 128), closed: make(chan struct{})}, entered: make(chan struct{})}}
	browser, err := chromedp.NewBrowserTransport(lifetime, wire)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancelTarget := chromedp.NewContext(lifetime, chromedp.WithTargetID("main"), func(c *chromedp.Context) { c.Browser = browser })
	sessionCtx, cancelSession := context.WithCancelCause(ctx)
	t.Cleanup(func() { cancelSession(nil); cancelTarget(); cancelTransport(); wire.Close() })
	if err := initializeTarget(ctx); err != nil {
		t.Fatal(err)
	}
	human := NewHuman(&Session{ctx: sessionCtx}, pacer.New(immediateClickClock{}), pacer.Between(0, 0))
	return human, &chromedp.Node{Node: &cdp.Node{BackendNodeID: 2, NodeType: 1}}, wire, cancelSession
}

func contextRead(h *Human, ctx context.Context, n *chromedp.Node, mode string) error {
	var err error
	switch mode {
	case "Box":
		_, _, _, _, err = h.BoxContext(ctx, n)
	case "Value":
		_, err = h.ValueContext(ctx, n)
	case "Visible":
		_, err = h.VisibleCheckedContext(ctx, n)
	case "HasCookie":
		_, err = h.s.HasCookieContext(ctx, []string{"http://127.0.0.1/"}, "unb")
	case "LegacyBox":
		_, _, _, _, err = h.Box(n)
	case "LegacyValue":
		_, err = h.Value(n)
	case "LegacyVisible":
		_, err = h.VisibleChecked(n)
	case "LegacyHasCookie":
		_, err = h.s.HasCookie([]string{"http://127.0.0.1/"}, "unb")
	default:
		panic("没有这个读取测试")
	}
	return err
}

func TestContextReadsCancelPendingProtocolAndKeepCause(t *testing.T) {
	for _, test := range []struct{ mode, method string }{
		{"Box", "DOM.getBoxModel"}, {"Value", "Accessibility.getPartialAXTree"}, {"Visible", "DOM.getBoxModel"},
		{"Visible", "DOM.pushNodesByBackendIdsToFrontend"}, {"Visible", "CSS.getComputedStyleForNode"},
		{"HasCookie", "Network.getCookies"},
	} {
		for _, source := range []string{"caller", "session"} {
			t.Run(test.mode+"/"+test.method+"/"+source, func(t *testing.T) {
				human, node, wire, stop := readProtocol(t)
				wire.hold = test.method
				caller, cancel := context.WithCancelCause(t.Context())
				defer cancel(nil)
				finished := make(chan error, 1)
				go func() { finished <- contextRead(human, caller, node, test.mode) }()
				select {
				case <-wire.entered:
				case <-time.After(time.Second):
					t.Fatal("读取没有进入指定协议命令")
				}
				cause := errors.New("风控停止本次读取")
				if source == "caller" {
					cancel(cause)
				} else {
					stop(cause)
				}
				err := awaitLookup(t, finished, stop)
				if !errors.Is(err, context.Canceled) || !errors.Is(err, cause) {
					t.Fatalf("读取取消的分类或原因丢失：%v", err)
				}
				if source == "caller" && human.s.ctx.Err() != nil {
					t.Fatal("取消读取关闭了整个浏览器")
				}
			})
		}
	}
}

func TestContextReadsPreCancellationSendsNoCommands(t *testing.T) {
	for _, mode := range []string{"Box", "Value", "Visible", "HasCookie", "LegacyBox", "LegacyValue", "LegacyVisible", "LegacyHasCookie"} {
		t.Run(mode, func(t *testing.T) {
			human, node, wire, stop := readProtocol(t)
			before := len(wire.commands())
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			cause := errors.New("读取已停止")
			if strings.HasPrefix(mode, "Legacy") {
				stop(cause)
			} else {
				cancel(cause)
			}
			err := contextRead(human, ctx, node, mode)
			if !errors.Is(err, context.Canceled) || !errors.Is(err, cause) {
				t.Fatalf("预取消读取的分类或原因丢失：%v", err)
			}
			if after := len(wire.commands()); after != before {
				t.Fatalf("预取消读取仍发出命令：%d→%d", before, after)
			}
		})
	}
}

func TestContextReadDeadlineAndProtocolErrorRemainDistinct(t *testing.T) {
	for _, test := range []struct{ mode, method string }{
		{"Box", "DOM.getBoxModel"}, {"Value", "Accessibility.getPartialAXTree"}, {"Visible", "DOM.getBoxModel"},
		{"HasCookie", "Network.getCookies"},
	} {
		for _, source := range []string{"deadline", "protocol"} {
			t.Run(test.mode+"/"+source, func(t *testing.T) {
				human, node, wire, stop := readProtocol(t)
				ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
				defer cancel()
				if source == "deadline" {
					wire.hold = test.method
				} else {
					wire.fail = test.method
				}
				finished := make(chan error, 1)
				go func() { finished <- contextRead(human, ctx, node, test.mode) }()
				err := awaitLookup(t, finished, stop)
				if errors.Is(err, ErrNotFound) {
					t.Fatalf("真正读取错误不能当作缺元素：%v", err)
				}
				var protocol *cdproto.Error
				if source == "deadline" && !errors.Is(err, context.DeadlineExceeded) || source == "protocol" && !errors.As(err, &protocol) {
					t.Fatalf("读取错误分类有误：%v", err)
				}
			})
		}
	}
}

func TestContextReadsKeepValuesAndCookiePrivacyRules(t *testing.T) {
	human, node, wire, _ := readProtocol(t)
	if x, y, w, h, err := human.Box(node); err != nil || x != 20 || y != 25 || w != 60 || h != 20 {
		t.Fatalf("旧位置读取结果有误：%v %v %v %v %v", x, y, w, h, err)
	}
	if value, err := human.Value(node); err != nil || value != "本地文字" {
		t.Fatalf("旧输入框读取结果有误：%q %v", value, err)
	}
	if visible, err := human.VisibleChecked(node); err != nil || !visible {
		t.Fatalf("旧可见性读取结果有误：%v %v", visible, err)
	}
	urls := []string{"http://127.0.0.1/"}
	for _, value := range []string{"", "local-fixture"} {
		wire.cookieValue = value
		for _, name := range []string{"unb", "absent"} {
			want := name == "unb"
			if present, err := human.s.HasCookie(urls, name); err != nil || present != want {
				t.Fatal("旧入口必须只按名字检查，空值同名也应存在", name, present, err)
			}
			if present, err := human.s.HasCookieContext(t.Context(), urls, name); err != nil || present != want {
				t.Fatal("有上下文的入口必须只按名字检查，空值同名也应存在", name, present, err)
			}
		}
	}
	for _, command := range wire.commands() {
		if command.Method == "Runtime.enable" || command.Method == "Runtime.evaluate" {
			continue
		}
		if strings.Contains(string(command.Params), "local-fixture") {
			t.Fatal("读取命令不应传出Cookie值")
		}
	}
}

func TestCookieValueBasedPresenceEntrypointsAreNotExposed(t *testing.T) {
	if _, exposed := any(&Session{}).(interface {
		LoginCookiePresent([]string, string) (bool, error)
	}); exposed {
		t.Fatal("浏览器不能再暴露依赖 Cookie 值的登录判断入口")
	}
	if _, exposed := any(&Session{}).(interface {
		LoginCookiePresentContext(context.Context, []string, string) (bool, error)
	}); exposed {
		t.Fatal("浏览器不能再暴露依赖 Cookie 值的带上下文登录判断入口")
	}
}

func TestLegacyValueReadHasBoundedProtocolWait(t *testing.T) {
	human, node, wire, stop := readProtocol(t)
	wire.hold = "Accessibility.getPartialAXTree"
	start := time.Now()
	finished := make(chan error, 1)
	go func() { finished <- contextRead(human, t.Context(), node, "LegacyValue") }()
	select {
	case err := <-finished:
		if !errors.Is(err, context.DeadlineExceeded) || human.s.ctx.Err() != nil || time.Since(start) > 6*time.Second {
			t.Fatalf("旧读取入口没有独立的五秒边界：%v", err)
		}
	case <-time.After(6 * time.Second):
		stop(errors.New("清理卡住的旧读取"))
		awaitLookup(t, finished, stop)
		t.Fatal("旧读取入口的协议等待没有上限")
	}
}
