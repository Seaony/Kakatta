package browser

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/chromedp"

	"github.com/Seaony/Kakatta/internal/pacer"
)

type generationWire struct {
	*lookupWire
	stateMu    sync.Mutex
	generation int
	stale      int
	holdMethod string
	paused     chan struct{}
	pending    chan cdproto.Message
}

func (w *generationWire) Write(ctx context.Context, msg *cdproto.Message) error {
	method := string(msg.Method)
	w.stateMu.Lock()
	if method == "DOM.getDocument" {
		w.generation++
	}
	rootID, nodeID := w.generation*10+1, w.generation*10+2
	response := cdproto.Message{ID: msg.ID, SessionID: msg.SessionID}
	var result any
	invalid := false
	switch method {
	case "DOM.getDocument":
		result = map[string]any{"root": map[string]any{"nodeId": rootID, "backendNodeId": 1, "nodeType": 9, "nodeName": "#document", "children": []any{map[string]any{"nodeId": nodeID, "backendNodeId": 2, "nodeType": 1, "nodeName": "BUTTON", "children": []any{map[string]any{"nodeId": nodeID + 1, "backendNodeId": 3, "nodeType": 3, "nodeName": "#text", "nodeValue": "信用及评价"}}}}}}
	case "DOM.querySelectorAll", "DOM.describeNode", "CSS.getComputedStyleForNode":
		var params struct{ NodeID int }
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			w.stateMu.Unlock()
			return err
		}
		want := nodeID
		if method == "DOM.querySelectorAll" {
			want = rootID
		}
		invalid = params.NodeID != want
		if method == "DOM.describeNode" {
			result = map[string]any{"node": map[string]any{"nodeId": nodeID, "backendNodeId": 2, "nodeType": 1, "nodeName": "BUTTON"}}
		} else if method == "CSS.getComputedStyleForNode" {
			display := "block"
			if w.hidden {
				display = "none"
			}
			result = map[string]any{"computedStyle": []any{map[string]any{"name": "display", "value": display}}}
		} else {
			ids := []int{nodeID}
			if w.empty {
				ids = nil
			}
			result = map[string]any{"nodeIds": ids}
		}
	case "DOM.pushNodesByBackendIdsToFrontend":
		result = map[string]any{"nodeIds": []int{nodeID}}
	case "DOM.performSearch":
		result = map[string]any{"searchId": strconv.Itoa(w.generation), "resultCount": 1}
	case "DOM.getSearchResults":
		var params struct{ SearchID string }
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			w.stateMu.Unlock()
			return err
		}
		invalid = params.SearchID != strconv.Itoa(w.generation)
		result = map[string]any{"nodeIds": []int{nodeID}}
	default:
		w.stateMu.Unlock()
		return w.lookupWire.Write(ctx, msg)
	}
	if invalid {
		w.stale++
		response.Error = &cdproto.Error{Code: -32000, Message: "Could not find node with given id"}
	} else {
		response.Result, _ = json.Marshal(result)
	}
	hold := method == w.holdMethod
	if hold {
		w.holdMethod = ""
	}
	w.stateMu.Unlock()
	w.mu.Lock()
	copy := *msg
	copy.Params = append(copy.Params[:0:0], msg.Params...)
	w.seen = append(w.seen, copy)
	w.mu.Unlock()
	if hold {
		w.pending <- response
		close(w.paused)
		return nil
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

func (w *generationWire) counts() (generation, stale int) {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	return w.generation, w.stale
}

func generationProtocol(t *testing.T) (*Human, *generationWire, context.CancelCauseFunc) {
	t.Helper()
	lifetime, cancel := context.WithCancelCause(t.Context())
	wire := &generationWire{
		lookupWire: &lookupWire{runtimeWire: &runtimeWire{in: make(chan cdproto.Message, 128), closed: make(chan struct{})}},
		paused:     make(chan struct{}), pending: make(chan cdproto.Message, 1),
	}
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

func awaitPausedDOM(t *testing.T, wire *generationWire) {
	t.Helper()
	select {
	case <-wire.paused:
	case <-time.After(time.Second):
		t.Fatal("没有进入指定的协议交错位置")
	}
}

func TestHumanDOMReadChainsDoNotInvalidateEachOther(t *testing.T) {
	for _, pair := range [][2]string{{"FirstVisible", "SearchText"}, {"SearchText", "FirstVisible"}, {"FindNow", "SearchText"}, {"SearchText", "FindNow"}} {
		t.Run(pair[0]+"/"+pair[1], func(t *testing.T) {
			human, wire, stop := generationProtocol(t)
			wire.holdMethod = "DOM.pushNodesByBackendIdsToFrontend"
			if pair[0] == "FindNow" {
				wire.holdMethod = "DOM.querySelectorAll"
			}
			owner := runLookup(human, t.Context(), pair[0], time.Second)
			awaitPausedDOM(t, wire)
			before, _ := wire.counts()
			caller, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
			defer cancel()
			waiting := runLookup(human, caller, pair[1], time.Second)
			waitErr := awaitLookup(t, waiting, stop)
			during, _ := wire.counts()
			wire.in <- <-wire.pending
			ownerErr := awaitLookup(t, owner, stop)
			_, stale := wire.counts()
			if during != before || stale != 0 || ownerErr != nil || !errors.Is(waitErr, context.DeadlineExceeded) {
				t.Fatalf("DOM读取链被插入或锁等待不能取消：代次%d→%d，失效%d，持有方%v，等待方%v", before, during, stale, ownerErr, waitErr)
			}
			if err := awaitLookup(t, runLookup(human, t.Context(), "FindNow", time.Second), stop); err != nil {
				t.Fatalf("读取结束后没有释放协调：%v", err)
			}
		})
	}
}

func TestHumanDOMGenerationFixtureDetectsStaleStyle(t *testing.T) {
	human, wire, stop := generationProtocol(t)
	wire.holdMethod = "DOM.pushNodesByBackendIdsToFrontend"
	owner := runLookup(human, t.Context(), "VisibleChecked", time.Second)
	awaitPausedDOM(t, wire)
	if _, err := cdp.Call(human.s.ctx, human.queries, dom.GetDocument, dom.GetDocumentParams{}); err != nil {
		t.Fatal(err)
	}
	wire.in <- <-wire.pending
	err := awaitLookup(t, owner, stop)
	_, stale := wire.counts()
	var protocol *cdproto.Error
	if !errors.As(err, &protocol) || protocol.Code != -32000 || stale != 1 {
		t.Fatalf("代次判据不能识别真实旧编号：%d，%v", stale, err)
	}
	if err := awaitLookup(t, runLookup(human, t.Context(), "FindNow", time.Second), stop); err != nil {
		t.Fatalf("失败后没有释放读取协调：%v", err)
	}
}

func TestHumanDOMReadWaiterPreservesCancellationCause(t *testing.T) {
	for _, source := range []string{"caller", "session"} {
		t.Run(source, func(t *testing.T) {
			human, wire, stop := generationProtocol(t)
			wire.holdMethod = "DOM.pushNodesByBackendIdsToFrontend"
			owner := runLookup(human, t.Context(), "VisibleChecked", time.Second)
			awaitPausedDOM(t, wire)
			caller, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			waiting := runLookup(human, caller, "SearchText", time.Second)
			cause := errors.New("风控结束本次读取")
			timer := time.AfterFunc(20*time.Millisecond, func() {
				if source == "caller" {
					cancel(cause)
				} else {
					stop(cause)
				}
			})
			defer timer.Stop()
			waitErr := awaitLookup(t, waiting, stop)
			wire.in <- <-wire.pending
			ownerErr := awaitLookup(t, owner, stop)
			if !errors.Is(waitErr, context.Canceled) || !errors.Is(waitErr, cause) {
				t.Fatalf("等待读取协调时丢失取消原因：%v", waitErr)
			}
			if source == "caller" && ownerErr != nil || source == "session" && !errors.Is(ownerErr, cause) {
				t.Fatalf("取消等待者影响了其他读取，或会话原因丢失：%v", ownerErr)
			}
		})
	}
}

type pausedLookupClock struct{ entered chan struct{} }

func (c pausedLookupClock) Sleep(ctx context.Context, _ time.Duration) error {
	select {
	case c.entered <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return ctx.Err()
}

func (pausedLookupClock) Float64() float64 { return 0.5 }

func TestHumanDOMCoordinationDoesNotCoverPollingWait(t *testing.T) {
	for _, mode := range []string{"Find", "FirstVisible"} {
		t.Run(mode, func(t *testing.T) {
			human, wire, stop := generationProtocol(t)
			wire.empty, wire.hidden = mode == "Find", mode == "FirstVisible"
			clock := pausedLookupClock{entered: make(chan struct{}, 1)}
			human.p = pacer.New(clock)
			caller, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			owner := runLookup(human, caller, mode, time.Second)
			select {
			case <-clock.entered:
			case <-time.After(time.Second):
				t.Fatal("查找没有进入轮询等待")
			}
			if err := awaitLookup(t, runLookup(human, t.Context(), "SearchText", time.Second), stop); err != nil {
				t.Fatalf("轮询间隔挡住了风控读取：%v", err)
			}
			cause := errors.New("结束本次查找")
			cancel(cause)
			if err := awaitLookup(t, owner, stop); !errors.Is(err, cause) || !errors.Is(err, context.Canceled) {
				t.Fatalf("轮询取消原因丢失：%v", err)
			}
		})
	}
}
