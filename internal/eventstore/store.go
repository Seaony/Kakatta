// Package eventstore 保存需要用户处理的系统事件，同一问题解决前只记一次。
package eventstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/Seaony/Kakatta/internal/locales"
)

type Store struct {
	DB  *sql.DB
	Now func() time.Time
}

func (s Store) Raise(ctx context.Context, kind string, subjectID int64, message locales.Message) (bool, error) {
	return s.raise(ctx, s.DB, kind, subjectID, message)
}

func (s Store) RaiseTx(ctx context.Context, tx *sql.Tx, kind string, subjectID int64, message locales.Message) (bool, error) {
	if tx == nil {
		return false, errors.New("系统事件事务不能为空")
	}
	return s.raise(ctx, tx, kind, subjectID, message)
}

type executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (s Store) raise(ctx context.Context, db executor, kind string, subjectID int64, message locales.Message) (bool, error) {
	raw, err := json.Marshal(message)
	if err != nil {
		return false, err
	}
	result, err := db.ExecContext(ctx, `INSERT INTO system_events(kind,subject_id,message,created_at)
		SELECT ?,?,?,? WHERE NOT EXISTS(SELECT 1 FROM system_events WHERE kind=? AND subject_id=? AND resolved_at IS NULL)`,
		kind, subjectID, string(raw), s.now(), kind, subjectID)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count > 0, err
}

func (s Store) Resolve(ctx context.Context, kind string, subjectID int64) (bool, error) {
	return s.resolve(ctx, s.DB, kind, subjectID)
}

func (s Store) ResolveTx(ctx context.Context, tx *sql.Tx, kind string, subjectID int64) (bool, error) {
	if tx == nil {
		return false, errors.New("系统事件事务不能为空")
	}
	return s.resolve(ctx, tx, kind, subjectID)
}

func (s Store) resolve(ctx context.Context, db executor, kind string, subjectID int64) (bool, error) {
	result, err := db.ExecContext(ctx, `UPDATE system_events SET resolved_at=? WHERE kind=? AND subject_id=? AND resolved_at IS NULL`, s.now(), kind, subjectID)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count > 0, err
}

func (s Store) now() int64 {
	if s.Now != nil {
		return s.Now().UnixMilli()
	}
	return time.Now().UnixMilli()
}
