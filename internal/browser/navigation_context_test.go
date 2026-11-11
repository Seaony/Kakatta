package browser

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

type navigationWire struct {
	*runtimeWire
	mu          sync.Mutex
	entered     chan struct{}
	enteredOnce sync.Once
	navigated   bool
	noReply     bool
	sameDoc     bool
	errorText   string
	download    bool
	frameError  bool
	holdURL     bool
}

func (w *navigationWire) Write(ctx context.Context, msg *cdproto.Message) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	method := string(msg.Method)
	if method != "Page.navigate" && !(method == "Page.getFrameTree" && (w.navigated || w.holdURL)) {
		return w.runtimeWire.Write(ctx, msg)
	}
	w.runtimeWire.mu.Lock()
	copy := *msg
	copy.Params = append(copy.Params[:0:0], msg.Params...)
	w.seen = append(w.seen, copy)
	w.runtimeWire.mu.Unlock()
	response := cdproto.Message{ID: msg.ID, SessionID: msg.SessionID}
	if method == "Page.getFrameTree" && w.holdURL {
		w.enteredOnce.Do(func() { close(w.entered) })
		return nil
	}
	if method == "Page.navigate" {
		w.navigated = true
		w.enteredOnce.Do(func() { close(w.entered) })
		if w.noReply {
			return nil
		}
		loader := "new-loader"
		if w.sameDoc {
			loader = ""
		}
		response.Result, _ = json.Marshal(map[string]any{"frameId": "main", "loaderId": loader, "errorText": w.errorText, "isDownload": w.download})
	} else if w.frameError {
		response.Error = &cdproto.Error{Code: -32000, Message: "读取主框架失败"}
	} else {
		response.Result = []byte(`{"frameTree":{"frame":{"id":"main","loaderId":"new-loader","url":"http://127.0.0.1/final","urlFragment":"#ready","securityOrigin":"http://127.0.0.1","mimeType":"text/html"}}}`)
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

func navigationProtocol(t *testing.T) (*Session, *navigationWire, context.CancelCauseFunc) {
	t.Helper()
	lifetime, cancelTransport := context.WithCancel(t.Context())
	wire := &navigationWire{
		runtimeWire: &runtimeWire{in: make(chan cdproto.Message, 128), closed: make(chan struct{})},
		entered:     make(chan struct{}),
	}
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
	return &Session{ctx: sessionCtx}, wire, cancelSession
}

func (w *navigationWire) lifecycle(frame, loader, name string) {
	params, _ := json.Marshal(map[string]string{"frameId": frame, "loaderId": loader, "name": name})
	w.in <- cdproto.Message{Method: cdproto.MethodType(page.LifecycleEvent.Method), SessionID: "main-session", Params: params}
}

type navigationResult struct {
	url string
	err error
}

func runNavigation(s *Session, ctx context.Context, timeout time.Duration) <-chan navigationResult {
	finished := make(chan navigationResult, 1)
	go func() {
		url, err := s.NavigateContext(ctx, "http://127.0.0.1/request", timeout)
		finished <- navigationResult{url: url, err: err}
	}()
	return finished
}

func awaitNavigation(t *testing.T, finished <-chan navigationResult) navigationResult {
	t.Helper()
	select {
	case result := <-finished:
		return result
	case <-time.After(time.Second):
		t.Fatal("导航取消后仍在等待，操作协程未回收")
		return navigationResult{}
	}
}

func TestNavigateContextCancelsWithoutClosingSession(t *testing.T) {
	for _, beforeResponse := range []bool{true, false} {
		t.Run(map[bool]string{true: "命令尚未返回", false: "等待加载完成"}[beforeResponse], func(t *testing.T) {
			s, wire, _ := navigationProtocol(t)
			wire.noReply = beforeResponse
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			finished := runNavigation(s, ctx, 20*time.Second)
			<-wire.entered
			cause := errors.New("需要人工验证，停止本次动作")
			cancel(cause)
			result := awaitNavigation(t, finished)
			if result.url != "" || !errors.Is(result.err, cause) || !errors.Is(result.err, context.Canceled) {
				t.Fatalf("取消原因丢失：%q %v", result.url, result.err)
			}
			if s.Context().Err() != nil {
				t.Fatal("取消导航不应关闭会话")
			}
			if _, err := s.URL(); err != nil {
				t.Fatalf("取消后不能继续读取同一浏览器：%v", err)
			}
		})
	}
}

func TestNavigateContextRespectsSessionCancellationAndTimeout(t *testing.T) {
	t.Run("会话结束", func(t *testing.T) {
		s, wire, cancel := navigationProtocol(t)
		finished := runNavigation(s, t.Context(), 20*time.Second)
		<-wire.entered
		cause := errors.New("浏览器会话已结束")
		cancel(cause)
		result := awaitNavigation(t, finished)
		if !errors.Is(result.err, cause) || !errors.Is(result.err, context.Canceled) {
			t.Fatalf("会话结束原因丢失：%v", result.err)
		}
	})
	t.Run("单次超时", func(t *testing.T) {
		s, _, _ := navigationProtocol(t)
		result := awaitNavigation(t, runNavigation(s, t.Context(), 20*time.Millisecond))
		if !errors.Is(result.err, context.DeadlineExceeded) || s.Context().Err() != nil {
			t.Fatalf("单次超时没有隔离：%v", result.err)
		}
	})
	t.Run("调用前已取消", func(t *testing.T) {
		s, wire, _ := navigationProtocol(t)
		ctx, cancel := context.WithCancelCause(t.Context())
		cause := errors.New("本次运行已经停止")
		cancel(cause)
		result := awaitNavigation(t, runNavigation(s, ctx, time.Second))
		if !errors.Is(result.err, cause) {
			t.Fatal("已取消的调用丢失原因", result.err)
		}
		for _, cmd := range wire.commands() {
			if string(cmd.Method) == "Page.navigate" {
				t.Fatal("已经取消仍发出导航")
			}
		}
	})
}

func TestNavigateContextReportsStartOnlyForAttemptedNavigation(t *testing.T) {
	for _, cancelBefore := range []bool{true, false} {
		t.Run(map[bool]string{true: "导航前取消", false: "导航后取消"}[cancelBefore], func(t *testing.T) {
			s, wire, _ := navigationProtocol(t)
			wire.noReply = true
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if cancelBefore {
				cancel()
			}
			starts := 0
			finished := make(chan navigationResult, 1)
			go func() {
				url, err := s.NavigateContextWithStart(ctx, "http://127.0.0.1/request", time.Second, func() { starts++ })
				finished <- navigationResult{url: url, err: err}
			}()
			want := 0
			if !cancelBefore {
				select {
				case <-wire.entered:
				case <-time.After(time.Second):
					t.Fatal("没有收到导航命令")
				}
				cancel()
				want = 1
			}
			result := awaitNavigation(t, finished)
			if !errors.Is(result.err, context.Canceled) || starts != want {
				t.Fatalf("导航开始回调次数不对：得到 %d，预期 %d，错误 %v", starts, want, result.err)
			}
		})
	}
}

func TestNavigateContextMatchesMainFrameAndReturnsActualURL(t *testing.T) {
	s, wire, _ := navigationProtocol(t)
	finished := runNavigation(s, t.Context(), time.Second)
	<-wire.entered
	wire.lifecycle("child", "new-loader", "DOMContentLoaded")
	wire.lifecycle("main", "old-loader", "DOMContentLoaded")
	wire.lifecycle("main", "new-loader", "load")
	select {
	case result := <-finished:
		t.Fatalf("接受了错误的完成事件：%+v", result)
	case <-time.After(20 * time.Millisecond):
	}
	wire.lifecycle("main", "new-loader", "DOMContentLoaded")
	result := awaitNavigation(t, finished)
	if result.err != nil || result.url != "http://127.0.0.1/final#ready" {
		t.Fatalf("没返回实际完成的主框架URL：%q %v", result.url, result.err)
	}
	commands := wire.commands()
	for _, cmd := range commands[len(commands)-2:] {
		if strings.HasPrefix(string(cmd.Method), "Runtime.") {
			t.Fatal("导航重新启用了运行时域")
		}
	}
}

func TestNavigateContextSameDocumentAndNavigationErrors(t *testing.T) {
	t.Run("同文档跳转", func(t *testing.T) {
		s, wire, _ := navigationProtocol(t)
		wire.sameDoc = true
		result := awaitNavigation(t, runNavigation(s, t.Context(), time.Second))
		if result.err != nil || result.url != "http://127.0.0.1/final#ready" {
			t.Fatalf("同文档跳转不应等待加载事件：%+v", result)
		}
	})
	for _, kind := range []string{"导航错误", "意外下载", "结果读取失败"} {
		t.Run(kind, func(t *testing.T) {
			s, wire, _ := navigationProtocol(t)
			wire.errorText = map[bool]string{true: "net::ERR_CONNECTION_REFUSED"}[kind == "导航错误"]
			wire.download = kind == "意外下载"
			wire.frameError = kind == "结果读取失败"
			finished := runNavigation(s, t.Context(), time.Second)
			<-wire.entered
			wire.lifecycle("main", "new-loader", "DOMContentLoaded")
			result := awaitNavigation(t, finished)
			if result.err == nil || result.url != "" {
				t.Fatalf("导航错误被吞：%+v", result)
			}
			if wire.frameError {
				var protocolErr *cdproto.Error
				if !errors.As(result.err, &protocolErr) || protocolErr.Code != -32000 {
					t.Fatal("协议错误原因丢失", result.err)
				}
			}
		})
	}
}

func TestLegacyNavigateDelegatesToContextNavigation(t *testing.T) {
	s, wire, _ := navigationProtocol(t)
	wire.sameDoc = true
	if err := s.Navigate("http://127.0.0.1/request#new", time.Second); err != nil {
		t.Fatal(err)
	}
	var requests int
	for _, cmd := range wire.commands() {
		if string(cmd.Method) == "Page.navigate" {
			requests++
			var params page.NavigateParams
			if err := json.Unmarshal(cmd.Params, &params); err != nil || params.URL != "http://127.0.0.1/request#new" {
				t.Fatalf("兼容入口改变了网址：%v", err)
			}
		}
	}
	if requests != 1 {
		t.Fatal("兼容入口重复导航")
	}
	if _, err := chromedp.Call(s.Context(), page.GetFrameTree, cdp.Empty{}); err != nil {
		t.Fatal(err)
	}
}

func TestURLContextCancelsWithoutNavigationOrClosingSession(t *testing.T) {
	for _, mode := range []string{"调用方取消", "调用方超时", "会话结束"} {
		t.Run(mode, func(t *testing.T) {
			s, wire, cancelSession := navigationProtocol(t)
			wire.holdURL = true
			caller, cancelCaller := context.WithCancelCause(t.Context())
			defer cancelCaller(nil)
			if mode == "调用方超时" {
				var cancel context.CancelFunc
				caller, cancel = context.WithTimeout(caller, 20*time.Millisecond)
				defer cancel()
			}
			finished := make(chan navigationResult, 1)
			go func() {
				url, err := s.URLContext(caller)
				finished <- navigationResult{url: url, err: err}
			}()
			<-wire.entered
			cause := errors.New("停止本次网址读取")
			if mode == "调用方取消" {
				cancelCaller(cause)
			} else if mode == "会话结束" {
				cancelSession(cause)
			}
			result := awaitNavigation(t, finished)
			if result.url != "" {
				t.Fatal("取消读取不应返回网址")
			}
			if mode == "调用方超时" {
				if !errors.Is(result.err, context.DeadlineExceeded) {
					t.Fatal("超时分类丢失", result.err)
				}
			} else if !errors.Is(result.err, context.Canceled) || !errors.Is(result.err, cause) {
				t.Fatal("取消分类或原原因丢失", result.err)
			}
			if mode != "会话结束" {
				if s.Context().Err() != nil {
					t.Fatal("读取取消关闭了浏览器")
				}
				wire.mu.Lock()
				wire.holdURL = false
				wire.mu.Unlock()
				if _, err := s.URLContext(t.Context()); err != nil {
					t.Fatal("同一会话不能再次读取", err)
				}
			}
			for _, cmd := range wire.commands() {
				if string(cmd.Method) == "Page.navigate" {
					t.Fatal("读取网址触发了导航")
				}
			}
		})
	}
}

func TestURLContextReturnsActualURLAndRejectsCanceledCaller(t *testing.T) {
	s, wire, _ := navigationProtocol(t)
	wire.navigated = true
	if url, err := s.URLContext(t.Context()); err != nil || url != "http://127.0.0.1/final#ready" {
		t.Fatalf("当前网址读取错误：%q %v", url, err)
	}
	if url, err := s.URL(); err != nil || url != "http://127.0.0.1/final#ready" {
		t.Fatalf("旧入口不兼容：%q %v", url, err)
	}
	before := len(wire.commands())
	ctx, cancel := context.WithCancelCause(t.Context())
	cause := errors.New("已停止")
	cancel(cause)
	if url, err := s.URLContext(ctx); url != "" || !errors.Is(err, cause) || !errors.Is(err, context.Canceled) {
		t.Fatalf("已取消读取丢失原因：%q %v", url, err)
	}
	if len(wire.commands()) != before {
		t.Fatal("已取消仍发出网址读取")
	}
}

func TestOperationErrorKeepsEventFailureAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	cause := errors.New("会话被终止")
	cancel(cause)
	eventErr := errors.New("页面事件连接已关闭")
	err := operationError(ctx, eventErr)
	if !errors.Is(err, eventErr) || !errors.Is(err, context.Canceled) || !errors.Is(err, cause) {
		t.Fatalf("原协议错误、标准取消分类或原cause丢失：%v", err)
	}
}
