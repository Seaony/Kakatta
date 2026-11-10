package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"time"

	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/pacer"
	"github.com/Seaony/Kakatta/internal/settings"
)

func (s *Service) schedulerLoop(ctx context.Context) {
	defer s.setNextWake(nil)
	for ctx.Err() == nil {
		status, _ := s.runtimeStatusSnapshot()
		if !status.Running {
			return
		}
		var next *time.Time
		if status.Accepting {
			var err error
			next, err = s.scheduleOnce(ctx)
			if err != nil {
				if ctx.Err() == nil {
					s.setRuntimeError(storageFailure(err))
				}
				return
			}
		}
		if !s.waitScheduler(ctx, next) {
			return
		}
	}
}

func (s *Service) scheduleOnce(ctx context.Context) (*time.Time, error) {
	next, err := s.advanceDeadlines(ctx)
	if err != nil {
		return nil, err
	}
	for _, request := range s.queuedStarts() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var started bool
		var blocked *RuntimeBlocked
		if request.Kind == SessionRun {
			started, blocked, err = s.tryStartRun(ctx, request)
		} else {
			started, blocked, err = s.tryStartSession(ctx, request)
		}
		if err != nil {
			if discardedStart(err) {
				s.removeQueued(request)
				continue
			}
			status, _ := s.runtimeStatusSnapshot()
			if !status.Accepting {
				return next, nil
			}
			return nil, err
		}
		if started || blocked != nil {
			s.removeQueued(request)
		}
	}
	starts, err := s.automaticStarts(ctx)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(starts, func(i, j int) bool {
		left, right := automaticReadyAt(starts[i]), automaticReadyAt(starts[j])
		if left.Equal(right) {
			return starts[i].Task.ID < starts[j].Task.ID
		}
		return left.Before(right)
	})
	for _, start := range starts {
		if start.Blocked != nil && start.Blocked.NextAllowedAt == nil && !start.NeedsUser {
			continue
		}
		ready := automaticReadyAt(start)
		if ready.After(start.Now) {
			next = earlierWake(next, ready)
			continue
		}
		if start.Blocked != nil && !start.NeedsUser {
			continue
		}
		request := startRequest{TaskID: start.Task.ID, Kind: SessionRun, Trigger: TriggerAuto}
		if start.Task.AccountID != nil {
			request.AccountID = *start.Task.AccountID
		}
		started, blocked, err := s.tryStartRun(ctx, request)
		if err != nil {
			if discardedStart(err) {
				continue
			}
			status, _ := s.runtimeStatusSnapshot()
			if !status.Accepting {
				return next, nil
			}
			return nil, err
		}
		if started || blocked != nil {
			// 前置跳过已经提交新计划，再看一轮才能准确睡到它的新时间。
			s.wakeScheduler()
		}
	}
	return next, nil
}

func (s *Service) automaticStarts(ctx context.Context) ([]preflightResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id FROM tasks WHERE enabled=1 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	starts := make([]preflightResult, 0, len(ids))
	for _, id := range ids {
		task, err := loadTaskTx(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		start, err := s.preflightTx(ctx, tx, task, TriggerAuto)
		if err != nil {
			return nil, err
		}
		if err := appsettings.SaveClock(ctx, tx, start.Clock); err != nil {
			return nil, err
		}
		starts = append(starts, start)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return starts, nil
}

func automaticReadyAt(start preflightResult) time.Time {
	at := start.Now
	if start.Task.NextRunAt != nil {
		at = time.UnixMilli(*start.Task.NextRunAt).UTC()
	}
	if until := start.Task.FailurePausedUntil; until != nil && *until > at.UnixMilli() {
		at = time.UnixMilli(*until).UTC()
	}
	if !start.NeedsUser {
		if start.Account.LastRunEndedAt != nil && start.Account.NextGapSeconds != nil {
			gap := time.UnixMilli(*start.Account.LastRunEndedAt).Add(time.Duration(*start.Account.NextGapSeconds) * time.Second)
			if gap.After(at) {
				at = gap.UTC()
			}
		}
		if until := start.Account.CooldownUntil; until != nil && *until > at.UnixMilli() &&
			(start.Account.State == AccountCooling || start.Account.State == AccountDenied) {
			at = time.UnixMilli(*until).UTC()
		}
	}
	if start.Blocked != nil && start.Blocked.NextAllowedAt != nil && *start.Blocked.NextAllowedAt > at.UnixMilli() {
		at = time.UnixMilli(*start.Blocked.NextAllowedAt).UTC()
	}
	return at
}

func earlierWake(next *time.Time, candidate time.Time) *time.Time {
	if next == nil || candidate.Before(*next) {
		copy := candidate.UTC()
		return &copy
	}
	return next
}

func discardedStart(err error) bool {
	var business *ServiceError
	return errors.As(err, &business) && (business.Kind == ErrorNotFound || business.Kind == ErrorConflict)
}

func (s *Service) waitScheduler(ctx context.Context, next *time.Time) bool {
	var at *int64
	if next != nil {
		ms := next.UnixMilli()
		at = &ms
	}
	s.setNextWake(at)
	if next == nil {
		select {
		case <-ctx.Done():
			return false
		case <-s.wake:
			return ctx.Err() == nil
		}
	}
	delay := next.Sub(s.now())
	if delay <= 0 {
		return ctx.Err() == nil
	}
	sleepCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- s.pacer.Wait(sleepCtx, pacer.Between(delay, delay)) }()
	select {
	case <-ctx.Done():
		cancel()
		<-done
		return false
	case <-s.wake:
		cancel()
		if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
			if ctx.Err() == nil {
				s.setRuntimeError(storageFailure(err))
			}
			return false
		}
		return ctx.Err() == nil
	case err := <-done:
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				s.setRuntimeError(storageFailure(err))
			}
			return false
		}
		return ctx.Err() == nil
	}
}

func nextRunAt(task Task, snapshot settings.Snapshot, ended time.Time, p *pacer.Pacer) (time.Time, error) {
	if ended.IsZero() || p == nil {
		return time.Time{}, errors.New("运行结束时间或随机等待来源缺失")
	}
	overrides := json.RawMessage(`{}`)
	if task.CrawlOverrides != nil {
		var err error
		overrides, err = json.Marshal(task.CrawlOverrides)
		if err != nil {
			return time.Time{}, err
		}
	}
	values, _, err := settings.ResolveTask(snapshot, task.IntervalMinutes, overrides)
	if err != nil {
		return time.Time{}, err
	}
	var timezone string
	if err := json.Unmarshal(snapshot.Values["general.timezone"], &timezone); err != nil {
		return time.Time{}, err
	}
	location, err := time.LoadLocation(timezone)
	if err != nil || timezone == "" || timezone == "Local" {
		return time.Time{}, errors.New("运行时区无效")
	}
	var quiet settings.QuietHours
	if err := json.Unmarshal(snapshot.Values["crawl.quiet_hours"], &quiet); err != nil {
		return time.Time{}, err
	}
	var percent int64
	if err := json.Unmarshal(values["interval_jitter_percent"], &percent); err != nil {
		return time.Time{}, err
	}
	base := time.Duration(task.IntervalMinutes) * time.Minute
	// 设置已检查总时长；先精确算上界，避免浮点舍入使合法的大抖动溢出。
	var extra big.Int
	extra.Mul(big.NewInt(int64(base)), big.NewInt(percent))
	extra.Quo(&extra, big.NewInt(100))
	maximum := time.Duration(extra.Int64())
	next := ended.Add(base + min(maximum, p.Pick(pacer.Between(0, maximum))))
	until, err := quietUntil(next, quiet, location)
	if err != nil {
		return time.Time{}, err
	}
	if until != nil {
		next = until.Add(p.Pick(pacer.Between(0, 5*time.Minute)))
	}
	return next.UTC(), nil
}

func quietUntil(now time.Time, quiet settings.QuietHours, location *time.Location) (*time.Time, error) {
	if now.IsZero() || location == nil {
		return nil, errors.New("静默时段缺少当前时间或时区")
	}
	for _, clock := range []string{quiet.Start, quiet.End} {
		parsed, err := time.Parse("15:04", clock)
		if err != nil || parsed.Format("15:04") != clock {
			return nil, fmt.Errorf("静默时段应使用 HH:mm：%q", clock)
		}
	}
	if !quiet.Enabled || quiet.Start == quiet.End || !inQuietHours(now.In(location), quiet) {
		return nil, nil
	}
	endClock, _ := time.Parse("15:04", quiet.End)
	cursor := now.UTC()
	for {
		local := cursor.In(location)
		if !inQuietHours(local, quiet) {
			return &cursor, nil
		}
		year, month, day := local.Date()
		wallEnd := time.Date(year, month, day, endClock.Hour(), endClock.Minute(), 0, 0, time.UTC)
		if quiet.Start > quiet.End && local.Format("15:04") >= quiet.Start {
			wallEnd = wallEnd.AddDate(0, 0, 1)
		}
		_, offset := local.Zone()
		_, zoneEnd := local.ZoneBounds()
		// Go 的未来规则在闰年末可能返回已过的年界，与额度日界使用同一推进口径。
		if !zoneEnd.IsZero() && !zoneEnd.After(cursor) {
			zoneEnd = time.Date(cursor.UTC().Year()+1, time.January, 1, 0, 0, 0, 0, time.UTC)
		}
		candidate := wallEnd.Add(-time.Duration(offset) * time.Second)
		if zoneEnd.IsZero() || candidate.Before(zoneEnd) {
			return &candidate, nil
		}
		// 结束时刻可能被跳过或重复，转到真实的下一个偏移区间后重算。
		cursor = zoneEnd
	}
}

func inQuietHours(now time.Time, quiet settings.QuietHours) bool {
	clock := now.Format("15:04")
	if quiet.Start < quiet.End {
		return clock >= quiet.Start && clock < quiet.End
	}
	return clock >= quiet.Start || clock < quiet.End
}
