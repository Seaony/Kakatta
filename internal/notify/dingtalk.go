package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"strconv"
	"strings"
)

type dingTalkSecrets struct {
	URL    string `json:"url"`
	Secret string `json:"secret,omitempty"`
}

func loadDingTalk(config ChannelConfig) (dingTalkSecrets, error) {
	var options struct{}
	var secrets dingTalkSecrets
	if err := decodeObject(config.Config, &options, "config"); err != nil {
		return secrets, err
	}
	if err := decodeObject(config.Secrets, &secrets, "secrets"); err != nil {
		return secrets, err
	}
	if !validHTTPURL(secrets.URL) {
		return secrets, configError("url")
	}
	return secrets, nil
}

func validateDingTalk(config ChannelConfig) error {
	_, err := loadDingTalk(config)
	return err
}

func (s *Sender) sendDingTalk(ctx context.Context, config ChannelConfig, message Message) Result {
	secrets, err := loadDingTalk(config)
	if err != nil {
		return failure("invalid_config")
	}
	address, _ := url.Parse(secrets.URL)
	if secrets.Secret != "" {
		timestamp := strconv.FormatInt(s.now().UnixMilli(), 10)
		mac := hmac.New(sha256.New, []byte(secrets.Secret))
		mac.Write([]byte(timestamp + "\n" + secrets.Secret))
		query := address.Query()
		query.Set("timestamp", timestamp)
		query.Set("sign", base64.StdEncoding.EncodeToString(mac.Sum(nil)))
		address.RawQuery = query.Encode()
	}
	parts := []string{"#### " + markdownText(singleLine(message.Title))}
	if message.ItemTitle != "" {
		parts = append(parts, "**"+markdownText(singleLine(message.ItemTitle))+"**")
	}
	if message.Body != "" {
		parts = append(parts, markdownText(singleLine(message.Body)))
	}
	if message.ImageURL != "" {
		parts = append(parts, "![]("+dingTalkURL(message.ImageURL)+")")
	}
	if message.Link != "" {
		parts = append(parts, "["+markdownText(singleLine(linkLabel(message)))+"]("+dingTalkURL(message.Link)+")")
	}
	footer := message.Footer
	if footer == "" {
		footer = "来自 Kakatta"
	}
	parts = append(parts, markdownText(singleLine(footer)))
	payload := struct {
		MsgType  string `json:"msgtype"`
		Markdown struct {
			Title string `json:"title"`
			Text  string `json:"text"`
		} `json:"markdown"`
	}{MsgType: "markdown", Markdown: struct {
		Title string `json:"title"`
		Text  string `json:"text"`
	}{Title: singleLine(message.Title), Text: strings.Join(parts, "\n\n")}}
	reply, result := s.sendJSON(ctx, address.String(), payload, nil, defaultTimeout)
	if !result.OK {
		return result
	}
	return codeResult(reply, "errcode", 0, false)
}

func dingTalkURL(value string) string {
	return strings.NewReplacer("\\", "\\\\", "(", "\\(", ")", "\\)").Replace(singleLine(value))
}
