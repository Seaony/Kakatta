package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Seaony/Kakatta/internal/notification"
	"github.com/Seaony/Kakatta/internal/notify"
	"github.com/Seaony/Kakatta/internal/service"
)

func webhookHTTPConfig(t *testing.T, body string, query *string) string {
	t.Helper()
	config := map[string]string{"method": "POST", "content_type": "JSON", "body_template": body}
	if query != nil {
		config["query_params"] = *query
	}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func assertWebhookHTTPText(t *testing.T, channel notification.Channel, body string, query *string) {
	t.Helper()
	var config map[string]json.RawMessage
	if err := json.Unmarshal(channel.Config, &config); err != nil {
		t.Fatal(err)
	}
	var text string
	if json.Unmarshal(config["body_template"], &text) != nil || text != body {
		t.Fatal("网页响应必须返回完整的JSON文本，不能返回数值对象或改变数值原文")
	}
	if query == nil {
		if _, present := config["query_params"]; present {
			t.Fatal("整体替换配置不能保留省略的旧查询模板")
		}
	} else if json.Unmarshal(config["query_params"], &text) != nil || text != *query {
		t.Fatal("查询模板必须以JSON文本完整往返")
	}
}

func TestWebhookWireRoundTripPreservesOpaqueJSONAndActualSending(t *testing.T) {
	type sentRequest struct {
		body, authorization string
		query               url.Values
	}
	sent := make(chan sentRequest, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		sent <- sentRequest{string(body), r.Header.Get("Authorization"), r.URL.Query()}
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	f, notifications := newNotificationHTTPFixture(t, notify.New(notify.Dependencies{HTTP: backend.Client()}))
	body := `{"id":9007199254740993,"nested":[-9223372036854775809,1e400,{"exact":123456789012345678901234567890}],"title":"{{title}}"}`
	query := `{"external":9007199254740993,"meta":{"id":9223372036854775808},"exponent":1e400}`
	secrets, _ := json.Marshal(map[string]any{"url": backend.URL + "/private-hook", "headers": map[string]string{"Authorization": "private-header"}})
	channel := createHTTPChannel(t, f, "webhook", webhookHTTPConfig(t, body, &query), string(secrets))
	path := fmt.Sprintf("/api/v1/channels/%d", channel.ID)
	assertWebhookHTTPText(t, channel, body, &query)
	for _, response := range []*httptest.ResponseRecorder{
		f.request(http.MethodGet, path, "", true),
		f.request(http.MethodPatch, path, `{"name":"只改名字","secrets":{}}`, true),
	} {
		requireStatus(t, response, http.StatusOK)
		assertChannelResponseSafe(t, response)
		assertWebhookHTTPText(t, decodeBody[notification.Channel](t, response), body, &query)
	}
	listed := f.request(http.MethodGet, "/api/v1/channels", "", true)
	requireStatus(t, listed, http.StatusOK)
	values := decodeBody[[]notification.Channel](t, listed)
	if len(values) != 1 {
		t.Fatal("真实渠道列表不正确")
	}
	assertWebhookHTTPText(t, values[0], body, &query)
	stored, err := notifications.GetChannel(t.Context(), channel.ID)
	if err != nil {
		t.Fatal(err)
	}
	var rawConfig map[string]json.RawMessage
	if json.Unmarshal(stored.Config, &rawConfig) != nil || string(rawConfig["body_template"]) != body || string(rawConfig["query_params"]) != query {
		t.Fatal("内部存储必须保留原JSON结构和数字，不能存网页转义文本")
	}
	response := f.request(http.MethodPost, path+"/test", "", true)
	requireStatus(t, response, http.StatusOK)
	if !decodeBody[notification.ChannelTestResult](t, response).OK {
		t.Fatal("本地Webhook必须真实发送成功")
	}
	request := <-sent
	for _, number := range []string{"9007199254740993", "-9223372036854775809", "1e400", "123456789012345678901234567890"} {
		if !strings.Contains(request.body, number) {
			t.Fatalf("实际Webhook丢失合法JSON数值：%s", number)
		}
	}
	if !strings.Contains(request.body, `"title":"Kakatta 测试通知"`) || request.authorization != "private-header" || request.query.Get("external") != "9007199254740993" || request.query.Get("meta") != `{"id":9223372036854775808}` || request.query.Get("exponent") != "1e400" {
		t.Fatal("占位符、加密请求头或查询参数没有沿原JSON语义发送")
	}
	for _, template := range []string{`"合法JSON字符串模板"`, `[9007199254740993,{"n":1e400}]`, `9223372036854775808`, `1.234567890123456789e999`, `null`, `false`} {
		patch := `{"config":` + webhookHTTPConfig(t, template, nil) + `}`
		updated := f.request(http.MethodPatch, path, patch, true)
		requireStatus(t, updated, http.StatusOK)
		value := decodeBody[notification.Channel](t, updated)
		assertWebhookHTTPText(t, value, template, nil)
		if !value.SecretSet["url"] || !value.SecretSet["headers"] {
			t.Fatal("省略秘密字段必须保留真实值")
		}
		tested := f.request(http.MethodPost, path+"/test", "", true)
		requireStatus(t, tested, http.StatusOK)
		if !decodeBody[notification.ChannelTestResult](t, tested).OK {
			t.Fatal("合法JSON模板不能被缩小为对象或有限浮点数")
		}
		request := <-sent
		if request.body != template || len(request.query) != 0 {
			t.Fatal("任意JSON模板实际发送时不能变成字符串包装、舍入数值或恢复旧查询参数")
		}
	}
}

func TestWebhookWireRejectsMalformedTextWithExactFields(t *testing.T) {
	f, notifications := newNotificationHTTPFixture(t, nil)
	query := `{}`
	channel := createHTTPChannel(t, f, "webhook", webhookHTTPConfig(t, `{}`, &query), `{"url":"http://127.0.0.1:1/private-hook"}`)
	path := fmt.Sprintf("/api/v1/channels/%d", channel.ID)
	for _, test := range []struct{ field, value string }{
		{"body_template", `null`}, {"body_template", `{}`}, {"body_template", `9007199254740993`},
		{"body_template", `""`}, {"body_template", `"{\"private-value\":"`}, {"body_template", `"{} {}"`}, {"body_template", `"NaN"`},
		{"query_params", `{}`}, {"query_params", `null`}, {"query_params", `"[]"`}, {"query_params", `"null"`}, {"query_params", `"123"`}, {"query_params", `"\"text\""`},
	} {
		response := f.request(http.MethodPatch, path, `{"config":{"`+test.field+`":`+test.value+`}}`, true)
		requireStatus(t, response, http.StatusUnprocessableEntity)
		failure := decodeBody[ErrorEnvelope](t, response)
		if failure.Error.Code != "channels.invalid" || len(failure.Fields) != 1 || failure.Fields[0].Key != "config."+test.field || bytes.Contains(response.Body.Bytes(), []byte("private-value")) {
			t.Fatalf("不透明JSON文本错误必须精确指向config.%s且不泄露内容", test.field)
		}
	}
	stored, err := notifications.GetChannel(t.Context(), channel.ID)
	if err != nil || !bytes.Contains(stored.Config, []byte(`"body_template":{}`)) || !stored.SecretSet["url"] {
		t.Fatal("非法文本不能改变已保存的配置和秘密")
	}
	response := f.request(http.MethodPatch, path, `{"config":{"content_type":"FORM","body_template":"\"text\""}}`, true)
	requireStatus(t, response, http.StatusUnprocessableEntity)
	if value := decodeBody[ErrorEnvelope](t, response); len(value.Fields) != 1 || value.Fields[0].Key != "config.body_template" {
		t.Fatal("FORM仍须由原业务校验要求对象模板")
	}
}

func TestWebhookWireTypeChangesKeepPresenceAndCurrentBusinessValidation(t *testing.T) {
	f, notifications := newNotificationHTTPFixture(t, nil)
	channel := createHTTPChannel(t, f, "webhook", webhookHTTPConfig(t, `{"n":9007199254740993}`, nil), `{"url":"http://127.0.0.1:1/private-hook","headers":{"Authorization":"private-header"}}`)
	path := fmt.Sprintf("/api/v1/channels/%d", channel.ID)
	var stale notification.ChannelPatch
	if err := json.Unmarshal([]byte(`{"config":{"body_template":"{\"n\":9007199254740993}"}}`), &stale); err != nil {
		t.Fatal(err)
	}
	stale, err := channelPatchFromHTTP(stale, notify.Webhook)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := stale["type"]; exists {
		t.Fatal("读取当前类型不能把旧type暗中补进补丁")
	}
	changed := f.request(http.MethodPatch, path, `{"type":"ntfy","secrets":{"url":"http://127.0.0.1:1/private-topic"}}`, true)
	requireStatus(t, changed, http.StatusOK)
	value := decodeBody[notification.Channel](t, changed)
	if value.Type != notify.Ntfy || string(value.Config) != `{}` || value.SecretSet["headers"] {
		t.Fatal("切换类型不能继承旧配置和秘密")
	}
	_, err = notifications.UpdateChannel(t.Context(), channel.ID, stale)
	var failure *service.ServiceError
	if !errors.As(err, &failure) || failure.Kind != service.ErrorInvalid {
		t.Fatal("读取类型后发生变更，最终业务校验仍须拒绝不适用的配置")
	}
	back := f.request(http.MethodPatch, path, `{"type":"webhook","config":`+webhookHTTPConfig(t, `"plain string"`, nil)+`,"secrets":{"url":"http://127.0.0.1:1/private-hook"}}`, true)
	requireStatus(t, back, http.StatusOK)
	assertWebhookHTTPText(t, decodeBody[notification.Channel](t, back), `"plain string"`, nil)
	omitted := f.request(http.MethodPatch, path, `{"enabled":false}`, true)
	requireStatus(t, omitted, http.StatusOK)
	assertWebhookHTTPText(t, decodeBody[notification.Channel](t, omitted), `"plain string"`, nil)
	reset := f.request(http.MethodPatch, path, `{"config":{}}`, true)
	requireStatus(t, reset, http.StatusOK)
	if string(decodeBody[notification.Channel](t, reset).Config) != `{}` {
		t.Fatal("config空对象仍须整体替换，不能暗中补回模板")
	}
}
