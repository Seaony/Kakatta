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

func mobileJSON(value any) json.RawMessage {
	result, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return result
}

func mobileChannel(kind Type, address string) ChannelConfig {
	config := ChannelConfig{Type: kind, Config: mobileJSON(map[string]any{})}
	switch kind {
	case Bark, Ntfy:
		config.Secrets = mobileJSON(map[string]any{"url": address})
	case Telegram:
		config.Config = mobileJSON(map[string]any{"chat_id": "-1001", "api_url": address})
		config.Secrets = mobileJSON(map[string]any{"token": "123:SECRET_mobile_key"})
	case Gotify:
		config.Config = mobileJSON(map[string]any{"url": address})
		config.Secrets = mobileJSON(map[string]any{"token": "SECRET_mobile_key"})
	}
	return config
}

func mobileMessage() Message {
	return Message{Title: `[任务] 捡漏 91 & "<`, ItemTitle: `相机😀 <品名> & "`, Body: "¥1,000 · 理由 <好> & \"齐全\"\n原话换行", Link: `https://example.invalid/item?id=1&a="x"`, ImageURL: `https://example.invalid/image?a=1&b=2`, Kind: "recommended"}
}

func mobileReadBody(t *testing.T, request *http.Request) map[string]json.RawMessage {
	t.Helper()
	var result map[string]json.RawMessage
	if err := json.NewDecoder(request.Body).Decode(&result); err != nil {
		t.Errorf("请求不是合法 JSON: %v", err)
	}
	return result
}

func mobileString(t *testing.T, object map[string]json.RawMessage, key string) string {
	t.Helper()
	var result string
	if err := json.Unmarshal(object[key], &result); err != nil {
		t.Errorf("%s 不是字符串: %v", key, err)
	}
	return result
}

func TestBarkProtocol(t *testing.T) {
	for _, mode := range []string{"ordinary", "urgent", "system"} {
		t.Run(mode, func(t *testing.T) {
			message := mobileMessage()
			if mode != "ordinary" {
				message.Urgent = true
			}
			if mode == "system" {
				message.ItemTitle, message.ImageURL, message.Link, message.Kind = "", "", "", "system"
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				calls.Add(1)
				if request.Method != http.MethodPost || request.URL.Path != "/device-key" {
					t.Errorf("请求路径或方法不符: %s %s", request.Method, request.URL.Path)
				}
				if request.Header.Get("Content-Type") != "application/json; charset=utf-8" {
					t.Errorf("Content-Type 不符")
				}
				body := mobileReadBody(t, request)
				for key, want := range map[string]string{"title": message.Title, "body": plainBody(message), "group": "Kakatta"} {
					if got := mobileString(t, body, key); got != want {
						t.Errorf("%s=%q, 期望%q", key, got, want)
					}
				}
				level := "active"
				if message.Urgent {
					level = "timeSensitive"
				}
				if mobileString(t, body, "level") != level {
					t.Errorf("紧急级别不符")
				}
				if mode == "system" {
					if _, ok := body["icon"]; ok {
						t.Error("系统事件不应带 icon")
					}
					if _, ok := body["url"]; ok {
						t.Error("无链接不应带 url")
					}
				} else {
					if mobileString(t, body, "icon") != message.ImageURL || mobileString(t, body, "url") != message.Link {
						t.Error("图或链接不符")
					}
				}
				io.WriteString(writer, `{"code":200}`)
			}))
			defer server.Close()
			if result := New(Dependencies{}).Send(context.Background(), mobileChannel(Bark, server.URL+"/device-key"), message); !result.OK || result.ErrorCode != "" || result.ProviderCode != "200" {
				t.Fatalf("发送失败: %+v", result)
			}
			if calls.Load() != 1 {
				t.Fatalf("实际发送%d次", calls.Load())
			}
		})
	}
}

func TestBarkBusinessResponses(t *testing.T) {
	for _, test := range []struct {
		name, body, code, provider string
		status                     int
		ok                         bool
	}{
		{"empty", "", "", "", 204, true},
		{"no_code", `{"message":"received"}`, "", "", 200, true},
		{"rejected", `{"code":400,"message":"SECRET_response"}`, "provider_rejected", "400", 200, false},
		{"null", `{"code":null}`, "invalid_response", "", 200, false},
		{"string_code", `{"code":"200"}`, "invalid_response", "", 200, false},
		{"truncated", `{"code":200`, "invalid_response", "", 200, false},
		{"invalid", `SECRET_response`, "invalid_response", "", 200, false},
		{"http_error", `{"code":200}`, "http_status", "", 401, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(test.status); io.WriteString(w, test.body) }))
			defer server.Close()
			got := New(Dependencies{}).Send(context.Background(), mobileChannel(Bark, server.URL+"/SECRET_url"), mobileMessage())
			if got.OK != test.ok || got.ErrorCode != test.code || got.ProviderCode != test.provider || got.HTTPStatus != test.status {
				t.Fatalf("结果不符: %+v", got)
			}
			if strings.Contains(fmt.Sprint(got), "SECRET_") {
				t.Fatal("结果泄露响应或地址")
			}
		})
	}
}

func TestMobileChannelsCommonFailures(t *testing.T) {
	for _, kind := range []Type{Bark, Telegram, Ntfy, Gotify} {
		t.Run(string(kind), func(t *testing.T) {
			t.Run("pre_cancelled", func(t *testing.T) {
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
				defer server.Close()
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				got := New(Dependencies{}).Send(ctx, mobileChannel(kind, server.URL), mobileMessage())
				if got.ErrorCode != "cancelled" || calls.Load() != 0 {
					t.Fatalf("取消后仍发送: %+v 次数%d", got, calls.Load())
				}
			})
			t.Run("cancel_during_request", func(t *testing.T) {
				entered := make(chan struct{})
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					io.Copy(io.Discard, r.Body)
					close(entered)
					<-r.Context().Done()
				}))
				defer server.Close()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make(chan Result, 1)
				go func() { done <- New(Dependencies{}).Send(ctx, mobileChannel(kind, server.URL), mobileMessage()) }()
				select {
				case <-entered:
				case <-time.After(3 * time.Second):
					t.Fatal("请求未到假服务")
				}
				cancel()
				select {
				case result := <-done:
					if result.ErrorCode != "cancelled" {
						t.Fatalf("结果不符: %+v", result)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("取消后未返回")
				}
			})
			t.Run("timeout", func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.Copy(io.Discard, r.Body); <-r.Context().Done() }))
				defer server.Close()
				got := New(Dependencies{HTTP: &http.Client{Timeout: 25 * time.Millisecond}}).Send(context.Background(), mobileChannel(kind, server.URL), mobileMessage())
				if got.ErrorCode != "timeout" {
					t.Fatalf("超时结果不符: %+v", got)
				}
			})
			t.Run("disconnect", func(t *testing.T) {
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					conn.Close()
				}))
				defer server.Close()
				got := New(Dependencies{}).Send(context.Background(), mobileChannel(kind, server.URL+"/SECRET_url"), mobileMessage())
				if got.ErrorCode != "transport" || calls.Load() != 1 || strings.Contains(fmt.Sprint(got), "SECRET_") {
					t.Fatalf("断线结果不符: %+v 次数%d", got, calls.Load())
				}
			})
			t.Run("redirect", func(t *testing.T) {
				var targetCalls atomic.Int32
				target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls.Add(1) }))
				defer target.Close()
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Location", target.URL)
					w.WriteHeader(http.StatusTemporaryRedirect)
				}))
				defer server.Close()
				got := New(Dependencies{}).Send(context.Background(), mobileChannel(kind, server.URL), mobileMessage())
				if got.ErrorCode != "http_status" || got.HTTPStatus != 307 || targetCalls.Load() != 0 {
					t.Fatalf("重定向隐式重发: %+v 次数%d", got, targetCalls.Load())
				}
			})
		})
	}
}

func TestMobileChannelValidation(t *testing.T) {
	for _, kind := range []Type{Bark, Telegram, Ntfy, Gotify} {
		t.Run(string(kind), func(t *testing.T) {
			good := mobileChannel(kind, "http://127.0.0.1:8080/base")
			if err := Validate(good); err != nil {
				t.Fatalf("合法配置被拒: %v", err)
			}
			bad := good
			bad.Config = mobileJSON(map[string]any{"unexpected": "SECRET_value"})
			if err := Validate(bad); err == nil || strings.Contains(err.Error(), "SECRET_") {
				t.Fatalf("未知字段未安全拒绝: %v", err)
			}
			bad = good
			bad.Secrets = mobileJSON(map[string]any{"unexpected": "SECRET_value"})
			if err := Validate(bad); err == nil || strings.Contains(err.Error(), "SECRET_") {
				t.Fatalf("未知敏感字段未安全拒绝: %v", err)
			}
			bad = good
			bad.Secrets = json.RawMessage(`[]`)
			if Validate(bad) == nil {
				t.Fatal("非对象配置被接受")
			}
		})
	}
}
