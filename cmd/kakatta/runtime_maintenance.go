package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis/client"
	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/quota"
	"github.com/Seaony/Kakatta/internal/service"
)

func (g *runtimeGraph) start() error {
	g.maintenanceMu.Lock()
	defer g.maintenanceMu.Unlock()
	if err := g.ctx.Err(); err != nil {
		return err
	}
	if g.maintenanceDone != nil {
		return nil
	}
	if err := g.core.Start(); err != nil {
		return err
	}
	g.maintenanceDone = make(chan struct{})
	go g.maintenanceLoop()
	return nil
}

func (g *runtimeGraph) wakeMaintenance() {
	select {
	case g.maintenanceWake <- struct{}{}:
	default:
	}
}

func (g *runtimeGraph) maintenanceLoop() {
	defer close(g.maintenanceDone)
	ctx := g.core.RuntimeContext()
	for ctx.Err() == nil {
		select {
		case <-g.maintenanceWake:
		default:
		}
		next := g.maintain()
		if ctx.Err() != nil {
			return
		}
		var timer *time.Timer
		var due <-chan time.Time
		if next != nil {
			timer = time.NewTimer(max(time.Until(*next), 0))
			due = timer.C
		}
		select {
		case <-ctx.Done():
		case <-g.maintenanceWake:
		case <-due:
		}
		if timer != nil {
			timer.Stop()
		}
	}
}

func (g *runtimeGraph) maintain() *time.Time {
	ctx := g.core.RuntimeContext()
	if ctx.Err() != nil {
		return nil
	}
	next, err := g.nextProbeWake(ctx, time.Now())
	if ctx.Err() != nil {
		return nil
	}
	g.logMaintenance(err)
	if err == nil && next != nil {
		g.logMaintenance(g.models.ProbeDue(ctx))
		if ctx.Err() != nil {
			return nil
		}
		next, err = g.nextProbeWake(ctx, time.Now())
		g.logMaintenance(err)
	}
	if ctx.Err() != nil {
		return nil
	}
	status, err := g.updates.Read(ctx)
	if ctx.Err() != nil {
		return nil
	}
	g.logMaintenance(err)
	if err != nil {
		return next
	}
	if status.Enabled && status.NextCheckAt != nil && *status.NextCheckAt <= time.Now().UnixMilli() {
		g.logMaintenance(g.updates.CheckDue(ctx))
		if ctx.Err() != nil {
			return nil
		}
		logGraphPublish(g.publish("update.changed", nil, nil))
		status, err = g.updates.Read(ctx)
		g.logMaintenance(err)
	}
	if ctx.Err() != nil {
		return nil
	}
	// 失败且没有推进期限时只等真实变更，不能用立即重试形成空转。
	if err == nil && status.Enabled && status.NextCheckAt != nil && *status.NextCheckAt > time.Now().UnixMilli() {
		at := time.UnixMilli(*status.NextCheckAt)
		if next == nil || at.Before(*next) {
			next = &at
		}
	}
	return next
}

func (g *runtimeGraph) nextProbeWake(ctx context.Context, observed time.Time) (*time.Time, error) {
	tx, err := g.database.DB().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT error_state FROM ai_providers WHERE enabled=1 AND error_state IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	eligible := false
	for rows.Next() {
		var raw string
		var state struct{ Code string }
		if err = rows.Scan(&raw); err == nil {
			err = json.Unmarshal([]byte(raw), &state)
		}
		if err != nil {
			break
		}
		if state.Code == "analysis.client.balance" || state.Code == "analysis.client.daily_quota" {
			eligible = true
		}
	}
	if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	if !eligible {
		return nil, tx.Commit()
	}
	snapshot, err := g.preferences.SnapshotTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	var timezone string
	if err := json.Unmarshal(snapshot.Values["general.timezone"], &timezone); err != nil {
		return nil, err
	}
	clock, now, err := appsettings.ObserveClock(ctx, tx, observed, timezone, false)
	if err != nil {
		return nil, err
	}
	location, _ := time.LoadLocation(clock.Timezone)
	_, next := quota.DayBounds(now.In(location))
	for _, window := range clock.Windows {
		at := time.UnixMilli(window.End)
		if at.After(now) && at.Before(next) {
			next = at
		}
	}
	return &next, tx.Commit()
}

func (g *runtimeGraph) logMaintenance(err error) {
	if err == nil || g.ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, client.ErrDailyLimit) {
		return
	}
	reason := locales.Message{Code: "service.storage_failure", Args: map[string]any{}}
	var failure *service.ServiceError
	if errors.As(err, &failure) {
		reason = failure.Reason
	}
	if reason.Code == "update.check_failed" {
		slog.Warn("更新检查失败", "reason", reason)
		return
	}
	slog.Error("运行维护失败", "reason", reason)
}

func (g *runtimeGraph) drain(ctx context.Context) error {
	g.drainOnce.Do(func() {
		g.maintenanceMu.Lock()
		g.cancel()
		done := g.maintenanceDone
		g.maintenanceMu.Unlock()
		if done != nil {
			<-done
		}
		if g.handler != nil {
			g.drainErr = g.handler.Close()
		}
		if g.core != nil {
			g.drainErr = errors.Join(g.drainErr, g.core.Close())
		}
		if g.models != nil {
			g.drainErr = errors.Join(g.drainErr, g.models.Close(ctx))
		}
		if g.notifications != nil {
			g.drainErr = errors.Join(g.drainErr, g.notifications.Close(ctx))
		}
		if g.updateTransport != nil {
			g.updateTransport.CloseIdleConnections()
		}
	})
	return g.drainErr
}
