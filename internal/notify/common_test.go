package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Seaony/Kakatta/internal/locales"
)

func TestTextLengthsAndEscapes(t *testing.T) {
	for _, test := range []struct {
		value string
		bytes int
		want  string
	}{
		{"中文😀a", 0, ""}, {"中文😀a", 1, ""}, {"中文😀a", 3, "中"}, {"中文😀a", 5, "中"},
		{"中文😀a", 6, "中文"}, {"中文😀a", 9, "中文"}, {"中文😀a", 10, "中文😀"}, {"中文😀a", 20, "中文😀a"},
	} {
		got := TruncateUTF8(test.value, test.bytes)
		if got != test.want || !utf8.ValidString(got) {
			t.Errorf("TruncateUTF8(%q,%d)=%q want %q", test.value, test.bytes, got, test.want)
		}
	}
	if got := TruncateRunes("中😀文", 2); got != "中😀" {
		t.Fatalf("runes=%q", got)
	}
	if got := UTF16Len("中😀文"); got != 4 {
		t.Fatalf("UTF16=%d", got)
	}
	if got := markdownText("a_[x](y)*\\"); got != `a\_\[x\]\(y\)\*\\` {
		t.Fatalf("markdown=%q", got)
	}
	if got := htmlLines("<&\"\n第二行"); got != "&lt;&amp;&#34;<br>第二行" {
		t.Fatalf("html=%q", got)
	}
	message := Message{ItemTitle: "商品", Body: "理由\n总结", Price: "¥10", Reason: "理由", Link: "https://example.invalid/item"}
	if got := plainBody(message); got != "商品\n理由\n总结" {
		t.Fatal(got)
	}
	if got := webhookContent(message); got != "商品\n¥10 · 理由\nhttps://example.invalid/item" {
		t.Fatal(got)
	}
	if got := plainBody(Message{Body: "系统事件"}); got != "系统事件" {
		t.Fatal(got)
	}
}

func TestStrictConfigurationDecode(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `"a"`, `{"value":1} {}`, `{"unknown":1}`, `{"value":`} {
		var value struct {
			Value string `json:"value"`
		}
		err := decodeObject(json.RawMessage(raw), &value, "config")
		if err == nil {
			t.Errorf("accepted %q", raw)
		}
		if err != nil && err.Error() != "invalid_config: config" {
			t.Fatalf("unsafe error: %v", err)
		}
	}
	var value struct {
		Value string `json:"value"`
	}
	if err := decodeObject(nil, &value, "config"); err != nil {
		t.Fatal(err)
	}
	if err := decodeObject(json.RawMessage(`{"value":"秘密"}`), &value, "config"); err != nil || value.Value != "秘密" {
		t.Fatalf("%q %v", value.Value, err)
	}
	for _, value := range []string{"https://example.invalid/path?key=secret", "http://127.0.0.1:123/path"} {
		if !validHTTPURL(value) {
			t.Errorf("rejected %s", value)
		}
	}
	for _, value := range []string{"", "file:///tmp/foo", "https:///path", "https://example.invalid/#ignored", "https://example.invalid/\r\nx:secret", "https://example.invalid/?bad=%ZZ", "https://example.invalid/?bad=one;two"} {
		if validHTTPURL(value) {
			t.Errorf("accepted unsafe URL")
		}
	}
}

func TestDefaultProviderEndpoints(t *testing.T) {
	sender := New(Dependencies{})
	if sender.endpoints.PushPlus != "https://www.pushplus.plus/send" || sender.endpoints.ServerChanTurbo != "https://sctapi.ftqq.com" || sender.endpoints.ServerChan3("123") != "https://123.push.ft07.com/send" {
		t.Fatal("default provider endpoints do not match the protocol")
	}
}

func TestProviderCodeResults(t *testing.T) {
	for _, test := range []struct {
		body                string
		optional            bool
		wantOK              bool
		wantError, wantCode string
	}{
		{`{"code":0}`, false, true, "", "0"},
		{`{"code":42,"message":"SENSITIVE"}`, false, false, "provider_rejected", "42"},
		{`{}`, false, false, "invalid_response", ""},
		{`{"code":null}`, false, false, "invalid_response", ""},
		{`{"code":"0"}`, false, false, "invalid_response", ""},
		{`{"code":0`, false, false, "invalid_response", ""},
		{`[]`, false, false, "invalid_response", ""},
		{``, true, true, "", ""},
		{`{}`, true, true, "", ""},
	} {
		result := codeResult(httpReply{Status: 200, Body: []byte(test.body)}, "code", 0, test.optional)
		if result.OK != test.wantOK || result.ErrorCode != test.wantError || result.ProviderCode != test.wantCode || result.HTTPStatus != 200 {
			t.Fatalf("%q: %+v", test.body, result)
		}
		if strings.Contains(fmt.Sprint(result), "SENSITIVE") {
			t.Fatal("leaked response")
		}
	}
	if result := codeResult(httpReply{Status: 200, ReadFailed: true}, "code", 0, false); result.ErrorCode != "invalid_response" {
		t.Fatal(result)
	}
}

func TestHTTPHelpersDoNotFollowRedirectsOrChangeClient(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1); w.WriteHeader(200) }))
	defer target.Close()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(302)
	}))
	defer first.Close()
	var callback atomic.Int32
	transport := http.DefaultTransport.(*http.Transport).Clone()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { callback.Add(1); return nil }}
	sender := New(Dependencies{HTTP: client})
	if transport.DisableKeepAlives || sender.http.Transport == transport {
		t.Fatal("mutated or reused injected transport")
	}
	_, result := sender.sendRequest(context.Background(), http.MethodGet, first.URL, nil, nil, time.Second)
	if result.OK || result.HTTPStatus != 302 || result.ErrorCode != "http_status" || redirected.Load() != 0 || callback.Load() != 0 {
		t.Fatalf("%+v redirected=%d", result, redirected.Load())
	}
	if err := client.CheckRedirect(nil, nil); err != nil || callback.Load() != 1 {
		t.Fatal("mutated injected client")
	}
}

func TestHTTPHelpersDoNotReplayOnLostResponse(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if calls.Add(1) == 2 {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = conn.Close()
					return
				}
				_, _ = io.WriteString(w, "accepted")
			}))
			defer server.Close()
			sender := New(Dependencies{})
			headers := http.Header{"Idempotency-Key": {"configured-key"}}
			if _, result := sender.sendRequest(context.Background(), method, server.URL, []byte("message"), headers, time.Second); !result.OK {
				t.Fatal(result)
			}
			_, result := sender.sendRequest(context.Background(), method, server.URL, []byte("message"), headers, time.Second)
			if result.OK || result.ErrorCode != "transport" || calls.Load() != 2 {
				t.Fatalf("lost response caused a hidden replay: %+v calls=%d", result, calls.Load())
			}
		})
	}
}

func TestHTTPHelpersJSONAndConcurrentUse(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json; charset=utf-8" {
			t.Errorf("request %s %s", r.Method, r.Header.Get("Content-Type"))
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"value":"中文\"\n😀"}` {
			t.Errorf("body=%q", body)
		}
		calls.Add(1)
		_, _ = io.WriteString(w, `{"code":0}`)
	}))
	defer server.Close()
	sender := New(Dependencies{})
	var group sync.WaitGroup
	for range 20 {
		group.Go(func() {
			reply, result := sender.sendJSON(context.Background(), server.URL, struct {
				Value string `json:"value"`
			}{"中文\"\n😀"}, nil, time.Second)
			if !result.OK || !codeResult(reply, "code", 0, false).OK {
				t.Errorf("result=%+v", result)
			}
		})
	}
	group.Wait()
	if calls.Load() != 20 {
		t.Fatal(calls.Load())
	}
}

func TestHTTPHelpersCancellationAndSafeErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sender := New(Dependencies{})
	_, result := sender.sendRequest(ctx, http.MethodGet, "http://127.0.0.1:1/SENSITIVE", nil, nil, time.Second)
	if result.ErrorCode != "cancelled" {
		t.Fatal(result)
	}
	_, result = sender.sendRequest(context.Background(), http.MethodGet, "http://127.0.0.1:1/SENSITIVE", nil, nil, time.Second)
	if result.ErrorCode != "transport" || strings.Contains(fmt.Sprint(result), "SENSITIVE") {
		t.Fatal(result)
	}
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() }))
	defer server.Close()
	ctx, cancel = context.WithCancel(context.Background())
	done := make(chan Result, 1)
	go func() {
		_, result := sender.sendRequest(ctx, http.MethodGet, server.URL, nil, nil, time.Second)
		done <- result
	}()
	<-started
	cancel()
	if result = <-done; result.ErrorCode != "cancelled" {
		t.Fatal(result)
	}
	if got := failureFor(context.DeadlineExceeded); got.ErrorCode != "timeout" {
		t.Fatal(got)
	}
	if got := failureFor(errors.New("SENSITIVE")); got.ErrorCode != "transport" || strings.Contains(fmt.Sprint(got), "SENSITIVE") {
		t.Fatal(got)
	}
}

func TestHTTPHelpersLimitBodyForJSONOnly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", maxResponseBytes+1))
	}))
	defer server.Close()
	reply, result := New(Dependencies{}).sendRequest(context.Background(), http.MethodGet, server.URL, nil, nil, time.Second)
	if !result.OK || !reply.ReadFailed || codeResult(reply, "code", 0, false).ErrorCode != "invalid_response" {
		t.Fatalf("%+v readfailed=%v", result, reply.ReadFailed)
	}
}

func TestUnknownChannelAndInvalidTextDoNotSend(t *testing.T) {
	sender := New(Dependencies{})
	if result := sender.Send(context.Background(), ChannelConfig{Type: "not-supported"}, Message{}); result.ErrorCode != "invalid_config" {
		t.Fatal(result)
	}
	if validMessage(Message{Title: string([]byte{0xff})}) {
		t.Fatal("accepted invalid UTF-8")
	}
	if !validMessage(Message{Title: "中文😀"}) {
		t.Fatal("rejected valid UTF-8")
	}
}

func TestNotificationErrorMessagesHaveBothLanguages(t *testing.T) {
	for _, code := range []string{"invalid_config", "invalid_message", "cancelled", "timeout", "transport", "http_status", "invalid_response", "provider_rejected", "provider_rejected_generic", "smtp_rejected"} {
		message := locales.Message{Code: "notify.error." + code, Args: map[string]any{}}
		if code == "http_status" {
			message.Args["status"] = 429
		}
		if code == "provider_rejected" {
			message.Args["code"] = "400"
		}
		for _, language := range []locales.Language{locales.ZhCN, locales.En} {
			if result, err := locales.Format(language, message); err != nil || result == "" {
				t.Errorf("%s %s: %q %v", language, code, result, err)
			}
		}
	}
}
