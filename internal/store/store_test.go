package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "kakatta.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func scalar[T any](t *testing.T, db queryer, query string, args ...any) T {
	t.Helper()
	var value T
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&value); err != nil {
		t.Fatal(query, err)
	}
	return value
}

func execute(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatal(query, err)
	}
}

func TestOpenRealFileAndReconnectPragmas(t *testing.T) {
	s := testStore(t)
	if info, err := os.Stat(s.path); err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		t.Fatalf("数据库文件或权限不对：%v %v", info, err)
	}
	if s.DB().Stats().MaxOpenConnections != 1 {
		t.Fatal("连接池必须只有一个连接")
	}
	// 每条语句后关闭空闲连接，检查新连接也执行连接级设置。
	s.DB().SetMaxIdleConns(0)
	for range 2 {
		if got := scalar[int](t, s.DB(), "PRAGMA foreign_keys"); got != 1 {
			t.Fatalf("新连接没有启用外键：%d", got)
		}
		if got := scalar[int](t, s.DB(), "PRAGMA busy_timeout"); got != 5000 {
			t.Fatalf("新连接等待时间不对：%d", got)
		}
		if got := scalar[int](t, s.DB(), "PRAGMA synchronous"); got != 2 {
			t.Fatalf("新连接没有启用FULL同步：%d", got)
		}
		if got := scalar[string](t, s.DB(), "PRAGMA journal_mode"); got != "wal" {
			t.Fatalf("没有保持WAL：%s", got)
		}
	}
	execute(t, s.DB(), "INSERT INTO meta(key,value) VALUES('keep','重开后保留')")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), s.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got := scalar[string](t, reopened.DB(), "SELECT value FROM meta WHERE key='keep'"); got != "重开后保留" {
		t.Fatal("重开丢失数据", got)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(s.path), "backups")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("新库或已是当前版本时不应创建升级快照")
	}
}

func TestOpenEncodesPathAndRejectsConnectionURIs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "中文 #?mode=memory&_pragma=foreign_keys(0);drop 'x'%2f.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	execute(t, s.DB(), "INSERT INTO meta(key,value) VALUES('real','file')")
	s.Close()
	if info, err := os.Stat(path); err != nil || info.Size() == 0 {
		t.Fatal("特殊字符被当作URI参数，未建立真实文件", err)
	}
	for _, path := range []string{"", " ", ":memory:", "FILE:/tmp/not-a-file", "file:" + filepath.Join(dir, "inject.db") + "?mode=memory", "bad\x00path"} {
		if s, err := Open(context.Background(), path); err == nil {
			s.Close()
			t.Fatalf("不应接受连接URI或无效路径：%q", path)
		}
	}
	if s, err := Open(context.Background(), dir); err == nil {
		s.Close()
		t.Fatal("不应把目录当数据库")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	never := filepath.Join(dir, "never.db")
	if s, err := Open(ctx, never); !errors.Is(err, context.Canceled) || s != nil {
		t.Fatalf("取消时不应打开数据库：%v", err)
	}
	if _, err := os.Stat(never); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("已取消仍创建了文件")
	}
}

func TestOpenRejectsUnknownFutureMissingAndCorruptSchema(t *testing.T) {
	for _, damage := range []string{"unknown", "future", "gap", "empty_versions", "missing_table", "view", "missing_column", "foreign_key", "bytes", "empty_file"} {
		t.Run(damage, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "test.db")
			if damage == "bytes" || damage == "empty_file" {
				data := []byte("not a sqlite database")
				if damage == "empty_file" {
					data = nil
				}
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				s, err := Open(context.Background(), path)
				if err != nil {
					t.Fatal(err)
				}
				switch damage {
				case "unknown":
					execute(t, s.DB(), "DROP TABLE schema_migrations")
				case "future":
					execute(t, s.DB(), "INSERT INTO schema_migrations VALUES (?, 1)", len(migrations)+1)
				case "gap":
					execute(t, s.DB(), "DELETE FROM schema_migrations WHERE version=1")
				case "empty_versions":
					execute(t, s.DB(), "DELETE FROM schema_migrations")
				case "missing_table":
					execute(t, s.DB(), "DROP TABLE meta")
				case "view":
					execute(t, s.DB(), "DROP TABLE meta; CREATE VIEW meta AS SELECT key,value_json AS value FROM settings")
				case "missing_column":
					execute(t, s.DB(), "ALTER TABLE admin DROP COLUMN totp_last_step")
				case "foreign_key":
					execute(t, s.DB(), "PRAGMA foreign_keys=OFF")
					execute(t, s.DB(), "INSERT INTO quota_events(account_id,kind,at) VALUES(999,'search',1)")
				}
				s.Close()
			}
			steps := migrations
			if s, err := open(context.Background(), path, steps); err == nil {
				s.Close()
				t.Fatal("损坏或不支持的数据库被接受")
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(path), "backups")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("拒绝未知版本之前不应先备份或迁移")
			}
		})
	}
}

func TestUserIndexIsAllowed(t *testing.T) {
	s := testStore(t)
	execute(t, s.DB(), "CREATE INDEX user_meta_query ON meta(value)")
	s.Close()
	reopened, err := Open(context.Background(), s.path)
	if err != nil {
		t.Fatalf("用户的额外索引不应被当作损坏：%v", err)
	}
	reopened.Close()
}

func TestInvalidPlanAndInitialFailureLeaveNoDatabase(t *testing.T) {
	for name, steps := range map[string][]migration{
		"没有迁移": nil,
		"编号跳跃": {{version: 2, sql: "SELECT 1"}},
		"空迁移":  {{version: 1, sql: " "}},
		"执行失败": {{version: 1, sql: "CREATE TABLE partial(id INTEGER); INVALID SQL"}},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "new.db")
			if s, err := open(context.Background(), path, steps); err == nil {
				s.Close()
				t.Fatal("非法迁移计划不应成功")
			}
			for _, suffix := range []string{"", "-wal", "-shm"} {
				if _, err := os.Stat(path + suffix); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("失败的首次迁移留下数据库或日志", suffix, err)
				}
			}
		})
	}
}

func TestConcurrentTransactionsAndBackupDoNotDeadlock(t *testing.T) {
	s := testStore(t)
	execute(t, s.DB(), "INSERT INTO meta(key,value) VALUES('counter','0')")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var workers sync.WaitGroup
	errorsOut := make(chan error, 17)
	for range 16 {
		workers.Go(func() {
			for range 5 {
				tx, err := s.DB().BeginTx(ctx, nil)
				if err != nil {
					errorsOut <- err
					return
				}
				if _, err := tx.ExecContext(ctx, "UPDATE meta SET value=CAST(value AS INTEGER)+1 WHERE key='counter'"); err != nil {
					tx.Rollback()
					errorsOut <- err
					return
				}
				if err := tx.Commit(); err != nil {
					errorsOut <- err
					return
				}
			}
		})
	}
	backup := filepath.Join(t.TempDir(), "concurrent.db")
	workers.Go(func() { errorsOut <- s.Backup(ctx, backup) })
	finished := make(chan struct{})
	go func() { workers.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-ctx.Done():
		t.Fatal("并发事务和备份未能结束")
	}
	close(errorsOut)
	for err := range errorsOut {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := scalar[int](t, s.DB(), "SELECT value FROM meta WHERE key='counter'"); got != 80 {
		t.Fatalf("并发更新丢失：%d", got)
	}
	copy, err := Open(ctx, backup)
	if err != nil {
		t.Fatal("并发备份不是可重开的库", err)
	}
	defer copy.Close()
	value := scalar[int](t, copy.DB(), "SELECT value FROM meta WHERE key='counter'")
	if value < 0 || value > 80 {
		t.Fatal("备份不是已提交事务的一致快照", value)
	}
}

func TestBackupWaitsForTransactionAndRespectsCancellation(t *testing.T) {
	s := testStore(t)
	execute(t, s.DB(), "INSERT INTO meta(key,value) VALUES('state','before')")
	tx, err := s.DB().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("UPDATE meta SET value='after' WHERE key='state'"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	canceled := filepath.Join(t.TempDir(), "cancel.db")
	if err := s.Backup(ctx, canceled); err == nil {
		t.Fatal("事务未结束时备份没有等待连接")
	}
	if _, err := os.Stat(canceled); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("失败备份留下文件")
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "已提交 #?&_pragma='x'.db")
	if err := s.Backup(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	copy, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	if got := scalar[string](t, copy.DB(), "SELECT value FROM meta WHERE key='state'"); got != "after" {
		t.Fatal("备份未包含WAL中已提交的数据", got)
	}
	if err := s.Backup(context.Background(), path); err == nil {
		t.Fatal("备份不能覆盖已有文件")
	}
	if err := s.Backup(context.Background(), s.path); err == nil {
		t.Fatal("备份不能覆盖源数据库")
	}
}

func TestMigrationBacksUpOldVersionAndRollsBackWholeUpgrade(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(strconv.FormatBool(fail), func(t *testing.T) {
			s := testStore(t)
			execute(t, s.DB(), "INSERT INTO meta(key,value) VALUES('state','before')")
			s.Close()
			steps := append(append([]migration(nil), migrations...), migration{len(migrations) + 1, "CREATE TABLE added(id INTEGER PRIMARY KEY); UPDATE meta SET value='after' WHERE key='state'"})
			if fail {
				steps = append(steps, migration{len(migrations) + 2, "THIS IS NOT SQL"})
			}
			upgraded, err := open(context.Background(), s.path, steps)
			if fail && err == nil || !fail && err != nil {
				t.Fatalf("迁移结果不符，失败测试%t：%v", fail, err)
			}
			if upgraded != nil {
				upgraded.Close()
			}
			db, err := sql.Open("sqlite", dataSource(s.path))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			version, value, tables := len(migrations)+1, "after", 1
			if fail {
				version, value, tables = len(migrations), "before", 0
			}
			if got := scalar[int](t, db, "SELECT max(version) FROM schema_migrations"); got != version {
				t.Fatal("失败后推进了迁移版本", got)
			}
			if got := scalar[string](t, db, "SELECT value FROM meta WHERE key='state'"); got != value {
				t.Fatal("迁移数据未一并提交或回滚", got)
			}
			if got := scalar[int](t, db, "SELECT count(*) FROM sqlite_schema WHERE name='added'"); got != tables {
				t.Fatal("迁移DDL没有一并提交或回滚", got)
			}
			files := ownedBackups(t, filepath.Join(filepath.Dir(s.path), "backups"))
			if len(files) != 1 {
				t.Fatalf("升级前必须产生一份快照：%v", files)
			}
			copy, err := Open(context.Background(), files[0])
			if err != nil {
				t.Fatal("升级前快照不能重开", err)
			}
			defer copy.Close()
			if got := scalar[string](t, copy.DB(), "SELECT value FROM meta WHERE key='state'"); got != "before" {
				t.Fatal("快照不是升级前数据", got)
			}
			if got := scalar[int](t, copy.DB(), "SELECT max(version) FROM schema_migrations"); got != len(migrations) {
				t.Fatal("快照不是升级前版本", got)
			}
		})
	}
}

func TestUpgradeChecksNewRequiredColumnAfterMigration(t *testing.T) {
	s := testStore(t)
	execute(t, s.DB(), "INSERT INTO admin(id,username,password_hash,created_at,password_changed_at) VALUES(1,'owner','hash',1,1)")
	s.Close()
	previous := requiredColumns["admin"]
	requiredColumns["admin"] = previous + " last_login_at"
	t.Cleanup(func() { requiredColumns["admin"] = previous })
	missingColumn := append(append([]migration(nil), migrations...), migration{len(migrations) + 1, "SELECT 1"})
	if invalid, err := open(context.Background(), s.path, missingColumn); err == nil {
		invalid.Close()
		t.Fatal("迁移未补齐最新必需列时不能提交")
	}
	unchanged, err := sql.Open("sqlite", dataSource(s.path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unchanged.Close() })
	if got := scalar[int](t, unchanged, "SELECT max(version) FROM schema_migrations"); got != len(migrations) {
		t.Fatalf("结构检查失败仍推进了版本：%d", got)
	}
	unchanged.Close()
	steps := append(append([]migration(nil), migrations...), migration{len(migrations) + 1, "ALTER TABLE admin ADD COLUMN last_login_at INTEGER"})
	upgraded, err := open(context.Background(), s.path, steps)
	if err != nil {
		t.Fatalf("旧库应先补齐新列，再校验最新结构：%v", err)
	}
	t.Cleanup(func() { upgraded.Close() })
	if got := scalar[int](t, upgraded.DB(), "SELECT max(version) FROM schema_migrations"); got != len(migrations)+1 {
		upgraded.Close()
		t.Fatalf("迁移未到新版本：%d", got)
	}
	if got := scalar[string](t, upgraded.DB(), "SELECT username FROM admin WHERE id=1"); got != "owner" {
		upgraded.Close()
		t.Fatal("升级丢失原有管理员", got)
	}
	execute(t, upgraded.DB(), "UPDATE admin SET last_login_at=123 WHERE id=1")
	upgraded.Close()
	reopened, err := open(context.Background(), s.path, steps)
	if err != nil {
		t.Fatal("升级后的库应能重开", err)
	}
	t.Cleanup(func() { reopened.Close() })
	if got := scalar[int](t, reopened.DB(), "SELECT last_login_at FROM admin WHERE id=1"); got != 123 {
		reopened.Close()
		t.Fatal("新列数据未保留", got)
	}
	execute(t, reopened.DB(), "ALTER TABLE admin DROP COLUMN last_login_at")
	reopened.Close()
	if broken, err := open(context.Background(), s.path, steps); err == nil {
		broken.Close()
		t.Fatal("当前版本重开必须仍检查完整列清单")
	}
}

func TestFailedBackupPreventsUpgradeAndNeverPrunesOldFiles(t *testing.T) {
	s := testStore(t)
	s.Close()
	dir := filepath.Join(filepath.Dir(s.path), "backups")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	files := make([]string, 4)
	for i := range files {
		files[i] = filepath.Join(dir, fmt.Sprintf("kakatta-upgrade-v000001-20261110T00000%d.000000000Z-1.db", i))
		if err := os.WriteFile(files[i], []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	closed := &Store{db: s.DB(), path: s.path}
	if err := closed.backupBeforeMigration(context.Background(), 1); err == nil {
		t.Fatal("已关闭连接不应生成备份")
	}
	for _, path := range files {
		if b, err := os.ReadFile(path); err != nil || string(b) != "keep" {
			t.Fatal("备份失败清理了旧文件", path, err)
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	steps := append(append([]migration(nil), migrations...), migration{len(migrations) + 1, "CREATE TABLE must_not_exist(id INTEGER)"})
	if upgraded, err := open(context.Background(), s.path, steps); err == nil {
		upgraded.Close()
		t.Fatal("备份失败仍执行了升级")
	}
	db, err := sql.Open("sqlite", dataSource(s.path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if version := scalar[int](t, db, "SELECT max(version) FROM schema_migrations"); version != len(migrations) {
		t.Fatal("备份失败后版本发生变化")
	}
}

func TestMigrationBackupRetentionOnlyRemovesOwnedFiles(t *testing.T) {
	s := testStore(t)
	s.Close()
	dir := filepath.Join(filepath.Dir(s.path), "backups")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"manual.db", "kakatta-upgrade-not-ours.db", ".kakatta-upgrade.pending"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	steps := append([]migration(nil), migrations...)
	for version := len(migrations) + 1; version <= len(migrations)+5; version++ {
		steps = append(steps, migration{version, fmt.Sprintf("ALTER TABLE meta ADD COLUMN test_%d INTEGER", version)})
		upgraded, err := open(context.Background(), s.path, steps)
		if err != nil {
			t.Fatal(err)
		}
		upgraded.Close()
	}
	backups := ownedBackups(t, dir)
	if len(backups) != 3 {
		t.Fatalf("应保留最新三份升级快照：%v", backups)
	}
	for _, name := range []string{"manual.db", "kakatta-upgrade-not-ours.db", ".kakatta-upgrade.pending"} {
		if b, err := os.ReadFile(filepath.Join(dir, name)); err != nil || string(b) != "keep" {
			t.Fatal("清理了非本程序备份", name, err)
		}
	}
	versions := map[int]bool{}
	for _, path := range backups {
		db, err := sql.Open("sqlite", dataSource(path))
		if err != nil {
			t.Fatal(err)
		}
		versions[scalar[int](t, db, "SELECT max(version) FROM schema_migrations")] = true
		db.Close()
	}
	if !versions[len(migrations)+2] || !versions[len(migrations)+3] || !versions[len(migrations)+4] {
		t.Fatal("没有保留最近三份版本", versions)
	}
}

func ownedBackups(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		if migrationBackupName.MatchString(entry.Name()) {
			names = append(names, filepath.Join(dir, entry.Name()))
		}
		if strings.HasSuffix(entry.Name(), ".pending") && strings.HasPrefix(entry.Name(), "kakatta-upgrade-") {
			t.Fatal("留下了未完成快照", entry.Name())
		}
	}
	return names
}
