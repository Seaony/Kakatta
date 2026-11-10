package notify

import "context"

type wecomSecrets struct {
	URL string `json:"url"`
}

func loadWeCom(config ChannelConfig) (wecomSecrets, error) {
	var options struct{}
	var secrets wecomSecrets
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

func validateWeCom(config ChannelConfig) error {
	_, err := loadWeCom(config)
	return err
}

func (s *Sender) sendWeCom(ctx context.Context, config ChannelConfig, message Message) Result {
	secrets, err := loadWeCom(config)
	if err != nil {
		return failure("invalid_config")
	}
	var payload any
	if message.Link == "" {
		payload = struct {
			MsgType string `json:"msgtype"`
			Text    struct {
				Content string `json:"content"`
			} `json:"text"`
		}{MsgType: "text", Text: struct {
			Content string `json:"content"`
		}{Content: TruncateUTF8(joinNonEmpty("\n", message.Title, plainBody(message)), WeComTextBytes)}}
	} else {
		type article struct {
			Title       string `json:"title"`
			Description string `json:"description"`
			URL         string `json:"url"`
			PicURL      string `json:"picurl,omitempty"`
		}
		payload = struct {
			MsgType string `json:"msgtype"`
			News    struct {
				Articles []article `json:"articles"`
			} `json:"news"`
		}{MsgType: "news", News: struct {
			Articles []article `json:"articles"`
		}{Articles: []article{{
			Title:       TruncateUTF8(joinNonEmpty(" · ", message.Title, message.ItemTitle), WeComTitleBytes),
			Description: TruncateUTF8(message.Body, WeComDescriptionBytes),
			URL:         message.Link,
			PicURL:      message.ImageURL,
		}}}}
	}
	reply, result := s.sendJSON(ctx, secrets.URL, payload, nil, defaultTimeout)
	if !result.OK {
		return result
	}
	return codeResult(reply, "errcode", 0, false)
}
