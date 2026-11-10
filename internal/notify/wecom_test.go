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
	"unicode/utf8"
)

func robotChannelConfig(t *testing.T, kind Type, address, secret string) ChannelConfig {
	t.Helper()
	values := map[string]string{"url": address}
	if secret != "" {
		values["secret"] = secret
	}
	raw, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	return ChannelConfig{Type: kind, Config: json.RawMessage(`{}`), Secrets: raw}
}

func TestWeComMessageForms(t *testing.T) {
	for _, test := range []struct {
		name     string
		message  Message
		wantText bool
	}{
		{"normal", Message{Title: "提醒😀", ItemTitle: "商品<&\"", Body: "价格¥1,000\n一项待确认", Link: "https://example.invalid/item", ImageURL: "https://example.invalid/image"}, false},
		{"urgent", Message{Title: "捡漏", ItemTitle: "商品", Body: "便宜", Link: "https://example.invalid/item", Urgent: true}, false},
		{"system", Message{Title: "需要验证", Body: "请打开 Kakatta", Kind: "system", Urgent: true}, true},
		{"summary", Message{Title: "还有3条提醒", Body: "商品1\n商品2\n商品3", Kind: "summary"}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			captured := make(chan map[string]any, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
					t.Errorf("unexpected request %s %s", r.Method, r.Header.Get("Content-Type"))
				}
				var object map[string]any
				if err := json.NewDecoder(r.Body).Decode(&object); err != nil {
					t.Error(err)
				}
				captured <- object
				_, _ = io.WriteString(w, `{"errcode":0}`)
			}))
			defer server.Close()
			result := New(Dependencies{}).Send(context.Background(), robotChannelConfig(t, WeCom, server.URL, ""), test.message)
			if !result.OK || result.ProviderCode != "0" {
				t.Fatal(result)
			}
			got := <-captured
			if test.wantText {
				if got["msgtype"] != "text" || got["news"] != nil {
					t.Fatalf("text message=%v", got)
				}
				content := got["text"].(map[string]any)["content"]
				if content != test.message.Title+"\n"+test.message.Body {
					t.Fatalf("content=%q", content)
				}
				return
			}
			if got["msgtype"] != "news" || got["text"] != nil {
				t.Fatalf("news message=%v", got)
			}
			articles := got["news"].(map[string]any)["articles"].([]any)
			if len(articles) != 1 {
				t.Fatalf("articles=%v", articles)
			}
			article := articles[0].(map[string]any)
			if article["title"] != test.message.Title+" · "+test.message.ItemTitle || article["description"] != test.message.Body || article["url"] != test.message.Link {
				t.Fatalf("article=%v", article)
			}
			if test.message.ImageURL == "" {
				if _, exists := article["picurl"]; exists {
					t.Fatal("empty image field")
				}
			} else if article["picurl"] != test.message.ImageURL {
				t.Fatal(article)
			}
		})
	}
}

func TestWeComUTF8Limits(t *testing.T) {
	captured := make(chan map[string]any, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var object map[string]any
		_ = json.NewDecoder(r.Body).Decode(&object)
		captured <- object
		_, _ = io.WriteString(w, `{"errcode":0}`)
	}))
	defer server.Close()
	sender := New(Dependencies{})
	config := robotChannelConfig(t, WeCom, server.URL, "")
	if result := sender.Send(context.Background(), config, Message{Title: strings.Repeat("a", 125) + "😀", ItemTitle: "商品", Body: strings.Repeat("中", 171), Link: "https://example.invalid/item"}); !result.OK {
		t.Fatal(result)
	}
	article := (<-captured)["news"].(map[string]any)["articles"].([]any)[0].(map[string]any)
	title, body := article["title"].(string), article["description"].(string)
	if len(title) != 125 || len(body) != 510 || !utf8.ValidString(title) || !utf8.ValidString(body) {
		t.Fatalf("title bytes=%d body bytes=%d", len(title), len(body))
	}
	if result := sender.Send(context.Background(), config, Message{Body: strings.Repeat("a", 2045) + "😀", Kind: "system"}); !result.OK {
		t.Fatal(result)
	}
	text := (<-captured)["text"].(map[string]any)["content"].(string)
	if len(text) != 2045 || !utf8.ValidString(text) {
		t.Fatalf("text bytes=%d", len(text))
	}
}

func TestRobotChannelValidation(t *testing.T) {
	for _, kind := range []Type{WeCom, DingTalk, Feishu} {
		t.Run(string(kind), func(t *testing.T) {
			for _, secrets := range []string{`{}`, `{"url":"file:///SENSITIVE"}`, `{"url":"http://127.0.0.1","extra":"SENSITIVE"}`, `{"url":123}`, `{"url":"http://127.0.0.1"} {}`} {
				err := Validate(ChannelConfig{Type: kind, Secrets: json.RawMessage(secrets)})
				if err == nil || strings.Contains(err.Error(), "SENSITIVE") {
					t.Fatalf("validation error=%v", err)
				}
			}
			config := robotChannelConfig(t, kind, "http://127.0.0.1/secret", "quote\"\n密钥")
			if kind == WeCom {
				config = robotChannelConfig(t, kind, "http://127.0.0.1/secret", "")
			}
			if err := Validate(config); err != nil {
				t.Fatal(err)
			}
			config.Config = json.RawMessage(`{"unused":"SENSITIVE"}`)
			if err := Validate(config); err == nil || strings.Contains(err.Error(), "SENSITIVE") {
				t.Fatalf("config error=%v", err)
			}
		})
	}
}

func TestRobotResponseFailures(t *testing.T) {
	for _, kind := range []Type{WeCom, DingTalk, Feishu} {
		field := "errcode"
		if kind == Feishu {
			field = "code"
		}
		for _, test := range []struct {
			name, body, wantError, wantCode string
			status                          int
		}{
			{"success", fmt.Sprintf(`{"%s":0}`, field), "", "0", 200},
			{"business", fmt.Sprintf(`{"%s":900,"message":"SENSITIVE_RESPONSE"}`, field), "provider_rejected", "900", 200},
			{"missing", `{}`, "invalid_response", "", 200},
			{"null", fmt.Sprintf(`{"%s":null}`, field), "invalid_response", "", 200},
			{"string", fmt.Sprintf(`{"%s":"0"}`, field), "invalid_response", "", 200},
			{"truncated", fmt.Sprintf(`{"%s":0`, field), "invalid_response", "", 200},
			{"invalid", `SENSITIVE_RESPONSE`, "invalid_response", "", 200},
			{"extra_document", fmt.Sprintf(`{"%s":0}{}`, field), "invalid_response", "", 200},
			{"http_status", `SENSITIVE_RESPONSE`, "http_status", "", 503},
		} {
			t.Run(string(kind)+"/"+test.name, func(t *testing.T) {
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					w.WriteHeader(test.status)
					_, _ = io.WriteString(w, test.body)
				}))
				defer server.Close()
				result := New(Dependencies{}).Send(context.Background(), robotChannelConfig(t, kind, server.URL+"/SENSITIVE_URL", ""), Message{Title: "提醒", Body: "正文", Kind: "system"})
				if result.ErrorCode != test.wantError || result.ProviderCode != test.wantCode || result.HTTPStatus != test.status || result.OK != (test.wantError == "") || calls.Load() != 1 {
					t.Fatalf("result=%+v calls=%d", result, calls.Load())
				}
				if strings.Contains(fmt.Sprint(result), "SENSITIVE") {
					t.Fatal("leaked provider response or URL")
				}
			})
		}
	}
}

func TestRobotNetworkFailures(t *testing.T) {
	for _, kind := range []Type{WeCom, DingTalk, Feishu} {
		t.Run(string(kind), func(t *testing.T) {
			t.Run("disconnected", func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = conn.Close()
				}))
				defer server.Close()
				result := New(Dependencies{}).Send(context.Background(), robotChannelConfig(t, kind, server.URL+"/SENSITIVE_URL", ""), Message{Title: "提醒", Body: "正文", Kind: "system"})
				if result.ErrorCode != "transport" || strings.Contains(fmt.Sprint(result), "SENSITIVE") {
					t.Fatal(result)
				}
			})
			t.Run("cancelled_before_send", func(t *testing.T) {
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
				defer server.Close()
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				result := New(Dependencies{}).Send(ctx, robotChannelConfig(t, kind, server.URL, ""), Message{Title: "提醒", Body: "正文"})
				if result.ErrorCode != "cancelled" || calls.Load() != 0 {
					t.Fatalf("%+v calls=%d", result, calls.Load())
				}
			})
			t.Run("cancelled_in_request", func(t *testing.T) {
				started := make(chan struct{})
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					close(started)
					<-r.Context().Done()
				}))
				defer server.Close()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make(chan Result, 1)
				go func() {
					done <- New(Dependencies{}).Send(ctx, robotChannelConfig(t, kind, server.URL, ""), Message{Title: "提醒", Body: "正文"})
				}()
				<-started
				cancel()
				if result := <-done; result.ErrorCode != "cancelled" {
					t.Fatal(result)
				}
			})
			t.Run("timeout", func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.Copy(io.Discard, r.Body); <-r.Context().Done() }))
				defer server.Close()
				result := New(Dependencies{HTTP: &http.Client{Timeout: 30 * time.Millisecond}}).Send(context.Background(), robotChannelConfig(t, kind, server.URL, ""), Message{Title: "提醒", Body: "正文"})
				if result.ErrorCode != "timeout" {
					t.Fatal(result)
				}
			})
		})
	}
}
