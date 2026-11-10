package client

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
)

func buildRequest(ctx context.Context, provider Provider, request Request, compat Compat, purpose string, parseRetry bool) (*http.Request, error) {
	base, err := url.Parse(provider.BaseURL)
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, errors.New("analysis.client.invalid_request")
	}
	model := provider.TextModel
	if request.Kind == Analysis && purpose != "probe" {
		model = provider.VisionModel
	}
	if model == "" {
		model = provider.VisionModel
	}
	if strings.TrimSpace(model) == "" {
		return nil, errors.New("analysis.client.invalid_request")
	}
	maxTokens, temperature, structured, err := requestParameters(request.Kind, purpose, parseRetry)
	if err != nil {
		return nil, err
	}
	if purpose != "probe" && compat.MaxTokensFloor != nil {
		if *compat.MaxTokensFloor < 0 {
			return nil, errors.New("analysis.client.invalid_request")
		}
		maxTokens = max(maxTokens, *compat.MaxTokensFloor)
	}
	if compat.MaxTokensParam != "max_tokens" && compat.MaxTokensParam != "max_completion_tokens" {
		return nil, errors.New("analysis.client.invalid_request")
	}
	if len(request.Images) > 0 && (request.Kind != Analysis || purpose == "probe") {
		return nil, errors.New("analysis.client.invalid_request")
	}
	var userContent any = request.User
	if len(request.Images) > 0 {
		parts := make([]any, 0, len(request.Images)+1)
		for _, image := range request.Images {
			encoded, ok := strings.CutPrefix(image, "data:image/jpeg;base64,")
			if !ok || encoded == "" {
				return nil, errors.New("analysis.client.invalid_request")
			}
			if _, err := io.Copy(io.Discard, base64.NewDecoder(base64.StdEncoding, strings.NewReader(encoded))); err != nil {
				return nil, errors.New("analysis.client.invalid_request")
			}
			parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]string{"url": image}})
		}
		parts = append(parts, map[string]string{"type": "text", "text": request.User})
		userContent = parts
	}
	messages := make([]any, 0, 2)
	if request.System != "" {
		messages = append(messages, map[string]any{"role": "system", "content": request.System})
	}
	messages = append(messages, map[string]any{"role": "user", "content": userContent})
	params := map[string]any{"model": model, "messages": messages, compat.MaxTokensParam: maxTokens}
	if temperature != nil && compat.SendTemperature {
		params["temperature"] = *temperature
	}
	if structured {
		switch compat.JSONMode {
		case JSONSchema:
			if request.Schema == nil || request.Schema.Name == "" || !json.Valid(request.Schema.JSON) || !bytes.HasPrefix(bytes.TrimSpace(request.Schema.JSON), []byte("{")) {
				return nil, errors.New("analysis.client.invalid_request")
			}
			params["response_format"] = map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": request.Schema.Name, "strict": true, "schema": request.Schema.JSON}}
		case JSONObject:
			params["response_format"] = map[string]string{"type": "json_object"}
		case JSONText:
		default:
			return nil, errors.New("analysis.client.invalid_request")
		}
	}
	body, err := json.Marshal(params)
	if err != nil {
		return nil, errors.New("analysis.client.invalid_request")
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/chat/completions"
	base.RawPath = ""
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, base.String(), bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("analysis.client.invalid_request")
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	if provider.APIKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+provider.APIKey)
	}
	return httpRequest, nil
}

func requestParameters(kind Kind, purpose string, parseRetry bool) (int64, *float64, bool, error) {
	if purpose == "probe" {
		return 5, nil, false, nil
	}
	temperature := 0.1
	switch kind {
	case TitleScreen:
		return 3000, &temperature, true, nil
	case Analysis:
		if parseRetry {
			temperature = 0.05
		}
		return 2000, &temperature, true, nil
	case Profile:
		temperature = 0.3
		return 2000, &temperature, true, nil
	case Market:
		return 400, nil, false, nil
	default:
		return 0, nil, false, errors.New("analysis.client.invalid_request")
	}
}

type decodedResponse struct {
	text         string
	usage        Usage
	finishReason string
	thinking     bool
}

func decodeResponse(body []byte, probe bool) (decodedResponse, error) {
	var response struct {
		Choices []struct {
			Message struct {
				Content          json.RawMessage `json:"content"`
				ReasoningContent json.RawMessage `json:"reasoning_content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage json.RawMessage `json:"usage"`
	}
	var decoded decodedResponse
	if err := json.Unmarshal(body, &response); err != nil {
		if probe {
			return decoded, nil
		}
		return decoded, errors.New("analysis.client.parse")
	}
	usage, usageErr := decodeUsage(response.Usage)
	decoded.usage = usage
	decoded.thinking = usage.ReasoningTokens != nil && *usage.ReasoningTokens > 0
	if len(response.Choices) == 0 {
		if probe {
			return decoded, nil
		}
		return decoded, errors.New("analysis.client.parse")
	}
	first := response.Choices[0]
	decoded.finishReason = first.FinishReason
	content, contentErr := responseText(first.Message.Content)
	reasoning, reasoningErr := responseText(first.Message.ReasoningContent)
	decoded.thinking = decoded.thinking || strings.Contains(content, "<think>") || len(first.Message.ReasoningContent) > 0 && !bytes.Equal(bytes.TrimSpace(first.Message.ReasoningContent), []byte("null"))
	if strings.TrimSpace(content) == "" {
		content, contentErr = reasoning, reasoningErr
	}
	decoded.text = strings.TrimSpace(stripThinking(content))
	if probe {
		return decoded, nil
	}
	if usageErr != nil || contentErr != nil || decoded.text == "" {
		return decoded, errors.New("analysis.client.parse")
	}
	return decoded, nil
}

func responseText(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", err
	}
	var joined strings.Builder
	for _, part := range parts {
		if err := json.Unmarshal(part, &text); err == nil {
			joined.WriteString(text)
			continue
		}
		var object struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(part, &object); err == nil {
			joined.WriteString(object.Text)
		}
	}
	return joined.String(), nil
}

func stripThinking(text string) string {
	var out strings.Builder
	for {
		start := strings.Index(text, "<think>")
		if start < 0 {
			out.WriteString(text)
			return out.String()
		}
		end := strings.Index(text[start+len("<think>"):], "</think>")
		if end < 0 {
			out.WriteString(text)
			return out.String()
		}
		out.WriteString(text[:start])
		text = text[start+len("<think>")+end+len("</think>"):]
	}
}

func decodeUsage(raw json.RawMessage) (Usage, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return Usage{}, nil
	}
	var fields struct {
		Input             int64 `json:"prompt_tokens"`
		Output            int64 `json:"completion_tokens"`
		CompletionDetails struct {
			Reasoning *int64 `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return Usage{Present: true}, err
	}
	usage := Usage{Present: true, InputTokens: max(0, fields.Input), OutputTokens: max(0, fields.Output)}
	if fields.CompletionDetails.Reasoning != nil && *fields.CompletionDetails.Reasoning >= 0 {
		usage.ReasoningTokens = fields.CompletionDetails.Reasoning
	}
	if fields.Input < 0 || fields.Output < 0 || fields.CompletionDetails.Reasoning != nil && *fields.CompletionDetails.Reasoning < 0 || usage.InputTokens > math.MaxInt64-usage.OutputTokens {
		return usage, errors.New("analysis.client.parse")
	}
	return usage, nil
}

func compatibilityChange(status int, body []byte, current Compat, structured bool) (Compat, bool) {
	next := current
	if status < 400 || status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusPaymentRequired || status == http.StatusNotFound || status == http.StatusTooManyRequests {
		return next, false
	}
	text := strings.ToLower(string(body))
	changed := false
	if (status == http.StatusBadRequest || status == http.StatusUnprocessableEntity) && structured && containsAny(text, "response_format", "json_schema", "json_object", "text.format") {
		switch current.JSONMode {
		case JSONSchema:
			next.JSONMode, changed = JSONObject, true
		case JSONObject:
			next.JSONMode, changed = JSONText, true
		}
	}
	if current.SendTemperature && strings.Contains(text, "temperature") && containsAny(text, "not supported", "unsupported", "invalid", "参数错误") {
		next.SendTemperature, changed = false, true
	}
	if status == http.StatusBadRequest && current.MaxTokensParam == "max_tokens" && strings.Contains(text, "max_tokens") && strings.Contains(text, "max_completion_tokens") {
		next.MaxTokensParam, changed = "max_completion_tokens", true
	}
	return next, changed
}

func classifyFailure(status int, detail string, networkErr error, baseURL string) (Reason, bool, bool) {
	code, retryable, blocked := "protocol", false, false
	low := strings.ToLower(detail)
	switch {
	case networkErr != nil:
		var timeout net.Error
		switch {
		case errors.Is(networkErr, context.Canceled):
			code = "cancelled"
		case errors.Is(networkErr, context.DeadlineExceeded), errors.As(networkErr, &timeout) && timeout.Timeout():
			code, retryable = "timeout", true
		default:
			code, retryable = "network", true
			address, _ := url.Parse(baseURL)
			if address != nil && containsLocalHost(address.Hostname()) && errors.Is(networkErr, syscall.ECONNREFUSED) {
				code = "localhost"
			}
		}
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		code, blocked = "auth", true
	case status == http.StatusPaymentRequired:
		code, blocked = "balance", true
	case status == http.StatusNotFound:
		code, blocked = "not_found", true
	case status == http.StatusTooManyRequests:
		if containsAny(low, "quota", "额度", "exceeded today", "insufficient", "balance") {
			code, blocked = "daily_quota", true
		} else {
			code, retryable = "rate_limit", true
		}
	case containsAny(low, "insufficient", "balance", "quota", "余额", "欠费"):
		code, blocked = "balance", true
	case status == http.StatusBadRequest && containsAny(low, "image", "vision", "image_url", "multimodal", "图片", "图像"):
		code, blocked = "unsupported_image", true
	case status >= 500 && status <= 599:
		code, retryable = "server", true
	}
	return Reason{Code: "analysis.client." + code, Args: map[string]any{"status": status, "detail": detail}}, retryable, blocked
}

func containsAny(text string, parts ...string) bool {
	for _, part := range parts {
		if strings.Contains(text, part) {
			return true
		}
	}
	return false
}

func containsLocalHost(host string) bool {
	return strings.EqualFold(host, "localhost") || host == "127.0.0.1" || host == "::1"
}
