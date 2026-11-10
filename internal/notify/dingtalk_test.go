package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDingTalkSignatureAndMarkdown(t *testing.T) {
	const expectedSign = "TFzHC9kbaNspJao0hOTM5tBkli18vzmkPFVrTFsUcHY="
	var nowCalls atomic.Int32
	type capturedRequest struct {
		query       url.Values
		title, text string
	}
	captured := make(chan capturedRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			t.Errorf("request %s %s", r.Method, r.Header.Get("Content-Type"))
		}
		var body struct {
			MsgType  string `json:"msgtype"`
			Markdown struct {
				Title string `json:"title"`
				Text  string `json:"text"`
			} `json:"markdown"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.MsgType != "markdown" {
			t.Errorf("msgtype=%q", body.MsgType)
		}
		captured <- capturedRequest{r.URL.Query(), body.Markdown.Title, body.Markdown.Text}
		_, _ = io.WriteString(w, `{"errcode":0}`)
	}))
	defer server.Close()
	sender := New(Dependencies{Now: func() time.Time { nowCalls.Add(1); return time.UnixMilli(1700000000123) }})
	message := Message{Title: "提醒\n\"😀<&_", ItemTitle: "商品[1]\r\n*", Body: "理由\n第二行¥1,000", Link: "https://example.invalid/a(b)?x=1&y=2", ImageURL: "https://example.invalid/i(m).jpg", Urgent: true}
	result := sender.Send(context.Background(), robotChannelConfig(t, DingTalk, server.URL+"/send?access_token=SENSITIVE_KEY&existing=a+b&same=1&same=2", "SEC测试+密钥/="), message)
	if !result.OK {
		t.Fatal(result)
	}
	got := <-captured
	if got.query.Get("timestamp") != "1700000000123" || got.query.Get("sign") != expectedSign || got.query.Get("access_token") != "SENSITIVE_KEY" || got.query.Get("existing") != "a b" || strings.Join(got.query["same"], ",") != "1,2" || nowCalls.Load() != 1 {
		t.Fatalf("query=%v now calls=%d", got.query, nowCalls.Load())
	}
	if got.title != "提醒\"😀<&_" {
		t.Fatalf("title=%q", got.title)
	}
	for _, want := range []string{"#### 提醒\"😀\\<&\\_", "**商品\\[1\\]\\***", "理由第二行¥1,000", "![](https://example.invalid/i\\(m\\).jpg)", "[在闲鱼打开](https://example.invalid/a\\(b\\)?x=1&y=2)", "\n\n来自 Kakatta"} {
		if !strings.Contains(got.text, want) {
			t.Fatalf("text=%q missing %q", got.text, want)
		}
	}
	if strings.Contains(got.text, "\r") || strings.Contains(got.text, "理由\n") {
		t.Fatalf("raw inserted linebreak=%q", got.text)
	}
}

func TestDingTalkUnsignedAndOptionalFields(t *testing.T) {
	for _, urgent := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "urgent"}[urgent], func(t *testing.T) {
			captured := make(chan string, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Has("timestamp") || r.URL.Query().Has("sign") {
					t.Error("unsigned request contained signature")
				}
				var object map[string]json.RawMessage
				_ = json.NewDecoder(r.Body).Decode(&object)
				var markdown map[string]string
				_ = json.Unmarshal(object["markdown"], &markdown)
				captured <- markdown["text"]
				_, _ = io.WriteString(w, `{"errcode":0}`)
			}))
			defer server.Close()
			result := New(Dependencies{Now: func() time.Time { t.Error("unsigned request read clock"); return time.Time{} }}).Send(context.Background(), robotChannelConfig(t, DingTalk, server.URL, ""), Message{Title: "系统事件", Body: "请处理", Kind: "system", Urgent: urgent})
			if !result.OK {
				t.Fatal(result)
			}
			if got := <-captured; got != "#### 系统事件\n\n请处理\n\n来自 Kakatta" {
				t.Fatalf("optional fields=%q", got)
			}
		})
	}
}
