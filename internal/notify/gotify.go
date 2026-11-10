package notify

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

type gotifyConfig struct {
	URL string `json:"url"`
}

type gotifySecrets struct {
	Token string `json:"token"`
}

func readGotify(config ChannelConfig) (gotifyConfig, gotifySecrets, error) {
	var options gotifyConfig
	var secrets gotifySecrets
	if err := decodeObject(config.Config, &options, "config"); err != nil {
		return options, secrets, err
	}
	if err := decodeObject(config.Secrets, &secrets, "secrets"); err != nil {
		return options, secrets, err
	}
	if !validHTTPURL(options.URL) {
		return options, secrets, configError("url")
	}
	if secrets.Token == "" || strings.ContainsAny(secrets.Token, "\r\n") {
		return options, secrets, configError("token")
	}
	return options, secrets, nil
}

func validateGotify(config ChannelConfig) error {
	_, _, err := readGotify(config)
	return err
}

func (s *Sender) sendGotify(ctx context.Context, config ChannelConfig, message Message) Result {
	options, secrets, err := readGotify(config)
	if err != nil {
		return failure("invalid_config")
	}
	address, _ := url.Parse(options.URL)
	escapedPrefix := strings.TrimRight(address.EscapedPath(), "/")
	address.Path = strings.TrimRight(address.Path, "/") + "/message"
	address.RawPath = escapedPrefix + "/message"
	type click struct {
		URL string `json:"url"`
	}
	type notification struct {
		Click       *click `json:"click,omitempty"`
		BigImageURL string `json:"bigImageUrl,omitempty"`
	}
	type extras struct {
		Notification notification `json:"client::notification"`
	}
	payload := struct {
		Title    string  `json:"title"`
		Message  string  `json:"message"`
		Priority int     `json:"priority"`
		Extras   *extras `json:"extras,omitempty"`
	}{Title: message.Title, Message: withLink(plainBody(message), message.Link), Priority: 5}
	if message.Urgent {
		payload.Priority = 8
	}
	if message.Link != "" || message.ImageURL != "" {
		entry := notification{BigImageURL: message.ImageURL}
		if message.Link != "" {
			entry.Click = &click{URL: message.Link}
		}
		payload.Extras = &extras{Notification: entry}
	}
	headers := make(http.Header)
	headers.Set("X-Gotify-Key", secrets.Token)
	_, result := s.sendJSON(ctx, address.String(), payload, headers, defaultTimeout)
	return result
}
