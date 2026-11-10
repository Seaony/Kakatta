package notify

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"
)

func TestTelegramProtocol(t *testing.T) {
	for _, mode := range []string{"photo", "text", "system"} {
		t.Run(mode, func(t *testing.T) {
			message := mobileMessage()
			if mode != "photo" {
				message.ImageURL = ""
			}
			if mode == "system" {
				message.ItemTitle, message.Link, message.Kind, message.Urgent = "", "", "system", true
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				method := "sendMessage"
				if mode == "photo" {
					method = "sendPhoto"
				}
				if r.Method != http.MethodPost || r.URL.Path != "/gateway/bot123:SECRET_mobile_key/"+method || r.URL.Query().Get("keep") != "x" {
					t.Errorf("路径或参数不符: %s %s", r.Method, r.URL.Path)
				}
				body := mobileReadBody(t, r)
				if mobileString(t, body, "chat_id") != "-1001" || mobileString(t, body, "parse_mode") != "HTML" {
					t.Error("chat_id或parse_mode不符")
				}
				key := "text"
				if mode == "photo" {
					key = "caption"
					if mobileString(t, body, "photo") != message.ImageURL {
						t.Error("图片不符")
					}
				} else {
					if _, ok := body["photo"]; ok {
						t.Error("文字消息不应带图片")
					}
					if _, ok := body["caption"]; ok {
						t.Error("文字消息不应带caption")
					}
				}
				content := mobileString(t, body, key)
				want := telegramHTML(message, message.ItemTitle)
				if content != want {
					t.Errorf("内容不符:\n%s\n期望:\n%s", content, want)
				}
				if strings.Contains(content, "<品名>") || strings.Contains(content, "<好>") {
					t.Error("插入文字未转义")
				}
				if mode == "system" && strings.Contains(content, "<a ") {
					t.Error("无链接系统事件不应有空链接")
				}
				io.WriteString(w, `{"ok":true,"result":{}}`)
			}))
			defer server.Close()
			result := New(Dependencies{}).Send(context.Background(), mobileChannel(Telegram, server.URL+"/gateway/?keep=x"), message)
			if !result.OK || result.ErrorCode != "" || calls.Load() != 1 {
				t.Fatalf("发送结果不符: %+v 次数%d", result, calls.Load())
			}
		})
	}
}

func TestTelegramFixedHTMLOnWire(t *testing.T) {
	message := Message{
		Title:     `标题 & <好> "双"`,
		ItemTitle: `商品 <A> & "B"`,
		Body:      "第一行 & <保留>\n第二行 \"引号\"",
		Link:      `https://example.invalid/item?a=1&b="x"`,
	}
	const want = `<b>标题 &amp; &lt;好&gt; &#34;双&#34;</b>
<b>商品 &lt;A&gt; &amp; &#34;B&#34;</b>
第一行 &amp; &lt;保留&gt;
第二行 &#34;引号&#34;
<a href="https://example.invalid/item?a=1&amp;b=&#34;x&#34;">在闲鱼打开</a>`
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.EscapedPath() != "/gateway%2Fprivate/bot123:SECRET_mobile_key/sendMessage" || r.URL.Query().Get("keep") != "x" {
			t.Errorf("代理路径转义或查询丢失: %s", r.URL.RequestURI())
		}
		body := mobileReadBody(t, r)
		if got := mobileString(t, body, "text"); got != want {
			t.Errorf("固定 HTML 不符:\n%s\n期望:\n%s", got, want)
		}
		io.WriteString(w, `{"ok":true}`)
	}))
	defer server.Close()
	result := New(Dependencies{}).Send(context.Background(), mobileChannel(Telegram, server.URL+"/gateway%2Fprivate/?keep=x"), message)
	if !result.OK || calls.Load() != 1 {
		t.Fatalf("发送结果不符: %+v 次数%d", result, calls.Load())
	}
}

func TestTelegramLengths(t *testing.T) {
	for _, photo := range []bool{true, false} {
		name := "text"
		if photo {
			name = "photo"
		}
		t.Run(name, func(t *testing.T) {
			limit := TelegramTextLimit
			if photo {
				limit = TelegramCaptionLimit
			}
			message := Message{Title: "推荐 ¥100", ItemTitle: strings.Repeat("😀", 40), Body: "完整理由不能截掉", Link: "https://example.invalid/item", LinkLabel: "Open on Xianyu"}
			if photo {
				message.ImageURL = "https://example.invalid/photo"
			}
			padding := limit - UTF16Len(telegramHTML(message, "")) - 20
			if padding < 0 {
				t.Fatal("测试基数不符")
			}
			message.Body += strings.Repeat("a", padding)
			contents := make(chan string, 1)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				body := mobileReadBody(t, r)
				key := "text"
				if photo {
					key = "caption"
				}
				contents <- mobileString(t, body, key)
				io.WriteString(w, `{"ok":true}`)
			}))
			defer server.Close()
			config := mobileChannel(Telegram, server.URL)
			result := New(Dependencies{}).Send(context.Background(), config, message)
			if !result.OK || calls.Load() != 1 {
				t.Fatalf("发送失败: %+v 次数%d", result, calls.Load())
			}
			sentContent := <-contents
			if !utf8.ValidString(sentContent) || UTF16Len(sentContent) > limit {
				t.Fatalf("截断破坏UTF-8或超限: %d", UTF16Len(sentContent))
			}
			if strings.Count(sentContent, "😀") != 10 {
				t.Fatalf("UTF-16预算错误: 保留%d个emoji", strings.Count(sentContent, "😀"))
			}
			if !strings.Contains(sentContent, htmlText(message.Body)) || !strings.Contains(sentContent, htmlText(message.Title)) || !strings.Contains(sentContent, `<a href="`+htmlText(message.Link)+`">Open on Xianyu</a>`) {
				t.Fatal("截断了必须保留的字段")
			}
			message.Body += strings.Repeat("b", 40)
			result = New(Dependencies{}).Send(context.Background(), config, message)
			if result.ErrorCode != "invalid_message" || calls.Load() != 1 {
				t.Fatalf("无法只缩商品标题时仍发送: %+v 次数%d", result, calls.Load())
			}
		})
	}
}

func TestTelegramResponses(t *testing.T) {
	for _, test := range []struct {
		name, body, code string
		status           int
		ok               bool
	}{
		{"accepted", `{"ok":true}`, "", 200, true},
		{"rejected", `{"ok":false,"description":"SECRET_response"}`, "provider_rejected", 200, false},
		{"missing", `{}`, "invalid_response", 200, false},
		{"null", `{"ok":null}`, "invalid_response", 200, false},
		{"string", `{"ok":"true"}`, "invalid_response", 200, false},
		{"truncated", `{"ok":true`, "invalid_response", 200, false},
		{"invalid", `SECRET_response`, "invalid_response", 200, false},
		{"http_error", `{"ok":true}`, "http_status", 503, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(test.status)
				io.WriteString(w, test.body)
			}))
			defer server.Close()
			result := New(Dependencies{}).Send(context.Background(), mobileChannel(Telegram, server.URL), mobileMessage())
			if result.OK != test.ok || result.ErrorCode != test.code || result.HTTPStatus != test.status || calls.Load() != 1 {
				t.Fatalf("结果不符: %+v 次数%d", result, calls.Load())
			}
			if strings.Contains(string(mobileJSON(result)), "SECRET_") {
				t.Fatal("结果泄露秘密")
			}
		})
	}
}

func TestTelegramConfiguration(t *testing.T) {
	config := ChannelConfig{Type: Telegram, Config: mobileJSON(map[string]string{"chat_id": "@sample"}), Secrets: mobileJSON(map[string]string{"token": "123:abc"})}
	options, _, err := readTelegram(config)
	if err != nil || options.APIURL != "https://api.telegram.org" {
		t.Fatalf("默认API地址不符: %+v %v", options, err)
	}
	for _, token := range []string{"", "SECRET/path", "SECRET?x", "SECRET#x", "SECRET\r\ninjected"} {
		config.Secrets = mobileJSON(map[string]string{"token": token})
		if err := Validate(config); err == nil || strings.Contains(err.Error(), "SECRET") {
			t.Fatalf("无效token未安全拒绝: %v", err)
		}
	}
	config.Secrets = mobileJSON(map[string]string{"token": "123:abc"})
	for _, options := range []map[string]string{{"chat_id": " "}, {"chat_id": "1", "api_url": "file:///SECRET"}, {"chat_id": "1", "api_url": "http://host/#fragment"}} {
		config.Config = mobileJSON(options)
		if Validate(config) == nil {
			t.Fatal("非法配置被接受")
		}
	}
}
