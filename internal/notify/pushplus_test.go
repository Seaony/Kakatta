package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func customJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func customMessage() Message {
	return Message{Title: "[相机] 划算 84 ¥1,300", ItemTitle: "小相机<&\"😺", Body: "价格低 & 配件全\n请核实<屏幕>", Link: "https://example.invalid/item?a=1&quote=\"",
		ImageURL: "https://example.invalid/image?x=1&quote=\"", Kind: "recommended", Price: "¥1,300", Reason: "价格低 & 配件全", Score: "84", Grade: "划算", Config: "相机 / 256G",
		FairPrice: "¥1,500", ItemID: "100", Task: "相机", DesktopLink: "https://example.invalid/desktop", MobileLink: "https://example.invalid/mobile", KakattaURL: "https://example.invalid/kakatta"}
}

func customSenderChannel(t *testing.T, kind Type, address string) (*Sender, ChannelConfig) {
	t.Helper()
	config := ChannelConfig{Type: kind, Config: json.RawMessage(`{}`)}
	deps := Dependencies{}
	switch kind {
	case PushPlus:
		deps.Endpoints.PushPlus = address
		config.Secrets = json.RawMessage(`{"token":"secret-custom-token"}`)
	case ServerChan:
		deps.Endpoints.ServerChanTurbo = address
		config.Secrets = json.RawMessage(`{"send_key":"secret-custom-key"}`)
	case Webhook:
		config.Secrets = customJSON(t, map[string]any{"url": address})
	}
	return New(deps), config
}

func TestPushPlusProtocol(t *testing.T) {
	for _, mode := range []string{"normal", "urgent", "system"} {
		t.Run(mode, func(t *testing.T) {
			message := customMessage()
			message.Urgent = mode != "normal"
			if mode == "system" {
				message = Message{Title: "需要处理", Body: "配置失效\n请检查", Kind: "system", Urgent: true}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/" || r.Header.Get("Content-Type") != "application/json; charset=utf-8" {
					t.Errorf("请求协议: %s %s %v", r.Method, r.URL.Path, r.Header)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				if body["token"] != "secret-custom-token" || body["title"] != message.Title || body["template"] != "html" {
					t.Errorf("基础字段: %#v", body)
				}
				content, _ := body["content"].(string)
				if mode == "system" {
					if content != "配置失效<br>请检查" || strings.Contains(content, "<img") || strings.Contains(content, "<a") {
						t.Errorf("系统内容: %q", content)
					}
					if _, exists := body["topic"]; exists {
						t.Error("空topic不应发送")
					}
				} else {
					want := `<b>小相机&lt;&amp;&#34;😺</b><br>价格低 &amp; 配件全<br>请核实&lt;屏幕&gt;<br><img src="https://example.invalid/image?x=1&amp;quote=&#34;" style="max-width:100%"><br><a href="https://example.invalid/item?a=1&amp;quote=&#34;">在闲鱼打开</a>`
					if content != want || body["topic"] != "家人群" {
						t.Errorf("HTML或topic: %q %#v", content, body["topic"])
					}
				}
				io.WriteString(w, `{"code":200,"msg":"accepted"}`)
			}))
			defer server.Close()
			sender, channel := customSenderChannel(t, PushPlus, server.URL)
			if mode != "system" {
				channel.Config = json.RawMessage(`{"topic":"家人群"}`)
			}
			if result := sender.Send(context.Background(), channel, message); !result.OK || result.HTTPStatus != 200 || result.ProviderCode != "200" || result.ErrorCode != "" {
				t.Fatalf("发送: %+v", result)
			}
		})
	}
}

func TestCustomHTTPFailures(t *testing.T) {
	for _, kind := range []Type{PushPlus, ServerChan, Webhook} {
		t.Run(string(kind), func(t *testing.T) {
			for _, test := range []struct {
				name   string
				status int
				body   string
				want   string
			}{
				{"non_2xx", 401, `secret-custom-token private failure`, "http_status"},
				{"provider_reject", 200, `{"code":400,"msg":"secret-custom-token"}`, "provider_rejected"},
				{"truncated_json", 200, `{"code":`, "invalid_response"},
				{"invalid_json", 200, `<html>secret-custom-token</html>`, "invalid_response"},
				{"missing_code", 200, `{}`, "invalid_response"},
				{"null_code", 200, `{"code":null}`, "invalid_response"},
				{"string_code", 200, `{"code":"0"}`, "invalid_response"},
			} {
				t.Run(test.name, func(t *testing.T) {
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.WriteHeader(test.status)
						io.WriteString(w, test.body)
					}))
					defer server.Close()
					sender, channel := customSenderChannel(t, kind, server.URL)
					result := sender.Send(context.Background(), channel, customMessage())
					want := test.want
					if kind == Webhook && test.status == 200 {
						want = ""
					}
					if result.ErrorCode != want || result.OK != (want == "") || result.HTTPStatus != test.status {
						t.Errorf("结果: %+v, want %s", result, want)
					}
					if strings.Contains(fmt.Sprint(result), "secret-custom") || strings.Contains(fmt.Sprint(result), server.URL) {
						t.Error("结果泄露地址或凭据")
					}
				})
			}
		})
	}
}

func TestCustomHTTPCancelTimeoutDisconnect(t *testing.T) {
	for _, kind := range []Type{PushPlus, ServerChan, Webhook} {
		t.Run(string(kind), func(t *testing.T) {
			t.Run("pre_cancel", func(t *testing.T) {
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
				defer server.Close()
				sender, channel := customSenderChannel(t, kind, server.URL)
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				if result := sender.Send(ctx, channel, customMessage()); result.ErrorCode != "cancelled" || calls.Load() != 0 {
					t.Fatalf("结果 %+v calls=%d", result, calls.Load())
				}
			})
			for _, timeout := range []bool{false, true} {
				t.Run(fmt.Sprintf("inflight_timeout_%v", timeout), func(t *testing.T) {
					entered := make(chan struct{})
					release := make(chan struct{})
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						close(entered)
						<-release
					}))
					defer server.Close()
					defer close(release)
					sender, channel := customSenderChannel(t, kind, server.URL)
					ctx, cancel := context.WithCancel(context.Background())
					if timeout {
						ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
					}
					defer cancel()
					done := make(chan Result, 1)
					go func() { done <- sender.Send(ctx, channel, customMessage()) }()
					select {
					case <-entered:
					case result := <-done:
						t.Fatalf("请求未到本地服务: %+v", result)
					case <-time.After(time.Second):
						t.Fatal("请求未到本地服务")
					}
					if !timeout {
						cancel()
					}
					result := <-done
					want := "cancelled"
					if timeout {
						want = "timeout"
					}
					if result.ErrorCode != want || result.OK {
						t.Errorf("结果: %+v want %s", result, want)
					}
				})
			}
			t.Run("disconnect", func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					connection, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					connection.Close()
				}))
				defer server.Close()
				sender, channel := customSenderChannel(t, kind, server.URL)
				if result := sender.Send(context.Background(), channel, customMessage()); result.OK || result.ErrorCode != "transport" {
					t.Fatalf("结果: %+v", result)
				}
			})
		})
	}
}

func TestCustomChannelValidation(t *testing.T) {
	for _, test := range []struct {
		kind            Type
		config, secrets string
	}{
		{PushPlus, `{}`, `{}`}, {PushPlus, `{"extra":1}`, `{"token":"private-secret"}`},
		{PushPlus, `{}`, `{"token":"","extra":"private-secret"}`},
		{ServerChan, `{}`, `{"send_key":"sctpXXt-private-secret"}`},
		{ServerChan, `{}`, `{"send_key":"sctp123-private-secret"}`},
		{ServerChan, `{}`, `{"send_key":"private-secret\n"}`},
		{Webhook, `{"method":"PUT"}`, `{"url":"https://private-secret.invalid"}`},
		{Webhook, `{"content_type":"XML"}`, `{"url":"https://private-secret.invalid"}`},
		{Webhook, `{"query_params":[]}`, `{"url":"https://private-secret.invalid"}`},
		{Webhook, `{"query_params":null}`, `{"url":"https://private-secret.invalid"}`},
		{Webhook, `{"content_type":"FORM","body_template":[]}`, `{"url":"https://private-secret.invalid"}`},
		{Webhook, `{}`, `{"url":"file:///private-secret"}`},
		{Webhook, `{}`, `{"url":"https://private-secret.invalid?bad=%ZZ"}`},
		{Webhook, `{}`, `{"url":"https://private-secret.invalid","headers":null}`},
		{Webhook, `{}`, `{"url":"https://private-secret.invalid","headers":{"X-Test":123}}`},
		{Webhook, `{}`, `{"url":"https://private-secret.invalid","headers":{"X-Test":"private-secret\r\nInjected:yes"}}`},
		{Webhook, `{}`, `{"url":"https://private-secret.invalid","headers":{"Content-Type":"first","content-type":"second"}}`},
		{Webhook, `{}`, `{"url":"https://private-secret.invalid","headers":{"Invalid Header":"private-secret"}}`},
	} {
		channel := ChannelConfig{Type: test.kind, Config: json.RawMessage(test.config), Secrets: json.RawMessage(test.secrets)}
		if err := Validate(channel); err == nil || strings.Contains(err.Error(), "private-secret") {
			t.Errorf("校验 kind=%s: %v", test.kind, err)
		}
	}
}
