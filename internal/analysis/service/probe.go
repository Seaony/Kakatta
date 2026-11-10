package modelservice

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis/client"
	"github.com/Seaony/Kakatta/internal/quota"
)

type probeContextKey struct{}

type probeGuard struct {
	provider client.ProviderRef
	started  bool
}

func probeFromContext(ctx context.Context) *probeGuard {
	guard, _ := ctx.Value(probeContextKey{}).(*probeGuard)
	return guard
}

func (s *Service) ProbeDue(ctx context.Context) error {
	ctx, finish, err := s.beginJob(ctx)
	if err != nil {
		return err
	}
	defer finish()
	s.mu.Lock()
	if s.probeRunning {
		s.mu.Unlock()
		return nil
	}
	s.probeRunning = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.probeRunning = false
		s.mu.Unlock()
	}()
	if err := s.accountingFault(); err != nil {
		return err
	}
	providers, err := s.dueProbes(ctx)
	if err != nil {
		return storageFailure(err)
	}
	for _, provider := range providers {
		current, release, err := s.borrowClient(ctx)
		if err != nil {
			return err
		}
		guard := &probeGuard{provider: provider}
		_, err = current.Probe(context.WithValue(ctx, probeContextKey{}, guard), provider.ID)
		release()
		if err == nil || errors.Is(err, client.ErrConfigChanged) {
			continue
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, client.ErrDailyLimit) || errors.Is(err, client.ErrInfrastructure) {
			return err
		}
		var failure *client.Failure
		if !errors.As(err, &failure) {
			return err
		}
		// 普通请求失败已记入当天探测，继续检查其他服务商。
	}
	return nil
}

func (s *Service) dueProbes(ctx context.Context) ([]client.ProviderRef, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	_, clock, now, err := s.observeNowTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+providerColumns+providerJoin+` ORDER BY p.sort_index,p.id`)
	if err != nil {
		return nil, err
	}
	var records []providerRecord
	for rows.Next() {
		p, e := scanProvider(rows)
		if e != nil {
			err = e
			break
		}
		records = append(records, p)
	}
	if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	providers := []client.ProviderRef{}
	for _, p := range records {
		due, err := probeDueTx(ctx, tx, p, clock, now)
		if err != nil {
			return nil, err
		}
		if due {
			providers = append(providers, client.ProviderRef{ID: p.view.ID, ConfigToken: p.token})
		}
	}
	return providers, commitClock(ctx, tx, clock)
}

func probeDueTx(ctx context.Context, tx *sql.Tx, p providerRecord, clock quota.ClockState, now time.Time) (bool, error) {
	if !p.view.Enabled || p.view.ErrorState == nil {
		return false, nil
	}
	switch p.view.ErrorState.Code {
	case "analysis.client.balance", "analysis.client.daily_quota":
	default:
		return false, nil
	}
	if p.view.ErrorAt == nil || *p.view.ErrorAt <= 0 || *p.view.ErrorAt > now.UnixMilli() {
		return false, errors.New("模型探测错误时间无效")
	}
	location, err := time.LoadLocation(clock.Timezone)
	if err != nil {
		return false, err
	}
	today, _ := quota.DayBounds(now.In(location))
	if *p.view.ErrorAt >= today.UnixMilli() {
		return false, nil
	}
	start, err := clock.DayStart()
	if err != nil {
		return false, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT at FROM ai_calls WHERE provider_id=? AND purpose='probe' AND (at IS NULL OR at>=?)`, p.view.ID, start.UnixMilli())
	if err != nil {
		return false, err
	}
	defer rows.Close()
	attempted := false
	for rows.Next() {
		var at sql.NullInt64
		if err := rows.Scan(&at); err != nil {
			return false, err
		}
		if !at.Valid || at.Int64 > now.UnixMilli() {
			return false, errors.New("模型探测调用时间无效")
		}
		attempted = true
	}
	return !attempted, errors.Join(rows.Err(), rows.Close())
}

func (s *Service) probeProviderRecord(ctx context.Context, id int64, guard *probeGuard) (providerRecord, error) {
	if id != guard.provider.ID {
		return providerRecord{}, client.ErrConfigChanged
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return providerRecord{}, err
	}
	defer tx.Rollback()
	p, err := matchProviderTx(ctx, tx, guard.provider)
	if err != nil {
		return providerRecord{}, err
	}
	_, clock, now, err := s.observeNowTx(ctx, tx)
	if err != nil {
		return providerRecord{}, err
	}
	due, err := probeDueTx(ctx, tx, p, clock, now)
	if err == nil {
		err = commitClock(ctx, tx, clock)
	}
	if err != nil {
		return providerRecord{}, err
	}
	if !due {
		return providerRecord{}, client.ErrConfigChanged
	}
	return p, nil
}
