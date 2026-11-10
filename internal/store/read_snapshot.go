package store

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"path/filepath"
)

// ReadSnapshot 在独立只读事务里读取，回调结束并关闭连接后才归还租约。
func (s *Store) ReadSnapshot(ctx context.Context, read func(*sql.Tx) error) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.snapshotMu.Lock()
	if s.closed {
		s.snapshotMu.Unlock()
		return errors.New("数据库已经关闭，不能读取快照")
	}
	s.snapshotReads.Add(1)
	s.snapshotMu.Unlock()
	defer s.snapshotReads.Done()
	defer func() {
		if canceled := ctx.Err(); canceled != nil && !errors.Is(err, canceled) {
			err = errors.Join(err, canceled)
		}
	}()
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(s.path)}
	u.RawQuery = url.Values{"mode": {"ro"}, "_pragma": {"query_only(1)", "trusted_schema(0)", "foreign_keys(1)", "busy_timeout(5000)"}}.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	defer func() { err = errors.Join(err, db.Close()) }()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	// Close 会等取消后的事务回滚结束，不能先把快照租约还出去。
	defer func() {
		if closeErr := conn.Close(); !errors.Is(closeErr, sql.ErrConnDone) {
			err = errors.Join(err, closeErr)
		}
	}()
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer func() {
		if rollbackErr := tx.Rollback(); !errors.Is(rollbackErr, sql.ErrTxDone) {
			err = errors.Join(err, rollbackErr)
		}
	}()
	if err = read(tx); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return tx.Commit()
}
