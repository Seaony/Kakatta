package client

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var urlCredentials = regexp.MustCompile(`(?i)\b(?:https?|socks5h?|socks4a?)://[^\s/@"<>]+@`)

func redactText(text string, secrets []string) string {
	// 先解开合法 JSON，避免转义后的密钥绕过直接替换。
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) == nil && decoder.Decode(new(any)) == io.EOF {
		if clean, err := json.Marshal(redactValue(value, secrets)); err == nil {
			return string(clean)
		}
	}
	return redactPlain(text, secrets)
}

func redactStructuredText(text string, secrets []string) string {
	// 和业务解析一样接受围栏或说明中的第一个 JSON 对象。
	for start, char := range text {
		if char != '{' {
			continue
		}
		decoder := json.NewDecoder(strings.NewReader(text[start:]))
		var object json.RawMessage
		if decoder.Decode(&object) == nil {
			end := start + int(decoder.InputOffset())
			return redactPlain(text[:start], secrets) + redactText(string(object), secrets) + redactPlain(text[end:], secrets)
		}
	}
	return redactPlain(text, secrets)
}

func redactPlain(text string, secrets []string) string {
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		encoded, _ := json.Marshal(secret)
		forms := []string{secret, strconv.QuoteToASCII(secret)[1 : len(strconv.QuoteToASCII(secret))-1], string(encoded[1 : len(encoded)-1]), url.QueryEscape(secret), url.PathEscape(secret)}
		for _, form := range forms {
			if form != "" {
				text = strings.ReplaceAll(text, form, "[REDACTED]")
			}
		}
	}
	return urlCredentials.ReplaceAllStringFunc(text, func(s string) string {
		scheme, _, _ := strings.Cut(s, "://")
		return scheme + "://[REDACTED]@"
	})
}

func redactValue(value any, secrets []string) any {
	switch v := value.(type) {
	case string:
		return redactPlain(v, secrets)
	case json.Number:
		if clean := redactPlain(string(v), secrets); clean != string(v) {
			return clean
		}
		return v
	case map[string]any:
		copy := make(map[string]any, len(v))
		for key, item := range v {
			copy[redactPlain(key, secrets)] = redactValue(item, secrets)
		}
		return copy
	case []any:
		copy := make([]any, len(v))
		for i, item := range v {
			copy[i] = redactValue(item, secrets)
		}
		return copy
	default:
		return value
	}
}

func cleanReason(reason Reason, secrets []string) Reason {
	args := make(map[string]any, len(reason.Args))
	for key, value := range reason.Args {
		args[key] = redactValue(value, secrets)
	}
	if detail, ok := reason.Args["detail"].(string); ok {
		clean := []rune(redactText(detail, secrets))
		if len(clean) > 200 {
			clean = clean[:200]
		}
		args["detail"] = string(clean)
	}
	return Reason{Code: reason.Code, Args: args}
}

type dependencyError struct {
	kind, cause error
	detail      string
}

func (e *dependencyError) Error() string   { return fmt.Sprintf("%s: %s", e.kind, e.detail) }
func (e *dependencyError) Unwrap() []error { return []error{e.kind, e.cause} }

func infrastructure(err error, secrets []string) error {
	kind := ErrInfrastructure
	if err == ErrConfigChanged {
		kind = ErrConfigChanged
	}
	reason := cleanReason(Reason{Args: map[string]any{"detail": err.Error()}}, secrets)
	return &dependencyError{kind: kind, cause: err, detail: reason.Args["detail"].(string)}
}
