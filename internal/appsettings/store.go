// Package appsettings 在同一事务里保存设置和额度时钟。
package appsettings

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/quota"
	"github.com/Seaony/Kakatta/internal/settings"
)

const ClockKey = "quota.clock"
const clockMarker = "quota.clock_initialized"

type Store struct {
	db  *sql.DB
	env settings.Environment
	now func() time.Time
}

func New(ctx context.Context, db *sql.DB, env settings.Environment, now func() time.Time) (*Store, error) {
	if db == nil {
		return nil, errors.New("设置存储缺少数据库")
	}
	if now == nil {
		now = time.Now
	}
	if env.TrustedProxies != nil {
		value := *env.TrustedProxies
		env.TrustedProxies = &value
	}
	s := &Store{db: db, env: env, now: now}
	if _, err := s.transact(ctx, nil, true); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Definitions() []settings.Definition { return settings.Definitions(s.env) }

func (s *Store) Get(ctx context.Context) (settings.Snapshot, error) {
	return s.transact(ctx, nil, false)
}

func (s *Store) Apply(ctx context.Context, patch settings.Values) (settings.Snapshot, error) {
	if patch == nil {
		patch = settings.Values{}
	}
	return s.transact(ctx, patch, false)
}

// SnapshotTx 只读取调用方的事务，提交和额度时钟由调用方处理。
func (s *Store) SnapshotTx(ctx context.Context, tx *sql.Tx) (settings.Snapshot, error) {
	stored, err := readValues(ctx, tx)
	if err != nil {
		return settings.Snapshot{}, err
	}
	return settings.Resolve(stored, s.env)
}

func (s *Store) transact(ctx context.Context, patch settings.Values, initialize bool) (settings.Snapshot, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return settings.Snapshot{}, err
	}
	defer tx.Rollback()
	snapshot, err := s.SnapshotTx(ctx, tx)
	if err != nil {
		return settings.Snapshot{}, err
	}
	if patch != nil {
		snapshot, err = settings.Apply(snapshot, patch)
		if err != nil {
			return settings.Snapshot{}, err
		}
		if err := validateReferences(ctx, tx, snapshot.Values); err != nil {
			return settings.Snapshot{}, err
		}
	}
	var timezone string
	if err := json.Unmarshal(snapshot.Values["general.timezone"], &timezone); err != nil {
		return settings.Snapshot{}, err
	}
	clock, now, err := ObserveClock(ctx, tx, s.now(), timezone, initialize)
	if err != nil {
		return settings.Snapshot{}, err
	}
	if patch == nil && !initialize {
		return snapshot, nil
	}
	for key := range patch {
		_, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value_json,updated_at) VALUES(?,?,?)
			ON CONFLICT(key) DO UPDATE SET value_json=excluded.value_json,updated_at=excluded.updated_at`, key, string(snapshot.Values[key]), now.UnixMilli())
		if err != nil {
			return settings.Snapshot{}, err
		}
	}
	if err := writeClock(ctx, tx, clock); err != nil {
		return settings.Snapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return settings.Snapshot{}, err
	}
	return snapshot, nil
}

func readValues(ctx context.Context, tx *sql.Tx) (settings.Values, error) {
	rows, err := tx.QueryContext(ctx, `SELECT key,value_json FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := settings.Values{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		values[key] = json.RawMessage(value)
	}
	return values, rows.Err()
}

// ObserveClock 必须在额度检查和记账所用的事务内调用，再保存返回的时钟。
func ObserveClock(ctx context.Context, tx *sql.Tx, observed time.Time, timezone string, initialize bool) (quota.ClockState, time.Time, error) {
	var raw, marker string
	clockErr := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, ClockKey).Scan(&raw)
	markerErr := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, clockMarker).Scan(&marker)
	var previous quota.ClockState
	if errors.Is(clockErr, sql.ErrNoRows) && errors.Is(markerErr, sql.ErrNoRows) && initialize {
		var used bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM quota_events) OR EXISTS(SELECT 1 FROM ai_calls)`).Scan(&used); err != nil {
			return quota.ClockState{}, time.Time{}, err
		}
		if used {
			return quota.ClockState{}, time.Time{}, errors.New("已有用量记录但额度时钟缺失，请恢复备份")
		}
	} else if clockErr != nil || markerErr != nil || marker != "1" {
		return quota.ClockState{}, time.Time{}, errors.New("额度时钟缺失或无法读取，请检查数据库")
	} else {
		decoder := json.NewDecoder(bytes.NewBufferString(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&previous); err != nil {
			return quota.ClockState{}, time.Time{}, errors.New("额度时钟记录损坏")
		}
		if decoder.Decode(new(any)) != io.EOF || previous.Validate() != nil {
			return quota.ClockState{}, time.Time{}, errors.New("额度时钟记录损坏")
		}
	}
	return quota.AdvanceClock(previous, observed, timezone)
}

// SaveClock 和访问记录、用量检查一起提交；提交失败不得执行外部请求。
func SaveClock(ctx context.Context, tx *sql.Tx, state quota.ClockState) error {
	if err := state.Validate(); err != nil {
		return err
	}
	return writeClock(ctx, tx, state)
}

func writeClock(ctx context.Context, tx *sql.Tx, state quota.ClockState) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, ClockKey, string(raw)); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, clockMarker, "1")
	return err
}

func validateReferences(ctx context.Context, tx *sql.Tx, values settings.Values) error {
	var proxy *int64
	if err := json.Unmarshal(values["ai.proxy_id"], &proxy); err != nil {
		return err
	}
	var channels []int64
	if err := json.Unmarshal(values["notify.system_channel_ids"], &channels); err != nil {
		return err
	}
	var issues []settings.Issue
	if proxy != nil {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM proxies WHERE id=?)`, *proxy).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			issues = append(issues, missingReference("ai.proxy_id"))
		}
	}
	for _, id := range channels {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM channels WHERE id=?)`, id).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			issues = append(issues, missingReference("notify.system_channel_ids"))
			break
		}
	}
	if len(issues) != 0 {
		return &settings.ValidationError{Issues: issues}
	}
	return nil
}

func missingReference(key string) settings.Issue {
	return settings.Issue{Key: key, Reason: locales.Message{Code: "settings.reference_not_found"}}
}
