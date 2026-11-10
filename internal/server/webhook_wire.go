package server

import (
	"bytes"
	"encoding/json"

	"github.com/Seaony/Kakatta/internal/notification"
	"github.com/Seaony/Kakatta/internal/notify"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/settings"
)

func channelPatchFromHTTP(patch notification.ChannelPatch, currentType notify.Type) (notification.ChannelPatch, error) {
	if raw, present := patch["type"]; present {
		if err := json.Unmarshal(raw, &currentType); err != nil {
			return patch, nil
		}
	}
	raw, present := patch["config"]
	if currentType != notify.Webhook || !present {
		return patch, nil
	}
	var config map[string]json.RawMessage
	if json.Unmarshal(raw, &config) != nil || config == nil {
		return patch, nil
	}
	for _, field := range []string{"query_params", "body_template"} {
		value, present := config[field]
		if !present {
			continue
		}
		var text *string
		if json.Unmarshal(value, &text) != nil || text == nil || !json.Valid([]byte(*text)) || field == "query_params" && bytes.TrimSpace([]byte(*text))[0] != '{' {
			return nil, &service.ServiceError{Kind: service.ErrorInvalid, Reason: message("channels.invalid"), Fields: []settings.Issue{{Key: "config." + field, Reason: message("channels.invalid")}}}
		}
		config[field] = json.RawMessage(*text)
	}
	converted, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	patch["config"] = converted
	return patch, nil
}

func channelToHTTP(channel notification.Channel) (notification.Channel, error) {
	if channel.Type != notify.Webhook {
		return channel, nil
	}
	var config map[string]json.RawMessage
	if err := json.Unmarshal(channel.Config, &config); err != nil {
		return notification.Channel{}, err
	}
	for _, field := range []string{"query_params", "body_template"} {
		if value, present := config[field]; present {
			text, err := json.Marshal(string(value))
			if err != nil {
				return notification.Channel{}, err
			}
			config[field] = text
		}
	}
	raw, err := json.Marshal(config)
	channel.Config = raw
	return channel, err
}
