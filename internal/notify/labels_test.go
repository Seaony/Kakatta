package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"strings"
	"sync/atomic"
	"testing"
)

type notificationLabelCase struct {
	name, label, footer, wantLabel, wantHTML, wantMarkdown, wantFooter string
	summary                                                            bool
}

func notificationLabelCases() []notificationLabelCase {
	return []notificationLabelCase{
		{name: "english", label: "Open on Xianyu", footer: "From Kakatta", wantLabel: "Open on Xianyu", wantHTML: "Open on Xianyu", wantMarkdown: "Open on Xianyu", wantFooter: "From Kakatta"},
		{name: "fallback", wantLabel: "在闲鱼打开", wantHTML: "在闲鱼打开", wantMarkdown: "在闲鱼打开", wantFooter: "来自 Kakatta"},
		{name: "escaped", label: `<img src=x onerror="x">&[view](x)_*`, footer: "From <Kakatta>\r\n[x](y)_*", wantLabel: `<img src=x onerror="x">&[view](x)_*`, wantHTML: `&lt;img src=x onerror=&#34;x&#34;&gt;&amp;[view](x)_*`, wantMarkdown: `\<img src=x onerror="x"\>&\[view\]\(x\)\_\*`, wantFooter: `From \<Kakatta\>\[x\]\(y\)\_\*`},
		{name: "summary", label: "Open in Kakatta", footer: "From Kakatta", wantLabel: "Open in Kakatta", wantHTML: "Open in Kakatta", wantMarkdown: "Open in Kakatta", wantFooter: "From Kakatta", summary: true},
	}
}

func notificationLabelMessage(test notificationLabelCase) Message {
	message := Message{Title: "Good deal", ItemTitle: "Camera", Body: "Complete reason", Link: "https://example.invalid/item", Kind: "recommended", LinkLabel: test.label, Footer: test.footer}
	if test.summary {
		message.Title, message.ItemTitle, message.Body = "2 more alerts", "", "Camera\nTablet"
		message.Link, message.KakattaURL, message.Kind = "https://example.invalid/tasks/7/results", "https://example.invalid/tasks/7/results", "summary"
	}
	return message
}

func TestNotificationLabelsOnWire(t *testing.T) {
	for _, kind := range []Type{Telegram, DingTalk, Feishu, PushPlus, ServerChan} {
		t.Run(string(kind), func(t *testing.T) {
			for _, test := range notificationLabelCases() {
				t.Run(test.name, func(t *testing.T) {
					message := notificationLabelMessage(test)
					if kind == Telegram && test.name == "english" {
						message.ImageURL = "https://example.invalid/photo"
					}
					var calls atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls.Add(1)
						var body map[string]json.RawMessage
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
							t.Error(err)
							return
						}
						var content string
						switch kind {
						case Telegram:
							key, fields := "text", 3
							if message.ImageURL != "" {
								key, fields = "caption", 4
							}
							content = mobileString(t, body, key)
							if len(body) != fields || !strings.Contains(content, `<a href="`+message.Link+`">`+test.wantHTML+`</a>`) {
								t.Errorf("链接或JSON字段不符: %s", content)
							}
							_, _ = io.WriteString(w, `{"ok":true}`)
						case DingTalk:
							var markdown map[string]string
							if err := json.Unmarshal(body["markdown"], &markdown); err != nil {
								t.Error(err)
							}
							content = markdown["text"]
							if len(body) != 2 || len(markdown) != 2 || !strings.Contains(content, "["+test.wantMarkdown+"]("+message.Link+")") || !strings.HasSuffix(content, "\n\n"+test.wantFooter) {
								t.Errorf("Markdown链接、落款或JSON字段不符: %s", content)
							}
							_, _ = io.WriteString(w, `{"errcode":0}`)
						case Feishu:
							var post struct {
								Post map[string]struct {
									Content [][]map[string]string `json:"content"`
								} `json:"post"`
							}
							if err := json.Unmarshal(body["content"], &post); err != nil {
								t.Error(err)
							}
							rows := post.Post["zh_cn"].Content
							if len(body) != 2 || len(post.Post) != 1 || len(rows) == 0 {
								t.Errorf("post字段不符: %v", post)
							} else {
								link := rows[len(rows)-1][0]
								content = link["text"]
								if len(link) != 3 || link["tag"] != "a" || link["href"] != message.Link || content != test.wantLabel {
									t.Errorf("富文本链接不符: %v", link)
								}
							}
							_, _ = io.WriteString(w, `{"code":0}`)
						case PushPlus:
							content = mobileString(t, body, "content")
							if len(body) != 4 || !strings.Contains(content, `<a href="`+message.Link+`">`+test.wantHTML+`</a>`) {
								t.Errorf("HTML链接或JSON字段不符: %s", content)
							}
							_, _ = io.WriteString(w, `{"code":200}`)
						case ServerChan:
							content = mobileString(t, body, "desp")
							fields := 3
							if message.ItemTitle == "" {
								fields = 2
							}
							if len(body) != fields || !strings.Contains(content, "["+test.wantMarkdown+"](https://example\\.invalid/"+strings.TrimPrefix(message.Link, "https://example.invalid/")+")") {
								t.Errorf("Markdown链接或JSON字段不符: %s", content)
							}
							_, _ = io.WriteString(w, `{"code":0}`)
						}
						if test.label != "" && strings.Contains(content, "在闲鱼打开") || kind == DingTalk && test.footer != "" && strings.Contains(content, "来自 Kakatta") {
							t.Errorf("显式文案仍混入中文默认: %s", content)
						}
					}))
					defer server.Close()
					var sender *Sender
					var channel ChannelConfig
					switch kind {
					case Telegram:
						sender, channel = New(Dependencies{}), mobileChannel(kind, server.URL)
					case DingTalk, Feishu:
						sender, channel = New(Dependencies{}), robotChannelConfig(t, kind, server.URL, "")
					default:
						sender, channel = customSenderChannel(t, kind, server.URL)
					}
					if result := sender.Send(context.Background(), channel, message); !result.OK || calls.Load() != 1 {
						t.Fatalf("发送结果=%+v 调用次数=%d", result, calls.Load())
					}
				})
			}
		})
	}
}

func TestEmailNotificationLabelsOnWire(t *testing.T) {
	for _, test := range notificationLabelCases() {
		t.Run(test.name, func(t *testing.T) {
			config, _, done := emailServer(t, emailServerOptions{})
			message := notificationLabelMessage(test)
			if result := New(Dependencies{}).Send(context.Background(), config, message); !result.OK {
				t.Fatal(result)
			}
			transcript := emailWaitTranscript(t, done)
			parsed, err := mail.ReadMessage(bytes.NewReader(transcript.Message))
			if err != nil {
				t.Fatal(err)
			}
			_, parameters, err := mime.ParseMediaType(parsed.Header.Get("Content-Type"))
			if err != nil {
				t.Fatal(err)
			}
			reader := multipart.NewReader(parsed.Body, parameters["boundary"])
			for _, contentType := range []string{"text/plain", "text/html"} {
				part, err := reader.NextPart()
				if err != nil {
					t.Fatal(err)
				}
				data, err := io.ReadAll(part)
				if err != nil {
					t.Fatal(err)
				}
				content := string(data)
				if !strings.HasPrefix(part.Header.Get("Content-Type"), contentType) {
					t.Fatalf("MIME类型不符: %v", part.Header)
				}
				if contentType == "text/plain" {
					if content != joinNonEmpty("\n", message.ItemTitle, message.Body, message.Link) {
						t.Errorf("纯文本内容改变: %s", content)
					}
				} else if !strings.Contains(content, `<a href="`+message.Link+`">`+test.wantHTML+`</a>`) || test.label != "" && strings.Contains(content, "在闲鱼打开") {
					t.Errorf("HTML链接文案不符: %s", content)
				}
			}
		})
	}
}

func TestNotificationLabelsRejectInvalidUTF8BeforeSend(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{"code":200}`)
	}))
	defer server.Close()
	for _, field := range []string{"link_label", "footer"} {
		t.Run(field, func(t *testing.T) {
			message := Message{Title: "Good deal", LinkLabel: "Open in Kakatta 中文😀", Footer: "From Kakatta 中文😀"}
			if !validMessage(message) {
				t.Fatal("有效文字被拒绝")
			}
			if field == "link_label" {
				message.LinkLabel = string([]byte{0xff})
			} else {
				message.Footer = string([]byte{0xff})
			}
			if result := New(Dependencies{}).Send(context.Background(), mobileChannel(Bark, server.URL), message); result.OK || result.ErrorCode != "invalid_message" || calls.Load() != 0 {
				t.Fatalf("非法UTF8仍发送: %+v 次数=%d", result, calls.Load())
			}
		})
	}
}
