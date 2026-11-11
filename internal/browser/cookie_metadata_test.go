package browser

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto"
	"github.com/chromedp/chromedp"
)

type cookieMetadataWire struct {
	*runtimeWire
	raw     json.RawMessage
	fail    bool
	hold    bool
	entered chan struct{}
}

func (w *cookieMetadataWire) Write(ctx context.Context, msg *cdproto.Message) error {
	if string(msg.Method) != "Storage.getCookies" {
		return w.runtimeWire.Write(ctx, msg)
	}
	w.mu.Lock()
	copy := *msg
	copy.Params = append(copy.Params[:0:0], msg.Params...)
	w.seen = append(w.seen, copy)
	w.mu.Unlock()
	close(w.entered)
	if w.hold {
		return nil
	}
	response := cdproto.Message{ID: msg.ID, SessionID: msg.SessionID, Result: w.raw}
	if w.fail {
		response.Error = &cdproto.Error{Code: -32000, Message: "本地元数据读取失败"}
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

func cookieMetadataProtocol(t *testing.T) (*Session, *cookieMetadataWire, context.CancelCauseFunc) {
	t.Helper()
	lifetime, cancelTransport := context.WithCancel(t.Context())
	wire := &cookieMetadataWire{runtimeWire: &runtimeWire{in: make(chan cdproto.Message, 16), closed: make(chan struct{})}, raw: json.RawMessage(`{"cookies":[]}`), entered: make(chan struct{})}
	process, err := chromedp.NewBrowserTransport(lifetime, wire)
	if err != nil {
		t.Fatal(err)
	}
	tab, cancelTab := chromedp.NewContext(lifetime, func(c *chromedp.Context) { c.Browser = process })
	ctx, cancelSession := context.WithCancelCause(tab)
	t.Cleanup(func() { cancelSession(nil); cancelTab(); cancelTransport(); wire.Close() })
	return &Session{ctx: ctx}, wire, cancelSession
}

func TestCookieMetadataOnlyReturnsFourFieldsAndReadsWholeProfile(t *testing.T) {
	session, wire, _ := cookieMetadataProtocol(t)
	wire.raw = json.RawMessage(`{"cookies":[{"name":"unb","domain":".local.test","session":true,"expires":-1,"value":"credential-sentinel","path":"/","httpOnly":true},{"name":"persistent","domain":"other.local.test","session":false,"expires":1893456000,"value":"another-credential"}]}`)
	got, err := session.CookieMetadata(t.Context())
	want := []CookieMetadata{{Name: "unb", Domain: ".local.test", Session: true, Expires: -1}, {Name: "persistent", Domain: "other.local.test", Expires: 1893456000}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("元数据字段丢失：%+v，%v", got, err)
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var fields []map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	for _, item := range fields {
		if len(item) != 4 || item["name"] == nil || item["domain"] == nil || item["session"] == nil || item["expires"] == nil {
			t.Fatal("元数据必须严格只含四个字段", string(data))
		}
	}
	if strings.Contains(string(data), "credential") {
		t.Fatal("Cookie 值进入了元数据")
	}
	commands := wire.commands()
	if len(commands) != 1 || string(commands[0].Method) != "Storage.getCookies" || commands[0].SessionID != "" || string(commands[0].Params) != "{}" {
		t.Fatal("必须只在浏览器端点读取整个配置，不能导航或只查当前页面", commands)
	}
}

func TestCookieMetadataSeparatesEmptyProfileAndProtocolFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		session, wire, _ := cookieMetadataProtocol(t)
		wire.fail = fail
		metadata, err := session.CookieMetadata(t.Context())
		if fail {
			var protocol *cdproto.Error
			if metadata != nil || !errors.As(err, &protocol) {
				t.Fatal("读取失败不能当作空配置", metadata, err)
			}
		} else if err != nil || metadata == nil || len(metadata) != 0 {
			t.Fatal("成功读取空配置必须和未知分开", metadata, err)
		}
	}
}

func TestCookieMetadataRespectsCallerAndBrowserCancellation(t *testing.T) {
	for _, before := range []bool{false, true} {
		for _, source := range []string{"caller", "browser"} {
			t.Run(source+map[bool]string{false: "/pending", true: "/before"}[before], func(t *testing.T) {
				session, wire, stopSession := cookieMetadataProtocol(t)
				wire.hold = true
				ctx, cancelCaller := context.WithCancelCause(t.Context())
				defer cancelCaller(nil)
				cause := errors.New("停止元数据读取")
				cancel := func() { cancelCaller(cause) }
				if source == "browser" {
					cancel = func() { stopSession(cause) }
				}
				if before {
					cancel()
				}
				finished := make(chan error, 1)
				go func() { _, err := session.CookieMetadata(ctx); finished <- err }()
				if !before {
					select {
					case <-wire.entered:
					case <-time.After(time.Second):
						t.Fatal("读取没有进入协议")
					}
					cancel()
				}
				select {
				case err := <-finished:
					if !errors.Is(err, context.Canceled) || !errors.Is(err, cause) {
						t.Fatal("取消分类或原因丢失", err)
					}
				case <-time.After(time.Second):
					t.Fatal("取消没有及时结束读取")
				}
				if before && len(wire.commands()) != 0 {
					t.Fatal("预先取消后仍发出协议命令")
				}
			})
		}
	}
}
