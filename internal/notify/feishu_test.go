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
	"time"
)

func TestFeishuSignatureAndRichText(t *testing.T) {
	const expectedSign = "atH8pWwoSVMluvk3SmOTDnMBISDU7t6Z2zlkGH2btxk="
	var nowCalls atomic.Int32
	captured := make(chan map[string]json.RawMessage, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			t.Errorf("request %s %s", r.Method, r.Header.Get("Content-Type"))
		}
		var object map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&object); err != nil {
			t.Error(err)
		}
		captured <- object
		_, _ = io.WriteString(w, `{"code":0}`)
	}))
	defer server.Close()
	message := Message{Title: "提醒\"😀<&", ItemTitle: "商品\n第二行", Body: "¥1,000\n理由\"<&😀", Link: "https://example.invalid/item?q=1&x=2", ImageURL: "https://example.invalid/UNSENT_IMAGE", Urgent: true}
	result := New(Dependencies{Now: func() time.Time { nowCalls.Add(1); return time.UnixMilli(1700000000123) }}).Send(context.Background(), robotChannelConfig(t, Feishu, server.URL, "飞书+密钥/="), message)
	if !result.OK {
		t.Fatal(result)
	}
	object := <-captured
	var timestamp, signature, kind string
	_ = json.Unmarshal(object["timestamp"], &timestamp)
	_ = json.Unmarshal(object["sign"], &signature)
	_ = json.Unmarshal(object["msg_type"], &kind)
	if timestamp != "1700000000" || signature != expectedSign || kind != "post" || nowCalls.Load() != 1 {
		t.Fatalf("timestamp=%s sign=%s kind=%s calls=%d", timestamp, signature, kind, nowCalls.Load())
	}
	var content struct {
		Post map[string]struct {
			Title   string                `json:"title"`
			Content [][]map[string]string `json:"content"`
		} `json:"post"`
	}
	if err := json.Unmarshal(object["content"], &content); err != nil {
		t.Fatal(err)
	}
	post := content.Post["zh_cn"]
	if post.Title != message.Title || len(post.Content) != 3 || post.Content[0][0]["text"] != message.ItemTitle || post.Content[1][0]["text"] != message.Body || post.Content[2][0]["href"] != message.Link || post.Content[2][0]["tag"] != "a" {
		t.Fatalf("post=%v", post)
	}
	encoded, _ := json.Marshal(object)
	if strings.Contains(string(encoded), "UNSENT_IMAGE") {
		t.Fatal("feishu attached an image")
	}
}

func TestFeishuUnsignedOptionalFields(t *testing.T) {
	for _, urgent := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "urgent"}[urgent], func(t *testing.T) {
			captured := make(chan map[string]json.RawMessage, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var object map[string]json.RawMessage
				_ = json.NewDecoder(r.Body).Decode(&object)
				captured <- object
				_, _ = io.WriteString(w, `{"code":0}`)
			}))
			defer server.Close()
			result := New(Dependencies{Now: func() time.Time { t.Error("unsigned request read clock"); return time.Time{} }}).Send(context.Background(), robotChannelConfig(t, Feishu, server.URL, ""), Message{Title: "系统事件", Body: "请处理", Kind: "system", Urgent: urgent})
			if !result.OK {
				t.Fatal(result)
			}
			object := <-captured
			if _, exists := object["timestamp"]; exists {
				t.Fatal("unexpected timestamp")
			}
			if _, exists := object["sign"]; exists {
				t.Fatal("unexpected sign")
			}
			var content struct {
				Post map[string]struct {
					Content [][]map[string]string `json:"content"`
				} `json:"post"`
			}
			_ = json.Unmarshal(object["content"], &content)
			rows := content.Post["zh_cn"].Content
			if len(rows) != 1 || rows[0][0]["tag"] != "text" || rows[0][0]["text"] != "请处理" {
				t.Fatalf("optional fields rows=%v", rows)
			}
		})
	}
}

func TestFeishuEncodedBodyLimit(t *testing.T) {
	var calls atomic.Int32
	var bodyBytes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		bodyBytes.Store(int32(len(data)))
		calls.Add(1)
		_, _ = io.WriteString(w, `{"code":0}`)
	}))
	defer server.Close()
	sender := New(Dependencies{})
	config := robotChannelConfig(t, Feishu, server.URL, "")
	// 单引号等普通字符不膨胀，先找出固定协议字段占用的长度。
	if result := sender.Send(context.Background(), config, Message{Title: "x", Body: "x"}); !result.OK {
		t.Fatal(result)
	}
	overhead := int(bodyBytes.Load()) - 1
	calls.Store(0)
	if result := sender.Send(context.Background(), config, Message{Title: "x", Body: strings.Repeat("a", FeishuBodyBytes-overhead)}); !result.OK || bodyBytes.Load() != FeishuBodyBytes || calls.Load() != 1 {
		t.Fatalf("boundary result=%+v bytes=%d calls=%d", result, bodyBytes.Load(), calls.Load())
	}
	calls.Store(0)
	for _, body := range []string{strings.Repeat("a", FeishuBodyBytes-overhead+1), strings.Repeat("<", FeishuBodyBytes/2), strings.Repeat("😀", FeishuBodyBytes/4)} {
		if result := sender.Send(context.Background(), config, Message{Title: "x", Body: body}); result.ErrorCode != "invalid_message" || result.OK || calls.Load() != 0 {
			t.Fatalf("over-limit result=%+v calls=%d", result, calls.Load())
		}
	}
}
