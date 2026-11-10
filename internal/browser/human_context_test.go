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
	"github.com/chromedp/chromedp"

	"github.com/Seaony/Kakatta/internal/pacer"
)

type lookupWire struct {
	*runtimeWire
	hold, fail string
	empty      bool
	hidden     bool
	entered    chan struct{}
	once       sync.Once
}

func (w *lookupWire) Write(ctx context.Context, msg *cdproto.Message) error {
	method := string(msg.Method)
	var result any
	switch method {
	case "DOM.getDocument":
		result = map[string]any{"root": map[string]any{"nodeId": 1, "backendNodeId": 1, "nodeType": 9, "nodeName": "#document", "children": []any{map[string]any{"nodeId": 2, "backendNodeId": 2, "nodeType": 1, "nodeName": "BUTTON", "children": []any{map[string]any{"nodeId": 3, "backendNodeId": 3, "nodeType": 3, "nodeName": "#text", "nodeValue": "信用及评价"}}}}}}
	case "DOM.querySelectorAll", "DOM.getSearchResults", "DOM.pushNodesByBackendIdsToFrontend":
		ids := []int{2}
		if w.empty {
			ids = nil
		}
		result = map[string]any{"nodeIds": ids}
	case "DOM.describeNode":
		result = map[string]any{"node": map[string]any{"nodeId": 2, "backendNodeId": 2, "nodeType": 1, "nodeName": "BUTTON"}}
	case "DOM.performSearch":
		result = map[string]any{"searchId": "lookup", "resultCount": 1}
	case "DOM.discardSearchResults":
		result = map[string]any{}
	case "DOM.getBoxModel":
		result = map[string]any{"model": map[string]any{"width": 60, "height": 20}}
	case "CSS.getComputedStyleForNode":
		display := "block"
		if w.hidden {
			display = "none"
		}
		result = map[string]any{"computedStyle": []any{map[string]any{"name": "display", "value": display}}}
	default:
		return w.runtimeWire.Write(ctx, msg)
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
		response.Error = &cdproto.Error{Code: -32000, Message: "读取页面失败"}
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

func lookupProtocol(t *testing.T) (*Human, *lookupWire, context.CancelCauseFunc) {
	t.Helper()
	lifetime, cancel := context.WithCancelCause(t.Context())
	wire := &lookupWire{runtimeWire: &runtimeWire{in: make(chan cdproto.Message, 128), closed: make(chan struct{})}, entered: make(chan struct{})}
	browser, err := chromedp.NewBrowserTransport(lifetime, wire)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancelTarget := chromedp.NewContext(lifetime, chromedp.WithTargetID("main"), func(c *chromedp.Context) { c.Browser = browser })
	t.Cleanup(func() { cancelTarget(); cancel(nil); wire.Close() })
	if err := initializeTarget(ctx); err != nil {
		t.Fatal(err)
	}
	return NewHuman(&Session{ctx: ctx}, pacer.New(nil), pacer.Between(0, 0)), wire, cancel
}

func awaitLookup(t *testing.T, finished <-chan error, stop context.CancelCauseFunc) error {
	t.Helper()
	select {
	case err := <-finished:
		return err
	case <-time.After(200 * time.Millisecond):
		stop(errors.New("清理超时测试"))
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Fatal("会话取消后仍未回收读取")
		}
		t.Fatal("读取没有服从本次操作的取消或期限")
		return nil
	}
}

func runLookup(h *Human, ctx context.Context, mode string, timeout time.Duration) <-chan error {
	finished := make(chan error, 1)
	go func() {
		var err error
		switch mode {
		case "Find":
			_, err = h.Find(ctx, "button", timeout)
		case "FirstVisible":
			_, err = h.FirstVisible(ctx, "button", timeout)
		case "FindNow":
			_, err = h.FindNow(ctx, "button")
		case "SearchText":
			_, err = h.SearchText(ctx, "信用及评价")
		case "VisibleChecked":
			_, err = h.VisibleChecked(&chromedp.Node{Node: &cdp.Node{BackendNodeID: 2, NodeType: 1}})
		}
		finished <- err
	}()
	return finished
}

func TestHumanLookupOwnDeadlineMeansNotFound(t *testing.T) {
	for _, mode := range []string{"Find", "FirstVisible"} {
		methods := []string{"DOM.getDocument", "DOM.querySelectorAll", "DOM.describeNode", ""}
		if mode == "FirstVisible" {
			methods = append(methods, "DOM.getBoxModel", "DOM.pushNodesByBackendIdsToFrontend", "CSS.getComputedStyleForNode")
		}
		for _, method := range methods {
			t.Run(mode+"/"+method, func(t *testing.T) {
				human, wire, stop := lookupProtocol(t)
				wire.hold = method
				wire.empty, wire.hidden = mode == "Find", mode == "FirstVisible"
				if method != "" {
					wire.empty = false
				}
				err := awaitLookup(t, runLookup(human, t.Context(), mode, 20*time.Millisecond), stop)
				if !errors.Is(err, ErrNotFound) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("查找自身超时不能被当成运行取消：%v", err)
				}
				if t.Context().Err() != nil || human.s.ctx.Err() != nil {
					t.Fatal("查找超时取消了调用方或浏览器")
				}
			})
		}
	}
}

func TestHumanLookupPreservesCallerAndSessionCancellation(t *testing.T) {
	for _, mode := range []string{"Find", "FirstVisible", "FindNow", "SearchText", "VisibleChecked"} {
		for _, source := range []string{"caller", "session"} {
			if mode == "VisibleChecked" && source == "caller" {
				continue
			}
			t.Run(mode+"/"+source, func(t *testing.T) {
				human, wire, stop := lookupProtocol(t)
				wire.hold = "DOM.getDocument"
				if mode == "VisibleChecked" {
					wire.hold = "DOM.getBoxModel"
				}
				caller, cancel := context.WithCancelCause(t.Context())
				defer cancel(nil)
				finished := runLookup(human, caller, mode, time.Second)
				<-wire.entered
				cause := errors.New("任务或账号已停止")
				if source == "caller" {
					cancel(cause)
				} else {
					stop(cause)
				}
				err := awaitLookup(t, finished, stop)
				if !errors.Is(err, context.Canceled) || !errors.Is(err, cause) || errors.Is(err, ErrNotFound) {
					t.Fatalf("真正取消的分类或原因丢失：%v", err)
				}
			})
		}
	}
}

func TestHumanDOMReadsRespectCallerDeadlineAtEveryStage(t *testing.T) {
	for _, mode := range []string{"FindNow", "SearchText"} {
		methods := []string{"DOM.getDocument", "DOM.querySelectorAll", "DOM.describeNode"}
		if mode == "SearchText" {
			methods = []string{"DOM.getDocument", "DOM.performSearch", "DOM.getSearchResults", "DOM.getBoxModel", "DOM.pushNodesByBackendIdsToFrontend", "CSS.getComputedStyleForNode"}
		}
		for _, method := range methods {
			t.Run(mode+"/"+method, func(t *testing.T) {
				human, wire, stop := lookupProtocol(t)
				wire.hold = method
				caller, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
				defer cancel()
				err := awaitLookup(t, runLookup(human, caller, mode, time.Second), stop)
				if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrNotFound) || human.s.ctx.Err() != nil {
					t.Fatalf("调用方超时被改成缺元素或丢失：%v", err)
				}
			})
		}
	}
}

func TestHumanLookupDoesNotHideProtocolErrors(t *testing.T) {
	for _, mode := range []string{"Find", "FirstVisible", "FindNow", "SearchText", "VisibleChecked"} {
		t.Run(mode, func(t *testing.T) {
			human, wire, stop := lookupProtocol(t)
			wire.fail = "DOM.getDocument"
			if mode == "VisibleChecked" {
				wire.fail = "DOM.getBoxModel"
			}
			err := awaitLookup(t, runLookup(human, t.Context(), mode, time.Second), stop)
			var protocol *cdproto.Error
			if !errors.As(err, &protocol) || protocol.Message != "读取页面失败" || errors.Is(err, ErrNotFound) {
				t.Fatalf("真正的页面读取错误被吞掉：%v", err)
			}
		})
	}
}

type lookupFailureSession struct{ cdp.Session }

func (s lookupFailureSession) Call(ctx context.Context, _ string, _, _ any) error {
	<-ctx.Done()
	return errors.Join(&cdproto.Error{Code: -32000, Message: "连接在超时边界中断"}, ctx.Err())
}

func TestHumanLookupKeepsProtocolFailureAtOwnDeadline(t *testing.T) {
	for _, mode := range []string{"Find", "FirstVisible"} {
		t.Run(mode, func(t *testing.T) {
			human, _, stop := lookupProtocol(t)
			human.queries = lookupFailureSession{human.queries}
			err := awaitLookup(t, runLookup(human, t.Context(), mode, 20*time.Millisecond), stop)
			var protocol *cdproto.Error
			if !errors.As(err, &protocol) || protocol.Message != "连接在超时边界中断" || errors.Is(err, ErrNotFound) {
				t.Fatalf("协议失败和期限重合时丢失了真实错误：%v", err)
			}
		})
	}
}

func TestHumanLookupPreCanceledCallerSendsNoCommands(t *testing.T) {
	for _, mode := range []string{"Find", "FirstVisible", "FindNow", "SearchText"} {
		t.Run(mode, func(t *testing.T) {
			human, wire, stop := lookupProtocol(t)
			before := len(wire.commands())
			caller, cancel := context.WithCancelCause(t.Context())
			cause := errors.New("本次运行已经停止")
			cancel(cause)
			err := awaitLookup(t, runLookup(human, caller, mode, time.Second), stop)
			if !errors.Is(err, cause) || !errors.Is(err, context.Canceled) || errors.Is(err, ErrNotFound) {
				t.Fatalf("预取消的分类或原因丢失：%v", err)
			}
			if after := len(wire.commands()); after != before {
				t.Fatalf("操作开始前已取消仍发出协议命令：%d→%d", before, after)
			}
		})
	}
}

func TestHumanLookupKeepsSuccessfulDOMAndTextReads(t *testing.T) {
	human, _, _ := lookupProtocol(t)
	for _, selector := range []string{"button", "//button"} {
		nodes, err := human.FindNow(t.Context(), selector)
		if err != nil || len(nodes) != 1 || nodes[0].BackendNodeID != 2 {
			t.Fatalf("CSS或XPath查找结果有误：%s %v %v", selector, nodes, err)
		}
	}
	nodes, err := human.Find(t.Context(), "button", time.Second)
	if err != nil || len(nodes) != 1 {
		t.Fatalf("等待元素结果有误：%v %v", nodes, err)
	}
	node, err := human.FirstVisible(t.Context(), "button", time.Second)
	if err != nil || node == nil || node.BackendNodeID != 2 {
		t.Fatalf("第一个可见元素结果有误：%v %v", node, err)
	}
	if visible, err := human.VisibleChecked(node); err != nil || !visible {
		t.Fatalf("旧可见性入口有误：%v %v", visible, err)
	}
	nodes, err = human.SearchText(t.Context(), "信用及评价")
	if err != nil || len(nodes) != 1 || nodes[0].BackendNodeID != 2 {
		t.Fatalf("可见文字查找结果有误：%v %v", nodes, err)
	}
}
