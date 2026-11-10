package notify

import (
	"html"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

func TruncateUTF8(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	for maxBytes > 0 && !utf8.RuneStart(value[maxBytes]) {
		maxBytes--
	}
	return value[:maxBytes]
}

func TruncateRunes(value string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes])
}

func UTF16Len(value string) int { return len(utf16.Encode([]rune(value))) }

func linkLabel(message Message) string {
	if message.LinkLabel != "" {
		return message.LinkLabel
	}
	return "在闲鱼打开"
}

func plainBody(message Message) string  { return joinNonEmpty("\n", message.ItemTitle, message.Body) }
func withLink(body, link string) string { return joinNonEmpty("\n", body, link) }

func joinNonEmpty(separator string, values ...string) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			parts = append(parts, value)
		}
	}
	return strings.Join(parts, separator)
}

func htmlText(value string) string { return html.EscapeString(value) }
func htmlLines(value string) string {
	return strings.ReplaceAll(htmlText(strings.ReplaceAll(value, "\r\n", "\n")), "\n", "<br>")
}
func singleLine(value string) string { return strings.NewReplacer("\r", "", "\n", "").Replace(value) }

func markdownText(value string) string {
	var result strings.Builder
	for _, r := range value {
		if strings.ContainsRune("\\`*_{}[]()<>#+-.!|~", r) {
			result.WriteByte('\\')
		}
		result.WriteRune(r)
	}
	return result.String()
}

func webhookContent(message Message) string {
	detail := joinNonEmpty(" · ", message.Price, message.Reason)
	return joinNonEmpty("\n", message.ItemTitle, detail, message.Link)
}
