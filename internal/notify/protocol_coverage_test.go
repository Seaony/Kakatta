package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"
	"unicode/utf8"
)

type protocolRoundTripper func(*http.Request) (*http.Response, error)

func (f protocolRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestChannelProtocolTimeouts(t *testing.T) {
	for _, kind := range []Type{Bark, Telegram, Ntfy, Gotify, WeCom, DingTalk, Feishu, PushPlus, ServerChan, Webhook, Email} {
		t.Run(string(kind), func(t *testing.T) {
			for _, shorterCaller := range []bool{false, true} {
				name := "default"
				if shorterCaller {
					name = "shorter_caller"
				}
				t.Run(name, func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						const address = "http://127.0.0.1:1"
						var channel ChannelConfig
						switch kind {
						case WeCom, DingTalk, Feishu:
							channel = robotChannelConfig(t, kind, address, "")
						case PushPlus, ServerChan, Webhook:
							_, channel = customSenderChannel(t, kind, address)
						case Email:
							channel = ChannelConfig{Type: Email, Config: json.RawMessage(`{"host":"localhost","port":25,"encryption":"none","from":"from@example.test","to":"to@example.test"}`)}
						default:
							channel = mobileChannel(kind, address)
						}
						wantTimeout := 10 * time.Second
						if kind == Webhook {
							wantTimeout = 15 * time.Second
						} else if kind == Email {
							wantTimeout = 30 * time.Second
						}
						ctx := context.Background()
						if shorterCaller {
							var cancel context.CancelFunc
							wantTimeout = 3 * time.Second
							ctx, cancel = context.WithTimeout(ctx, wantTimeout)
							defer cancel()
						}
						wantDeadline := time.Now().Add(wantTimeout)
						calls := 0
						var gotDeadline time.Time
						observe := func(ctx context.Context) error {
							calls++
							gotDeadline, _ = ctx.Deadline()
							return errors.New("本地截止时间检查")
						}
						sender := New(Dependencies{
							HTTP: &http.Client{Transport: protocolRoundTripper(func(request *http.Request) (*http.Response, error) {
								return nil, observe(request.Context())
							})},
							DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return nil, observe(ctx) },
							Endpoints:   Endpoints{PushPlus: address, ServerChanTurbo: address},
						})
						result := sender.Send(ctx, channel, Message{Title: "测试", Body: "正文"})
						if calls != 1 || result.ErrorCode != "transport" || !gotDeadline.Equal(wantDeadline) {
							t.Fatalf("调用=%d，结果=%+v，截止=%v，期望=%v", calls, result, gotDeadline, wantDeadline)
						}
					})
				})
			}
		})
	}
}

func TestWebhookDefaultProtocolFields(t *testing.T) {
	want := map[string]string{
		"title": "[相机] 划算 84 ¥1,300", "body": "价格低 & 配件全\n请核实<屏幕>",
		"item_title": "小相机<&\"😺", "item_id": "100", "price": "¥1,300", "score": "84", "grade": "划算",
		"config": "相机 / 256G", "fair_price": "¥1,500", "reason": "价格低 & 配件全",
		"link": "https://example.invalid/item?a=1&quote=\"", "desktop_link": "https://example.invalid/desktop",
		"mobile_link": "https://example.invalid/mobile", "kakatta_url": "https://example.invalid/kakatta",
		"image": "https://example.invalid/image?x=1&quote=\"", "task": "相机", "kind": "recommended", "urgent": "true",
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var got map[string]string
		if err := json.NewDecoder(request.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		if request.Method != http.MethodPost || request.Header.Get("Content-Type") != "application/json; charset=utf-8" || !reflect.DeepEqual(got, want) {
			t.Errorf("默认请求=%s %s %#v，期望字段=%#v", request.Method, request.Header.Get("Content-Type"), got, want)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	sender, channel := customSenderChannel(t, Webhook, server.URL)
	channel.Config = json.RawMessage(`{"content_type":"FORM"}`)
	message := customMessage()
	message.Urgent = true
	if result := sender.Send(t.Context(), channel, message); !result.OK {
		t.Fatal(result)
	}
}

func TestEmailHTMLAndUTF8Protocol(t *testing.T) {
	channel, _, done := emailServer(t, emailServerOptions{})
	message := customMessage()
	message.Title = `通知 <标题> & "引号" 😺`
	if result := New(Dependencies{}).Send(t.Context(), channel, message); !result.OK {
		t.Fatal(result)
	}
	transcript := emailWaitTranscript(t, done)
	parsed, err := mail.ReadMessage(bytes.NewReader(transcript.Message))
	if err != nil {
		t.Fatal(err)
	}
	kind, parameters, err := mime.ParseMediaType(parsed.Header.Get("Content-Type"))
	if err != nil || kind != "multipart/alternative" {
		t.Fatalf("邮件类型=%q，错误=%v", kind, err)
	}
	want := []struct{ kind, body string }{
		{"text/plain", "小相机<&\"😺\n价格低 & 配件全\n请核实<屏幕>\nhttps://example.invalid/item?a=1&quote=\""},
		{"text/html", `<b>通知 &lt;标题&gt; &amp; &#34;引号&#34; 😺</b><br>小相机&lt;&amp;&#34;😺<br>价格低 &amp; 配件全<br>请核实&lt;屏幕&gt;<br><img src="https://example.invalid/image?x=1&amp;quote=&#34;" width="300"><br><a href="https://example.invalid/item?a=1&amp;quote=&#34;">在闲鱼打开</a>`},
	}
	reader := multipart.NewReader(parsed.Body, parameters["boundary"])
	for _, expected := range want {
		part, err := reader.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		kind, parameters, err := mime.ParseMediaType(part.Header.Get("Content-Type"))
		if err != nil || kind != expected.kind || !strings.EqualFold(parameters["charset"], "UTF-8") {
			t.Errorf("正文类型=%q，参数=%v，错误=%v", kind, parameters, err)
		}
		body, err := io.ReadAll(part)
		if err != nil || !utf8.Valid(body) || string(body) != expected.body {
			t.Errorf("%s正文=%q，期望=%q，错误=%v", expected.kind, body, expected.body, err)
		}
	}
	if _, err := reader.NextPart(); err != io.EOF {
		t.Fatalf("多余的邮件正文，错误=%v", err)
	}
}
