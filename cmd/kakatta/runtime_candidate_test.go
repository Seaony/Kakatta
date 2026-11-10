package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/quota"
	"github.com/Seaony/Kakatta/internal/security"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/store"
)

const candidatePHC = "$argon2id$v=19$m=65536,t=3,p=4$AAECAwQFBgcICQoLDA0ODw$vUrOhcKV+zjYROFncankOSUoKsmCyGVr5Vou9s8g/Vg"

func candidateExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func candidateOpen(t *testing.T, path string) *store.Store {
	t.Helper()
	s, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if s.DB().Stats().MaxOpenConnections != 1 {
		t.Fatal("候选测试必须使用单连接数据库")
	}
	return s
}

func candidateClock(t *testing.T, db *sql.DB, clock quota.ClockState) {
	t.Helper()
	raw, err := json.Marshal(clock)
	if err != nil {
		t.Fatal(err)
	}
	candidateExec(t, db, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, appsettings.ClockKey, string(raw))
	candidateExec(t, db, `INSERT INTO meta(key,value) VALUES('quota.clock_initialized','1') ON CONFLICT(key) DO UPDATE SET value=excluded.value`)
}

func candidateFixture(t *testing.T) (string, *store.Store, [32]byte, [32]byte, time.Time) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "candidate.db")
	s := candidateOpen(t, path)
	var oldKey, newKey [32]byte
	for i := range oldKey {
		oldKey[i], newKey[i] = byte(i+1), byte(i+33)
	}
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	clock := quota.ClockState{Version: 1, HighestUTC: now.Add(-time.Hour).UnixMilli(), Timezone: "UTC", Windows: []quota.DayWindow{}}
	candidateClock(t, s.DB(), clock)
	cipher, err := security.NewCipher(oldKey[:])
	if err != nil {
		t.Fatal(err)
	}
	seal := func(plaintext, table, column, key string) []byte {
		t.Helper()
		blob, err := cipher.Seal([]byte(plaintext), table, column, key)
		if err != nil {
			t.Fatal(err)
		}
		return blob
	}
	candidateExec(t, s.DB(), `INSERT INTO admin(id,username,password_hash,totp_secret_enc,totp_enabled_at,totp_last_step,created_at,password_changed_at) VALUES(1,'本地用户',?,?,1,-1,-1000,-2000)`, candidatePHC, seal("JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP", "admin", "totp_secret_enc", "1"))
	for _, entry := range []struct {
		id   int
		uuid string
	}{{1, "proxy-one"}, {2, "proxy-two"}} {
		candidateExec(t, s.DB(), `INSERT INTO proxies(id,uuid,name,url_enc) VALUES(?,?,?,?)`, entry.id, entry.uuid, entry.uuid, seal("http://local-user:local-password@127.0.0.1:9", "proxies", "url_enc", entry.uuid))
	}
	candidateExec(t, s.DB(), `INSERT INTO ai_providers(id,uuid,name,base_url,api_key_enc,vision_model,enabled,sort_index) VALUES(1,'provider-one','未启用服务商','http://127.0.0.1:9/v1',?,'local',0,0)`, seal("local-model-key", "ai_providers", "api_key_enc", "provider-one"))
	candidateExec(t, s.DB(), `INSERT INTO ai_providers(id,uuid,name,base_url,vision_model,enabled,sort_index) VALUES(2,'provider-null','无密钥','http://127.0.0.1:9/v1','local',0,1)`)
	candidateExec(t, s.DB(), `INSERT INTO channels(id,uuid,type,name,enabled,config_json,secret_enc) VALUES(1,'channel-one','webhook','未启用渠道',0,'{}',?)`, seal(`{"url":"http://127.0.0.1:9"}`, "channels", "secret_enc", "channel-one"))
	candidateExec(t, s.DB(), `INSERT INTO channels(id,uuid,type,name,enabled,config_json) VALUES(2,'channel-null','ntfy','无密钥',0,'{}')`)
	for i, state := range []string{"active", "cooling", "denied", "denied"} {
		var until any
		if i > 0 {
			until = now.Add(time.Hour).UnixMilli()
		}
		if i == 3 {
			until = now.Add(-time.Hour).UnixMilli()
		}
		candidateExec(t, s.DB(), `INSERT INTO accounts(id,name,state,state_reason,state_changed_at,cooldown_until,verify_level,verify_url,next_gap_seconds) VALUES(?,?,?,'{"code":"old.reason","args":{}}',111,?,4,'old-url',240)`, i+1, state, state, until)
	}
	for _, kind := range []string{"web_session", "api_token"} {
		hash := security.HashToken(kind)
		candidateExec(t, s.DB(), `INSERT INTO auth_tokens(kind,token_hash,name,created_at) VALUES(?,?,?,1)`, kind, hash[:], kind)
	}
	generation, _ := security.NewToken()
	candidateExec(t, s.DB(), `INSERT INTO meta(key,value) VALUES('auth.generation',?),('runtime.session.1','old'),('runtime.session.999','old'),('preserve.meta','keep')`, generation)
	candidateExec(t, s.DB(), `INSERT INTO quota_events(account_id,kind,at) VALUES(1,'search',1)`)
	candidateExec(t, s.DB(), `INSERT INTO tasks(id,name,keyword,max_pages,interval_minutes,judge_mode,notify_policy,sort_index,last_error) VALUES(1,'本地任务','本地',1,30,'keyword','none',0,'{"code":"old.task","args":{}}')`)
	candidateExec(t, s.DB(), `INSERT INTO ai_calls(at,provider_id,model,purpose,input_tokens,output_tokens) VALUES(9999999999999,1,'local','analysis',123,456)`)
	candidateExec(t, s.DB(), `INSERT INTO notification_intents(finding_id,task_id,kind,state,created_at,updated_at) VALUES(100,1,'recommended','pending',1,2)`)
	candidateExec(t, s.DB(), `INSERT INTO run_records(task_id,task_name,trigger,outcome,started_at,ended_at,message) VALUES(1,'本地任务','auto','failed',1,2,'{"code":"old.run","args":{}}')`)
	return path, s, oldKey, newKey, now
}

func candidateSnapshot(t *testing.T, db *sql.DB, tables ...string) string {
	t.Helper()
	var snapshot []any
	for _, table := range tables {
		rows, err := db.QueryContext(t.Context(), `SELECT * FROM `+table+` ORDER BY 1`)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			snapshot = append(snapshot, []any{table, values})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		rows.Close()
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestRuntimeCandidateRekeysAllSecretsAndPreservesBusinessData(t *testing.T) {
	for _, test := range []struct {
		name string
		same bool
	}{{"different-key", false}, {"same-key", true}} {
		t.Run(test.name, func(t *testing.T) {
			path, s, oldKey, newKey, now := candidateFixture(t)
			if test.same {
				newKey = oldKey
			}
			preserved := []string{"settings", "tasks", "ai_calls", "notification_intents", "run_records", "sqlite_sequence"}
			before := candidateSnapshot(t, s.DB(), preserved...)
			beforeSecrets := candidateSnapshot(t, s.DB(), "admin", "proxies", "ai_providers", "channels")
			host, err := readRuntimeClock(t.Context(), s.DB())
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if err := prepareRuntimeCandidate(ctx, path, oldKey, newKey, host, settings.Environment{TZ: "UTC"}, now); err != nil {
				t.Fatal(err)
			}
			// 只复制主文件，证明成功结果不依赖候选旁边的 WAL。
			main, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			copyPath := filepath.Join(t.TempDir(), "main-only.db")
			if err := os.WriteFile(copyPath, main, 0600); err != nil {
				t.Fatal(err)
			}
			result := candidateOpen(t, copyPath)
			db := result.DB()
			if candidateSnapshot(t, db, preserved...) != before {
				t.Fatal("恢复修改了业务历史、通知任务、用量或编号水位")
			}
			if test.same && candidateSnapshot(t, db, "admin", "proxies", "ai_providers", "channels") != beforeSecrets {
				t.Fatal("相同主密钥不应改写密文")
			}
			cipher, _ := security.NewCipher(newKey[:])
			oldCipher, _ := security.NewCipher(oldKey[:])
			wantSecrets := map[string]string{
				"1": "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP", "provider-one": "local-model-key", "channel-one": `{"url":"http://127.0.0.1:9"}`,
				"proxy-one": "http://local-user:local-password@127.0.0.1:9", "proxy-two": "http://local-user:local-password@127.0.0.1:9",
			}
			for _, field := range []struct{ table, column, rowKey string }{
				{"admin", "totp_secret_enc", "CAST(id AS TEXT)"}, {"proxies", "url_enc", "uuid"},
				{"ai_providers", "api_key_enc", "uuid"}, {"channels", "secret_enc", "uuid"},
			} {
				rows, err := db.QueryContext(t.Context(), `SELECT `+field.rowKey+`,`+field.column+` FROM `+field.table+` WHERE `+field.column+` IS NOT NULL`)
				if err != nil {
					t.Fatal(err)
				}
				for rows.Next() {
					var rowKey string
					var blob []byte
					if err := rows.Scan(&rowKey, &blob); err != nil {
						rows.Close()
						t.Fatal(err)
					}
					plain, err := cipher.Open(blob, field.table, field.column, rowKey)
					want, exists := wantSecrets[rowKey]
					matches := exists && bytes.Equal(plain, []byte(want))
					clear(plain)
					if err != nil || !matches {
						rows.Close()
						t.Fatal("目标密钥、原AAD或还原内容不符")
					}
					delete(wantSecrets, rowKey)
					if !test.same {
						plain, err := oldCipher.Open(blob, field.table, field.column, rowKey)
						clear(plain)
						if err == nil {
							rows.Close()
							t.Fatal("仍然保留旧主密钥密文")
						}
					}
				}
				if err := rows.Err(); err != nil {
					rows.Close()
					t.Fatal(err)
				}
				rows.Close()
			}
			if len(wantSecrets) != 0 {
				t.Fatal("恢复丢失密文记录")
			}
			var remains int
			if err := db.QueryRowContext(t.Context(), `SELECT (SELECT count(*) FROM auth_tokens)+(SELECT count(*) FROM quota_events)+(SELECT count(*) FROM meta WHERE key='auth.generation' OR key GLOB 'runtime.session.*')`).Scan(&remains); err != nil || remains != 0 {
				t.Fatal("恢复未清空登录凭证、运行会话或访问记录", err)
			}
			var untouched int
			if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM meta WHERE key='preserve.meta' AND value='keep'`).Scan(&untouched); err != nil || untouched != 1 {
				t.Fatal("恢复改写了无关元数据", err)
			}
			for id, want := range []string{"signed_out", "cooling", "denied", "signed_out"} {
				var state string
				var verify sql.NullString
				var level, gap, markers int64
				if err := db.QueryRowContext(t.Context(), `SELECT state,verify_url,verify_level,next_gap_seconds,(SELECT count(*) FROM meta WHERE key='account.logged_out.'||accounts.id) FROM accounts WHERE id=?`, id+1).Scan(&state, &verify, &level, &gap, &markers); err != nil {
					t.Fatal(err)
				}
				if state != want || verify.Valid || level != 4 || gap != 240 || (markers == 1) != (id == 1 || id == 2) {
					t.Fatal("退出规则没有保留未过期暂停及既有保护事实")
				}
			}
			clock, err := readRuntimeClock(t.Context(), db)
			if err != nil || clock.HighestUTC != now.UnixMilli() {
				t.Fatal("恢复时钟未持久化", err)
			}
		})
	}
}

func TestRuntimeCandidateFailuresRollbackTheWholeCandidate(t *testing.T) {
	for _, test := range []struct{ name, mutation string }{
		{"bad-admin-secret", `UPDATE admin SET totp_secret_enc=x'00'`},
		{"swapped-row-aad", `UPDATE proxies SET url_enc=(SELECT url_enc FROM proxies WHERE id=2) WHERE id=1`},
		{"swapped-table-aad", `UPDATE ai_providers SET api_key_enc=(SELECT url_enc FROM proxies WHERE id=1) WHERE id=1`},
		{"empty-secret", `UPDATE ai_providers SET api_key_enc=zeroblob(0) WHERE id=1`},
		{"last-secret-corrupted", `UPDATE channels SET secret_enc=x'00' WHERE id=1`},
		{"missing-admin", `DELETE FROM admin`},
		{"bad-clock", `UPDATE meta SET value='{}' WHERE key='quota.clock'`},
		{"settings-error", `INSERT INTO settings(key,value_json) VALUES('general.timezone','false')`},
		{"late-query-error", `CREATE TRIGGER reject_restore_cleanup BEFORE DELETE ON auth_tokens BEGIN SELECT RAISE(ABORT,'local failure'); END`},
	} {
		t.Run(test.name, func(t *testing.T) {
			path, s, oldKey, newKey, now := candidateFixture(t)
			if test.name == "last-secret-corrupted" {
				// 即使不用重加密，也要检查停用渠道的密文。
				newKey = oldKey
			}
			host, err := readRuntimeClock(t.Context(), s.DB())
			if err != nil {
				t.Fatal(err)
			}
			candidateExec(t, s.DB(), test.mutation)
			tables := []string{"admin", "proxies", "ai_providers", "channels", "accounts", "meta", "auth_tokens", "quota_events", "ai_calls", "tasks", "notification_intents", "run_records", "sqlite_sequence"}
			before := candidateSnapshot(t, s.DB(), tables...)
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if err := prepareRuntimeCandidate(ctx, path, oldKey, newKey, host, settings.Environment{TZ: "UTC"}, now); err == nil {
				t.Fatal("损坏或读写失败的候选被接受")
			}
			if candidateSnapshot(t, candidateOpen(t, path).DB(), tables...) != before {
				t.Fatal("候选失败后仍留下部分密文、账号、时钟或凭证修改")
			}
		})
	}
}

func TestRuntimeCandidateClockMergesAtEffectiveNow(t *testing.T) {
	path, s, oldKey, newKey, now := candidateFixture(t)
	latest := now.Add(2 * time.Hour).UnixMilli()
	oldStart := now.Add(-time.Hour).UnixMilli()
	host := quota.ClockState{Version: 1, HighestUTC: latest, Timezone: "America/Los_Angeles", Windows: []quota.DayWindow{
		{Start: latest - 30000, End: latest + 10000}, {Start: latest - 40000, End: latest - 1},
	}}
	restored := quota.ClockState{Version: 1, HighestUTC: now.UnixMilli(), Timezone: "UTC", Windows: []quota.DayWindow{
		{Start: oldStart, End: latest + 10000}, {Start: oldStart - 1000, End: latest + 20000},
	}}
	candidateClock(t, s.DB(), restored)
	candidateExec(t, s.DB(), `INSERT INTO settings(key,value_json) VALUES('general.timezone','"Asia/Shanghai"')`)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := prepareRuntimeCandidate(t.Context(), path, oldKey, newKey, host, settings.Environment{TZ: "Europe/London"}, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	db := candidateOpen(t, path).DB()
	actual, err := readRuntimeClock(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	if actual.HighestUTC != latest || actual.Timezone != "Asia/Shanghai" {
		t.Fatal("恢复未保留最高时间或没有采用恢复后的设置时区")
	}
	var stillPaused int
	if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM accounts WHERE state IN ('cooling','denied')`).Scan(&stillPaused); err != nil || stillPaused != 0 {
		t.Fatal("账号退出没有按合并后的有效时钟判断暂停期限", err)
	}
	want := map[int64]int64{latest + 10000: oldStart, latest + 20000: oldStart - 1000}
	for _, timezone := range []string{host.Timezone, restored.Timezone} {
		location, err := time.LoadLocation(timezone)
		if err != nil {
			t.Fatal(err)
		}
		start, end := quota.DayBounds(time.UnixMilli(latest).In(location))
		if prior, exists := want[end.UnixMilli()]; !exists || start.UnixMilli() < prior {
			want[end.UnixMilli()] = start.UnixMilli()
		}
	}
	got := map[int64]int64{}
	for _, window := range actual.Windows {
		got[window.End] = window.Start
	}
	if !reflect.DeepEqual(got, want) || len(actual.Windows) != len(want) {
		t.Fatal("旧窗口没有保留更早起点、未去重或过期窗口未清除")
	}
	for _, test := range []struct{ host, backup, wall int64 }{{3, 2, 1}, {1, 3, 2}, {1, 2, 3}} {
		base := quota.ClockState{Version: 1, Timezone: "UTC", Windows: []quota.DayWindow{}}
		a, b := base, base
		a.HighestUTC, b.HighestUTC = test.host, test.backup
		merged, err := mergeRuntimeClocks(a, b, time.UnixMilli(test.wall), "UTC")
		if err != nil || merged.HighestUTC != 3 {
			t.Fatal("最高时间没有取本机、备份和当前时间三者最大值", err)
		}
	}
}

func TestRuntimeCandidateClockReaderIsStrictReadOnlyAndCancelable(t *testing.T) {
	_, s, _, _, _ := candidateFixture(t)
	valid := candidateSnapshot(t, s.DB(), "meta")
	clock, err := readRuntimeClock(t.Context(), s.DB())
	if err != nil || clock.Validate() != nil || candidateSnapshot(t, s.DB(), "meta") != valid {
		t.Fatal("读取时钟改变了原记录或拒绝合法记录", err)
	}
	if _, err := readRuntimeClock(t.Context(), nil); err == nil {
		t.Fatal("空数据库未拒绝")
	}
	var raw string
	if err := s.DB().QueryRowContext(t.Context(), `SELECT value FROM meta WHERE key='quota.clock'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	for _, malformed := range []string{"", "null", "{}", raw + `{}`, strings.Replace(raw, `"version":1`, `"version":2`, 1), strings.Replace(raw, `"windows":[]`, `"windows":null`, 1), strings.Replace(raw, `"windows":[]`, `"windows":[],"unknown":true`, 1)} {
		candidateExec(t, s.DB(), `UPDATE meta SET value=? WHERE key='quota.clock'`, malformed)
		before := candidateSnapshot(t, s.DB(), "meta")
		if _, err := readRuntimeClock(t.Context(), s.DB()); err == nil || candidateSnapshot(t, s.DB(), "meta") != before {
			t.Fatal("损坏时钟被接受或被重新初始化")
		}
	}
	candidateExec(t, s.DB(), `UPDATE meta SET value=? WHERE key='quota.clock'`, raw)
	for _, mutation := range []string{`UPDATE meta SET value='0' WHERE key='quota.clock_initialized'`, `DELETE FROM meta WHERE key='quota.clock_initialized'`, `DELETE FROM meta WHERE key='quota.clock'`} {
		candidateExec(t, s.DB(), mutation)
		if _, err := readRuntimeClock(t.Context(), s.DB()); err == nil {
			t.Fatal("缺失时钟或坏初始化标记被接受")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := readRuntimeClock(ctx, s.DB()); !errors.Is(err, context.Canceled) {
		t.Fatal("时钟读取吞掉了取消错误")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := readRuntimeClock(t.Context(), s.DB()); err == nil {
		t.Fatal("时钟读取吞掉了数据库故障")
	}
}

func TestRuntimeCandidateCancellationAndMissingFileDoNotWrite(t *testing.T) {
	path, s, oldKey, newKey, now := candidateFixture(t)
	host, err := readRuntimeClock(t.Context(), s.DB())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := prepareRuntimeCandidate(ctx, path, oldKey, newKey, host, settings.Environment{TZ: "UTC"}, now); !errors.Is(err, context.Canceled) {
		t.Fatal("准备候选吞掉了取消错误")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("已取消的候选发生写入", err)
	}
	missing := filepath.Join(t.TempDir(), "absent.db")
	if err := prepareRuntimeCandidate(t.Context(), missing, oldKey, newKey, host, settings.Environment{TZ: "UTC"}, now); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("缺失候选没有明确拒绝")
	}
	if _, err := os.Lstat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("准备恢复创建了空数据库")
	}
}
