package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestGotifyProtocol(t *testing.T) {
	for _, mode := range []string{"ordinary", "urgent", "system", "image_only", "link_only"} {
		t.Run(mode, func(t *testing.T) {
			message := mobileMessage()
			if mode == "urgent" || mode == "system" {
				message.Urgent = true
			}
			if mode == "system" {
				message.ItemTitle, message.ImageURL, message.Link, message.Kind = "", "", "", "system"
			}
			if mode == "image_only" {
				message.Link = ""
			}
			if mode == "link_only" {
				message.ImageURL = ""
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != http.MethodPost || r.URL.EscapedPath() != "/gateway%2Fprivate/message" || r.URL.Query().Get("keep") != "x" {
					t.Errorf("路径或参数不符: %s %s", r.Method, r.URL.RequestURI())
				}
				if r.Header.Get("X-Gotify-Key") != "SECRET_mobile_key" || strings.Contains(r.URL.RawQuery, "token") || strings.Contains(r.URL.RawQuery, "SECRET_") {
					t.Error("令牌未仅放专用头")
				}
				body := mobileReadBody(t, r)
				if mobileString(t, body, "title") != message.Title || mobileString(t, body, "message") != withLink(plainBody(message), message.Link) {
					t.Error("通知内容不符")
				}
				var priority int
				if json.Unmarshal(body["priority"], &priority) != nil {
					t.Error("priority不是整数")
				}
				wantPriority := 5
				if message.Urgent {
					wantPriority = 8
				}
				if priority != wantPriority {
					t.Error("优先级不符")
				}
				if mode == "system" {
					if _, ok := body["extras"]; ok {
						t.Error("没有图或链接时不应带extras")
					}
				} else {
					var extras map[string]map[string]json.RawMessage
					if err := json.Unmarshal(body["extras"], &extras); err != nil {
						t.Error(err)
						return
					}
					notification := extras["client::notification"]
					if message.ImageURL == "" {
						if _, ok := notification["bigImageUrl"]; ok {
							t.Error("无图不应有bigImageUrl")
						}
					} else if mobileString(t, notification, "bigImageUrl") != message.ImageURL {
						t.Error("大图不符")
					}
					if message.Link == "" {
						if _, ok := notification["click"]; ok {
							t.Error("无链接不应有click")
						}
					} else {
						var click map[string]json.RawMessage
						if err := json.Unmarshal(notification["click"], &click); err != nil {
							t.Error(err)
							return
						}
						if mobileString(t, click, "url") != message.Link {
							t.Error("点击链接不符")
						}
					}
				}
				w.WriteHeader(200)
				io.WriteString(w, "not-json")
			}))
			defer server.Close()
			result := New(Dependencies{}).Send(context.Background(), mobileChannel(Gotify, server.URL+"/gateway%2Fprivate/?keep=x"), message)
			if !result.OK || result.ErrorCode != "" || calls.Load() != 1 {
				t.Fatalf("发送结果不符: %+v 次数%d", result, calls.Load())
			}
		})
	}
}

func TestGotifyResponseStatusAndValidation(t *testing.T) {
	for _, status := range []int{201, 204, 400, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				io.WriteString(w, "SECRET_response")
			}))
			defer server.Close()
			result := New(Dependencies{}).Send(context.Background(), mobileChannel(Gotify, server.URL), mobileMessage())
			wantCode := ""
			if status >= 300 {
				wantCode = "http_status"
			}
			if result.OK != (status < 300) || result.ErrorCode != wantCode || result.HTTPStatus != status {
				t.Fatalf("结果不符: %+v", result)
			}
			if strings.Contains(string(mobileJSON(result)), "SECRET_") {
				t.Fatal("结果泄露秘密")
			}
		})
	}
	for _, secrets := range []map[string]string{{"token": ""}, {"token": "SECRET\r\nInjected: yes"}} {
		config := mobileChannel(Gotify, "http://127.0.0.1:8080")
		config.Secrets = mobileJSON(secrets)
		if err := Validate(config); err == nil || strings.Contains(err.Error(), "SECRET") {
			t.Fatalf("非法令牌未安全拒绝: %v", err)
		}
	}
}
