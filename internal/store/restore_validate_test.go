package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func restoreCandidate(t *testing.T, version int) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "candidate.db")
	work := t.TempDir()
	t.Setenv("TMPDIR", work)
	t.Cleanup(func() {
		entries, err := os.ReadDir(work)
		if err != nil || len(entries) != 0 {
			t.Error("校验结束后留下临时副本或日志", entries, err)
		}
	})
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dataSource(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	execute(t, db, migrationTable)
	for _, step := range migrations[:version] {
		execute(t, db, step.sql)
		execute(t, db, "INSERT INTO schema_migrations VALUES(?,1)", step.version)
	}
	return path, db
}

func TestValidateBackupAcceptsEverySupportedVersionWithoutMigration(t *testing.T) {
	if got := SchemaVersion(); got != len(migrations) {
		t.Fatalf("当前版本不符：%d", got)
	}
	for version := 1; version <= SchemaVersion(); version++ {
		t.Run(strconv.Itoa(version), func(t *testing.T) {
			path, db := restoreCandidate(t, version)
			// 这里只验证结构，JSON 内容由恢复流程另行检查。
			execute(t, db, "INSERT INTO settings(key,value_json) VALUES('raw','not json')")
			db.Close()
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0400); err != nil {
				t.Fatal(err)
			}
			if err := ValidateBackup(t.Context(), path, version); err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("只读校验改写了候选库", err)
			}
			entries, err := os.ReadDir(filepath.Dir(path))
			if err != nil || len(entries) != 1 {
				t.Fatal("只读校验创建了日志或迁移快照", entries, err)
			}
		})
	}
}

func TestValidateBackupRejectsManifestAndMigrationChain(t *testing.T) {
	for _, kind := range []string{"zero", "negative", "manifest_future", "mismatch", "future", "gap", "empty", "missing"} {
		t.Run(kind, func(t *testing.T) {
			path, db := restoreCandidate(t, SchemaVersion())
			version := SchemaVersion()
			switch kind {
			case "zero":
				version = 0
			case "negative":
				version = -1
			case "manifest_future":
				version++
			case "mismatch":
				version--
			case "future":
				execute(t, db, "INSERT INTO schema_migrations VALUES(?,1)", version+1)
			case "gap":
				execute(t, db, "DELETE FROM schema_migrations WHERE version=2")
			case "empty":
				execute(t, db, "DELETE FROM schema_migrations")
			case "missing":
				execute(t, db, "DROP TABLE schema_migrations")
			}
			db.Close()
			if err := ValidateBackup(t.Context(), path, version); err == nil {
				t.Fatal("版本或迁移记录不合法仍被接受")
			}
		})
	}
}

func TestValidateBackupRejectsUntrustedSchema(t *testing.T) {
	for _, kind := range []string{"view", "trigger", "virtual", "unknown_table", "check", "foreign_key", "index", "unique_index", "partial_index", "expression_index", "collation_index", "check_data", "damaged_index"} {
		t.Run(kind, func(t *testing.T) {
			path, db := restoreCandidate(t, SchemaVersion())
			switch kind {
			case "view":
				execute(t, db, "DROP TABLE meta; CREATE VIEW meta AS SELECT key,value_json AS value FROM settings")
			case "trigger":
				execute(t, db, "CREATE TRIGGER unexpected AFTER INSERT ON meta BEGIN SELECT RAISE(ABORT,'no'); END")
			case "virtual":
				execute(t, db, "CREATE VIRTUAL TABLE unexpected USING fts5(content)")
			case "unknown_table":
				execute(t, db, "CREATE TABLE unexpected(value TEXT)")
			case "check":
				ddl := scalar[string](t, db, "SELECT sql FROM sqlite_schema WHERE name='admin'")
				execute(t, db, "DROP TABLE admin")
				execute(t, db, strings.Replace(ddl, "CHECK (id = 1)", "CHECK (id IN (1,2))", 1))
			case "foreign_key":
				ddl := scalar[string](t, db, "SELECT sql FROM sqlite_schema WHERE name='accounts'")
				execute(t, db, "DROP TABLE accounts")
				execute(t, db, strings.Replace(ddl, "REFERENCES proxies", "REFERENCES channels", 1))
			case "index":
				execute(t, db, "DROP INDEX tasks_enabled_next_run_at; CREATE INDEX tasks_enabled_next_run_at ON tasks(next_run_at,enabled)")
			case "unique_index":
				execute(t, db, "CREATE UNIQUE INDEX extra ON meta(value)")
			case "partial_index":
				execute(t, db, "CREATE INDEX extra ON meta(value) WHERE value<>''")
			case "expression_index":
				execute(t, db, "CREATE INDEX extra ON meta(lower(value))")
			case "collation_index":
				execute(t, db, "CREATE INDEX extra ON meta(value COLLATE NOCASE)")
			case "check_data":
				execute(t, db, "PRAGMA ignore_check_constraints=ON")
				execute(t, db, "INSERT INTO admin(id,username,password_hash,created_at,password_changed_at) VALUES(2,'bad','bad',1,1)")
				execute(t, db, "PRAGMA ignore_check_constraints=OFF")
			case "damaged_index":
				execute(t, db, "PRAGMA writable_schema=ON")
				execute(t, db, "UPDATE sqlite_schema SET rootpage=1 WHERE name='tasks_enabled_next_run_at'")
			}
			db.Close()
			if err := ValidateBackup(t.Context(), path, SchemaVersion()); err == nil {
				t.Fatal("不可信结构或损坏数据仍被接受")
			}
		})
	}
}

func TestReadOnlyBackupPreservesChecksForIntegrityValidation(t *testing.T) {
	path, db := restoreCandidate(t, SchemaVersion())
	execute(t, db, "PRAGMA ignore_check_constraints=ON")
	execute(t, db, "INSERT INTO admin(id,username,password_hash,created_at,password_changed_at) VALUES(2,'bad','bad',1,1)")
	db.Close()
	source := strings.Replace(dataSource(path), "mode=rw", "mode=ro", 1) + "&_pragma=query_only(1)&_pragma=trusted_schema(0)"
	readOnly, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	defer readOnly.Close()
	t.Log("SQLite", scalar[string](t, readOnly, "SELECT sqlite_version()"), "只读完整性", scalar[string](t, readOnly, "PRAGMA integrity_check"))
	copyPath := filepath.Join(t.TempDir(), "check.db")
	if err := (&Store{db: readOnly, path: path}).Backup(t.Context(), copyPath); err != nil {
		t.Log("query_only 下 VACUUM INTO", err)
		execute(t, readOnly, "PRAGMA query_only=OFF")
		if _, err := readOnly.Exec("INSERT INTO meta(key,value) VALUES('forbidden','write')"); err == nil {
			t.Fatal("临时关闭 query_only 后，mode=ro 未阻止原库写入")
		}
		if err := (&Store{db: readOnly, path: path}).Backup(t.Context(), copyPath); err != nil {
			t.Fatal("mode=ro 下 VACUUM INTO", err)
		}
	}
	copyDB, err := sql.Open("sqlite", dataSource(copyPath)+"&_pragma=query_only(1)&_pragma=trusted_schema(0)")
	if err != nil {
		t.Fatal(err)
	}
	defer copyDB.Close()
	result := scalar[string](t, copyDB, "PRAGMA integrity_check")
	t.Log("副本完整性", result)
	if !strings.Contains(result, "CHECK constraint failed in admin") {
		t.Fatal("副本未保留 CHECK 校验", result)
	}
	if info, err := os.Stat(copyPath); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("私有副本文件权限不正确", info, err)
	}
}

func TestValidateBackupAcceptsOrdinaryUserIndexes(t *testing.T) {
	path, db := restoreCandidate(t, SchemaVersion())
	execute(t, db, "CREATE INDEX user_meta_query ON meta(value)")
	execute(t, db, `CREATE INDEX "带引号的查询索引" ON tasks(keyword DESC,name)`)
	db.Close()
	if err := ValidateBackup(t.Context(), path, SchemaVersion()); err != nil {
		t.Fatal(err)
	}
}

func TestValidateBackupReadsWALWithoutChangingDatabaseOrWAL(t *testing.T) {
	for _, orphan := range []bool{false, true} {
		t.Run(strconv.FormatBool(orphan), func(t *testing.T) {
			path, db := restoreCandidate(t, SchemaVersion())
			db.Close()
			var err error
			db, err = sql.Open("sqlite", dataSource(path))
			if err != nil {
				t.Fatal(err)
			}
			db.SetMaxOpenConns(1)
			t.Cleanup(func() { db.Close() })
			if mode := scalar[string](t, db, "PRAGMA journal_mode=WAL"); mode != "wal" {
				t.Fatal("没有启用 WAL", mode)
			}
			var busy, log, checkpointed int
			if err := db.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &log, &checkpointed); err != nil {
				t.Fatal(err)
			}
			if orphan {
				execute(t, db, "PRAGMA foreign_keys=OFF")
				execute(t, db, "INSERT INTO quota_events(account_id,kind,at) VALUES(999,'search',1)")
			} else {
				execute(t, db, "INSERT INTO meta(key,value) VALUES('wal','committed')")
			}
			before := map[string][]byte{}
			for _, file := range []string{path, path + "-wal"} {
				data, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				before[file] = data
			}
			err = ValidateBackup(t.Context(), path, SchemaVersion())
			if (err != nil) != orphan {
				t.Fatalf("未按 WAL 中已提交的外键事实判断：%v", err)
			}
			for file, old := range before {
				after, err := os.ReadFile(file)
				if err != nil || !bytes.Equal(old, after) {
					t.Fatal("只读校验改写了数据库或 WAL", file, err)
				}
			}
		})
	}
}

func TestValidateBackupRejectsInvalidFilesAndCancellation(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.db")
	for _, path := range []string{missing, dir, ":memory:", "file:" + missing} {
		if err := ValidateBackup(t.Context(), path, SchemaVersion()); err == nil {
			t.Fatalf("无效文件被接受：%q", path)
		}
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("校验创建了不存在的文件")
	}
	bad := filepath.Join(dir, "bad.db")
	if err := os.WriteFile(bad, []byte("not sqlite"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateBackup(t.Context(), bad, SchemaVersion()); err == nil {
		t.Fatal("坏文件被接受")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := ValidateBackup(ctx, missing, SchemaVersion()); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消错误没有真实返回：%v", err)
	}
}

func TestValidateBackupCancellationRemovesPrivateSnapshot(t *testing.T) {
	path, db := restoreCandidate(t, SchemaVersion())
	execute(t, db, "INSERT INTO settings(key,value_json) VALUES('large',?)", strings.Repeat("x", 8<<20))
	db.Close()
	work := os.Getenv("TMPDIR")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	observed := make(chan error, 1)
	go func() {
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		for {
			entries, err := os.ReadDir(work)
			if err != nil {
				observed <- err
				return
			}
			if len(entries) > 0 {
				info, err := entries[0].Info()
				if err == nil && info.Mode().Perm() != 0700 {
					err = errors.New("私有校验目录权限不是0700")
				}
				cancel()
				observed <- err
				return
			}
			select {
			case <-ctx.Done():
				observed <- ctx.Err()
				return
			case <-tick.C:
			}
		}
	}()
	if err := ValidateBackup(ctx, path, SchemaVersion()); !errors.Is(err, context.Canceled) {
		t.Fatalf("创建副本后取消未返回真实错误：%v", err)
	}
	if err := <-observed; err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(work)
	if err != nil || len(entries) != 0 {
		t.Fatal("取消后留下私有副本或 WAL/SHM", entries, err)
	}
}
