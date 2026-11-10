package notification

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/notify"
	"github.com/Seaony/Kakatta/internal/security"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/settings"
)

const channelColumns = `id,uuid,type,name,enabled,config_json,secret_enc,last_test_at,last_test_ok,last_error`

type channelScanner interface{ Scan(...any) error }

func channelError(kind service.ErrorKind, code, field string) error {
	result := &service.ServiceError{Kind: kind, Reason: locales.Message{Code: code}, Fields: []settings.Issue{}}
	if field != "" {
		result.Fields = append(result.Fields, settings.Issue{Key: field, Reason: locales.Message{Code: code}})
	}
	return result
}

func scanChannel(row channelScanner) (channelRecord, error) {
	var result channelRecord
	var config string
	var lastError sql.NullString
	err := row.Scan(&result.ID, &result.UUID, &result.Type, &result.Name, &result.Enabled, &config, &result.SecretEnc,
		&result.LastTestAt, &result.LastTestOK, &lastError)
	if errors.Is(err, sql.ErrNoRows) {
		return channelRecord{}, channelError(service.ErrorNotFound, "channels.not_found", "")
	}
	if err != nil {
		return channelRecord{}, err
	}
	result.Config = json.RawMessage(config)
	if lastError.Valid {
		if err := json.Unmarshal([]byte(lastError.String), &result.LastError); err != nil {
			return channelRecord{}, err
		}
	}
	return result, nil
}

func (s *Service) loadChannelTx(ctx context.Context, tx *sql.Tx, id int64) (channelRecord, error) {
	return scanChannel(tx.QueryRowContext(ctx, `SELECT `+channelColumns+` FROM channels WHERE id=?`, id))
}

func channelSecretKeys(kind notify.Type) []string {
	switch kind {
	case notify.Bark, notify.WeCom:
		return []string{"url"}
	case notify.Telegram, notify.Gotify, notify.PushPlus:
		return []string{"token"}
	case notify.Ntfy:
		return []string{"url", "token"}
	case notify.DingTalk, notify.Feishu:
		return []string{"url", "secret"}
	case notify.ServerChan:
		return []string{"send_key"}
	case notify.Webhook:
		return []string{"url", "headers"}
	case notify.Email:
		return []string{"password"}
	default:
		return nil
	}
}

func channelObject(raw json.RawMessage, field string) (map[string]json.RawMessage, error) {
	if !utf8.Valid(raw) || len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '{' {
		return nil, channelError(service.ErrorInvalid, "channels.invalid", field)
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return nil, channelError(service.ErrorInvalid, "channels.invalid", field)
	}
	return object, nil
}

func (s *Service) channelSecrets(record channelRecord) (map[string]json.RawMessage, error) {
	if len(record.SecretEnc) == 0 {
		return map[string]json.RawMessage{}, nil
	}
	if s.cipher == nil {
		return nil, security.ErrMasterKey
	}
	plain, err := s.cipher.Open(record.SecretEnc, "channels", "secret_enc", record.UUID)
	if err != nil {
		return nil, err
	}
	defer clear(plain)
	return channelObject(plain, "secrets")
}

func (s *Service) channelConfig(record channelRecord) (notify.ChannelConfig, error) {
	secrets, err := s.channelSecrets(record)
	if err != nil {
		return notify.ChannelConfig{}, err
	}
	raw, err := json.Marshal(secrets)
	if err != nil {
		return notify.ChannelConfig{}, err
	}
	result := notify.ChannelConfig{Type: record.Type, Name: record.Name, Config: bytes.Clone(record.Config), Secrets: raw}
	if err := notify.Validate(result); err != nil {
		return notify.ChannelConfig{}, err
	}
	if err := validatePublicChannelAddress(record.Type, record.Config); err != nil {
		return notify.ChannelConfig{}, err
	}
	return result, nil
}

func (s *Service) channelView(record channelRecord) (Channel, error) {
	config, err := s.channelConfig(record)
	if err != nil {
		return Channel{}, err
	}
	defer clear(config.Secrets)
	var secrets map[string]json.RawMessage
	if err := json.Unmarshal(config.Secrets, &secrets); err != nil {
		return Channel{}, err
	}
	result := record.Channel
	result.Config = bytes.Clone(record.Config)
	result.SecretSet = make(map[string]bool)
	for _, key := range channelSecretKeys(record.Type) {
		value := secrets[key]
		if key == "headers" {
			var headers map[string]string
			if len(value) != 0 && json.Unmarshal(value, &headers) != nil {
				return Channel{}, channelError(service.ErrorInvalid, "channels.invalid", "secrets.headers")
			}
			result.SecretSet[key] = len(headers) != 0
		} else {
			var text string
			if len(value) != 0 && json.Unmarshal(value, &text) != nil {
				return Channel{}, channelError(service.ErrorInvalid, "channels.invalid", "secrets."+key)
			}
			result.SecretSet[key] = text != ""
		}
	}
	return result, nil
}

func newChannelUUID() string {
	var data [16]byte
	rand.Read(data[:])
	data[6], data[8] = data[6]&0x0f|0x40, data[8]&0x3f|0x80
	value := hex.EncodeToString(data[:])
	return value[:8] + "-" + value[8:12] + "-" + value[12:16] + "-" + value[16:20] + "-" + value[20:]
}

func (s *Service) patchChannel(previous *channelRecord, patch ChannelPatch) (channelRecord, error) {
	result := channelRecord{Channel: Channel{Enabled: true, Config: json.RawMessage(`{}`)}, UUID: newChannelUUID()}
	if previous != nil {
		result = *previous
	}
	for key, raw := range patch {
		if !utf8.Valid(raw) || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return channelRecord{}, channelError(service.ErrorInvalid, "channels.invalid", key)
		}
		switch key {
		case "type":
			if json.Unmarshal(raw, &result.Type) != nil {
				return channelRecord{}, channelError(service.ErrorInvalid, "channels.invalid", key)
			}
		case "name":
			if json.Unmarshal(raw, &result.Name) != nil {
				return channelRecord{}, channelError(service.ErrorInvalid, "channels.invalid", key)
			}
		case "enabled":
			if json.Unmarshal(raw, &result.Enabled) != nil {
				return channelRecord{}, channelError(service.ErrorInvalid, "channels.invalid", key)
			}
		case "config", "secrets":
		default:
			return channelRecord{}, channelError(service.ErrorInvalid, "channels.invalid", key)
		}
	}
	result.Name = strings.TrimSpace(result.Name)
	if result.Name == "" {
		return channelRecord{}, channelError(service.ErrorInvalid, "channels.invalid", "name")
	}
	if channelSecretKeys(result.Type) == nil {
		return channelRecord{}, channelError(service.ErrorInvalid, "channels.invalid", "type")
	}
	secrets := map[string]json.RawMessage{}
	if previous != nil && previous.Type == result.Type {
		var err error
		secrets, err = s.channelSecrets(*previous)
		if err != nil {
			return channelRecord{}, err
		}
	} else {
		result.Config = json.RawMessage(`{}`)
	}
	if raw, present := patch["config"]; present {
		if _, err := channelObject(raw, "config"); err != nil {
			return channelRecord{}, err
		}
		result.Config = bytes.Clone(raw)
	}
	if raw, present := patch["secrets"]; present {
		values, err := channelObject(raw, "secrets")
		if err != nil {
			return channelRecord{}, err
		}
		allowed := make(map[string]bool)
		for _, key := range channelSecretKeys(result.Type) {
			allowed[key] = true
		}
		for key, value := range values {
			if !allowed[key] || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return channelRecord{}, channelError(service.ErrorInvalid, "channels.invalid", "secrets."+key)
			}
			if key == "headers" {
				if _, err := channelObject(value, "secrets.headers"); err != nil {
					return channelRecord{}, err
				}
			} else {
				var text string
				if json.Unmarshal(value, &text) != nil {
					return channelRecord{}, channelError(service.ErrorInvalid, "channels.invalid", "secrets."+key)
				}
			}
			secrets[key] = bytes.Clone(value)
		}
	}
	secretJSON, err := json.Marshal(secrets)
	if err != nil {
		return channelRecord{}, err
	}
	config := notify.ChannelConfig{Type: result.Type, Name: result.Name, Config: result.Config, Secrets: secretJSON}
	if err := notify.Validate(config); err != nil {
		var invalid *notify.ConfigError
		if errors.As(err, &invalid) {
			field := "config." + invalid.Field
			if invalid.Field == "config" || invalid.Field == "secrets" {
				field = invalid.Field
			}
			for _, key := range channelSecretKeys(result.Type) {
				if key == invalid.Field {
					field = "secrets." + key
				}
			}
			return channelRecord{}, channelError(service.ErrorInvalid, "channels.invalid", field)
		}
		return channelRecord{}, err
	}
	if err := validatePublicChannelAddress(result.Type, result.Config); err != nil {
		return channelRecord{}, err
	}
	if s.cipher == nil {
		return channelRecord{}, security.ErrMasterKey
	}
	result.SecretEnc, err = s.cipher.Seal(secretJSON, "channels", "secret_enc", result.UUID)
	clear(secretJSON)
	if err != nil {
		return channelRecord{}, err
	}
	return result, nil
}

func validatePublicChannelAddress(kind notify.Type, config json.RawMessage) error {
	if kind != notify.Gotify && kind != notify.Telegram {
		return nil
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(config, &fields)
	key := "url"
	if kind == notify.Telegram {
		key = "api_url"
	}
	var value string
	_ = json.Unmarshal(fields[key], &value)
	address, _ := url.Parse(value)
	if address != nil && address.User != nil {
		return channelError(service.ErrorInvalid, "channels.invalid", "config."+key)
	}
	return nil
}

func checkChannelNameTx(ctx context.Context, tx *sql.Tx, record channelRecord) error {
	var duplicate bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM channels WHERE type=? AND name=? AND id<>?)`, record.Type, record.Name, record.ID).Scan(&duplicate); err != nil {
		return err
	}
	if duplicate {
		return channelError(service.ErrorInvalid, "channels.name_in_use", "name")
	}
	return nil
}
