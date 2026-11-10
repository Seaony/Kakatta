package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestWebhookAllPlaceholdersAndStructure(t *testing.T) {
	message := customMessage()
	message.LinkLabel, message.Footer = "Open in Kakatta", "From Kakatta"
	message.Title = "引号\"换行\n😺 {{body}}"
	message.Urgent = true
	want := map[string]string{"title": message.Title, "content": message.ItemTitle + "\n¥1,300 · 价格低 & 配件全\n" + message.Link, "body": message.Body, "price": message.Price, "reason": message.Reason, "score": message.Score, "grade": message.Grade, "config": message.Config, "fair_price": message.FairPrice, "desktop_link": message.DesktopLink, "mobile_link": message.MobileLink, "link": message.Link, "kakatta_url": message.KakattaURL, "urgent": "true", "item_id": message.ItemID, "item_title": message.ItemTitle, "image": message.ImageURL, "task": message.Task, "kind": message.Kind}
	template := map[string]any{"keep_number": json.Number("9007199254740993"), "keep_bool": true, "keep_null": nil, "{{title}}": "不替换键", "nested": []any{map[string]any{"v": "{{body}}"}, "${item_title}"}, "unknown": "{{unknown}} {{link_label}} ${footer}"}
	for name := range want {
		template[name] = []any{"{{" + name + "}}", "${" + name + "}"}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Host != "custom.local" || r.Header.Get("X-Secret") != "private-header" {
			t.Errorf("请求头: %s %s %#v", r.Method, r.Host, r.Header)
		}
		if r.URL.Query().Get("old") != "保留" || r.URL.Query().Get("replace") != message.Title || r.URL.Query().Get("quote") != message.Body || r.URL.Query().Get("n") != "9007199254740993" {
			t.Errorf("查询: %s", r.URL.RawQuery)
		}
		decoder := json.NewDecoder(r.Body)
		decoder.UseNumber()
		var body map[string]any
		if err := decoder.Decode(&body); err != nil {
			t.Error(err)
			return
		}
		for name, value := range want {
			if !reflect.DeepEqual(body[name], []any{value, value}) {
				t.Errorf("占位符%s: %#v", name, body[name])
			}
		}
		if body["keep_number"] != json.Number("9007199254740993") || body["keep_bool"] != true || body["keep_null"] != nil || body["{{title}}"] != "不替换键" || body["unknown"] != "{{unknown}} {{link_label}} ${footer}" {
			t.Errorf("结构: %#v", body)
		}
		if !reflect.DeepEqual(body["nested"], []any{map[string]any{"v": message.Body}, message.ItemTitle}) {
			t.Errorf("嵌套: %#v", body["nested"])
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	channel := ChannelConfig{Type: Webhook, Config: customJSON(t, map[string]any{"method": "POST", "query_params": map[string]any{"replace": "{{title}}", "quote": "${body}", "n": json.Number("9007199254740993")}, "body_template": template}), Secrets: customJSON(t, map[string]any{"url": server.URL + "?old=%E4%BF%9D%E7%95%99&replace=old", "headers": map[string]string{"Host": "custom.local", "X-Secret": "private-header"}})}
	if result := New(Dependencies{}).Send(context.Background(), channel, message); !result.OK || result.HTTPStatus != 204 {
		t.Fatalf("结果: %+v", result)
	}
}

func TestWebhookMethodsAndDefaultTemplate(t *testing.T) {
	for _, mode := range []string{"get", "form", "default_json", "custom_json"} {
		t.Run(mode, func(t *testing.T) {
			message := customMessage()
			message.LinkLabel, message.Footer = "Open in Kakatta", "From Kakatta"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "get":
					body, _ := io.ReadAll(r.Body)
					if r.Method != "GET" || len(body) != 0 || r.URL.Query().Get("v") != message.Title {
						t.Errorf("GET: %s %s %q", r.Method, r.URL, body)
					}
				case "form":
					if err := r.ParseForm(); err != nil {
						t.Error(err)
					}
					var nested map[string]string
					if err := json.Unmarshal([]byte(r.PostForm.Get("nested")), &nested); err != nil {
						t.Error(err)
					}
					if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" || r.PostForm.Get("title") != message.Title || nested["text"] != message.Body {
						t.Errorf("FORM: %#v", r.PostForm)
					}
				case "default_json":
					var body map[string]string
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
						return
					}
					if len(body) != 18 || body["title"] != message.Title || body["item_id"] != message.ItemID || body["urgent"] != "false" || body["mobile_link"] != message.MobileLink || body["image"] != message.ImageURL || r.Header.Get("Content-Type") != "application/json; charset=utf-8" {
						t.Errorf("默认模板: %#v %v", body, r.Header)
					}
				case "custom_json":
					body, _ := io.ReadAll(r.Body)
					var value []string
					if json.Unmarshal(body, &value) != nil || !reflect.DeepEqual(value, []string{message.Title}) || r.Header.Get("Content-Type") != "application/custom+json" {
						t.Errorf("自定义JSON: %q %v", body, r.Header)
					}
				}
				io.WriteString(w, "not JSON is allowed")
			}))
			defer server.Close()
			config := map[string]any{}
			headers := map[string]string{}
			switch mode {
			case "get":
				config = map[string]any{"method": "GET", "query_params": map[string]string{"v": "{{title}}"}, "body_template": map[string]string{"ignored": "{{body}}"}}
			case "form":
				config = map[string]any{"content_type": "FORM", "body_template": map[string]any{"title": "${title}", "nested": map[string]string{"text": "{{body}}"}}}
			case "default_json":
				config = map[string]any{"content_type": "FORM"}
			case "custom_json":
				config = map[string]any{"body_template": []string{"{{title}}"}}
				headers["content-type"] = "application/custom+json"
			}
			channel := ChannelConfig{Type: Webhook, Config: customJSON(t, config), Secrets: customJSON(t, map[string]any{"url": server.URL, "headers": headers})}
			if result := New(Dependencies{}).Send(context.Background(), channel, message); !result.OK {
				t.Fatalf("结果: %+v", result)
			}
		})
	}
}

func TestWebhookEmptySystemValues(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if body["item_title"] != "" || body["image"] != "" || body["link"] != "" || body["score"] != "" || body["kind"] != "system" || body["urgent"] != "true" || body["body"] != "请处理" {
			t.Errorf("系统事件: %#v", body)
		}
		w.WriteHeader(202)
	}))
	defer server.Close()
	sender, channel := customSenderChannel(t, Webhook, server.URL)
	if result := sender.Send(context.Background(), channel, Message{Title: "接口失效", Body: "请处理", Kind: "system", Urgent: true}); !result.OK {
		t.Fatalf("结果: %+v", result)
	}
}

func TestWebhookPreservesQueryDuplicates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !reflect.DeepEqual(r.URL.Query()["old"], []string{"1", "2"}) || r.URL.Query().Get("new") != "a & b=\"c\"" || strings.Contains(r.URL.RawQuery, " b=") {
			t.Errorf("查询: %q", r.URL.RawQuery)
		}
		w.WriteHeader(200)
	}))
	defer server.Close()
	channel := ChannelConfig{Type: Webhook, Config: json.RawMessage(`{"method":"GET","query_params":{"new":"{{title}}"}}`), Secrets: customJSON(t, map[string]string{"url": server.URL + "?old=1&old=2"})}
	if result := New(Dependencies{}).Send(context.Background(), channel, Message{Title: `a & b="c"`}); !result.OK {
		t.Fatalf("结果: %+v", result)
	}
}
