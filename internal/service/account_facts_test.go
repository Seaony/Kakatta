package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

func accountEventMessage(t *testing.T, db *sql.DB, id int64, kind string) locales.Message {
	t.Helper()
	var raw string
	if err := db.QueryRowContext(t.Context(), `SELECT message FROM system_events WHERE subject_id=? AND kind=? AND resolved_at IS NULL`, id, kind).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var message locales.Message
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&message); err != nil {
		t.Fatal(err)
	}
	return message
}

func TestAccountDenyEventPersistsDeadlineAndEnvironment(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		env  bool
	}{{"直接拒绝", web.ErrDenied, false}, {"环境异常", web.ErrEnvBlocked, true}} {
		t.Run(test.name, func(t *testing.T) {
			db, values, now := accountStateDB(t)
			id := insertStateAccount(t, db, AccountActive)
			var until int64
			stateTx(t, db, func(ctx context.Context, tx *sql.Tx) error {
				account, err := applyAccountRiskTx(ctx, tx, id, test.err, "", values, now)
				if err == nil {
					until = *account.CooldownUntil
				}
				return err
			})
			message := accountEventMessage(t, db, id, "account_deny")
			deadline, ok := message.Args["until"].(json.Number)
			if !ok {
				t.Fatalf("拒绝事件没有实际UTC截止：%+v", message.Args)
			}
			got, err := deadline.Int64()
			if err != nil || got != until || message.Args["environment"] != test.env {
				t.Fatalf("拒绝事件事实不符：%+v %v", message.Args, err)
			}
		})
	}
}

func TestAccountLoginEventCountsOnlyItsActualTransfers(t *testing.T) {
	for _, active := range []int{0, 1, 2} {
		t.Run(string(rune('0'+active)), func(t *testing.T) {
			s := protectionTestService(t)
			id := accountTestInsert(t, s, "失效账号", AccountActive, 1)
			if active > 0 {
				accountTestInsert(t, s, "备用甲", AccountActive, 2)
			}
			if active > 1 {
				accountTestInsert(t, s, "备用乙", AccountActive, 3)
			}
			accountTestTask(t, s, "原本无绑定", nil, false, true)
			accountTestTask(t, s, "自动一", &id, false, true)
			accountTestTask(t, s, "自动二", &id, false, true)
			pinned := accountTestTask(t, s, "固定", &id, true, true)
			stateTx(t, s.db, func(ctx context.Context, tx *sql.Tx) error {
				snapshot, err := s.settings.SnapshotTx(ctx, tx)
				if err != nil {
					return err
				}
				_, err = applyAccountRiskTx(ctx, tx, id, web.ErrLoginExpired, "", snapshot.Values, s.now().UnixMilli())
				return err
			})
			message := accountEventMessage(t, s.db, id, "account_login")
			number, ok := message.Args["count"].(json.Number)
			if !ok {
				t.Fatalf("任务转移数不是整数：%+v", message.Args)
			}
			count, err := number.Int64()
			wantCount, wantNames := int64(0), ""
			if active == 1 {
				wantCount, wantNames = 2, "备用甲"
			} else if active == 2 {
				wantCount, wantNames = 2, "备用甲、备用乙"
			}
			if err != nil || count != wantCount || wantCount > 0 && message.Args["reassigned_to"] != wantNames {
				t.Fatalf("把解绑或其他无绑定任务当成已转移：%+v，期望%d/%s", message.Args, wantCount, wantNames)
			}
			var remains int64
			if err := s.db.QueryRowContext(t.Context(), `SELECT account_id FROM tasks WHERE id=?`, pinned).Scan(&remains); err != nil || remains != id {
				t.Fatal("补事件事实改变了真正的账号分配流程")
			}
		})
	}
}

func TestSuccessfulLoginReplacesObservedProfileIncludingMissingFields(t *testing.T) {
	for _, test := range []struct {
		name             string
		nickname, avatar *string
	}{{"都没取到", nil, nil}, {"只有昵称", new("新昵称"), nil}, {"只有头像", nil, new("https://fixture.invalid/new.jpg")}} {
		t.Run(test.name, func(t *testing.T) {
			db, _, now := accountStateDB(t)
			id := insertStateAccount(t, db, AccountExpired)
			if _, err := db.ExecContext(t.Context(), `UPDATE accounts SET nickname='旧账号昵称',avatar_url='https://fixture.invalid/old.jpg' WHERE id=?`, id); err != nil {
				t.Fatal(err)
			}
			stateTx(t, db, func(ctx context.Context, tx *sql.Tx) error {
				_, err := completeLoginTx(ctx, tx, id, test.nickname, test.avatar, now)
				return err
			})
			account := stateAccount(t, db, id)
			if account.Name != "测试账号" || account.Nickname == nil != (test.nickname == nil) || account.AvatarURL == nil != (test.avatar == nil) {
				t.Fatalf("没有的本次资料沿用了旧账号，或改了用户名称：%+v", account)
			}
			if test.nickname != nil && *account.Nickname != *test.nickname || test.avatar != nil && *account.AvatarURL != *test.avatar {
				t.Fatal("本次拿到的账号资料没有保存")
			}
		})
	}
}

func TestFailedLoginProfileReplacementRollsBackOldFields(t *testing.T) {
	db, _, now := accountStateDB(t)
	id := insertStateAccount(t, db, AccountExpired)
	if _, err := db.ExecContext(t.Context(), `UPDATE accounts SET nickname='原昵称',avatar_url='https://fixture.invalid/old.jpg' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO system_events(kind,subject_id,message,created_at) VALUES('account_login',?,'{"code":"accounts.login_expired","args":{}}',1)`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `CREATE TRIGGER login_profile_event_failure BEFORE UPDATE OF resolved_at ON system_events BEGIN SELECT RAISE(ABORT,'假事件写失败'); END`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := completeLoginTx(t.Context(), tx, id, nil, nil, now); err == nil {
		t.Fatal("登录完成事务失败没有返回错误")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	account := stateAccount(t, db, id)
	if account.State != AccountExpired || account.Nickname == nil || *account.Nickname != "原昵称" || account.AvatarURL == nil || *account.AvatarURL != "https://fixture.invalid/old.jpg" || stateEventCount(t, db, "account_login", true) != 1 {
		t.Fatal("失败后旧账号资料或失效事件被部分改写")
	}
}

func TestAccountMessageReaderKeepsLargeNumberTokens(t *testing.T) {
	s := protectionTestService(t)
	id := accountTestInsert(t, s, "历史消息", AccountActive, 1)
	const raw = `{"code":"historical.account","args":{"integer":9007199254740993123456789,"large":1e1000,"nested":{"number":9007199254740993123456790}}}`
	if _, err := s.db.ExecContext(t.Context(), `UPDATE accounts SET state_reason=? WHERE id=?`, raw, id); err != nil {
		t.Fatal(err)
	}
	view, err := s.GetAccount(t.Context(), id)
	if err != nil || view.StateReason == nil {
		t.Fatalf("通过恢复校验的消息不能读取：%+v %v", view, err)
	}
	for key, want := range map[string]string{"integer": "9007199254740993123456789", "large": "1e1000"} {
		value, ok := view.StateReason.Args[key].(json.Number)
		if !ok || value.String() != want {
			t.Fatalf("消息参数被转成浮点：%s=%v", key, view.StateReason.Args[key])
		}
	}
	encoded, err := json.Marshal(view.StateReason)
	if err != nil || !strings.Contains(string(encoded), "9007199254740993123456789") || !strings.Contains(string(encoded), "1e1000") {
		t.Fatal("消息再次输出时丢了数字原文", string(encoded), err)
	}
	taskID := accountTestTask(t, s, "消息读取", &id, true, true)
	tx := accountTestTx(t, s)
	task, err := loadTaskTx(t.Context(), tx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	start, err := s.preflightTx(t.Context(), tx, task, TriggerManual)
	if err != nil || start.Blocked != nil {
		t.Fatalf("合法历史参数让前置门禁报存储失败：%+v %v", start.Blocked, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var kept string
	if err := s.db.QueryRowContext(t.Context(), `SELECT state_reason FROM accounts WHERE id=?`, id).Scan(&kept); err != nil || kept != raw {
		t.Fatal("只读账号时改写了消息来源")
	}
}

func TestAccountMessageReaderStillRejectsInvalidMessages(t *testing.T) {
	s := protectionTestService(t)
	id := accountTestInsert(t, s, "坏消息", AccountActive, 1)
	for _, raw := range []string{`{`, `{"code":"valid","args":{}} {}`, `{"code":"","args":{}}`, `{"code":"valid","args":[]}`} {
		if _, err := s.db.ExecContext(t.Context(), `UPDATE accounts SET state_reason=? WHERE id=?`, raw, id); err != nil {
			t.Fatal(err)
		}
		if _, err := s.GetAccount(t.Context(), id); err == nil {
			t.Fatalf("坏账号消息被静默放过：%s", raw)
		}
	}
}
