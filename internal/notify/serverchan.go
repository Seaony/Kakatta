package notify

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

type serverChanSecrets struct {
	SendKey string `json:"send_key"`
}

var serverChanShard = regexp.MustCompile(`^sctp(\d+)t`)

func parseServerChan(channel ChannelConfig) (serverChanSecrets, error) {
	var config struct{}
	var secrets serverChanSecrets
	if err := decodeObject(channel.Config, &config, "config"); err != nil {
		return secrets, err
	}
	if err := decodeObject(channel.Secrets, &secrets, "secrets"); err != nil {
		return secrets, err
	}
	if strings.TrimSpace(secrets.SendKey) == "" || strings.ContainsAny(secrets.SendKey, "\r\n") {
		return secrets, configError("send_key")
	}
	if strings.HasPrefix(secrets.SendKey, "sctp") && serverChanShard.FindStringSubmatch(secrets.SendKey) == nil {
		return secrets, configError("send_key")
	}
	return secrets, nil
}

func validateServerChan(channel ChannelConfig) error {
	_, err := parseServerChan(channel)
	return err
}

func (s *Sender) sendServerChan(ctx context.Context, channel ChannelConfig, message Message) Result {
	secrets, err := parseServerChan(channel)
	if err != nil {
		return failure("invalid_config")
	}
	base := s.endpoints.ServerChanTurbo
	if shard := serverChanShard.FindStringSubmatch(secrets.SendKey); shard != nil {
		base = s.endpoints.ServerChan3(shard[1])
	}
	address := strings.TrimRight(base, "/") + "/" + url.PathEscape(secrets.SendKey) + ".send"
	if !validHTTPURL(address) {
		return failure("invalid_config")
	}
	parts := make([]string, 0, 4)
	if message.ItemTitle != "" {
		parts = append(parts, "**"+markdownText(message.ItemTitle)+"**")
	}
	if message.Body != "" {
		parts = append(parts, markdownText(message.Body))
	}
	imageURL, imageErr := url.Parse(message.ImageURL)
	if message.Kind != "system" && imageErr == nil && imageURL.Scheme == "https" && imageURL.Hostname() != "" {
		parts = append(parts, "![]("+markdownText(message.ImageURL)+")")
	}
	if message.Link != "" {
		parts = append(parts, "["+markdownText(singleLine(linkLabel(message)))+"]("+markdownText(message.Link)+")")
	}
	var short string
	if message.Kind != "system" {
		short = message.ItemTitle
	}
	payload := struct {
		Title       string `json:"title"`
		Description string `json:"desp"`
		Short       string `json:"short,omitempty"`
	}{TruncateRunes(message.Title, ServerChanTitleRunes), strings.Join(parts, "\n\n"), short}
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json;charset=utf-8")
	reply, result := s.sendJSON(ctx, address, payload, headers, defaultTimeout)
	if !result.OK {
		return result
	}
	return codeResult(reply, "code", 0, false)
}
