package notification

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"github.com/Seaony/Kakatta/internal/locales"
)

func decodeNotificationMessage(raw []byte) (*locales.Message, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var message *locales.Message
	if err := decoder.Decode(&message); err != nil {
		return nil, err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("通知消息包含多余内容")
	}
	return message, nil
}
