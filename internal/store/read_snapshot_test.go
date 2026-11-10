package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestReadSnapshotKeepsWALViewWithoutBlockingWriter(t *testing.T) {
	s := testStore(t)
	execute(t, s.DB(), "INSERT INTO meta(key,value) VALUES('version','before')")
	if info, err := os.Stat(s.path + "-wal"); err != nil || info.Size() == 0 {
		t.Fatal("测试需要包含已提交内容的 WAL", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	entered, release := make(chan *sql.Tx, 1), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	done := make(chan error, 1)
	go func() {
		done <- s.ReadSnapshot(ctx, func(tx *sql.Tx) error {
			var first string
			if err := tx.QueryRowContext(ctx, "SELECT value FROM meta WHERE key='version'").Scan(&first); err != nil {
				return err
			}
			if first != "before" {
				return fmt.Errorf("快照没有读到 WAL 中的提交：%s", first)
			}
			entered <- tx
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
			var later string
			var count int
			if err := tx.QueryRowContext(ctx, "SELECT value FROM meta WHERE key='version'").Scan(&later); err != nil {
				return err
			}
			if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM meta WHERE key='new'").Scan(&count); err != nil {
				return err
			}
			if later != first || count != 0 {
				return fmt.Errorf("同一次快照混入后续提交：%s %d", later, count)
			}
			return nil
		})
	}()
	var saved *sql.Tx
	select {
	case saved = <-entered:
	case err := <-done:
		t.Fatal("快照未进入读取", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err := s.DB().ExecContext(ctx, "UPDATE meta SET value='after' WHERE key='version'; INSERT INTO meta(key,value) VALUES('new','value')"); err != nil {
		t.Fatal("独立快照阻塞了主连接的写入", err)
	}
	if s.DB().Stats().MaxOpenConnections != 1 {
		t.Fatal("快照改变了主池连接数")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := saved.QueryRow("SELECT 1").Scan(new(int)); !errors.Is(err, sql.ErrTxDone) {
		t.Fatalf("回调结束后事务仍未关闭：%v", err)
	}
	if got := scalar[string](t, s.DB(), "SELECT value FROM meta WHERE key='version'"); got != "after" {
		t.Fatal("主连接写入未提交", got)
	}
}

func TestReadSnapshotIsReadOnlyAndPreservesCallbackError(t *testing.T) {
	s := testStore(t)
	want := errors.New("读取失败")
	var saved *sql.Tx
	err := s.ReadSnapshot(t.Context(), func(tx *sql.Tx) error {
		saved = tx
		for query, want := range map[string]int{"PRAGMA query_only": 1, "PRAGMA trusted_schema": 0, "PRAGMA foreign_keys": 1} {
			var got int
			if err := tx.QueryRow(query).Scan(&got); err != nil {
				return err
			}
			if got != want {
				return fmt.Errorf("连接保护不符：%s=%d", query, got)
			}
		}
		if _, err := tx.Exec("INSERT INTO meta(key,value) VALUES('forbidden','write')"); err == nil {
			return errors.New("快照允许写入")
		}
		if _, err := tx.Exec("PRAGMA query_only=OFF"); err != nil {
			return err
		}
		if _, err := tx.Exec("INSERT INTO meta(key,value) VALUES('forbidden','write')"); err == nil {
			return errors.New("mode=ro 没有阻止写入")
		}
		return fmt.Errorf("回调：%w", want)
	})
	if !errors.Is(err, want) {
		t.Fatalf("回调错误链丢失：%v", err)
	}
	if err := saved.QueryRow("SELECT 1").Scan(new(int)); !errors.Is(err, sql.ErrTxDone) {
		t.Fatalf("失败后事务仍未关闭：%v", err)
	}
	if count := scalar[int](t, s.DB(), "SELECT count(*) FROM meta WHERE key='forbidden'"); count != 0 {
		t.Fatal("只读回调改写了原库")
	}
}

func TestReadSnapshotCancellationBeforeAndDuringRead(t *testing.T) {
	s := testStore(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	called := false
	if err := s.ReadSnapshot(ctx, func(*sql.Tx) error { called = true; return nil }); !errors.Is(err, context.Canceled) || called {
		t.Fatalf("取消后仍调用回调或丢失错误：%v %t", err, called)
	}
	ctx, cancel = context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	err := s.ReadSnapshot(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n) SELECT sum(x) FROM n`).Scan(new(int64))
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("读取时取消的错误链丢失：%v", err)
	}
	if err := s.ReadSnapshot(t.Context(), func(tx *sql.Tx) error { return tx.QueryRow("SELECT 1").Scan(new(int)) }); err != nil {
		t.Fatal("取消后没有释放连接和租约", err)
	}
}

func TestReadSnapshotCancellationWhileDatabaseLocked(t *testing.T) {
	s := testStore(t)
	execute(t, s.DB(), "PRAGMA locking_mode=EXCLUSIVE")
	execute(t, s.DB(), "INSERT INTO meta(key,value) VALUES('lock','held')")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := s.ReadSnapshot(ctx, func(tx *sql.Tx) error { return tx.QueryRowContext(ctx, "SELECT count(*) FROM meta").Scan(new(int)) })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("等待锁取消的错误链丢失：%v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("等待锁没有随上下文及时结束：%s", elapsed)
	}
}

func TestCloseWaitsForSnapshotCallbackAndRefusesNewLeases(t *testing.T) {
	s := testStore(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	readDone := make(chan error, 1)
	go func() {
		readDone <- s.ReadSnapshot(ctx, func(tx *sql.Tx) error {
			if err := tx.QueryRow("SELECT 1").Scan(new(int)); err != nil {
				return err
			}
			close(entered)
			<-release
			return nil
		})
	}()
	select {
	case <-entered:
	case err := <-readDone:
		t.Fatal(err)
	case <-time.After(3 * time.Second):
		t.Fatal("快照没有进入回调")
	}
	cancel()
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	for {
		s.snapshotMu.Lock()
		closing := s.closed
		s.snapshotMu.Unlock()
		if closing {
			break
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case err := <-closed:
		t.Fatal("回调尚未返回，Close 已结束", err)
	case <-time.After(30 * time.Millisecond):
	}
	if err := s.ReadSnapshot(ctx, func(*sql.Tx) error { return errors.New("不能调用") }); !errors.Is(err, context.Canceled) {
		t.Fatalf("Close 等待期间取消错误丢失：%v", err)
	}
	called := false
	if err := s.ReadSnapshot(t.Context(), func(*sql.Tx) error { called = true; return nil }); err == nil || called {
		t.Fatal("Close 等待期间登记了新快照", err)
	}
	close(release)
	if err := <-readDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("取消的快照返回错误不符：%v", err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.path); err != nil {
		t.Fatal(err)
	}
	if err := s.ReadSnapshot(t.Context(), func(*sql.Tx) error { called = true; return nil }); err == nil || !strings.Contains(err.Error(), "关闭") || called {
		t.Fatal("关闭后重新打开了路径", err)
	}
	if _, err := os.Stat(s.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("关闭后重新创建了数据库", err)
	}
}
