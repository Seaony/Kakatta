package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
)

type feishuSecrets struct {
	URL    string `json:"url"`
	Secret string `json:"secret,omitempty"`
}

type feishuElement struct {
	Tag  string `json:"tag"`
	Text string `json:"text"`
	Href string `json:"href,omitempty"`
}

type feishuPost struct {
	Title   string            `json:"title"`
	Content [][]feishuElement `json:"content"`
}

func loadFeishu(config ChannelConfig) (feishuSecrets, error) {
	var options struct{}
	var secrets feishuSecrets
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

func validateFeishu(config ChannelConfig) error {
	_, err := loadFeishu(config)
	return err
}

func buildFeishuBody(message Message, secret, timestamp string) ([]byte, error) {
	rows := make([][]feishuElement, 0, 3)
	if message.ItemTitle != "" {
		rows = append(rows, []feishuElement{{Tag: "text", Text: message.ItemTitle}})
	}
	if message.Body != "" {
		rows = append(rows, []feishuElement{{Tag: "text", Text: message.Body}})
	}
	if message.Link != "" {
		rows = append(rows, []feishuElement{{Tag: "a", Text: linkLabel(message), Href: message.Link}})
	}
	payload := struct {
		Timestamp string `json:"timestamp,omitempty"`
		Sign      string `json:"sign,omitempty"`
		MsgType   string `json:"msg_type"`
		Content   struct {
			Post map[string]feishuPost `json:"post"`
		} `json:"content"`
	}{MsgType: "post", Content: struct {
		Post map[string]feishuPost `json:"post"`
	}{Post: map[string]feishuPost{"zh_cn": {Title: message.Title, Content: rows}}}}
	if secret != "" {
		payload.Timestamp = timestamp
		mac := hmac.New(sha256.New, []byte(payload.Timestamp+"\n"+secret))
		payload.Sign = base64.StdEncoding.EncodeToString(mac.Sum(nil))
	}
	return json.Marshal(payload)
}

func (s *Sender) sendFeishu(ctx context.Context, config ChannelConfig, message Message) Result {
	secrets, err := loadFeishu(config)
	if err != nil {
		return failure("invalid_config")
	}
	timestamp := ""
	if secrets.Secret != "" {
		timestamp = strconv.FormatInt(s.now().Unix(), 10)
	}
	body, err := buildFeishuBody(message, secrets.Secret, timestamp)
	if err != nil || len(body) > FeishuBodyBytes {
		return failure("invalid_message")
	}
	reply, result := s.sendRequest(ctx, http.MethodPost, secrets.URL, body, http.Header{"Content-Type": {"application/json; charset=utf-8"}}, defaultTimeout)
	if !result.OK {
		return result
	}
	return codeResult(reply, "code", 0, false)
}
