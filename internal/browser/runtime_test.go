package browser

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto"
	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"

	"github.com/Seaony/Kakatta/internal/pacer"
)

type runtimeWire struct {
	mu      sync.Mutex
	seen    []cdproto.Message
	in      chan cdproto.Message
	closed  chan struct{}
	once    sync.Once
	failOff bool
}

func (w *runtimeWire) Read(ctx context.Context, msg *cdproto.Message) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-w.closed:
		return io.EOF
	case next := <-w.in:
		*msg = next
		return nil
	}
}

func (w *runtimeWire) Write(ctx context.Context, msg *cdproto.Message) error {
	w.mu.Lock()
	copy := *msg
	copy.Params = slices.Clone(msg.Params)
	w.seen = append(w.seen, copy)
	w.mu.Unlock()
	response := cdproto.Message{ID: msg.ID, SessionID: msg.SessionID, Result: []byte(`{}`)}
	switch string(msg.Method) {
	case "Target.attachToTarget":
		response.Result = []byte(`{"sessionId":"main-session"}`)
	case "Runtime.evaluate":
		response.Result = []byte(`{"result":{"type":"object","className":"Window"}}`)
	case "Runtime.disable":
		if w.failOff {
			response.Error = &cdproto.Error{Code: -32601, Message: "关闭失败"}
		}
	case "Page.getFrameTree":
		response.Result = []byte(`{"frameTree":{"frame":{"id":"main","loaderId":"initial","url":"about:blank","securityOrigin":"null","mimeType":"text/html"}}}`)
	case "DOM.getDocument":
		response.Result = []byte(`{"root":{"nodeId":1,"backendNodeId":1,"nodeType":9,"nodeName":"#document","localName":"","nodeValue":""}}`)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-w.closed:
		return io.EOF
	case w.in <- response:
		return nil
	}
}

func (w *runtimeWire) Close() error { w.once.Do(func() { close(w.closed) }); return nil }

func (w *runtimeWire) commands() []cdproto.Message {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.seen)
}

func runtimeProtocol(t *testing.T, failOff bool) (context.Context, *runtimeWire) {
	t.Helper()
	lifetime, cancel := context.WithCancel(t.Context())
	wire := &runtimeWire{in: make(chan cdproto.Message, 128), closed: make(chan struct{}), failOff: failOff}
	browser, err := chromedp.NewBrowserTransport(lifetime, wire)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancelTarget := chromedp.NewContext(lifetime, chromedp.WithTargetID("main"), func(c *chromedp.Context) {
		c.Browser = browser
	})
	t.Cleanup(func() { cancelTarget(); cancel(); wire.Close() })
	return ctx, wire
}

func TestTargetInitializationStopsRuntimeEvents(t *testing.T) {
	ctx, wire := runtimeProtocol(t, false)
	if err := initializeTarget(ctx); err != nil {
		t.Fatal(err)
	}
	commands := wire.commands()
	enables, evaluates, disables, disabledAt := 0, 0, 0, -1
	for i, cmd := range commands {
		switch string(cmd.Method) {
		case "Runtime.enable":
			enables++
		case "Runtime.evaluate":
			evaluates++
			var params struct{ Expression string }
			if json.Unmarshal(cmd.Params, &params) != nil || params.Expression != "self" {
				t.Fatal("驱动初始化不应执行其他表达式")
			}
		case "Runtime.disable":
			disables++
			disabledAt = i
			if cmd.SessionID != "main-session" {
				t.Fatal("关闭命令没有发给实际页面会话")
			}
		}
	}
	if enables != 1 || evaluates != 1 || disables != 1 || disabledAt != len(commands)-1 {
		t.Fatalf("初始化后仍未立即关闭运行时域：enable=%d evaluate=%d disable=%d", enables, evaluates, disables)
	}
	// 后续使用的协议域不要求运行时事件，也不能偷偷重新启用。
	if _, err := chromedp.Call(ctx, dom.GetDocument, dom.GetDocumentParams{}); err != nil {
		t.Fatal(err)
	}
	if _, err := chromedp.Call(ctx, input.InsertText, input.InsertTextParams{Text: "本地"}); err != nil {
		t.Fatal(err)
	}
	if _, err := chromedp.Call(ctx, network.GetCookies, network.GetCookiesParams{}); err != nil {
		t.Fatal(err)
	}
	if _, err := chromedp.Call(ctx, accessibility.GetPartialAXTree, accessibility.GetPartialAXTreeParams{NodeID: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := chromedp.Call(ctx, page.GetFrameTree, cdp.Empty{}); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range wire.commands()[disabledAt+1:] {
		if strings.HasPrefix(string(cmd.Method), "Runtime.") {
			t.Fatalf("普通页面操作重新使用了运行时域：%s", cmd.Method)
		}
	}
}

func TestTargetInitializationReturnsRuntimeDisableFailure(t *testing.T) {
	ctx, wire := runtimeProtocol(t, true)
	err := initializeTarget(ctx)
	var protocolErr *cdproto.Error
	if !errors.As(err, &protocolErr) || protocolErr.Code != -32601 {
		t.Fatalf("关闭失败应阻止启动：%v", err)
	}
	commands := wire.commands()
	if string(commands[len(commands)-1].Method) != "Runtime.disable" {
		t.Fatal("关闭失败后仍继续操作页面")
	}
}

func TestAutoAttachedChildrenDoNotEnableRuntime(t *testing.T) {
	ctx, wire := runtimeProtocol(t, false)
	if err := initializeTarget(ctx); err != nil {
		t.Fatal(err)
	}
	tab := chromedp.FromContext(ctx).Target
	events, stop := tab.Subscribe(target.AttachedToTarget.Method)
	defer stop()
	for _, kind := range []string{"iframe", "worker", "service_worker"} {
		params, err := json.Marshal(map[string]any{
			"sessionId": kind + "-session", "waitingForDebugger": false,
			"targetInfo": map[string]any{"targetId": kind, "type": kind, "url": "http://127.0.0.1/" + kind, "attached": true},
		})
		if err != nil {
			t.Fatal(err)
		}
		wire.in <- cdproto.Message{Method: cdproto.MethodType(target.AttachedToTarget.Method), SessionID: tab.SessionID, Params: params}
		select {
		case <-events:
		case <-time.After(time.Second):
			t.Fatal("没有收到子目标附加事件")
		}
	}
	if _, err := chromedp.Call(ctx, page.GetFrameTree, cdp.Empty{}); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range wire.commands() {
		if string(cmd.Method) == "Runtime.enable" && cmd.SessionID != tab.SessionID {
			t.Fatalf("自动附加子目标被启用了运行时域：%s", cmd.SessionID)
		}
	}
}

const runtimeDetector = `async function detectRuntime() {
  let detected = false;
  const error = new Error('local runtime check');
  Object.defineProperty(error, 'stack', {get() { detected = true; return ''; }});
  console.debug(error);
  await new Promise(resolve => setTimeout(resolve, 60));
  return detected;
}`

const runtimeConsoleMarker = "kakatta-local-runtime-probe"

type runtimeConsoleProbe struct {
	mu     sync.Mutex
	phases map[string]int
}

func (p *runtimeConsoleProbe) event(value any) {
	event, ok := value.(*runtime.EventConsoleAPICalled)
	if !ok || event.Type != runtime.ConsoleAPICalledTypeDebug || len(event.Args) < 2 || event.Args[0] == nil || event.Args[1] == nil {
		return
	}
	var marker, phase string
	if json.Unmarshal(event.Args[0].Value, &marker) != nil || marker != runtimeConsoleMarker ||
		json.Unmarshal(event.Args[1].Value, &phase) != nil || phase == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.phases == nil {
		p.phases = map[string]int{}
	}
	p.phases[phase]++
}

func (p *runtimeConsoleProbe) count(phase string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.phases[phase]
}

func TestRuntimeConsoleProbeKeepsStagesSeparate(t *testing.T) {
	ctx, wire := runtimeProtocol(t, false)
	if err := initializeTarget(ctx); err != nil {
		t.Fatal(err)
	}
	probe := &runtimeConsoleProbe{}
	if err := chromedp.Listen(ctx, probe.event); err != nil {
		t.Fatal(err)
	}
	for _, input := range []struct{ marker, phase string }{
		{runtimeConsoleMarker, "disabled-initial"},
		{runtimeConsoleMarker, "enabled-control"},
		{runtimeConsoleMarker, "disabled-initial"},
		{"unrelated-console", "disabled-again"},
	} {
		params, err := json.Marshal(map[string]any{
			"type": "debug", "executionContextId": 1, "timestamp": 1,
			"args": []map[string]any{{"type": "string", "value": input.marker}, {"type": "string", "value": input.phase}},
		})
		if err != nil {
			t.Fatal(err)
		}
		wire.in <- cdproto.Message{Method: cdproto.MethodType(runtime.ConsoleAPICalled.Method), SessionID: "main-session", Params: params}
	}
	if _, err := chromedp.Call(ctx, page.GetFrameTree, cdp.Empty{}); err != nil {
		t.Fatal(err)
	}
	if probe.count("disabled-initial") != 2 || probe.count("enabled-control") != 1 || probe.count("disabled-again") != 0 {
		t.Fatal("旧阶段重放或无关控制台消息混进了当前阶段")
	}
}

func TestRuntimeDisabledOnLocalPageAndChildren(t *testing.T) {
	requireBrowser(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	mux := http.NewServeMux()
	mux.HandleFunc("/worker.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		io.WriteString(w, runtimeDetector+`detectRuntime().then(detected => postMessage({kind:'worker', detected}));`)
	})
	mux.HandleFunc("/frame", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, `<script>`+runtimeDetector+`detectRuntime().then(detected => parent.postMessage({kind:'iframe', detected}, '*'));</script>`)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, `<!doctype html><meta charset="utf-8"><input id="results"><input id="phase-result">
<button id="disabled-initial">关闭前段</button><button id="enabled-control">开启对照</button><button id="disabled-again">再次关闭</button>
<script>`+runtimeDetector+`
const results = {};
function record(value) {
  if (!['main','iframe','worker'].includes(value.kind)) return;
  results[value.kind] = value.detected;
  if (Object.keys(results).length === 3) document.getElementById('results').value = JSON.stringify(results);
}
addEventListener('message', e => record(e.data));
detectRuntime().then(detected => record({kind:'main', detected}));
new Worker('/worker.js').onmessage = e => record(e.data);
for (const phase of ['disabled-initial','enabled-control','disabled-again']) {
  document.getElementById(phase).onclick = async () => {
    document.getElementById('phase-result').value = '';
    let emitted = 0;
    for (; emitted < 12; emitted++) {
      console.debug('`+runtimeConsoleMarker+`', phase, emitted);
      await new Promise(resolve => setTimeout(resolve, 10));
    }
    const getter = await detectRuntime();
    document.getElementById('phase-result').value = JSON.stringify({phase, emitted, getter});
  };
}
const frame = document.createElement('iframe');
frame.src = location.origin.replace('127.0.0.1', 'localhost') + '/frame';
document.body.append(frame);
</script>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	s, err := Start(ctx, Options{AccountID: 584, ProfileDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	probe := &runtimeConsoleProbe{}
	if err := chromedp.Listen(s.Context(), probe.event); err != nil {
		t.Fatal(err)
	}
	if err := s.Navigate(srv.URL, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	human := NewHuman(s, pacer.New(nil), pacer.Between(0, 0))
	read := func(selector, phase string) string {
		t.Helper()
		node, err := human.FirstVisible(ctx, selector, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			value, err := human.Value(node)
			if err != nil {
				t.Fatal(err)
			}
			if value != "" {
				if phase == "" {
					return value
				}
				var completed struct{ Phase string }
				if json.Unmarshal([]byte(value), &completed) == nil && completed.Phase == phase {
					return value
				}
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(20 * time.Millisecond):
			}
		}
		t.Fatal("本地检测页没有完成")
		return ""
	}
	var results map[string]bool
	if value := read("#results", ""); json.Unmarshal([]byte(value), &results) != nil || len(results) != 3 {
		t.Fatalf("本地检测结果不完整：%s", value)
	}
	// 新版 V8 跳过用户 getter，这些值只作兼容诊断，不能证明网页不可检测。
	t.Logf("原 getter 诊断：main=%v iframe=%v worker=%v", results["main"], results["iframe"], results["worker"])
	defer chromedp.Call(s.Context(), runtime.Disable, cdp.Empty{})
	for i, phase := range []string{"disabled-initial", "enabled-control", "disabled-again"} {
		if i == 1 {
			if _, err := chromedp.Call(s.Context(), runtime.Enable, cdp.Empty{}); err != nil {
				t.Fatal(err)
			}
		}
		if i == 2 {
			if _, err := chromedp.Call(s.Context(), runtime.Disable, cdp.Empty{}); err != nil {
				t.Fatal(err)
			}
		}
		button, err := human.FirstVisible(ctx, "#"+phase, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if err := human.Click(ctx, button); err != nil {
			t.Fatal(err)
		}
		var completed struct {
			Phase   string
			Emitted int
			Getter  bool
		}
		if raw := read("#phase-result", phase); json.Unmarshal([]byte(raw), &completed) != nil || completed.Emitted != 12 {
			t.Fatalf("页面没有完成本阶段控制台批次：%s", raw)
		}
		if _, err := chromedp.Call(s.Context(), page.GetFrameTree, cdp.Empty{}); err != nil {
			t.Fatal(err)
		}
		// Enable 会重放旧日志，每阶段只检查它自己唯一的标识。
		count := probe.count(phase)
		if i == 1 && count == 0 || i != 1 && count != 0 {
			t.Fatalf("运行时域事件开关不符：phase=%s 已发=%d 已收=%d", phase, completed.Emitted, count)
		}
		t.Logf("phase=%s 已发=%d 已收=%d getter=%v（仅诊断）", phase, completed.Emitted, count, completed.Getter)
	}
}
