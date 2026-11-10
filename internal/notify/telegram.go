package notify

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
)

type telegramConfig struct {
	ChatID string `json:"chat_id"`
	APIURL string `json:"api_url"`
}

type telegramSecrets struct {
	Token string `json:"token"`
}

func readTelegram(config ChannelConfig) (telegramConfig, telegramSecrets, error) {
	var options telegramConfig
	var secrets telegramSecrets
	if err := decodeObject(config.Config, &options, "config"); err != nil {
		return options, secrets, err
	}
	if err := decodeObject(config.Secrets, &secrets, "secrets"); err != nil {
		return options, secrets, err
	}
	if strings.TrimSpace(options.ChatID) == "" {
		return options, secrets, configError("chat_id")
	}
	if options.APIURL == "" {
		options.APIURL = "https://api.telegram.org"
	}
	if !validHTTPURL(options.APIURL) {
		return options, secrets, configError("api_url")
	}
	if secrets.Token == "" || strings.ContainsAny(secrets.Token, "/?# \t\r\n") {
		return options, secrets, configError("token")
	}
	return options, secrets, nil
}

func validateTelegram(config ChannelConfig) error {
	_, _, err := readTelegram(config)
	return err
}

func telegramHTML(message Message, itemTitle string) string {
	var lines []string
	if message.Title != "" {
		lines = append(lines, "<b>"+htmlText(message.Title)+"</b>")
	}
	if message.ItemTitle != "" {
		lines = append(lines, "<b>"+htmlText(itemTitle)+"</b>")
	}
	if message.Body != "" {
		lines = append(lines, htmlText(message.Body))
	}
	if message.Link != "" {
		lines = append(lines, `<a href="`+htmlText(message.Link)+`">`+htmlText(linkLabel(message))+`</a>`)
	}
	return strings.Join(lines, "\n")
}

func (s *Sender) sendTelegram(ctx context.Context, config ChannelConfig, message Message) Result {
	options, secrets, err := readTelegram(config)
	if err != nil {
		return failure("invalid_config")
	}
	method, limit := "sendMessage", TelegramTextLimit
	if message.ImageURL != "" {
		method, limit = "sendPhoto", TelegramCaptionLimit
	}
	itemTitle := []rune(TruncateRunes(message.ItemTitle, 40))
	content := telegramHTML(message, string(itemTitle))
	for len(itemTitle) > 0 && UTF16Len(content) > limit {
		itemTitle = itemTitle[:len(itemTitle)-1]
		content = telegramHTML(message, string(itemTitle))
	}
	if UTF16Len(content) > limit {
		return failure("invalid_message")
	}
	address, _ := url.Parse(options.APIURL)
	escapedPrefix := strings.TrimRight(address.EscapedPath(), "/")
	address.Path = strings.TrimRight(address.Path, "/") + "/bot" + secrets.Token + "/" + method
	address.RawPath = escapedPrefix + "/" + url.PathEscape("bot"+secrets.Token) + "/" + method
	payload := struct {
		ChatID    string `json:"chat_id"`
		Photo     string `json:"photo,omitempty"`
		Caption   string `json:"caption,omitempty"`
		Text      string `json:"text,omitempty"`
		ParseMode string `json:"parse_mode"`
	}{ChatID: options.ChatID, ParseMode: "HTML"}
	if method == "sendPhoto" {
		payload.Photo, payload.Caption = message.ImageURL, content
	} else {
		payload.Text = content
	}
	reply, result := s.sendJSON(ctx, address.String(), payload, nil, defaultTimeout)
	if !result.OK {
		return result
	}
	var response struct {
		OK *bool `json:"ok"`
	}
	if reply.ReadFailed || json.Unmarshal(reply.Body, &response) != nil || response.OK == nil {
		return Result{HTTPStatus: reply.Status, ErrorCode: "invalid_response"}
	}
	if !*response.OK {
		return Result{HTTPStatus: reply.Status, ErrorCode: "provider_rejected"}
	}
	return Result{OK: true, HTTPStatus: reply.Status}
}
