package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"syscall"
	"testing"
)

func protocolRequest(kind Kind) Request {
	return Request{Kind: kind, System: "只返回JSON", User: "待判断商品", Schema: &Schema{Name: "facts", JSON: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)}}
}

func readProtocolRequest(t *testing.T, request *http.Request) map[string]any {
	t.Helper()
	defer request.Body.Close()
	var params map[string]any
	if err := json.NewDecoder(request.Body).Decode(&params); err != nil {
		t.Fatal(err)
	}
	return params
}

func TestProtocolPurposeParameters(t *testing.T) {
	provider := Provider{BaseURL: "http://127.0.0.1:1234/v1/", APIKey: "fake-key", VisionModel: "vision", TextModel: "text"}
	for _, tc := range []struct {
		name, purpose, model string
		kind                 Kind
		retry                bool
		max                  int64
		temperature          *float64
		structured           bool
	}{
		{"title", "title_screen", "text", TitleScreen, false, 3000, protocolTemperature(0.1), true},
		{"analysis", "analysis", "vision", Analysis, false, 2000, protocolTemperature(0.1), true},
		{"analysis_retry", "analysis", "vision", Analysis, true, 2000, protocolTemperature(0.05), true},
		{"title_retry", "title_screen", "text", TitleScreen, true, 3000, protocolTemperature(0.1), true},
		{"profile", "profile", "text", Profile, false, 2000, protocolTemperature(0.3), true},
		{"market", "market", "text", Market, false, 400, nil, false},
		{"test_text", "test", "text", TitleScreen, false, 3000, protocolTemperature(0.1), true},
		{"test_vision", "test", "vision", Analysis, false, 2000, protocolTemperature(0.1), true},
		{"probe", "probe", "text", "", false, 5, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request, err := buildRequest(context.Background(), provider, protocolRequest(tc.kind), DefaultCompat(), tc.purpose, tc.retry)
			if err != nil {
				t.Fatal(err)
			}
			if request.Method != "POST" || request.URL.String() != "http://127.0.0.1:1234/v1/chat/completions" || request.Header.Get("Authorization") != "Bearer fake-key" || request.Header.Get("Content-Type") != "application/json" {
				t.Fatalf("请求路径或头错误：%s %s", request.Method, request.URL)
			}
			params := readProtocolRequest(t, request)
			if params["model"] != tc.model || params["max_tokens"] != float64(tc.max) {
				t.Fatalf("模型和上限错误：%v", params)
			}
			if tc.temperature == nil {
				if _, ok := params["temperature"]; ok {
					t.Fatal("不应发送温度")
				}
			} else if params["temperature"] != *tc.temperature {
				t.Fatalf("温度错误：%v", params)
			}
			format, hasFormat := params["response_format"].(map[string]any)
			if hasFormat != tc.structured {
				t.Fatalf("结构化输出错误：%v", params)
			}
			if hasFormat {
				schema := format["json_schema"].(map[string]any)
				if format["type"] != "json_schema" || schema["name"] != "facts" || schema["strict"] != true {
					t.Fatalf("Schema包装错误：%v", format)
				}
			}
		})
	}
}

func protocolTemperature(value float64) *float64 { return &value }

func TestProtocolCompatAndProbeFloor(t *testing.T) {
	provider := Provider{BaseURL: "http://127.0.0.1/v1", VisionModel: "fallback"}
	floor := int64(9000)
	compat := Compat{JSONMode: JSONObject, MaxTokensParam: "max_completion_tokens", MaxTokensFloor: &floor}
	request, err := buildRequest(context.Background(), provider, protocolRequest(TitleScreen), compat, "title_screen", false)
	if err != nil {
		t.Fatal(err)
	}
	params := readProtocolRequest(t, request)
	if params["model"] != "fallback" || params["max_completion_tokens"] != float64(9000) || params["response_format"].(map[string]any)["type"] != "json_object" {
		t.Fatalf("兼容参数没有复用：%v", params)
	}
	if _, exists := params["temperature"]; exists {
		t.Fatal("禁用的温度不能重新发送")
	}
	if request.Header.Get("Authorization") != "" {
		t.Fatal("没有Key不能发送Authorization")
	}
	request, err = buildRequest(context.Background(), provider, protocolRequest(""), compat, "probe", false)
	if err != nil {
		t.Fatal(err)
	}
	params = readProtocolRequest(t, request)
	if params["max_completion_tokens"] != float64(5) {
		t.Fatal("probe必须固定5，不能套floor")
	}
	if _, exists := params["response_format"]; exists {
		t.Fatal("probe不能发送JSON格式参数")
	}
	compat.JSONMode = JSONText
	request, err = buildRequest(context.Background(), provider, protocolRequest(Profile), compat, "profile", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := readProtocolRequest(t, request)["response_format"]; exists {
		t.Fatal("text级别不发response_format")
	}
}

func TestProtocolLocalHTTPAndImageOrder(t *testing.T) {
	image := "data:image/jpeg;base64,Zml4dHVyZQ=="
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/prefix/chat/completions" || r.Method != "POST" {
			t.Errorf("收到错误请求：%s %s", r.Method, r.URL.Path)
		}
		params := readProtocolRequest(t, r)
		messages := params["messages"].([]any)
		parts := messages[1].(map[string]any)["content"].([]any)
		if len(parts) != 3 || parts[0].(map[string]any)["image_url"].(map[string]any)["url"] != image || parts[1].(map[string]any)["type"] != "image_url" || parts[2].(map[string]any)["text"] != "待判断商品" {
			t.Errorf("图片必须在前文本在后：%v", parts)
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"{}"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	provider := Provider{BaseURL: server.URL + "/prefix", VisionModel: "local"}
	input := protocolRequest(Analysis)
	input.Images = []string{image, image}
	request, err := buildRequest(context.Background(), provider, input, DefaultCompat(), "test", false)
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal(response.Status)
	}
}

func TestProtocolInvalidInputs(t *testing.T) {
	provider := Provider{BaseURL: "http://127.0.0.1/v1", VisionModel: "local"}
	for _, base := range []string{"/relative", "ftp://local/v1", "http://name:password@local/v1", "http://local/v1?key=secret", "http://local/v1#fragment"} {
		bad := provider
		bad.BaseURL = base
		if _, err := buildRequest(context.Background(), bad, protocolRequest(Analysis), DefaultCompat(), "analysis", false); err == nil {
			t.Fatalf("非法地址未拒绝：%s", base)
		}
	}
	for _, image := range []string{"https://invalid.example/image.jpg", "data:image/png;base64,Zml4dHVyZQ==", "data:image/jpeg;base64,$bad", "data:image/jpeg;base64,"} {
		input := protocolRequest(Analysis)
		input.Images = []string{image}
		if _, err := buildRequest(context.Background(), provider, input, DefaultCompat(), "analysis", false); err == nil {
			t.Fatal("非法图片未拒绝")
		}
	}
	input := protocolRequest(TitleScreen)
	input.Images = []string{"data:image/jpeg;base64,Zml4dHVyZQ=="}
	if _, err := buildRequest(context.Background(), provider, input, DefaultCompat(), "title_screen", false); err == nil {
		t.Fatal("标题请求不能带图片")
	}
	input = protocolRequest(Analysis)
	input.Schema.JSON = json.RawMessage(`[]`)
	if _, err := buildRequest(context.Background(), provider, input, DefaultCompat(), "analysis", false); err == nil {
		t.Fatal("Schema必须是对象")
	}
}

func TestProtocolResponseExtraction(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		thinking         bool
		fail             bool
	}{
		{"plain", `{"choices":[{"message":{"content":"  正文  "},"finish_reason":"stop"}]}`, "正文", false, false},
		{"array", `{"choices":[{"message":{"content":[{"type":"text","text":"第一"},{"type":"ignored"},"第二",{"text":"第三"}]}}]}`, "第一第二第三", false, false},
		{"fallback", `{"choices":[{"message":{"content":null,"reasoning_content":"最终JSON"}}]}`, "最终JSON", true, false},
		{"content_first", `{"choices":[{"message":{"content":"最终内容","reasoning_content":"思考"}}]}`, "最终内容", true, false},
		{"thinking", `{"choices":[{"message":{"content":"<think>内部\n推理</think>正文<think>再次</think>"}}]}`, "正文", true, false},
		{"unclosed", `{"choices":[{"message":{"content":"<think>内部推理"}}]}`, "<think>内部推理", true, false},
		{"only_thinking", `{"choices":[{"message":{"content":"<think>内部推理</think>"}}]}`, "", true, true},
		{"empty", `{"choices":[{"message":{"content":""}}]}`, "", false, true},
		{"no_choices", `{}`, "", false, true},
		{"bad_json", `not JSON`, "", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decoded, err := decodeResponse([]byte(tc.body), false)
			if (err != nil) != tc.fail || decoded.text != tc.want || decoded.thinking != tc.thinking {
				t.Fatalf("提取错误：%+v %v", decoded, err)
			}
		})
	}
}

func TestProtocolUsageSurvivesUnavailableOutput(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"content":""},"finish_reason":"length"}],"usage":{"prompt_tokens":30,"completion_tokens":40,"completion_tokens_details":{"reasoning_tokens":35}}}`)
	decoded, err := decodeResponse(body, false)
	if err == nil || decoded.finishReason != "length" || !decoded.usage.Present || decoded.usage.InputTokens != 30 || decoded.usage.OutputTokens != 40 || decoded.usage.ReasoningTokens == nil || *decoded.usage.ReasoningTokens != 35 || !decoded.thinking {
		t.Fatalf("不可用回复不能丢用量和截断信号：%+v %v", decoded, err)
	}
	decoded, err = decodeResponse([]byte(`{"usage":{"prompt_tokens":4,"completion_tokens":5}}`), false)
	if err == nil || decoded.usage.InputTokens != 4 || decoded.usage.OutputTokens != 5 {
		t.Fatalf("无choices也应保留用量：%+v %v", decoded, err)
	}
	decoded, err = decodeResponse([]byte(`{"choices":[{"message":{"content":"正文"}}]}`), false)
	if err != nil || decoded.usage.Present || decoded.usage.InputTokens != 0 || decoded.usage.OutputTokens != 0 {
		t.Fatalf("无用量按零且标记缺失：%+v %v", decoded, err)
	}
}

func TestProtocolInvalidUsageNeverDecreasesTotals(t *testing.T) {
	for _, raw := range []string{`{"prompt_tokens":-1,"completion_tokens":8}`, `{"prompt_tokens":8,"completion_tokens":-1}`, `{"completion_tokens_details":{"reasoning_tokens":-1}}`, `{"prompt_tokens":"8"}`, fmt.Sprintf(`{"prompt_tokens":%d,"completion_tokens":1}`, int64(math.MaxInt64))} {
		decoded, err := decodeResponse([]byte(`{"choices":[{"message":{"content":"正文"}}],"usage":`+raw+`}`), false)
		if err == nil || decoded.usage.InputTokens < 0 || decoded.usage.OutputTokens < 0 || decoded.usage.ReasoningTokens != nil && *decoded.usage.ReasoningTokens < 0 {
			t.Fatalf("非法用量未拦或出现负数：%+v %v", decoded, err)
		}
	}
}

func TestProtocolProbeOnlyNeedsHTTPStatus(t *testing.T) {
	for _, body := range []string{"", "plain OK", `{}`, `{"choices":[{"message":{"content":""},"finish_reason":"length"}],"usage":{"prompt_tokens":3,"completion_tokens":5}}`, `{"usage":{"prompt_tokens":-2}}`} {
		if _, err := decodeResponse([]byte(body), true); err != nil {
			t.Fatalf("probe不能做正文或截断校验：%v", err)
		}
	}
}

func TestProtocolCompatibilityOnlyMovesForward(t *testing.T) {
	current := DefaultCompat()
	current.MaxTokensFloor = func() *int64 { v := int64(8000); return &v }()
	for _, tc := range []struct {
		name               string
		status             int
		body               string
		structured, change bool
	}{
		{"schema400", 400, "unsupported RESPONSE_FORMAT", true, true},
		{"schema422", 422, "JSON_SCHEMA", true, true},
		{"not_structured", 400, "response_format", false, false},
		{"auth", 401, "temperature unsupported response_format max_tokens max_completion_tokens", true, false},
		{"server_parameter", 500, "response_format unsupported temperature", true, true},
		{"server_format_only", 500, "response_format unsupported", true, false},
		{"server_busy", 503, "busy", true, false},
		{"ordinary", 400, "bad request", true, false},
		{"temperature", 400, "TEMPERATURE 参数错误", true, true},
		{"token_param", 400, "use max_completion_tokens instead of max_tokens", true, true},
		{"token_only422", 422, "use max_completion_tokens instead of max_tokens", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next, changed := compatibilityChange(tc.status, []byte(tc.body), current, tc.structured)
			if changed != tc.change {
				t.Fatalf("兼容识别错误：%+v %v", next, changed)
			}
			if next.MaxTokensFloor == nil || *next.MaxTokensFloor != 8000 || current.JSONMode != JSONSchema || !current.SendTemperature || current.MaxTokensParam != "max_tokens" {
				t.Fatal("兼容识别改坏原配置或floor")
			}
		})
	}
	next, changed := compatibilityChange(400, []byte("response_format temperature unsupported max_tokens max_completion_tokens"), current, true)
	if !changed || next.JSONMode != JSONObject || next.SendTemperature || next.MaxTokensParam != "max_completion_tokens" {
		t.Fatalf("组合变化未全部应用：%+v", next)
	}
	next, changed = compatibilityChange(422, []byte("json_object"), next, true)
	if !changed || next.JSONMode != JSONText {
		t.Fatal("object应降至text")
	}
	_, changed = compatibilityChange(422, []byte("json_schema temperature unsupported max_tokens max_completion_tokens"), next, true)
	if changed {
		t.Fatal("状态无前进不能无限重发")
	}
}

func TestProtocolFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		name           string
		status         int
		detail, code   string
		retry, blocked bool
	}{
		{"auth", 401, "temperature invalid balance", "auth", false, true},
		{"forbidden", 403, "", "auth", false, true},
		{"payment", 402, "", "balance", false, true},
		{"notfound", 404, "", "not_found", false, true},
		{"balance", 400, "INSUFFICIENT funds", "balance", false, true},
		{"chinese_balance", 400, "余额不足", "balance", false, true},
		{"image", 400, "unsupported MULTIMODAL model", "unsupported_image", false, true},
		{"daily", 429, "Exceeded today", "daily_quota", false, true},
		{"daily_balance", 429, "BALANCE", "daily_quota", false, true},
		{"rate", 429, "too many requests", "rate_limit", true, false},
		{"server", 503, "unavailable", "server", true, false},
		{"redirect", 302, "redirect", "protocol", false, false},
		{"invalid", 400, "bad request", "protocol", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reason, retry, blocked := classifyFailure(tc.status, tc.detail, nil, "http://local/v1")
			if reason.Code != "analysis.client."+tc.code || retry != tc.retry || blocked != tc.blocked || reason.Args["status"] != tc.status || reason.Args["detail"] != tc.detail {
				t.Fatalf("错误分类：%+v %v %v", reason, retry, blocked)
			}
		})
	}
	long := strings.Repeat("界", 210) + "quota secret-is-whole"
	reason, _, blocked := classifyFailure(429, long, nil, "")
	if !blocked || reason.Args["detail"] != long {
		t.Fatal("必须按完整正文分类，遮密前不能先截断")
	}
}

func TestProtocolNetworkClassificationPreservesCauses(t *testing.T) {
	for _, tc := range []struct {
		err        error
		base, code string
		retry      bool
	}{
		{fmt.Errorf("wrapped: %w", context.Canceled), "http://local/v1", "cancelled", false},
		{fmt.Errorf("wrapped: %w", context.DeadlineExceeded), "http://local/v1", "timeout", true},
		{&url.Error{Op: "Post", URL: "http://localhost/v1", Err: syscall.ECONNREFUSED}, "http://localhost/v1", "localhost", true},
		{syscall.ECONNREFUSED, "http://[::1]:1234/v1", "localhost", true},
		{syscall.ECONNREFUSED, "http://127.0.0.1:1234/v1", "localhost", true},
		{syscall.ECONNREFUSED, "http://192.0.2.1/v1", "network", true},
		{errors.New("network stopped"), "http://local/v1", "network", true},
	} {
		reason, retry, blocked := classifyFailure(0, "network detail", tc.err, tc.base)
		if reason.Code != "analysis.client."+tc.code || retry != tc.retry || blocked {
			t.Fatalf("网络错误分类：%+v %v %v", reason, retry, blocked)
		}
	}
}
