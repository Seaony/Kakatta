package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestServerChanRoutingAndContent(t *testing.T) {
	for _, test := range []struct{ name, key, image, kind string }{
		{"turbo", "SCT-private-key", "https://example.invalid/a_(图).jpg", "recommended"},
		{"version3", "sctp123t-private-key", "https://example.invalid/a_(图).jpg", "recommended"},
		{"http_image_omitted", "SCT-private-key", "http://example.invalid/image.jpg", "recommended"},
		{"system", "SCT-private-key", "https://example.invalid/image.jpg", "system"},
	} {
		t.Run(test.name, func(t *testing.T) {
			message := Message{Title: strings.Repeat("中文😺", 20), ItemTitle: "相机 [二手]*", Body: "正文_文字\n价格¥100", Link: "https://example.invalid/a_(b)", ImageURL: test.image, Kind: test.kind}
			if test.kind == "system" {
				message.ItemTitle = ""
				message.Link = ""
			}
			var shard string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				prefix := "/turbo/"
				if test.name == "version3" {
					prefix = "/send/"
				}
				if r.Method != "POST" || r.URL.Path != prefix+test.key+".send" || r.Header.Get("Content-Type") != "application/json;charset=utf-8" {
					t.Errorf("请求: %s %s %q", r.Method, r.URL.Path, r.Header.Get("Content-Type"))
				}
				var body map[string]string
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				if utf8.RuneCountInString(body["title"]) != 32 || !utf8.ValidString(body["title"]) || body["title"] != TruncateRunes(message.Title, 32) {
					t.Errorf("标题: %q", body["title"])
				}
				if test.kind == "system" {
					if _, exists := body["short"]; exists {
						t.Error("系统事件不得带short")
					}
					if strings.Contains(body["desp"], "![]") || strings.Contains(body["desp"], "[在闲鱼打开]") {
						t.Errorf("系统事件: %q", body["desp"])
					}
				} else {
					if body["short"] != message.ItemTitle || !strings.Contains(body["desp"], `**相机 \[二手\]\***`) || !strings.Contains(body["desp"], "正文\\_文字\n价格¥100") || !strings.Contains(body["desp"], `[在闲鱼打开](https://example\.invalid/a\_\(b\))`) {
						t.Errorf("Markdown: %#v", body)
					}
					if strings.Contains(body["desp"], "![]") != strings.HasPrefix(test.image, "https:") {
						t.Errorf("图片: %q", body["desp"])
					}
				}
				io.WriteString(w, `{"code":0}`)
			}))
			defer server.Close()
			sender := New(Dependencies{Endpoints: Endpoints{ServerChanTurbo: server.URL + "/turbo", ServerChan3: func(value string) string { shard = value; return server.URL + "/send" }}})
			channel := ChannelConfig{Type: ServerChan, Secrets: customJSON(t, map[string]string{"send_key": test.key})}
			if result := sender.Send(context.Background(), channel, message); !result.OK || result.ProviderCode != "0" {
				t.Fatalf("结果: %+v", result)
			}
			if test.name == "version3" && shard != "123" {
				t.Errorf("分片: %q", shard)
			}
		})
	}
}
