package notify

import (
	"context"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"
)

func TestNtfyProtocol(t *testing.T) {
	for _, mode := range []string{"ordinary", "urgent", "system"} {
		t.Run(mode, func(t *testing.T) {
			message := mobileMessage()
			message.Title = strings.Repeat("中文😀标题", 25)
			if mode != "ordinary" {
				message.Urgent = true
			}
			if mode == "system" {
				message.ItemTitle, message.ImageURL, message.Link, message.Kind = "", "", "", "system"
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != http.MethodPost || r.URL.Path != "/topic" {
					t.Error("请求方法或路径不符")
				}
				body, err := io.ReadAll(r.Body)
				if err != nil || string(body) != plainBody(message) {
					t.Errorf("纯文本正文不符: %q %v", body, err)
				}
				if r.Header.Get("Content-Type") != "text/plain; charset=utf-8" || r.Header.Get("Tags") != "bell" {
					t.Error("基础请求头不符")
				}
				priority := "default"
				if message.Urgent {
					priority = "urgent"
				}
				if r.Header.Get("Priority") != priority {
					t.Error("紧急级别不符")
				}
				encoded := r.Header.Get("Title")
				decoded, err := new(mime.WordDecoder).DecodeHeader(encoded)
				if err != nil || decoded != message.Title {
					t.Errorf("标题RFC2047无法往返: %v", err)
				}
				for _, word := range strings.Fields(encoded) {
					if len(word) > 75 || !strings.EqualFold(word[:min(len(word), len("=?UTF-8?B?"))], "=?UTF-8?B?") {
						t.Errorf("编码段超限或编码错误: %s", word)
					}
					decodedWord, err := new(mime.WordDecoder).Decode(word)
					if err != nil || !utf8.ValidString(decodedWord) {
						t.Error("编码拆坏UTF-8字符")
					}
				}
				if r.Header.Get("Click") != message.Link || r.Header.Get("Attach") != message.ImageURL {
					t.Error("图和链接不符")
				}
				wantAuth := "Bearer SECRET_ntfy"
				if mode == "system" {
					wantAuth = ""
				}
				if r.Header.Get("Authorization") != wantAuth {
					t.Error("可选令牌不符")
				}
				w.WriteHeader(202)
				io.WriteString(w, `{"id":"accepted"}`)
			}))
			defer server.Close()
			config := mobileChannel(Ntfy, server.URL+"/topic")
			secrets := map[string]string{"url": server.URL + "/topic"}
			if mode != "system" {
				secrets["token"] = "SECRET_ntfy"
			}
			config.Secrets = mobileJSON(secrets)
			result := New(Dependencies{}).Send(context.Background(), config, message)
			if !result.OK || result.ErrorCode != "" || result.HTTPStatus != 202 || calls.Load() != 1 {
				t.Fatalf("发送结果不符: %+v 次数%d", result, calls.Load())
			}
		})
	}
}

func TestNtfyASCIIAndResponseStatus(t *testing.T) {
	for _, test := range []struct {
		status   int
		response string
		ok       bool
	}{{200, "not-json", true}, {204, "", true}, {200, `{"code":500}`, true}, {403, "SECRET_response", false}} {
		t.Run(http.StatusText(test.status)+test.response, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Title") != "ASCII title" {
					t.Error("纯ASCII标题不应额外编码")
				}
				w.WriteHeader(test.status)
				io.WriteString(w, test.response)
			}))
			defer server.Close()
			message := mobileMessage()
			message.Title = "ASCII title"
			result := New(Dependencies{}).Send(context.Background(), mobileChannel(Ntfy, server.URL), message)
			wantCode := ""
			if !test.ok {
				wantCode = "http_status"
			}
			if result.OK != test.ok || result.ErrorCode != wantCode || result.HTTPStatus != test.status {
				t.Fatalf("结果不符: %+v", result)
			}
			if strings.Contains(string(mobileJSON(result)), "SECRET_") {
				t.Fatal("泄露响应内容")
			}
		})
	}
}

func TestNtfyHeaderInjection(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	config := mobileChannel(Ntfy, server.URL)
	for _, field := range []string{"title", "link", "image"} {
		message := mobileMessage()
		switch field {
		case "title":
			message.Title = "title\r\nInjected: SECRET"
		case "link":
			message.Link = "https://example.invalid/\r\nInjected: SECRET"
		case "image":
			message.ImageURL = "https://example.invalid/\r\nInjected: SECRET"
		}
		if result := New(Dependencies{}).Send(context.Background(), config, message); result.ErrorCode != "invalid_message" {
			t.Fatalf("%s注入未被拒: %+v", field, result)
		}
	}
	config.Secrets = mobileJSON(map[string]string{"url": server.URL, "token": "SECRET\r\nInjected: yes"})
	if err := Validate(config); err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("令牌注入未安全拒绝: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("无效请求仍发送%d次", calls.Load())
	}
}
