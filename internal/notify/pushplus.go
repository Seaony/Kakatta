package notify

import (
	"context"
	"strings"
)

type pushPlusConfig struct {
	Topic string `json:"topic"`
}

type pushPlusSecrets struct {
	Token string `json:"token"`
}

func parsePushPlus(channel ChannelConfig) (pushPlusConfig, pushPlusSecrets, error) {
	var config pushPlusConfig
	var secrets pushPlusSecrets
	if err := decodeObject(channel.Config, &config, "config"); err != nil {
		return config, secrets, err
	}
	if err := decodeObject(channel.Secrets, &secrets, "secrets"); err != nil {
		return config, secrets, err
	}
	if strings.TrimSpace(secrets.Token) == "" {
		return config, secrets, configError("token")
	}
	return config, secrets, nil
}

func validatePushPlus(channel ChannelConfig) error {
	_, _, err := parsePushPlus(channel)
	return err
}

func (s *Sender) sendPushPlus(ctx context.Context, channel ChannelConfig, message Message) Result {
	config, secrets, err := parsePushPlus(channel)
	if err != nil || !validHTTPURL(s.endpoints.PushPlus) {
		return failure("invalid_config")
	}
	parts := make([]string, 0, 4)
	if message.ItemTitle != "" {
		parts = append(parts, "<b>"+htmlLines(message.ItemTitle)+"</b>")
	}
	if message.Body != "" {
		parts = append(parts, htmlLines(message.Body))
	}
	if message.ImageURL != "" {
		parts = append(parts, `<img src="`+htmlText(message.ImageURL)+`" style="max-width:100%">`)
	}
	if message.Link != "" {
		parts = append(parts, `<a href="`+htmlText(message.Link)+`">`+htmlText(linkLabel(message))+`</a>`)
	}
	payload := struct {
		Token    string `json:"token"`
		Title    string `json:"title"`
		Content  string `json:"content"`
		Template string `json:"template"`
		Topic    string `json:"topic,omitempty"`
	}{secrets.Token, message.Title, strings.Join(parts, "<br>"), "html", config.Topic}
	reply, result := s.sendJSON(ctx, s.endpoints.PushPlus, payload, nil, defaultTimeout)
	if !result.OK {
		return result
	}
	return codeResult(reply, "code", 200, false)
}
