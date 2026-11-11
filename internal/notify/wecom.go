package notify

import "context"

type wecomSecrets struct {
	URL string `json:"url"`
}

type wecomArticle struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	URL         string `json:"url"`
	PicURL      string `json:"picurl,omitempty"`
}

type wecomPayload struct {
	MsgType string `json:"msgtype"`
	Text    *struct {
		Content string `json:"content"`
	} `json:"text,omitempty"`
	News *struct {
		Articles []wecomArticle `json:"articles"`
	} `json:"news,omitempty"`
}

func buildWeComPayload(message Message) wecomPayload {
	if message.TextOnly || message.Link == "" {
		return wecomPayload{MsgType: "text", Text: &struct {
			Content string `json:"content"`
		}{Content: withLink(joinNonEmpty("\n", message.Title, plainBody(message)), message.Link)}}
	}
	return wecomPayload{MsgType: "news", News: &struct {
		Articles []wecomArticle `json:"articles"`
	}{Articles: []wecomArticle{{Title: joinNonEmpty(" · ", message.Title, message.ItemTitle), Description: message.Body, URL: message.Link, PicURL: message.ImageURL}}}}
}

func wecomPayloadFits(payload wecomPayload) bool {
	if payload.Text != nil {
		return len(payload.Text.Content) <= WeComTextBytes
	}
	article := payload.News.Articles[0]
	return len(article.Title) <= WeComTitleBytes && len(article.Description) <= WeComDescriptionBytes
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
	payload := buildWeComPayload(message)
	if message.TextOnly {
		if !wecomPayloadFits(payload) {
			return failure("invalid_message")
		}
	} else if payload.Text != nil {
		payload.Text.Content = TruncateUTF8(payload.Text.Content, WeComTextBytes)
	} else {
		article := &payload.News.Articles[0]
		article.Title = TruncateUTF8(article.Title, WeComTitleBytes)
		article.Description = TruncateUTF8(article.Description, WeComDescriptionBytes)
	}
	reply, result := s.sendJSON(ctx, secrets.URL, payload, nil, defaultTimeout)
	if !result.OK {
		return result
	}
	return codeResult(reply, "errcode", 0, false)
}
