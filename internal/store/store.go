// Package store 管理数据库连接、编号迁移和一致性快照。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
)

type Store struct {
	db   *sql.DB
	path string

	snapshotMu    sync.Mutex
	snapshotReads sync.WaitGroup
	closed        bool
}

func Open(ctx context.Context, path string) (*Store, error) {
	return open(ctx, path, migrations)
}

func open(ctx context.Context, path string, steps []migration) (_ *Store, err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = validateMigrations(steps); err != nil {
		return nil, err
	}
	path, err = filePath(path)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("创建数据库目录：%w", err)
	}
	created := false
	f, createErr := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if createErr == nil {
		created = true
		if err = f.Close(); err != nil {
			os.Remove(path)
			return nil, err
		}
	} else if !errors.Is(createErr, os.ErrExist) {
		return nil, fmt.Errorf("打开数据库文件：%w", createErr)
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("数据库路径必须是普通文件：%s", path)
	}
	db, err := sql.Open("sqlite", dataSource(path))
	if err != nil {
		if created {
			os.Remove(path)
		}
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	defer func() {
		if err != nil {
			db.Close()
			if created {
				os.Remove(path)
				os.Remove(path + "-wal")
				os.Remove(path + "-shm")
			}
		}
	}()
	if err = db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("连接数据库：%w", err)
	}
	if err = checkDatabase(ctx, db); err != nil {
		return nil, err
	}
	version, err := readVersion(ctx, db, steps, created)
	if err != nil {
		return nil, err
	}
	// 旧库还没有新增列，升级时要等迁移完成后再检查最新结构。
	if version == len(steps) {
		if err = checkColumns(ctx, db); err != nil {
			return nil, err
		}
	}
	if version > 0 {
		if err = checkForeignKeys(ctx, db); err != nil {
			return nil, err
		}
	}
	var journal string
	if err = db.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&journal); err != nil {
		return nil, fmt.Errorf("数据库不能启用 WAL：%w", err)
	}
	if !strings.EqualFold(journal, "wal") {
		err = fmt.Errorf("数据库不能启用 WAL，实际为 %s", journal)
		return nil, err
	}
	s := &Store{db: db, path: path}
	if version < len(steps) {
		if !created {
			if err = s.backupBeforeMigration(ctx, version); err != nil {
				return nil, err
			}
		}
		if err = migrate(ctx, db, steps, version, created); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) Close() error {
	s.snapshotMu.Lock()
	s.closed = true
	s.snapshotMu.Unlock()
	s.snapshotReads.Wait()
	return s.db.Close()
}

func filePath(path string) (string, error) {
	if strings.TrimSpace(path) == "" || strings.EqualFold(strings.TrimSpace(path), ":memory:") || strings.HasPrefix(strings.ToLower(path), "file:") || strings.ContainsRune(path, 0) {
		return "", errors.New("数据库必须使用文件路径，不能使用内存库或连接 URI")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func dataSource(path string) string {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	q := url.Values{"mode": {"rw"}, "_pragma": {"foreign_keys(1)", "busy_timeout(5000)", "synchronous(FULL)"}}
	u.RawQuery = q.Encode()
	return u.String()
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func checkDatabase(ctx context.Context, db queryer) error {
	rows, err := db.QueryContext(ctx, "PRAGMA quick_check")
	if err != nil {
		return fmt.Errorf("数据库完整性检查失败：%w", err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			return err
		}
		if result != "ok" {
			return errors.New("数据库已经损坏：" + result)
		}
		found = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !found {
		return errors.New("数据库完整性检查没有结果")
	}
	return nil
}
