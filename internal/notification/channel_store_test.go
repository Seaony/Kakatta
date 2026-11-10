package notification

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Seaony/Kakatta/internal/notify"
	"github.com/Seaony/Kakatta/internal/security"
	"github.com/Seaony/Kakatta/internal/service"
)

func channelPatch(value string) ChannelPatch {
	var result ChannelPatch
	if err := json.Unmarshal([]byte(value), &result); err != nil {
		panic(err)
	}
	return result
}

func channelCipher(t *testing.T) *security.Cipher {
	t.Helper()
	cipher, err := security.NewCipher(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return cipher
}

func channelProblem(t *testing.T, err error, kind service.ErrorKind, code string) {
	t.Helper()
	var problem *service.ServiceError
	if !errors.As(err, &problem) || problem.Kind != kind || problem.Reason.Code != code {
		t.Fatalf("错误不符: %v", err)
	}
}

func TestChannelAllTypesKeepSecretsOutOfViews(t *testing.T) {
	s := &Service{cipher: channelCipher(t)}
	tests := []struct {
		kind    notify.Type
		config  string
		secrets string
		keys    []string
	}{
		{notify.Bark, `{}`, `{"url":"https://push.invalid/private-bark"}`, []string{"url"}},
		{notify.Telegram, `{"chat_id":"42","api_url":"https://proxy.invalid/bot-api"}`, `{"token":"private-telegram"}`, []string{"token"}},
		{notify.Ntfy, `{}`, `{"url":"https://push.invalid/private-topic","token":"private-ntfy"}`, []string{"url", "token"}},
		{notify.Gotify, `{"url":"https://gotify.invalid"}`, `{"token":"private-gotify"}`, []string{"token"}},
		{notify.WeCom, `{}`, `{"url":"https://push.invalid/private-wecom"}`, []string{"url"}},
		{notify.DingTalk, `{}`, `{"url":"https://push.invalid/private-dingtalk","secret":"private-sign"}`, []string{"url", "secret"}},
		{notify.Feishu, `{}`, `{"url":"https://push.invalid/private-feishu","secret":"private-sign"}`, []string{"url", "secret"}},
		{notify.PushPlus, `{"topic":"family"}`, `{"token":"private-pushplus"}`, []string{"token"}},
		{notify.ServerChan, `{}`, `{"send_key":"private-serverchan"}`, []string{"send_key"}},
		{notify.Webhook, `{"method":"POST","query_params":{"id":"{{item_id}}"},"content_type":"JSON","body_template":{"body":"{{body}}"}}`, `{"url":"https://push.invalid/private-hook","headers":{"Authorization":"private-header"}}`, []string{"url", "headers"}},
		{notify.Email, `{"host":"smtp.invalid","port":587,"encryption":"starttls","username":"sender","from":"from@example.invalid","to":"first@example.invalid, Second <second@example.invalid>"}`, `{"password":"private-password"}`, []string{"password"}},
	}
	for _, test := range tests {
		t.Run(string(test.kind), func(t *testing.T) {
			patch := channelPatch(`{"type":"` + string(test.kind) + `","name":"家人","config":` + test.config + `,"secrets":` + test.secrets + `}`)
			record, err := s.patchChannel(nil, patch)
			if err != nil {
				t.Fatal(err)
			}
			if !record.Enabled || len(record.SecretEnc) == 0 || bytes.Contains(record.SecretEnc, []byte("private-")) {
				t.Fatalf("未加密或默认状态错误: %v", record.Enabled)
			}
			view, err := s.channelView(record)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(view)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "private-") || strings.Contains(string(raw), "uuid") || strings.Contains(string(raw), "secret_enc") || strings.Contains(string(raw), record.UUID) {
				t.Fatalf("读取泄露敏感字段: %s", raw)
			}
			if len(view.SecretSet) != len(test.keys) {
				t.Fatalf("秘密标记不符: %v", view.SecretSet)
			}
			for _, key := range test.keys {
				if !view.SecretSet[key] {
					t.Fatalf("缺少秘密标记: %s", key)
				}
			}
		})
	}
}

func TestChannelSecretsMergeAndTypeChange(t *testing.T) {
	s := &Service{cipher: channelCipher(t)}
	original, err := s.patchChannel(nil, channelPatch(`{"type":"ntfy","name":"家人","secrets":{"url":"https://push.invalid/topic","token":"old-token"}}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, patch := range []string{`{"name":"另一群"}`, `{"secrets":{}}`} {
		updated, err := s.patchChannel(&original, channelPatch(patch))
		if err != nil {
			t.Fatal(err)
		}
		config, err := s.channelConfig(updated)
		if err != nil || !bytes.Contains(config.Secrets, []byte("old-token")) || !bytes.Contains(config.Secrets, []byte("https://push.invalid/topic")) {
			t.Fatalf("省略秘密应保留: %s, %v", config.Secrets, err)
		}
	}
	cleared, err := s.patchChannel(&original, channelPatch(`{"secrets":{"token":""}}`))
	if err != nil {
		t.Fatal(err)
	}
	view, err := s.channelView(cleared)
	if err != nil || view.SecretSet["token"] || !view.SecretSet["url"] {
		t.Fatalf("可选秘密没有清空: %v, %v", view.SecretSet, err)
	}
	_, err = s.patchChannel(&original, channelPatch(`{"secrets":{"url":""}}`))
	channelProblem(t, err, service.ErrorInvalid, "channels.invalid")
	_, err = s.patchChannel(&original, channelPatch(`{"type":"bark"}`))
	channelProblem(t, err, service.ErrorInvalid, "channels.invalid")
	changed, err := s.patchChannel(&original, channelPatch(`{"type":"bark","secrets":{"url":"https://push.invalid/new"}}`))
	if err != nil {
		t.Fatal(err)
	}
	config, err := s.channelConfig(changed)
	if err != nil || bytes.Contains(config.Secrets, []byte("old-token")) {
		t.Fatalf("类型切换继承了旧秘密: %s, %v", config.Secrets, err)
	}
}

func TestChannelWebhookHeadersReplaceAndAAD(t *testing.T) {
	s := &Service{cipher: channelCipher(t)}
	original, err := s.patchChannel(nil, channelPatch(`{"type":"webhook","name":"家人","secrets":{"url":"https://push.invalid/hook","headers":{"Authorization":"old","X-Private":"also-old"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	updated, err := s.patchChannel(&original, channelPatch(`{"secrets":{"headers":{"Authorization":"new"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	config, err := s.channelConfig(updated)
	if err != nil || bytes.Contains(config.Secrets, []byte("X-Private")) || !bytes.Contains(config.Secrets, []byte("new")) {
		t.Fatalf("请求头未整体替换: %s, %v", config.Secrets, err)
	}
	updated, err = s.patchChannel(&updated, channelPatch(`{"secrets":{"headers":{}}}`))
	if err != nil {
		t.Fatal(err)
	}
	view, err := s.channelView(updated)
	if err != nil || view.SecretSet["headers"] {
		t.Fatalf("请求头未清空: %v, %v", view.SecretSet, err)
	}
	updated.UUID = newChannelUUID()
	if _, err := s.channelConfig(updated); !errors.Is(err, security.ErrCiphertext) {
		t.Fatalf("跨行密文应拒绝: %v", err)
	}
	if _, err := s.cipher.Open(original.SecretEnc, "channels", "other_enc", original.UUID); !errors.Is(err, security.ErrCiphertext) {
		t.Fatalf("跨列密文应拒绝: %v", err)
	}
}

func TestChannelRejectsInvalidPatchesAndAllEmailRecipients(t *testing.T) {
	s := &Service{cipher: channelCipher(t)}
	valid := `{"type":"email","name":"家人","config":{"host":"smtp.invalid","port":25,"encryption":"none","from":"from@example.invalid","to":"first@example.invalid, broken@@example.invalid"}}`
	_, err := s.patchChannel(nil, channelPatch(valid))
	channelProblem(t, err, service.ErrorInvalid, "channels.invalid")
	for _, patch := range []string{
		`{"type":"bark","name":"家人","secrets":{"url":null}}`,
		`{"type":"bark","name":"家人","secrets":null}`,
		`{"type":"bark","name":"家人","secrets":{"url":"https://push.invalid","token":"extra"}}`,
		`{"type":"bark","name":"家人","config":{"url":"https://push.invalid"},"secrets":{"url":"https://push.invalid"}}`,
		`{"type":"bark","name":"家人","enabled":null,"secrets":{"url":"https://push.invalid"}}`,
		`{"type":"bark","name":"家人","uuid":"forged","secrets":{"url":"https://push.invalid"}}`,
		`{"type":"gotify","name":"家人","config":{"url":"https://user:password@push.invalid"},"secrets":{"token":"key"}}`,
		`{"type":"telegram","name":"家人","config":{"chat_id":"42","api_url":"https://user:password@push.invalid"},"secrets":{"token":"key"}}`,
	} {
		_, err := s.patchChannel(nil, channelPatch(patch))
		channelProblem(t, err, service.ErrorInvalid, "channels.invalid")
	}
}
