package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/backup"
	"github.com/Seaony/Kakatta/internal/security"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/store"
)

const candidatePasswordHash = "$argon2id$v=19$m=65536,t=3,p=4$AAECAwQFBgcICQoLDA0ODw$vUrOhcKV+zjYROFncankOSUoKsmCyGVr5Vou9s8g/Vg"

type runtimeCandidateFixture struct {
	path string
	db   *sql.DB
	key  [backup.MasterKeyBytes]byte
}

func runtimeCandidateExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func runtimeCandidateDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	u.RawQuery = url.Values{"_pragma": {"foreign_keys(1)", "busy_timeout(5000)"}}.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newRuntimeCandidateFixture(t *testing.T, version int) runtimeCandidateFixture {
	t.Helper()
	f := runtimeCandidateFixture{path: filepath.Join(t.TempDir(), "上传 候选 #?%.db")}
	for i := range f.key {
		f.key[i] = byte(i + 1)
	}
	if version == store.SchemaVersion() {
		database, err := store.Open(t.Context(), f.path)
		if err != nil {
			t.Fatal(err)
		}
		f.db = database.DB()
		t.Cleanup(func() { _ = database.Close() })
	} else {
		f.db = runtimeCandidateDB(t, f.path)
		runtimeCandidateExec(t, f.db, `CREATE TABLE schema_migrations (
 version INTEGER NOT NULL PRIMARY KEY CHECK (version > 0),
 applied_at INTEGER NOT NULL
)`)
		files, err := filepath.Glob("../../internal/store/migrations/*.sql")
		if err != nil || len(files) < version {
			t.Fatal("找不到真实迁移文件", err)
		}
		for i, file := range files[:version] {
			ddl, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			runtimeCandidateExec(t, f.db, string(ddl))
			runtimeCandidateExec(t, f.db, `INSERT INTO schema_migrations(version,applied_at) VALUES(?,1)`, i+1)
		}
	}
	if _, err := appsettings.New(t.Context(), f.db, settings.Environment{TZ: "UTC"}, func() time.Time { return time.UnixMilli(1_700_000_000_000) }); err != nil {
		t.Fatal(err)
	}
	cipher, err := security.NewCipher(f.key[:])
	if err != nil {
		t.Fatal(err)
	}
	seal := func(plaintext, table, column, row string) []byte {
		t.Helper()
		value, err := cipher.Seal([]byte(plaintext), table, column, row)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	runtimeCandidateExec(t, f.db, `INSERT INTO admin(id,username,password_hash,totp_secret_enc,totp_enabled_at,created_at,password_changed_at)
		VALUES(1,'备份管理员',?,?,1,1,1)`, candidatePasswordHash, seal(security.NewTOTPSecret(), "admin", "totp_secret_enc", "1"))
	runtimeCandidateExec(t, f.db, `INSERT INTO proxies(id,uuid,name,url_enc) VALUES(1,'unused-proxy','未引用代理',?)`, seal("http://private-user:private-password@127.0.0.1:7890", "proxies", "url_enc", "unused-proxy"))
	runtimeCandidateExec(t, f.db, `INSERT INTO tasks(id,name,keyword,enabled,max_pages,interval_minutes,judge_mode,match_keywords_json,notify_policy,sort_index)
		VALUES(1,'停用任务','相机',0,1,30,'keyword','["相机"]','none',0)`)
	runtimeCandidateExec(t, f.db, `INSERT INTO ai_providers(id,uuid,name,base_url,api_key_enc,vision_model,enabled,sort_index)
		VALUES(1,'unused-provider','停用模型','http://127.0.0.1:1/v1',?,'vision-model',0,0)`, seal("private-api-key", "ai_providers", "api_key_enc", "unused-provider"))
	runtimeCandidateExec(t, f.db, `INSERT INTO channels(id,uuid,type,name,enabled,config_json,secret_enc)
		VALUES(1,'unused-channel','ntfy','停用通知',0,'{}',?)`, seal(`{"url":"http://127.0.0.1:1/topic","token":"private-notify-key"}`, "channels", "secret_enc", "unused-channel"))
	runtimeCandidateExec(t, f.db, `INSERT INTO accounts(id,name,state,verify_level,next_gap_seconds) VALUES(1,'风险账号','needs_verification',2,240)`)
	runtimeCandidateExec(t, f.db, `INSERT INTO run_records(id,task_id,task_name,trigger,account_id,started_at,outcome,failure_kind)
		VALUES(1,1,'旧运行','auto',1,1,'failed','restart')`)
	runtimeCandidateExec(t, f.db, `INSERT INTO meta(key,value) VALUES('runtime.session.1','{"version":1,"session_id":"held","started_at":1,"run_id":1}'),('account.cleanup.2','1'),('stats.started_at','1')`)
	return f
}

func runtimeCandidateSnapshot(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), `SELECT name FROM sqlite_schema WHERE type='table' AND name<>'schema_migrations' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		t.Fatal(err)
	}
	result := map[string]string{}
	for _, table := range tables {
		rows, err := db.QueryContext(t.Context(), `SELECT * FROM "`+strings.ReplaceAll(table, `"`, `""`)+`" ORDER BY rowid`)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		values := [][]any{}
		for rows.Next() {
			row, targets := make([]any, len(columns)), make([]any, len(columns))
			for i := range row {
				targets[i] = &row[i]
			}
			if err := rows.Scan(targets...); err != nil {
				t.Fatal(err)
			}
			values = append(values, row)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(values)
		if err != nil {
			t.Fatal(err)
		}
		result[table] = string(raw)
	}
	return result
}

func runtimeCandidateNoSidecars(t *testing.T, path string) {
	t.Helper()
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Lstat(path + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("候选校验未完成检查点或关闭连接", suffix, err)
		}
	}
}

func TestRuntimeCandidateEncryptedArchiveIsReadOnly(t *testing.T) {
	f := newRuntimeCandidateFixture(t, store.SchemaVersion())
	before := runtimeCandidateSnapshot(t, f.db)
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := backup.NewManifest("candidate-test", store.SchemaVersion(), time.UnixMilli(1_700_000_000_000).UTC())
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	const password = "local-candidate-archive-password"
	if err := backup.WriteArchive(t.Context(), &archive, f.path, f.key, manifest, password); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "private-candidate")
	got, key, err := backup.ReadArchive(t.Context(), bytes.NewReader(archive.Bytes()), int64(archive.Len()), password, dir, store.SchemaVersion())
	if err != nil {
		t.Fatal(err)
	}
	defer clear(key[:])
	path := filepath.Join(dir, backup.DatabaseEntry)
	if err := validateRuntimeCandidate(t.Context(), path, key, got.SchemaVersion, settings.Environment{TZ: "Asia/Shanghai"}); err != nil {
		t.Fatal(err)
	}
	runtimeCandidateNoSidecars(t, path)
	after := runtimeCandidateSnapshot(t, runtimeCandidateDB(t, path))
	if !reflect.DeepEqual(before, after) {
		t.Fatal("候选校验初始化了认证、模型、时钟或恢复业务记录")
	}
	if key != f.key {
		t.Fatal("候选校验清除了调用方的密钥")
	}
	if source, err := os.ReadFile(f.path); err != nil || !bytes.Equal(original, source) {
		t.Fatal("候选校验触碰了备份源库", err)
	}
}

func TestRuntimeCandidateRejectsEachBusinessValidator(t *testing.T) {
	for name, query := range map[string]string{
		"settings":         `INSERT INTO settings(key,value_json) VALUES('general.timezone','"Invalid/Timezone"')`,
		"disabled_task":    `UPDATE tasks SET match_keywords_json='[null]'`,
		"unbound_proxy":    `UPDATE proxies SET url_enc=X'00'`,
		"disabled_model":   `UPDATE ai_providers SET api_key_enc=X'00'`,
		"disabled_channel": `UPDATE channels SET secret_enc=X'00'`,
		"admin_password":   `UPDATE admin SET password_hash='invalid-password-hash'`,
		"admin_totp":       `UPDATE admin SET totp_secret_enc=X'00'`,
		"missing_admin":    `DELETE FROM admin`,
		"missing_clock":    `DELETE FROM meta WHERE key='quota.clock'`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newRuntimeCandidateFixture(t, store.SchemaVersion())
			runtimeCandidateExec(t, f.db, query)
			before := runtimeCandidateSnapshot(t, f.db)
			if err := f.db.Close(); err != nil {
				t.Fatal(err)
			}
			if err := validateRuntimeCandidate(t.Context(), f.path, f.key, store.SchemaVersion(), settings.Environment{TZ: "UTC"}); err == nil {
				t.Fatal("损坏或缺失的候选记录仍被接受")
			}
			runtimeCandidateNoSidecars(t, f.path)
			if after := runtimeCandidateSnapshot(t, runtimeCandidateDB(t, f.path)); !reflect.DeepEqual(before, after) {
				t.Fatal("失败校验初始化或改写了候选业务数据")
			}
		})
	}
}

func TestRuntimeCandidateMigratesOnlyTrustedPrivateOldDatabase(t *testing.T) {
	f := newRuntimeCandidateFixture(t, 1)
	before := runtimeCandidateSnapshot(t, f.db)
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := validateRuntimeCandidate(t.Context(), f.path, f.key, 1, settings.Environment{TZ: "UTC"}); err != nil {
		t.Fatal(err)
	}
	runtimeCandidateNoSidecars(t, f.path)
	db := runtimeCandidateDB(t, f.path)
	var version, sellerColumn int
	if err := db.QueryRowContext(t.Context(), `SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil || version != store.SchemaVersion() {
		t.Fatal("可信旧候选未补齐迁移", version, err)
	}
	if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM pragma_table_info('findings') WHERE name='seller_checks_json'`).Scan(&sellerColumn); err != nil || sellerColumn != 1 {
		t.Fatal("最新结构未准备完成", err)
	}
	after := runtimeCandidateSnapshot(t, db)
	for table, values := range before {
		// 迁移可给新增表建立编号序列，已有业务行不能被校验改写。
		if table == "sqlite_sequence" {
			continue
		}
		if values != after[table] {
			t.Fatal("迁移后的组合校验改写了原有业务记录", table)
		}
	}
	for _, table := range []string{"proxies", "tasks", "ai_providers", "channels", "accounts", "run_records"} {
		var sequence int64
		if err := db.QueryRowContext(t.Context(), `SELECT seq FROM sqlite_sequence WHERE name=?`, table).Scan(&sequence); err != nil || sequence != 1 {
			t.Fatal("迁移或校验改了原有编号序列", table, sequence, err)
		}
	}
	backups, err := filepath.Glob(filepath.Join(filepath.Dir(f.path), "backups", "*.db"))
	if err != nil || len(backups) != 1 {
		t.Fatal("旧候选迁移缺少升级前快照", backups, err)
	}
	if err := store.ValidateBackup(t.Context(), backups[0], 1); err != nil {
		t.Fatal("升级快照不是可信原版本", err)
	}
	if err := store.ValidateBackup(t.Context(), f.path, store.SchemaVersion()); err != nil {
		t.Fatal("迁移后候选结构或完整性校验失败", err)
	}
}

func TestRuntimeCandidateRejectsBeforeMigrating(t *testing.T) {
	for _, kind := range []string{"schema", "check", "manifest"} {
		t.Run(kind, func(t *testing.T) {
			f := newRuntimeCandidateFixture(t, 1)
			schema := 1
			switch kind {
			case "schema":
				runtimeCandidateExec(t, f.db, `CREATE TRIGGER uploaded_trigger AFTER INSERT ON meta BEGIN UPDATE accounts SET next_gap_seconds=0; END`)
			case "check":
				runtimeCandidateExec(t, f.db, `PRAGMA ignore_check_constraints=ON`)
				runtimeCandidateExec(t, f.db, `INSERT INTO admin(id,username,password_hash,created_at,password_changed_at) VALUES(2,'坏管理员','invalid',1,1)`)
			case "manifest":
				schema = 2
			}
			if err := f.db.Close(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(f.path)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateRuntimeCandidate(t.Context(), f.path, f.key, schema, settings.Environment{TZ: "UTC"}); err == nil {
				t.Fatal("未验证原结构和数据就打开候选迁移")
			}
			after, err := os.ReadFile(f.path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("拒绝前已改写不可信原库", err)
			}
			if backups, err := filepath.Glob(filepath.Join(filepath.Dir(f.path), "backups", "*.db")); err != nil || len(backups) != 0 {
				t.Fatal("不可信候选进入了升级流程", err)
			}
		})
	}
}

func TestRuntimeCandidateOldInvalidBusinessIsNotRepaired(t *testing.T) {
	f := newRuntimeCandidateFixture(t, 1)
	runtimeCandidateExec(t, f.db, `DELETE FROM meta WHERE key='quota.clock'`)
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := validateRuntimeCandidate(t.Context(), f.path, f.key, 1, settings.Environment{TZ: "UTC"}); err == nil {
		t.Fatal("旧候选在迁移后未校验原有业务数据")
	}
	runtimeCandidateNoSidecars(t, f.path)
	db := runtimeCandidateDB(t, f.path)
	var version, clocks int
	if err := db.QueryRowContext(t.Context(), `SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil || version != store.SchemaVersion() {
		t.Fatal("旧候选未按可信迁移准备最新结构", err)
	}
	if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM meta WHERE key='quota.clock'`).Scan(&clocks); err != nil || clocks != 0 {
		t.Fatal("候选校验重新初始化了缺失的保护时钟", err)
	}
}

func TestRuntimeCandidateCancellationAndWrongKey(t *testing.T) {
	for _, kind := range []string{"cancelled", "wrong_key"} {
		t.Run(kind, func(t *testing.T) {
			f := newRuntimeCandidateFixture(t, store.SchemaVersion())
			before := runtimeCandidateSnapshot(t, f.db)
			if err := f.db.Close(); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			key := f.key
			if kind == "cancelled" {
				cancel()
			} else {
				key[0] ^= 1
			}
			err := validateRuntimeCandidate(ctx, f.path, key, store.SchemaVersion(), settings.Environment{TZ: "UTC"})
			if err == nil || kind == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatal("取消或错误密钥未被拒绝", err)
			}
			runtimeCandidateNoSidecars(t, f.path)
			if after := runtimeCandidateSnapshot(t, runtimeCandidateDB(t, f.path)); !reflect.DeepEqual(before, after) {
				t.Fatal("取消或错误密钥改写了候选业务数据")
			}
		})
	}
}
