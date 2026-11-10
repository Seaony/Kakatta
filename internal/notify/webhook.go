package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type webhookConfig struct {
	Method       string          `json:"method"`
	QueryParams  json.RawMessage `json:"query_params"`
	ContentType  string          `json:"content_type"`
	BodyTemplate json.RawMessage `json:"body_template"`
}

type webhookSecrets struct {
	URL     string          `json:"url"`
	Headers json.RawMessage `json:"headers"`
}

func parseWebhook(channel ChannelConfig) (webhookConfig, webhookSecrets, error) {
	var config webhookConfig
	var secrets webhookSecrets
	if err := decodeObject(channel.Config, &config, "config"); err != nil {
		return config, secrets, err
	}
	if err := decodeObject(channel.Secrets, &secrets, "secrets"); err != nil {
		return config, secrets, err
	}
	if !validHTTPURL(secrets.URL) {
		return config, secrets, configError("url")
	}
	if config.Method == "" {
		config.Method = http.MethodPost
	}
	if config.Method != http.MethodGet && config.Method != http.MethodPost {
		return config, secrets, configError("method")
	}
	if config.ContentType == "" {
		config.ContentType = "JSON"
	}
	if config.ContentType != "JSON" && config.ContentType != "FORM" {
		return config, secrets, configError("content_type")
	}
	if len(config.QueryParams) != 0 {
		query, err := webhookJSON(config.QueryParams)
		if _, ok := query.(map[string]any); err != nil || !ok {
			return config, secrets, configError("query_params")
		}
	}
	if len(config.BodyTemplate) != 0 {
		body, err := webhookJSON(config.BodyTemplate)
		if err != nil {
			return config, secrets, configError("body_template")
		}
		if config.Method == http.MethodPost && config.ContentType == "FORM" {
			if _, ok := body.(map[string]any); !ok {
				return config, secrets, configError("body_template")
			}
		}
	}
	if _, err := webhookHeaderValues(secrets.Headers); err != nil {
		return config, secrets, err
	}
	return config, secrets, nil
}

func webhookHeaderValues(raw json.RawMessage) (map[string]string, error) {
	var headers map[string]string
	if err := decodeObject(raw, &headers, "headers"); err != nil {
		return nil, err
	}
	seenHeaders := make(map[string]bool)
	for name, value := range headers {
		canonicalName := http.CanonicalHeaderKey(name)
		if !validWebhookHeader(name, value) || seenHeaders[canonicalName] {
			return nil, configError("headers")
		}
		seenHeaders[canonicalName] = true
	}
	return headers, nil
}

func validateWebhook(channel ChannelConfig) error {
	_, _, err := parseWebhook(channel)
	return err
}

func validWebhookHeader(name, value string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", r)) {
			return false
		}
	}
	for _, r := range value {
		if r == 127 || r < 32 && r != '\t' {
			return false
		}
	}
	return true
}

func webhookJSON(raw json.RawMessage) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var result any
	if err := decoder.Decode(&result); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, configError("body_template")
	}
	return result, nil
}

func webhookReplacer(message Message) *strings.Replacer {
	values := []string{
		"title", message.Title, "content", webhookContent(message), "body", message.Body,
		"price", message.Price, "reason", message.Reason, "score", message.Score,
		"grade", message.Grade, "config", message.Config, "fair_price", message.FairPrice,
		"desktop_link", message.DesktopLink, "mobile_link", message.MobileLink,
		"link", message.Link, "kakatta_url", message.KakattaURL,
		"urgent", strconv.FormatBool(message.Urgent), "item_id", message.ItemID,
		"item_title", message.ItemTitle, "image", message.ImageURL,
		"task", message.Task, "kind", message.Kind,
	}
	replacements := make([]string, 0, len(values)*2)
	for i := 0; i < len(values); i += 2 {
		replacements = append(replacements, "{{"+values[i]+"}}", values[i+1], "${"+values[i]+"}", values[i+1])
	}
	return strings.NewReplacer(replacements...)
}

func renderWebhook(value any, replacer *strings.Replacer) any {
	switch value := value.(type) {
	case string:
		return replacer.Replace(value)
	case []any:
		for i := range value {
			value[i] = renderWebhook(value[i], replacer)
		}
	case map[string]any:
		for key := range value {
			value[key] = renderWebhook(value[key], replacer)
		}
	}
	return value
}

func webhookValues(object map[string]any) url.Values {
	values := make(url.Values, len(object))
	for key, value := range object {
		if text, ok := value.(string); ok {
			values.Set(key, text)
			continue
		}
		encoded, _ := json.Marshal(value)
		values.Set(key, string(encoded))
	}
	return values
}

func defaultWebhookBody(message Message) map[string]any {
	return map[string]any{
		"title": message.Title, "body": message.Body, "item_title": message.ItemTitle,
		"item_id": message.ItemID, "price": message.Price, "score": message.Score,
		"grade": message.Grade, "config": message.Config, "fair_price": message.FairPrice,
		"reason": message.Reason, "link": message.Link, "desktop_link": message.DesktopLink,
		"mobile_link": message.MobileLink, "kakatta_url": message.KakattaURL,
		"image": message.ImageURL, "task": message.Task, "kind": message.Kind,
		"urgent": strconv.FormatBool(message.Urgent),
	}
}

func (s *Sender) sendWebhook(ctx context.Context, channel ChannelConfig, message Message) Result {
	config, secrets, err := parseWebhook(channel)
	if err != nil {
		return failure("invalid_config")
	}
	replacer := webhookReplacer(message)
	address, _ := url.Parse(secrets.URL)
	if len(config.QueryParams) != 0 {
		query, _ := webhookJSON(config.QueryParams)
		query = renderWebhook(query, replacer)
		merged := address.Query()
		for name, values := range webhookValues(query.(map[string]any)) {
			merged[name] = values
		}
		address.RawQuery = merged.Encode()
	}
	headerValues, _ := webhookHeaderValues(secrets.Headers)
	headers := make(http.Header, len(headerValues)+1)
	for name, value := range headerValues {
		headers.Set(name, value)
	}
	var payload []byte
	if config.Method == http.MethodPost {
		var body any
		if len(config.BodyTemplate) == 0 {
			body = defaultWebhookBody(message)
			config.ContentType = "JSON"
		} else {
			body, _ = webhookJSON(config.BodyTemplate)
			body = renderWebhook(body, replacer)
		}
		if config.ContentType == "FORM" {
			payload = []byte(webhookValues(body.(map[string]any)).Encode())
			if headers.Get("Content-Type") == "" {
				headers.Set("Content-Type", "application/x-www-form-urlencoded")
			}
		} else {
			payload, err = json.Marshal(body)
			if err != nil {
				return failure("invalid_message")
			}
			if headers.Get("Content-Type") == "" {
				headers.Set("Content-Type", "application/json; charset=utf-8")
			}
		}
	}
	_, result := s.sendRequest(ctx, config.Method, address.String(), payload, headers, 15*time.Second)
	return result
}
