package notify

import (
	"context"
	"mime"
	"net/http"
	"strings"
)

type ntfySecrets struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

func readNtfy(config ChannelConfig) (ntfySecrets, error) {
	var options struct{}
	var secrets ntfySecrets
	if err := decodeObject(config.Config, &options, "config"); err != nil {
		return secrets, err
	}
	if err := decodeObject(config.Secrets, &secrets, "secrets"); err != nil {
		return secrets, err
	}
	if !validHTTPURL(secrets.URL) {
		return secrets, configError("url")
	}
	if strings.ContainsAny(secrets.Token, "\r\n") {
		return secrets, configError("token")
	}
	return secrets, nil
}

func validateNtfy(config ChannelConfig) error {
	_, err := readNtfy(config)
	return err
}

func (s *Sender) sendNtfy(ctx context.Context, config ChannelConfig, message Message) Result {
	secrets, err := readNtfy(config)
	if err != nil {
		return failure("invalid_config")
	}
	if strings.ContainsAny(message.Title+message.Link+message.ImageURL, "\r\n") {
		return failure("invalid_message")
	}
	headers := make(http.Header)
	headers.Set("Content-Type", "text/plain; charset=utf-8")
	headers.Set("Title", mime.BEncoding.Encode("UTF-8", message.Title))
	headers.Set("Priority", "default")
	if message.Urgent {
		headers.Set("Priority", "urgent")
	}
	headers.Set("Tags", "bell")
	if message.Link != "" {
		headers.Set("Click", message.Link)
	}
	if message.ImageURL != "" {
		headers.Set("Attach", message.ImageURL)
	}
	if secrets.Token != "" {
		headers.Set("Authorization", "Bearer "+secrets.Token)
	}
	_, result := s.sendRequest(ctx, http.MethodPost, secrets.URL, []byte(plainBody(message)), headers, defaultTimeout)
	return result
}
