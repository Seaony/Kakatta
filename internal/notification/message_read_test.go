package notification

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Seaony/Kakatta/internal/locales"
)

func messageReadTx(t *testing.T, s *Service) *sql.Tx {
	t.Helper()
	tx, err := s.db.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tx.Rollback() })
	return tx
}

func messageReadFixture(t *testing.T, raw string, channelError bool) (*Service, int64) {
	t.Helper()
	s := lifecycleService(t)
	channel, err := s.CreateChannel(t.Context(), channelPatch(`{"type":"webhook","name":"本地历史消息","secrets":{"url":"http://127.0.0.1:1/not-networked"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if channelError {
		planExec(t, s.db, `UPDATE channels SET last_error=? WHERE id=?`, raw, channel.ID)
	}
	planExec(t, s.db, `INSERT INTO deliveries(id,channel_id,kind,ok,error) VALUES(1,?,'test',0,?)`, channel.ID, raw)
	planExec(t, s.db, `INSERT INTO notification_intents(id,finding_id,task_id,kind,state,created_at,updated_at,reason)
		VALUES(1,123,456,'recommended','pending',1,1,?)`, raw)
	planExec(t, s.db, `INSERT INTO system_events(id,kind,subject_id,message) VALUES(1,'login_failures',1,?)`, raw)
	return s, channel.ID
}

func assertMessageNumbers(t *testing.T, message *locales.Message, exponent bool) {
	t.Helper()
	if message == nil {
		t.Fatal("历史消息丢失")
	}
	for key, expected := range map[string]string{"integer": "9007199254740993", "decimal": "1.2300e+04"} {
		got, ok := message.Args[key].(json.Number)
		if !ok || got.String() != expected {
			t.Fatalf("历史参数被转成浮点：%s=%v (%T)", key, message.Args[key], message.Args[key])
		}
	}
	nested, ok := message.Args["nested"].(map[string]any)
	if !ok {
		t.Fatal("嵌套参数被改变")
	}
	number, ok := nested["number"].(json.Number)
	if !ok || number.String() != "9007199254740993123456789" {
		t.Fatalf("嵌套整数原文丢失：%v", nested)
	}
	if exponent {
		got, ok := message.Args["large"].(json.Number)
		if !ok || got.String() != "1e1000" {
			t.Fatalf("合法大指数被拒绝或改变：%v", message.Args["large"])
		}
	}
	encoded, err := json.Marshal(message)
	if err != nil || !strings.Contains(string(encoded), "9007199254740993") || !strings.Contains(string(encoded), "1.2300e+04") || exponent && !strings.Contains(string(encoded), "1e1000") {
		t.Fatal("消息再次输出丢失数字原文", err)
	}
}

type storedMessageReader struct {
	name         string
	channelError bool
	nullAllowed  bool
	read         func(*testing.T, *Service, int64) (*locales.Message, error)
}

func storedMessageReaders() []storedMessageReader {
	return []storedMessageReader{
		{"get_channel", true, true, func(t *testing.T, s *Service, id int64) (*locales.Message, error) {
			value, err := s.GetChannel(t.Context(), id)
			return value.LastError, err
		}},
		{"list_channels", true, true, func(t *testing.T, s *Service, _ int64) (*locales.Message, error) {
			values, err := s.ListChannels(t.Context())
			if err != nil || len(values) != 1 {
				return nil, err
			}
			return values[0].LastError, nil
		}},
		{"channel_deliveries", false, true, func(t *testing.T, s *Service, id int64) (*locales.Message, error) {
			values, err := s.ChannelDeliveries(t.Context(), id)
			if err != nil || len(values) != 1 {
				return nil, err
			}
			return values[0].Error, nil
		}},
		{"delivery_intent", false, false, func(t *testing.T, s *Service, _ int64) (*locales.Message, error) {
			tx := messageReadTx(t, s)
			defer tx.Rollback()
			value, err := loadDeliveryIntentTx(t.Context(), tx, 1)
			return value.Reason, err
		}},
		{"pending_intent", false, true, func(t *testing.T, s *Service, _ int64) (*locales.Message, error) {
			tx := messageReadTx(t, s)
			defer tx.Rollback()
			value, err := loadPendingPlannedIntentTx(t.Context(), tx, 123)
			if err != nil || value == nil {
				return nil, err
			}
			return value.Reason, nil
		}},
		{"system_event", false, false, func(t *testing.T, s *Service, _ int64) (*locales.Message, error) {
			tx := messageReadTx(t, s)
			defer tx.Rollback()
			value, err := s.loadSystemEventTx(t.Context(), tx, 1)
			return value.Message, err
		}},
	}
}

func TestStoredMessagesKeepNumberTokensOnEveryNotificationRead(t *testing.T) {
	for _, exponent := range []bool{false, true} {
		for _, reader := range storedMessageReaders() {
			t.Run(reader.name+map[bool]string{false: "/integer", true: "/exponent"}[exponent], func(t *testing.T) {
				args := `"integer":9007199254740993,"decimal":1.2300e+04,"nested":{"number":9007199254740993123456789}`
				if exponent {
					args += `,"large":1e1000`
				}
				raw := `{"code":"historical.unknown_notification","args":{` + args + `}}`
				s, channelID := messageReadFixture(t, raw, reader.channelError)
				tx := messageReadTx(t, s)
				if err := ValidateCandidate(t.Context(), tx, s.cipher); err != nil {
					t.Fatal("合法历史没有通过候选校验", err)
				}
				if err := tx.Rollback(); err != nil {
					t.Fatal(err)
				}
				message, err := reader.read(t, s, channelID)
				if err != nil {
					t.Fatal("候选允许的历史消息读失败", err)
				}
				assertMessageNumbers(t, message, exponent)
				var saved string
				if err := s.db.QueryRowContext(t.Context(), `SELECT reason FROM notification_intents WHERE id=1`).Scan(&saved); err != nil || saved != raw {
					t.Fatal("读取改写了持久消息", err)
				}
			})
		}
	}
}

func TestStoredMessageReadersRejectMalformedJSON(t *testing.T) {
	for _, reader := range storedMessageReaders() {
		for index, raw := range []string{`{`, `[]`, `{"code":"valid","args":[]}`, `{"code":"valid","args":{}} {}`} {
			t.Run(reader.name+"/"+string(rune('0'+index)), func(t *testing.T) {
				s, channelID := messageReadFixture(t, raw, reader.channelError)
				if message, err := reader.read(t, s, channelID); err == nil {
					t.Fatalf("坏历史消息被接受：%+v", message)
				}
			})
		}
	}
}

func TestStoredMessageReadersKeepExistingNullRules(t *testing.T) {
	for _, reader := range storedMessageReaders() {
		for _, sqlNull := range []bool{false, true} {
			t.Run(reader.name+map[bool]string{false: "/json_null", true: "/sql_null"}[sqlNull], func(t *testing.T) {
				s, channelID := messageReadFixture(t, `null`, reader.channelError)
				if sqlNull {
					planExec(t, s.db, `UPDATE channels SET last_error=NULL`)
					planExec(t, s.db, `UPDATE deliveries SET error=NULL`)
					planExec(t, s.db, `UPDATE notification_intents SET reason=NULL`)
					planExec(t, s.db, `UPDATE system_events SET message=NULL`)
				}
				message, err := reader.read(t, s, channelID)
				if err != nil && (sqlNull || reader.nullAllowed) || err == nil && (!sqlNull && !reader.nullAllowed || message != nil) {
					t.Fatalf("读取消息的原有 NULL 口径改变：%+v %v", message, err)
				}
			})
		}
	}
}

func TestStoredNestedSystemReasonKeepsNumberTokens(t *testing.T) {
	const raw = `{"code":"historical.outer","args":{"reason":{"code":"historical.inner","args":{"integer":9007199254740993,"decimal":1.2300e+04,"nested":{"number":9007199254740993123456789},"large":1e1000}}}}`
	s, _ := messageReadFixture(t, raw, false)
	tx := messageReadTx(t, s)
	defer tx.Rollback()
	event, err := s.loadSystemEventTx(t.Context(), tx, 1)
	if err != nil {
		t.Fatal(err)
	}
	message, err := formatNestedMessage(event.Message.Args["reason"])
	if err != nil {
		t.Fatal("已读取的嵌套系统原因再次解码失败", err)
	}
	assertMessageNumbers(t, &message, true)
}
