package notification

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/notify"
	"github.com/Seaony/Kakatta/internal/security"
)

const candidateOldMessage = `{"code":"retired.notification.kind","args":{"old_shape":{"list":[true,null,"原话{count}"]},"integer":900719925474099312345678901234567890,"exponent":1e9999,"decimal":1.2300e+04}}`

func candidateFixture(t *testing.T) (*sql.DB, *security.Cipher) {
	t.Helper()
	db := deliveryDB(t)
	db.SetMaxOpenConns(1)
	return db, channelCipher(t)
}

func candidateInsertChannel(t *testing.T, db *sql.DB, cipher *security.Cipher, id int64, kind notify.Type, config, secrets string) {
	t.Helper()
	uuid := fmt.Sprintf("candidate-channel-%d", id)
	blob, err := cipher.Seal([]byte(secrets), "channels", "secret_enc", uuid)
	if err != nil {
		t.Fatal(err)
	}
	planExec(t, db, `INSERT INTO channels(id,uuid,type,name,enabled,config_json,secret_enc)
		VALUES(?,?,?,?,0,?,?)`, id, uuid, kind, fmt.Sprintf("停用未引用-%d", id), config, blob)
}

func candidateSeedHistory(t *testing.T, db *sql.DB) {
	t.Helper()
	planExec(t, db, `INSERT INTO deliveries(id,at,channel_id,task_id,event_id,kind,item_count,ok,error)
		VALUES(1,?,?,?,?, 'summary',100,0,?)`, int64(math.MaxInt64), 9901, 9902, 9903, candidateOldMessage)
	planExec(t, db, `INSERT INTO deliveries(id) VALUES(2)`)
	planExec(t, db, `INSERT INTO deliveries(id,kind,ok,error) VALUES(3,'test',1,'null')`)
	for i, kind := range []string{"recommended", "all_new", "price_drop", "system"} {
		planExec(t, db, `INSERT INTO deliveries(id,kind) VALUES(?,?)`, i+4, kind)
	}
	for i, kind := range []intentKind{intentRecommended, intentNew, intentPriceDrop, intentPriceUpgrade} {
		state := []intentState{intentPending, intentSent, intentAbandoned, intentCancelled}[i]
		failures := int64(0)
		if state == intentAbandoned {
			failures = 7
		}
		reason := candidateOldMessage
		if state == intentAbandoned {
			reason = `{"code":"notification.legacy_retry_unknown","args":{}}`
		}
		planExec(t, db, `INSERT INTO notification_intents(id,finding_id,task_id,kind,state,created_at,updated_at,price,previous_price,failure_count,reason)
			VALUES(?,?,?,?,?,?,?,?,?,?,?)`, i+1, 9910+i, 9920+i, kind, state, int64(math.MinInt64), int64(math.MaxInt64), nil, nil, failures, reason)
	}
	planExec(t, db, `INSERT INTO delivery_items(delivery_id,finding_id) VALUES(1,9910)`)
	planExec(t, db, `INSERT INTO delivery_intents(delivery_id,intent_id) VALUES(1,1)`)
	planExec(t, db, `INSERT INTO system_events(id) VALUES(1)`)
	for i, kind := range []string{"account_verify", "account_login", "account_deny", "ai_error", "ai_budget", "task_failure_pause", "xianyu_layout", "login_failures"} {
		planExec(t, db, `INSERT INTO system_events(id,kind,subject_id,message,created_at,resolved_at,notified_at,failure_count)
			VALUES(?,?,?,?,?,?,?,7)`, i+2, kind, 9930+i, candidateOldMessage, int64(math.MaxInt64), int64(math.MinInt64), nil)
	}
}

func candidateRunReadOnly(t *testing.T, ctx context.Context, db *sql.DB, cipher *security.Cipher) error {
	t.Helper()
	planExec(t, db, `PRAGMA query_only=ON`)
	limit, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	tx, err := db.BeginTx(limit, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var before, after int64
	if err := tx.QueryRowContext(limit, `SELECT total_changes()`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	err = ValidateCandidate(ctx, tx, cipher)
	if readErr := tx.QueryRowContext(limit, `SELECT total_changes()`).Scan(&after); readErr != nil {
		t.Fatal("校验留下未关闭的读取或占住单连接", readErr)
	}
	if before != after {
		t.Fatalf("候选校验写入了数据库：%d -> %d", before, after)
	}
	return err
}

func TestCandidateAllChannelsAndHistoricalExceptionsStayReadOnly(t *testing.T) {
	db, cipher := candidateFixture(t)
	webhookConfig := `{"method":"POST","query_params":{"integer":900719925474099312345678901234567890,"exponent":1e9999},"content_type":"JSON","body_template":{"raw":[1.2300e+04,900719925474099312345678901234567890,1e9999],"body":"{{body}}"}}`
	for i, channel := range []struct {
		kind            notify.Type
		config, secrets string
	}{
		{notify.Bark, `{}`, `{"url":"http://127.0.0.1:1/private-bark"}`},
		{notify.Telegram, `{"chat_id":"42","api_url":"http://127.0.0.1:1/bot-api"}`, `{"token":"PRIVATE_TELEGRAM"}`},
		{notify.Ntfy, `{}`, `{"url":"http://127.0.0.1:1/topic","token":"PRIVATE_NTFY"}`},
		{notify.Gotify, `{"url":"http://127.0.0.1:1"}`, `{"token":"PRIVATE_GOTIFY"}`},
		{notify.WeCom, `{}`, `{"url":"http://127.0.0.1:1/private-wecom"}`},
		{notify.DingTalk, `{}`, `{"url":"http://127.0.0.1:1/private-dingtalk","secret":"PRIVATE_SIGN"}`},
		{notify.Feishu, `{}`, `{"url":"http://127.0.0.1:1/private-feishu","secret":"PRIVATE_SIGN"}`},
		{notify.PushPlus, `{"topic":"family"}`, `{"token":"PRIVATE_PUSHPLUS"}`},
		{notify.ServerChan, `{}`, `{"send_key":"PRIVATE_SERVERCHAN"}`},
		{notify.Webhook, webhookConfig, `{"url":"http://127.0.0.1:1/private-hook","headers":{"Authorization":"PRIVATE_HEADER"}}`},
		{notify.Email, `{"host":"127.0.0.1","port":587,"encryption":"starttls","username":"sender","from":"from@example.invalid","to":"first@example.invalid, Second <second@example.invalid>"}`, `{"password":"PRIVATE_PASSWORD"}`},
	} {
		candidateInsertChannel(t, db, cipher, int64(i+1), channel.kind, channel.config, channel.secrets)
	}
	planExec(t, db, `UPDATE channels SET last_error=? WHERE id=1`, candidateOldMessage)
	planExec(t, db, `UPDATE channels SET last_error='null' WHERE id=2`)
	// 系统渠道引用由设置层校验，本入口不代替其职责。
	planExec(t, db, `INSERT INTO settings(key,value_json,updated_at) VALUES('notify.system_channel_ids','[999999]',0)`)
	candidateSeedHistory(t, db)
	var beforeCipher []byte
	if err := db.QueryRowContext(t.Context(), `SELECT secret_enc FROM channels WHERE id=10`).Scan(&beforeCipher); err != nil {
		t.Fatal(err)
	}
	if err := candidateRunReadOnly(t, t.Context(), db, cipher); err != nil {
		t.Fatal("合法停用渠道或旧历史被拒绝", err)
	}
	var config, message string
	var afterCipher []byte
	if err := db.QueryRowContext(t.Context(), `SELECT config_json,secret_enc FROM channels WHERE id=10`).Scan(&config, &afterCipher); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(t.Context(), `SELECT error FROM deliveries WHERE id=1`).Scan(&message); err != nil {
		t.Fatal(err)
	}
	if config != webhookConfig || message != candidateOldMessage || !bytes.Equal(beforeCipher, afterCipher) {
		t.Fatal("只读校验改写了不透明数字、历史参数或密文")
	}
}

func TestCandidateRejectsDisabledUnreferencedBadChannelsWithoutSecretsInErrors(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*testing.T, *sql.DB, *security.Cipher)
	}{
		{"broken_cipher", func(t *testing.T, db *sql.DB, _ *security.Cipher) {
			planExec(t, db, `UPDATE channels SET secret_enc=?`, []byte("PRIVATE_BROKEN_CIPHER"))
		}},
		{"different_row_aad", func(t *testing.T, db *sql.DB, _ *security.Cipher) {
			planExec(t, db, `UPDATE channels SET uuid='another-row'`)
		}},
		{"different_column_aad", func(t *testing.T, db *sql.DB, cipher *security.Cipher) {
			blob, err := cipher.Seal([]byte(`{"url":"http://127.0.0.1:1/PRIVATE_SECRET"}`), "channels", "other_enc", "candidate-channel-1")
			if err != nil {
				t.Fatal(err)
			}
			planExec(t, db, `UPDATE channels SET secret_enc=?`, blob)
		}},
		{"invalid_plain_json", func(t *testing.T, db *sql.DB, cipher *security.Cipher) {
			blob, err := cipher.Seal([]byte(`{"url":"PRIVATE_SECRET"`), "channels", "secret_enc", "candidate-channel-1")
			if err != nil {
				t.Fatal(err)
			}
			planExec(t, db, `UPDATE channels SET secret_enc=?`, blob)
		}},
		{"invalid_plain_utf8", func(t *testing.T, db *sql.DB, cipher *security.Cipher) {
			blob, err := cipher.Seal([]byte{'{', '"', 'u', 'r', 'l', '"', ':', '"', 0xff, '"', '}'}, "channels", "secret_enc", "candidate-channel-1")
			if err != nil {
				t.Fatal(err)
			}
			planExec(t, db, `UPDATE channels SET secret_enc=?`, blob)
		}},
		{"unknown_secret_key", func(t *testing.T, db *sql.DB, cipher *security.Cipher) {
			blob, err := cipher.Seal([]byte(`{"url":"http://127.0.0.1:1/PRIVATE_SECRET","PRIVATE_EXTRA":true}`), "channels", "secret_enc", "candidate-channel-1")
			if err != nil {
				t.Fatal(err)
			}
			planExec(t, db, `UPDATE channels SET secret_enc=?`, blob)
		}},
		{"missing_secret", func(t *testing.T, db *sql.DB, _ *security.Cipher) {
			planExec(t, db, `UPDATE channels SET secret_enc=NULL`)
		}},
		{"bad_config", func(t *testing.T, db *sql.DB, _ *security.Cipher) {
			planExec(t, db, `UPDATE channels SET config_json='{"PRIVATE_INVALID":'`)
		}},
		{"null_config", func(t *testing.T, db *sql.DB, _ *security.Cipher) {
			planExec(t, db, `UPDATE channels SET config_json='null'`)
		}},
		{"unknown_config_key", func(t *testing.T, db *sql.DB, _ *security.Cipher) {
			planExec(t, db, `UPDATE channels SET config_json='{"PRIVATE_EXTRA":true}'`)
		}},
		{"bad_config_utf8", func(t *testing.T, db *sql.DB, _ *security.Cipher) {
			planExec(t, db, `UPDATE channels SET config_json=?`, string([]byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'}))
		}},
		{"empty_name", func(t *testing.T, db *sql.DB, _ *security.Cipher) { planExec(t, db, `UPDATE channels SET name='   '`) }},
		{"bad_name_utf8", func(t *testing.T, db *sql.DB, _ *security.Cipher) {
			planExec(t, db, `UPDATE channels SET name=?`, string([]byte{0xff}))
		}},
		{"empty_uuid", func(t *testing.T, db *sql.DB, _ *security.Cipher) { planExec(t, db, `UPDATE channels SET uuid=''`) }},
		{"colon_uuid", func(t *testing.T, db *sql.DB, _ *security.Cipher) {
			planExec(t, db, `UPDATE channels SET uuid='bad:row'`)
		}},
		{"invalid_time_type", func(t *testing.T, db *sql.DB, _ *security.Cipher) {
			planExec(t, db, `UPDATE channels SET last_test_at='PRIVATE_NOT_INTEGER'`)
		}},
		{"public_url_credentials", func(t *testing.T, db *sql.DB, cipher *security.Cipher) {
			planExec(t, db, `DELETE FROM channels`)
			candidateInsertChannel(t, db, cipher, 1, notify.Gotify, `{"url":"http://PRIVATE_USER:PRIVATE_PASS@127.0.0.1:1"}`, `{"token":"PRIVATE_TOKEN"}`)
		}},
		{"invalid_email_recipient", func(t *testing.T, db *sql.DB, cipher *security.Cipher) {
			planExec(t, db, `DELETE FROM channels`)
			candidateInsertChannel(t, db, cipher, 1, notify.Email, `{"host":"127.0.0.1","port":25,"encryption":"none","from":"from@example.invalid","to":"good@example.invalid, PRIVATE_BAD@@example.invalid"}`, `{}`)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, cipher := candidateFixture(t)
			candidateInsertChannel(t, db, cipher, 1, notify.Webhook, `{}`, `{"url":"http://127.0.0.1:1/PRIVATE_SECRET"}`)
			test.edit(t, db, cipher)
			err := candidateRunReadOnly(t, t.Context(), db, cipher)
			if err == nil || !strings.HasPrefix(err.Error(), "通知候选 channels[1].") || strings.Contains(err.Error(), "PRIVATE_") || strings.Contains(err.Error(), "127.0.0.1") {
				t.Fatalf("坏停用渠道被放过或错误泄露内容：%v", err)
			}
		})
	}
}

func TestCandidateHistoricalMessagesKeepNullableFormsAndOpaqueArguments(t *testing.T) {
	for _, column := range []struct {
		table, field string
		allowNull    bool
	}{
		{"channels", "last_error", true}, {"deliveries", "error", true},
		{"notification_intents", "reason", false}, {"system_events", "message", false},
	} {
		t.Run(column.table+"/sql_null", func(t *testing.T) {
			db, cipher := candidateFixture(t)
			candidateInsertChannel(t, db, cipher, 1, notify.Webhook, `{}`, `{"url":"http://127.0.0.1:1/PRIVATE_SECRET"}`)
			candidateSeedHistory(t, db)
			planExec(t, db, `UPDATE `+column.table+` SET `+column.field+`=NULL WHERE id=1`)
			if err := candidateRunReadOnly(t, t.Context(), db, cipher); err != nil {
				t.Fatal("历史 SQL NULL 被拒绝", err)
			}
		})
		for _, message := range []string{candidateOldMessage, `{"code":"retired.code"}`, `{"code":"retired.code","args":null}`, `null`, `[]`, `{"code":""}`, `{"code":"retired.code","args":[]}`, `{"code":"retired.code"} {}`, `{"code":"PRIVATE_INVALID"`, string([]byte{'{', '"', 'c', 'o', 'd', 'e', '"', ':', '"', 0xff, '"', '}'})} {
			t.Run(column.table+"/"+fmt.Sprint(len(message))+"/"+message[:min(len(message), 8)], func(t *testing.T) {
				db, cipher := candidateFixture(t)
				candidateInsertChannel(t, db, cipher, 1, notify.Webhook, `{}`, `{"url":"http://127.0.0.1:1/PRIVATE_SECRET"}`)
				candidateSeedHistory(t, db)
				planExec(t, db, `UPDATE `+column.table+` SET `+column.field+`=? WHERE id=1`, message)
				err := candidateRunReadOnly(t, t.Context(), db, cipher)
				valid := message == candidateOldMessage || message == `{"code":"retired.code"}` || message == `{"code":"retired.code","args":null}` || message == `null` && column.allowNull
				if (err == nil) != valid || err != nil && (strings.Contains(err.Error(), "PRIVATE_") || !strings.HasPrefix(err.Error(), "通知候选 "+column.table+"[1].")) {
					t.Fatalf("历史 Message 兼容范围错误：valid=%t err=%v", valid, err)
				}
			})
		}
	}
}

func TestCandidateRejectsOnlyStoredHistoryFormatsAndEnums(t *testing.T) {
	for _, test := range []struct{ name, statement, table string }{
		{"channel_type", `UPDATE channels SET type='retired_type'`, "channels"},
		{"channel_enabled", `UPDATE channels SET enabled=2`, "channels"},
		{"channel_test_boolean", `UPDATE channels SET last_test_ok=2`, "channels"},
		{"delivery_kind", `UPDATE deliveries SET kind='new' WHERE id=1`, "deliveries"},
		{"delivery_boolean", `UPDATE deliveries SET ok=2 WHERE id=1`, "deliveries"},
		{"delivery_time_type", `UPDATE deliveries SET at='PRIVATE_TEXT' WHERE id=1`, "deliveries"},
		{"delivery_count_type", `UPDATE deliveries SET item_count=0.5 WHERE id=1`, "deliveries"},
		{"intent_kind", `UPDATE notification_intents SET kind='all_new' WHERE id=1`, "notification_intents"},
		{"intent_state", `UPDATE notification_intents SET state='expired' WHERE id=1`, "notification_intents"},
		{"intent_price", `UPDATE notification_intents SET price=-1 WHERE id=1`, "notification_intents"},
		{"intent_previous_price", `UPDATE notification_intents SET previous_price=-1 WHERE id=1`, "notification_intents"},
		{"intent_failures", `UPDATE notification_intents SET failure_count=-1 WHERE id=1`, "notification_intents"},
		{"intent_time_type", `UPDATE notification_intents SET first_failed_at='PRIVATE_TEXT' WHERE id=1`, "notification_intents"},
		{"event_kind", `UPDATE system_events SET kind='retired_event' WHERE id=1`, "system_events"},
		{"event_failures", `UPDATE system_events SET failure_count=-1 WHERE id=1`, "system_events"},
		{"event_subject_type", `UPDATE system_events SET subject_id='PRIVATE_TEXT' WHERE id=1`, "system_events"},
		{"event_time_type", `UPDATE system_events SET resolved_at=0.5 WHERE id=1`, "system_events"},
		{"delivery_item_type", `UPDATE delivery_items SET finding_id='PRIVATE_TEXT'`, "delivery_items"},
		{"delivery_intent_type", `UPDATE delivery_intents SET intent_id='PRIVATE_TEXT'`, "delivery_intents"},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, cipher := candidateFixture(t)
			candidateInsertChannel(t, db, cipher, 1, notify.Webhook, `{}`, `{"url":"http://127.0.0.1:1/PRIVATE_SECRET"}`)
			candidateSeedHistory(t, db)
			planExec(t, db, `PRAGMA ignore_check_constraints=ON`)
			if test.table == "delivery_intents" {
				planExec(t, db, `PRAGMA foreign_keys=OFF`)
			}
			planExec(t, db, test.statement)
			planExec(t, db, `PRAGMA foreign_keys=ON`)
			planExec(t, db, `PRAGMA ignore_check_constraints=OFF`)
			err := candidateRunReadOnly(t, t.Context(), db, cipher)
			if err == nil || !strings.HasPrefix(err.Error(), "通知候选 "+test.table+"[") || strings.Contains(err.Error(), "PRIVATE_") {
				t.Fatalf("坏历史格式被放过或回显原文：%v", err)
			}
		})
	}
}

func TestCandidateCancellationMissingKeyAndReadFailuresDoNotWrite(t *testing.T) {
	db, cipher := candidateFixture(t)
	candidateInsertChannel(t, db, cipher, 1, notify.Webhook, `{}`, `{"url":"http://127.0.0.1:1/PRIVATE_SECRET"}`)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := candidateRunReadOnly(t, ctx, db, cipher); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消原因丢失：%v", err)
	}
	deadline, expire := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer expire()
	if err := candidateRunReadOnly(t, deadline, db, cipher); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("超时原因丢失：%v", err)
	}
	if err := candidateRunReadOnly(t, t.Context(), db, nil); err == nil {
		t.Fatal("缺少密钥仍通过")
	}
	if err := candidateRunReadOnly(t, t.Context(), db, channelCipherWithOtherKey(t)); err == nil || strings.Contains(err.Error(), "PRIVATE_") {
		t.Fatalf("错误密钥被放过或回显秘密：%v", err)
	}
	if err := ValidateCandidate(t.Context(), nil, cipher); err == nil {
		t.Fatal("缺少候选事务仍通过")
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCandidate(t.Context(), tx, cipher); err == nil || err.Error() != "通知候选 channels[0].rows 无效" {
		t.Fatalf("失效事务返回不安全错误：%v", err)
	}
	planExec(t, db, `PRAGMA query_only=OFF`)
	planExec(t, db, `DROP TABLE system_events`)
	if err := candidateRunReadOnly(t, t.Context(), db, cipher); err == nil || err.Error() != "通知候选 system_events[0].rows 无效" {
		t.Fatalf("缺表读取错误没有安全返回：%v", err)
	}
}

func channelCipherWithOtherKey(t *testing.T) *security.Cipher {
	t.Helper()
	cipher, err := security.NewCipher(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return cipher
}

func TestCandidateAcceptsUnencryptedEmptyEmailSecrets(t *testing.T) {
	db, cipher := candidateFixture(t)
	planExec(t, db, `INSERT INTO channels(uuid,type,name,enabled,config_json) VALUES('empty-email','email','无认证邮件',0,
		'{"host":"127.0.0.1","port":25,"encryption":"none","from":"from@example.invalid","to":"to@example.invalid"}')`)
	if err := candidateRunReadOnly(t, t.Context(), db, cipher); err != nil {
		t.Fatal("合法无认证邮件被强制要求密文", err)
	}
}

func TestCandidateRejectsPresentEmptyCiphertext(t *testing.T) {
	for _, kind := range []notify.Type{notify.Email, notify.Webhook} {
		t.Run(string(kind), func(t *testing.T) {
			db, cipher := candidateFixture(t)
			config, secrets := `{}`, `{"url":"http://127.0.0.1:1/PRIVATE_SECRET"}`
			if kind == notify.Email {
				config, secrets = `{"host":"127.0.0.1","port":25,"encryption":"none","from":"from@example.invalid","to":"to@example.invalid"}`, `{}`
			}
			candidateInsertChannel(t, db, cipher, 1, kind, config, secrets)
			planExec(t, db, `UPDATE channels SET secret_enc=zeroblob(0) WHERE id=1`)
			var present bool
			var length int64
			if err := db.QueryRowContext(t.Context(), `SELECT secret_enc IS NOT NULL,length(secret_enc) FROM channels WHERE id=1`).Scan(&present, &length); err != nil || !present || length != 0 {
				t.Fatal("没有形成真实非空值的零字节密文夹具", err)
			}
			if err := candidateRunReadOnly(t, t.Context(), db, cipher); err == nil || err.Error() != "通知候选 channels[1].secret_enc 无效" {
				t.Fatalf("非 NULL 空密文被当成未配置：%v", err)
			}
		})
	}
}
