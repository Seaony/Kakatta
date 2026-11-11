package browser

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/Seaony/Kakatta/internal/pacer"
	"github.com/chromedp/cdproto"
	"github.com/chromedp/chromedp"
)

type diagnosticWire struct {
	*runtimeWire
	failShot bool
	failHTML bool
	document string
}

func (w *diagnosticWire) Write(ctx context.Context, msg *cdproto.Message) error {
	var result any
	switch string(msg.Method) {
	case "DOM.getOuterHTML":
		document := w.document
		if document == "" {
			document = `<html><head><title>本地 &amp; 商品</title></head><body>详情原文</body></html>`
		}
		result = map[string]any{"outerHTML": document}
	case "Browser.getVersion":
		result = map[string]any{"product": "Chrome/157.0.8103.50"}
	case "Page.captureScreenshot":
		if !w.failShot {
			return w.runtimeWire.Write(ctx, msg)
		}
	default:
		return w.runtimeWire.Write(ctx, msg)
	}
	w.mu.Lock()
	copy := *msg
	copy.Params = slices.Clone(msg.Params)
	w.seen = append(w.seen, copy)
	w.mu.Unlock()
	response := cdproto.Message{ID: msg.ID, SessionID: msg.SessionID}
	if string(msg.Method) == "Page.captureScreenshot" || string(msg.Method) == "DOM.getOuterHTML" && w.failHTML {
		response.Error = &cdproto.Error{Code: -32000, Message: "本地截图故障"}
	} else {
		response.Result, _ = json.Marshal(result)
	}
	select {
	case w.in <- response:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-w.closed:
		return context.Canceled
	}
}

func diagnosticProtocol(t *testing.T, failShot bool) (*Session, *diagnosticWire) {
	t.Helper()
	ctx, stop := context.WithCancel(t.Context())
	wire := &diagnosticWire{runtimeWire: &runtimeWire{in: make(chan cdproto.Message, 128), closed: make(chan struct{})}, failShot: failShot}
	process, err := chromedp.NewBrowserTransport(ctx, wire)
	if err != nil {
		t.Fatal(err)
	}
	tab, cancel := chromedp.NewContext(ctx, chromedp.WithTargetID("main"), func(c *chromedp.Context) { c.Browser = process })
	t.Cleanup(func() { cancel(); stop(); wire.Close() })
	if err := initializeTarget(tab); err != nil {
		t.Fatal(err)
	}
	return &Session{ctx: tab}, wire
}

func TestDiagnosticCaptureReadsCurrentDocumentWithoutScript(t *testing.T) {
	for _, brokenShot := range []bool{false, true} {
		t.Run(map[bool]string{false: "完整", true: "截图故障仍留HTML"}[brokenShot], func(t *testing.T) {
			session, wire := diagnosticProtocol(t, brokenShot)
			before := len(wire.commands())
			page, err := session.DiagnosticPage(t.Context())
			if (err != nil) != brokenShot || page.URL != "about:blank" || page.Title != "本地 & 商品" || page.HTML == "" || (len(page.Screenshot) == 0) != brokenShot || page.LoginPage {
				t.Fatalf("当前页面读取或部分证据保留失败：%+v %v", page, err)
			}
			if brokenShot && page.Errors["screenshot"] == "" {
				t.Fatal("截图失败原因丢失")
			}
			commands := wire.commands()[before:]
			want := []string{"Page.captureScreenshot", "Page.getFrameTree", "DOM.getDocument", "DOM.getOuterHTML"}
			if len(commands) != len(want) {
				t.Fatal("诊断执行了额外协议操作", commands)
			}
			for index, command := range commands {
				if string(command.Method) != want[index] || command.SessionID != "main-session" {
					t.Fatal("诊断未只读取当前主页面", commands)
				}
			}
			version, err := session.Version(t.Context())
			if err != nil || version != "Chrome/157.0.8103.50" {
				t.Fatal("未读取实际浏览器版本", version, err)
			}
		})
	}
}

func TestDiagnosticCaptureMarksLoginAndUnknownPages(t *testing.T) {
	for _, test := range []struct {
		name, document string
		failHTML       bool
	}{
		{name: "登录框", document: `<html><iframe id="alibaba-login-box"></iframe></html>`},
		{name: "扫码图", document: `<html><img class="qrcode" src="data:image/png;base64,local"></html>`},
		{name: "检测失败", failHTML: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			session, wire := diagnosticProtocol(t, false)
			wire.document, wire.failHTML = test.document, test.failHTML
			page, err := session.DiagnosticPage(t.Context())
			if !page.LoginPage || len(page.Screenshot) == 0 || (err != nil) != test.failHTML {
				t.Fatal("本机截图未保留或登录风险未标记", page, err)
			}
		})
	}
}

func TestDiagnosticCaptureSharesDOMPermitAndRespectsCancellation(t *testing.T) {
	session, wire := diagnosticProtocol(t, false)
	human := NewHuman(session, pacer.New(nil), pacer.Between(0, 0))
	second := NewHuman(session, pacer.New(nil), pacer.Between(0, 0))
	if human.domRead != second.domRead {
		t.Fatal("同页的读取没有共用许可")
	}
	unlock, err := human.lockDOM(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	before := len(wire.commands())
	ctx, cancel := context.WithCancel(t.Context())
	finished := make(chan error, 1)
	go func() {
		_, err := session.DiagnosticPage(ctx)
		finished <- err
	}()
	cancel()
	if err := <-finished; !errors.Is(err, context.Canceled) || len(wire.commands()) != before {
		t.Fatal("取消后诊断仍重取了文档", err)
	}
	if _, err := session.Version(ctx); !errors.Is(err, context.Canceled) || len(wire.commands()) != before {
		t.Fatal("取消后仍读取版本", err)
	}
}
