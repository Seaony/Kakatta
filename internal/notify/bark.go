package notify

import "context"

type barkSecrets struct {
	URL string `json:"url"`
}

func readBark(config ChannelConfig) (barkSecrets, error) {
	var options struct{}
	var secrets barkSecrets
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

func validateBark(config ChannelConfig) error {
	_, err := readBark(config)
	return err
}

func (s *Sender) sendBark(ctx context.Context, config ChannelConfig, message Message) Result {
	secrets, err := readBark(config)
	if err != nil {
		return failure("invalid_config")
	}
	level := "active"
	if message.Urgent {
		level = "timeSensitive"
	}
	payload := struct {
		Title string `json:"title"`
		Body  string `json:"body"`
		URL   string `json:"url,omitempty"`
		Group string `json:"group"`
		Icon  string `json:"icon,omitempty"`
		Level string `json:"level"`
	}{message.Title, plainBody(message), message.Link, "Kakatta", message.ImageURL, level}
	reply, result := s.sendJSON(ctx, secrets.URL, payload, nil, defaultTimeout)
	if !result.OK {
		return result
	}
	return codeResult(reply, "code", 200, true)
}
