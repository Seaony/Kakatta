package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/quota"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

// readObservedNowTx 给只读页面取时间，不推进时钟或修改旧日窗口。
func readObservedNowTx(ctx context.Context, tx *sql.Tx, observed time.Time) (time.Time, error) {
	state, err := readQuotaClockTx(ctx, tx)
	if err != nil {
		return time.Time{}, err
	}
	now := observed.UTC()
	if now.UnixMilli() < state.HighestUTC {
		now = time.UnixMilli(state.HighestUTC).UTC()
	}
	return now, nil
}

func readQuotaClockTx(ctx context.Context, tx *sql.Tx) (quota.ClockState, error) {
	if tx == nil {
		return quota.ClockState{}, storageFailure(errors.New("只读时间需要已有事务"))
	}
	var raw, marker string
	if err := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, appsettings.ClockKey).Scan(&raw); err != nil {
		return quota.ClockState{}, storageFailure(err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='quota.clock_initialized'`).Scan(&marker); err != nil {
		return quota.ClockState{}, storageFailure(err)
	}
	if marker != "1" {
		return quota.ClockState{}, storageFailure(errors.New("额度时钟初始化记录无效"))
	}
	var state quota.ClockState
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return quota.ClockState{}, storageFailure(err)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return quota.ClockState{}, storageFailure(errors.New("额度时钟包含额外内容"))
	}
	if err := state.Validate(); err != nil {
		return quota.ClockState{}, storageFailure(err)
	}
	return state, nil
}

// observeNowTx 不提交，保护时钟和业务写入由调用方一起保存。
func (s *Service) observeNowTx(ctx context.Context, tx *sql.Tx) (settings.Snapshot, quota.ClockState, time.Time, error) {
	if s.settings == nil || tx == nil {
		return settings.Snapshot{}, quota.ClockState{}, time.Time{}, storageFailure(errors.New("事务或设置存储缺失"))
	}
	snapshot, err := s.settings.SnapshotTx(ctx, tx)
	if err != nil {
		return settings.Snapshot{}, quota.ClockState{}, time.Time{}, storageFailure(err)
	}
	var timezone string
	if err := json.Unmarshal(snapshot.Values["general.timezone"], &timezone); err != nil {
		return settings.Snapshot{}, quota.ClockState{}, time.Time{}, storageFailure(err)
	}
	observed := time.Now()
	if s.now != nil {
		observed = s.now()
	}
	state, now, err := appsettings.ObserveClock(ctx, tx, observed, timezone, false)
	if err != nil {
		return settings.Snapshot{}, quota.ClockState{}, time.Time{}, storageFailure(err)
	}
	return snapshot, state, now, nil
}

func quotaLimits(snapshot settings.Snapshot) (quota.Limits, error) {
	var limits quota.Limits
	for _, field := range []struct {
		key   string
		value *int
	}{
		{"crawl.account_hourly_visits", &limits.Hourly},
		{"crawl.account_daily_search_visits", &limits.DailySearch},
		{"crawl.account_daily_detail_visits", &limits.DailyDetail},
	} {
		var value int64
		if err := json.Unmarshal(snapshot.Values[field.key], &value); err != nil {
			return quota.Limits{}, err
		}
		if value < 1 || int64(int(value)) != value {
			return quota.Limits{}, errors.New("访问额度上限无效")
		}
		*field.value = int(value)
	}
	return limits, nil
}

func loadQuotaTx(ctx context.Context, tx *sql.Tx, accountID int64, state quota.ClockState, now time.Time, limits quota.Limits) (QuotaView, []quota.Visit, error) {
	if accountID < 1 || tx == nil || now.UnixMilli() != state.HighestUTC {
		return QuotaView{}, nil, errors.New("读取访问额度的账号、事务或时间无效")
	}
	if err := validateQuotaFacts(nil, state, quota.Search, limits); err != nil {
		return QuotaView{}, nil, err
	}
	start, _ := state.DayStart()
	hourAgo := now.Add(-time.Hour)
	rows, err := tx.QueryContext(ctx, `SELECT at,kind FROM quota_events WHERE account_id=? AND at>=? ORDER BY at,id`,
		accountID, min(start.UnixMilli(), hourAgo.UnixMilli()))
	if err != nil {
		return QuotaView{}, nil, err
	}
	defer rows.Close()
	visits := []quota.Visit{}
	view := QuotaView{HourlyLimit: int64(limits.Hourly), DailySearchLimit: int64(limits.DailySearch),
		DailyDetailLimit: int64(limits.DailyDetail), DayStartAt: start.UnixMilli(), AsOf: now.UnixMilli()}
	for rows.Next() {
		var at int64
		var kind quota.Kind
		if err := rows.Scan(&at, &kind); err != nil {
			return QuotaView{}, nil, err
		}
		visit := quota.Visit{At: time.UnixMilli(at).UTC(), Kind: kind}
		visits = append(visits, visit)
		if visit.At.After(hourAgo) {
			view.HourlyUsed++
		}
		if !visit.At.Before(start) {
			if kind == quota.Search {
				view.DailySearchUsed++
			} else {
				view.DailyDetailUsed++
			}
		}
	}
	if err := rows.Err(); err != nil {
		return QuotaView{}, nil, err
	}
	if err := validateQuotaFacts(visits, state, quota.Search, limits); err != nil {
		return QuotaView{}, nil, err
	}
	return view, visits, nil
}

type visitBudget struct {
	service        *Service
	ctx            context.Context
	accountID      int64
	runID          *int64
	allowOverQuota bool
}

func (s *Service) newVisitBudget(ctx context.Context, accountID int64, runID *int64, allowOverQuota bool) web.Budget {
	if runID != nil {
		id := *runID
		runID = &id
	}
	return &visitBudget{service: s, ctx: ctx, accountID: accountID, runID: runID, allowOverQuota: allowOverQuota}
}

func (b *visitBudget) Acquire(kind quota.Kind, _ string) error {
	if err := b.ctx.Err(); err != nil {
		return err
	}
	if b.service.db == nil || kind != quota.Search && kind != quota.Detail {
		return storageFailure(errors.New("访问预算的数据库或类别无效"))
	}
	tx, err := b.service.db.BeginTx(b.ctx, nil)
	if err != nil {
		return storageFailure(err)
	}
	defer tx.Rollback()
	snapshot, state, now, err := b.service.observeNowTx(b.ctx, tx)
	if err != nil {
		return err
	}
	limits, err := quotaLimits(snapshot)
	if err != nil {
		return storageFailure(err)
	}
	_, visits, err := loadQuotaTx(b.ctx, tx, b.accountID, state, now, limits)
	if err != nil {
		return storageFailure(err)
	}
	var denied error
	if blocked := b.service.experimentBlock(now); blocked != nil {
		denied = blockedRunError(blocked)
	}
	if denied == nil && !b.allowOverQuota {
		reason, next, err := quotaReadyAt(visits, state, 1, kind, limits)
		if err != nil {
			return storageFailure(err)
		}
		if reason != nil {
			denied = fmt.Errorf("%w: %w", web.ErrQuota, &ServiceError{
				Kind: ErrorConflict, Reason: *reason, Fields: []settings.Issue{}, NextAllowedAt: next,
			})
		}
	}
	if denied == nil {
		if _, err := tx.ExecContext(b.ctx, `INSERT INTO quota_events(account_id,kind,run_id,at) VALUES(?,?,?,?)`, b.accountID, kind, b.runID, now.UnixMilli()); err != nil {
			return storageFailure(err)
		}
	}
	// 拒绝也保存观察时间，之后回调系统时钟不能退回旧窗口。
	if err := appsettings.SaveClock(b.ctx, tx, state); err != nil {
		return storageFailure(err)
	}
	if err := tx.Commit(); err != nil {
		return storageFailure(err)
	}
	return denied
}
