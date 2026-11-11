package notify

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

func TestForcedWeComTextPreservesFullContentAndLinks(t *testing.T) {
	captured := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		captured <- payload
		_, _ = io.WriteString(w, `{"errcode":0}`)
	}))
	defer server.Close()
	message := Message{Title: "推荐", ItemTitle: "商品", Body: strings.Repeat("证", 500) + "\nhttps://example.invalid/photo", Link: "https://example.invalid/item", TextOnly: true}
	result := New(Dependencies{}).Send(t.Context(), robotChannelConfig(t, WeCom, server.URL, ""), message)
	if !result.OK {
		t.Fatal(result)
	}
	payload := <-captured
	if payload["msgtype"] != "text" || payload["news"] != nil {
		t.Fatalf("强制纯文字仍按图文发送：%v", payload["msgtype"])
	}
	want := message.Title + "\n" + message.ItemTitle + "\n" + message.Body + "\n" + message.Link
	if payload["text"].(map[string]any)["content"] != want {
		t.Fatal("纯文字裁掉关键证据或丢失链接")
	}
}

func TestMessageFitsTelegramWireBounds(t *testing.T) {
	for _, mode := range []string{"photo", "text", "forced_text"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			captured := make(chan string, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				body := mobileReadBody(t, r)
				field := "text"
				if mode == "photo" {
					field = "caption"
				}
				captured <- mobileString(t, body, field)
				_, _ = io.WriteString(w, `{"ok":true}`)
			}))
			defer server.Close()
			config := mobileChannel(Telegram, server.URL)
			message := Message{Title: "标题<&😀", Body: "引文<&>😀", Link: `https://example.invalid/a?b="<&`, LinkLabel: "原文<&😀"}
			limit := TelegramTextLimit
			if mode != "text" {
				message.ImageURL = "https://example.invalid/image"
			}
			if mode == "photo" {
				limit = TelegramCaptionLimit
			}
			message.TextOnly = mode == "forced_text"
			message.Body += strings.Repeat("a", limit-TelegramContentUnits(message, ""))
			if !MessageFits(config, message) {
				t.Fatal("恰好到上限的 HTML 被预检拒绝")
			}
			sender := New(Dependencies{})
			if result := sender.Send(t.Context(), config, message); !result.OK {
				t.Fatal(result)
			}
			wire := <-captured
			if UTF16Len(wire) != limit || !strings.Contains(wire, "&lt;") || !strings.Contains(wire, "😀") {
				t.Fatalf("未按真实转义和 UTF-16 度量：%d", UTF16Len(wire))
			}
			message.Body += "a"
			if MessageFits(config, message) {
				t.Fatal("超出一单位仍通过预检")
			}
			if result := sender.Send(t.Context(), config, message); result.OK || result.ErrorCode != "invalid_message" || calls.Load() != 1 {
				t.Fatal("超长内容仍出站", result, calls.Load())
			}
		})
	}
}

func TestMessageFitsWeComWireBounds(t *testing.T) {
	for _, mode := range []string{"news_title", "news_body", "text", "forced_text"} {
		t.Run(mode, func(t *testing.T) {
			captured := make(chan wecomPayload, 1)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var payload wecomPayload
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				captured <- payload
				_, _ = io.WriteString(w, `{"errcode":0}`)
			}))
			defer server.Close()
			config := robotChannelConfig(t, WeCom, server.URL, "")
			message := Message{}
			switch mode {
			case "news_title":
				message.Title = strings.Repeat("中", 41) + "aaaaa"
				message.Body, message.Link = "证据", "https://example.invalid/item"
			case "news_body":
				message.Title, message.Body, message.Link = "推荐", strings.Repeat("中", 170)+"aa", "https://example.invalid/item"
			case "text":
				message.Body = strings.Repeat("中", 682) + "aa"
			case "forced_text":
				message.Title, message.Body, message.Link, message.TextOnly = "推荐", "证据😀", "https://example.invalid/item", true
				content := withLink(joinNonEmpty("\n", message.Title, plainBody(message)), message.Link)
				message.Body += strings.Repeat("a", WeComTextBytes-len(content))
			}
			if !MessageFits(config, message) {
				t.Fatal("恰好到字节上限被预检拒绝")
			}
			sender := New(Dependencies{})
			if result := sender.Send(t.Context(), config, message); !result.OK {
				t.Fatal(result)
			}
			payload := <-captured
			var wire string
			limit := WeComTextBytes
			if payload.Text != nil {
				wire = payload.Text.Content
			} else if mode == "news_title" {
				wire, limit = payload.News.Articles[0].Title, WeComTitleBytes
			} else {
				wire, limit = payload.News.Articles[0].Description, WeComDescriptionBytes
			}
			if len(wire) != limit || !utf8.ValidString(wire) {
				t.Fatalf("真实字段长度错误：%d，预期%d", len(wire), limit)
			}
			if mode == "news_title" {
				message.Title += "a"
			} else {
				message.Body += "a"
			}
			if MessageFits(config, message) {
				t.Fatal("超出一字节仍通过预检")
			}
			if message.TextOnly {
				if result := sender.Send(t.Context(), config, message); result.OK || result.ErrorCode != "invalid_message" || calls.Load() != 1 {
					t.Fatal("强制纯文字仍悄悄裁剪或发出", result, calls.Load())
				}
			}
		})
	}
}

func TestMessageFitsFeishuIncludesEncodedSignature(t *testing.T) {
	for _, signed := range []bool{false, true} {
		name := "unsigned"
		if signed {
			name = "signed"
		}
		t.Run(name, func(t *testing.T) {
			captured := make(chan []byte, 2)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				body, _ := io.ReadAll(r.Body)
				captured <- body
				_, _ = io.WriteString(w, `{"code":0}`)
			}))
			defer server.Close()
			secret := ""
			if signed {
				secret = "local-signature-only"
			}
			config := robotChannelConfig(t, Feishu, server.URL, secret)
			sender := New(Dependencies{Now: func() time.Time { return time.Unix(math.MinInt64, 0) }})
			message := Message{Title: "提醒😀<>&", ItemTitle: "商品", Body: "原文😀<>&\u2028", Link: "https://example.invalid/item?q=1&x=2"}
			if result := sender.Send(t.Context(), config, message); !result.OK {
				t.Fatal(result)
			}
			initial := <-captured
			var envelope map[string]any
			if err := json.Unmarshal(initial, &envelope); err != nil {
				t.Fatal(err)
			}
			if signed && (envelope["timestamp"] != "-9223372036854775808" || len(envelope["sign"].(string)) != 44) {
				t.Fatal("签名预留的独立边界样例不成立")
			}
			if !signed && (envelope["timestamp"] != nil || envelope["sign"] != nil) {
				t.Fatal("无签名请求不应预留额外签名字段")
			}
			message.Body += strings.Repeat("a", FeishuBodyBytes-len(initial))
			if !MessageFits(config, message) {
				t.Fatal("恰好到编码上限仍被拒绝")
			}
			if result := sender.Send(t.Context(), config, message); !result.OK {
				t.Fatal(result)
			}
			if body := <-captured; len(body) != FeishuBodyBytes || !strings.Contains(string(body), `\u003c`) || !strings.Contains(string(body), `\u2028`) {
				t.Fatalf("没有按实际编码 JSON 度量：%d", len(body))
			}
			message.Body += "a"
			if MessageFits(config, message) {
				t.Fatal("编码后超出一字节仍通过预检")
			}
			if result := sender.Send(t.Context(), config, message); result.OK || result.ErrorCode != "invalid_message" || calls.Load() != 2 {
				t.Fatal("超长请求仍外发", result, calls.Load())
			}
		})
	}
}

func TestMessageFitsServerChanTitleAndNoInventedLimits(t *testing.T) {
	config := ChannelConfig{Type: ServerChan}
	message := Message{Title: strings.Repeat("中", 31) + "😀", Body: strings.Repeat("文", 10000)}
	if !MessageFits(config, message) {
		t.Fatal("32字标题或未规定上限的正文被误拒")
	}
	message.Title += "a"
	if MessageFits(config, message) {
		t.Fatal("33字标题通过预检")
	}
	var wireTitle string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]string
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		wireTitle = payload["title"]
		_, _ = io.WriteString(w, `{"code":0}`)
	}))
	defer server.Close()
	channel := ChannelConfig{Type: ServerChan, Secrets: json.RawMessage(`{"send_key":"local-test-key"}`)}
	message.Title = strings.Repeat("中", 31) + "😀"
	if result := New(Dependencies{Endpoints: Endpoints{ServerChanTurbo: server.URL}}).Send(t.Context(), channel, message); !result.OK || wireTitle != message.Title {
		t.Fatal("32字标题未原样送达本地假渠道", result)
	}
	for _, kind := range []Type{Bark, Ntfy, Gotify, DingTalk, PushPlus, Webhook, Email} {
		if !MessageFits(ChannelConfig{Type: kind}, message) {
			t.Fatalf("%s 添加了规格未规定的长度上限或配置检查", kind)
		}
	}
	if MessageFits(ChannelConfig{Type: "unknown"}, Message{}) || MessageFits(ChannelConfig{Type: Feishu, Secrets: json.RawMessage(`{`)}, Message{}) {
		t.Fatal("未知格式或无法解析的签名数据不能确认长度安全")
	}
}

func TestFormattingMetadataDoesNotEnterWebhookProtocol(t *testing.T) {
	captured := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		captured <- payload
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	config := ChannelConfig{Type: Webhook, Secrets: customJSON(t, map[string]string{"url": server.URL})}
	message := Message{Title: "标题", Body: "正文", TextOnly: true, FormatLevel: 5}
	if result := New(Dependencies{}).Send(t.Context(), config, message); !result.OK {
		t.Fatal(result)
	}
	payload := <-captured
	if len(payload) != 18 || payload["body"] != message.Body || payload["format_level"] != nil || payload["FormatLevel"] != nil || payload["text_only"] != nil || payload["TextOnly"] != nil {
		t.Fatal("内部降级信息改变了Webhook十八字段协议")
	}
}

func TestForcedTelegramTextUsesTextLimit(t *testing.T) {
	captured := make(chan map[string]json.RawMessage, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/sendMessage") {
			t.Errorf("强制纯文字仍调用图片发送：%s", r.URL.Path)
		}
		captured <- mobileReadBody(t, r)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer server.Close()
	message := Message{Title: "推荐", Body: strings.Repeat("证", 1200), Link: "https://example.invalid/item", ImageURL: "https://example.invalid/photo", TextOnly: true}
	if result := New(Dependencies{}).Send(context.Background(), mobileChannel(Telegram, server.URL), message); !result.OK {
		t.Fatal("纯文字被图片说明上限拦截", result)
	}
	payload := <-captured
	if mobileString(t, payload, "text") != telegramHTML(message, message.ItemTitle) || payload["photo"] != nil || payload["caption"] != nil {
		t.Fatal("纯文字被截断或仍携带图片字段")
	}
}
