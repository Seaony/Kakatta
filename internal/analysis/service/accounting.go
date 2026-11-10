package modelservice

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis/client"
	"github.com/Seaony/Kakatta/internal/eventstore"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/quota"
	"github.com/Seaony/Kakatta/internal/settings"
)

var _ client.Accounting = (*Service)(nil)

type accountingState struct {
	mu      sync.Mutex
	fault   error
	next    int64
	tickets map[int64]callTicket
}

type callTicket struct {
	start  client.CallStart
	at     int64
	finish func()
}

type budgetChange struct {
	id      int64
	raised  bool
	changed bool
}

func (s *Service) accountingFault() error {
	s.accounting.mu.Lock()
	defer s.accounting.mu.Unlock()
	return s.accounting.fault
}

func validCall(start client.CallStart) bool {
	if start.Provider.ID < 1 || start.Provider.ConfigToken == "" || strings.TrimSpace(start.Model) == "" || compatRank(start.JSONMode) < 0 || start.TaskID != nil && *start.TaskID < 1 {
		return false
	}
	switch start.Purpose {
	case "title_screen", "analysis", "profile", "market", "test", "probe":
		return true
	}
	return false
}

func (s *Service) Before(ctx context.Context, start client.CallStart) (client.Ticket, error) {
	ctx, finish, err := s.beginJob(ctx)
	if err != nil {
		return client.Ticket{}, err
	}
	retained := false
	defer func() {
		if !retained {
			finish()
		}
	}()
	s.accounting.mu.Lock()
	defer s.accounting.mu.Unlock()
	if s.accounting.fault != nil {
		return client.Ticket{}, s.accounting.fault
	}
	if !validCall(start) {
		return client.Ticket{}, storageFailure(errors.New("模型请求票据参数无效"))
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return client.Ticket{}, storageFailure(err)
	}
	defer tx.Rollback()
	p, err := matchProviderTx(ctx, tx, start.Provider)
	if err == nil && start.Model != p.view.VisionModel && (p.view.TextModel == nil || start.Model != *p.view.TextModel) {
		err = client.ErrConfigChanged
	}
	if err != nil {
		if err == client.ErrConfigChanged {
			return client.Ticket{}, err
		}
		return client.Ticket{}, storageFailure(err)
	}
	snapshot, clock, now, err := s.observeNowTx(ctx, tx)
	if err != nil {
		return client.Ticket{}, storageFailure(err)
	}
	guard := probeFromContext(ctx)
	if guard != nil {
		if start.Purpose != "probe" || start.Provider != guard.provider {
			return client.Ticket{}, client.ErrConfigChanged
		}
		if !guard.started {
			due, err := probeDueTx(ctx, tx, p, clock, now)
			if err != nil {
				return client.Ticket{}, storageFailure(err)
			}
			if !due {
				if err := commitClock(ctx, tx, clock); err != nil {
					return client.Ticket{}, storageFailure(err)
				}
				return client.Ticket{}, client.ErrConfigChanged
			}
		}
	}
	limit, used, err := budgetUsageTx(ctx, tx, snapshot, clock)
	if err != nil {
		return client.Ticket{}, storageFailure(err)
	}
	change, err := budgetEventTx(ctx, tx, limit, used, now)
	if err == nil {
		err = commitClock(ctx, tx, clock)
	}
	if err != nil {
		return client.Ticket{}, storageFailure(err)
	}
	if limit > 0 && used >= limit && start.Purpose != "test" {
		// 回调在锁外，通知失败不会改变已经提交的额度事实。
		s.accounting.mu.Unlock()
		s.committedAccountingEvents(ctx, nil, change)
		s.accounting.mu.Lock()
		if err := ctx.Err(); err != nil {
			return client.Ticket{}, err
		}
		if s.accounting.fault != nil {
			return client.Ticket{}, s.accounting.fault
		}
		return client.Ticket{}, client.ErrDailyLimit
	}
	if err := ctx.Err(); err != nil {
		return client.Ticket{}, err
	}
	if start.TaskID != nil {
		id := *start.TaskID
		start.TaskID = &id
	}
	if s.accounting.tickets == nil {
		s.accounting.tickets = make(map[int64]callTicket)
	}
	s.accounting.next++
	ticket := client.Ticket{ID: s.accounting.next}
	s.accounting.tickets[ticket.ID] = callTicket{start: start, at: now.UnixMilli(), finish: finish}
	retained = true
	s.accounting.mu.Unlock()
	s.committedAccountingEvents(ctx, nil, change)
	s.accounting.mu.Lock()
	if err := ctx.Err(); err != nil || s.accounting.fault != nil {
		delete(s.accounting.tickets, ticket.ID)
		retained = false
		if err != nil {
			return client.Ticket{}, err
		}
		return client.Ticket{}, s.accounting.fault
	}
	if guard != nil {
		// 同一逻辑探测的参数兼容重发仍逐次记账，但不按当天记录挡住自己。
		guard.started = true
	}
	return ticket, nil
}

func budgetUsageTx(ctx context.Context, tx *sql.Tx, snapshot settings.Snapshot, clock quota.ClockState) (int64, int64, error) {
	var limit int64
	if err := json.Unmarshal(snapshot.Values["ai.daily_token_limit"], &limit); err != nil || limit < 0 {
		return 0, 0, errors.New("模型每日上限无效")
	}
	start, err := clock.DayStart()
	if err != nil {
		return 0, 0, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT at,input_tokens,output_tokens FROM ai_calls WHERE at IS NULL OR at>=?`, start.UnixMilli())
	if err != nil {
		return 0, 0, err
	}
	var used int64
	for rows.Next() {
		var at, input, output sql.NullInt64
		if err = rows.Scan(&at, &input, &output); err != nil {
			break
		}
		if !at.Valid || at.Int64 > clock.HighestUTC || input.Int64 < 0 || output.Int64 < 0 || input.Int64 > math.MaxInt64-output.Int64 || used > math.MaxInt64-input.Int64-output.Int64 {
			err = errors.New("模型用量记录无效或溢出")
			break
		}
		used += input.Int64 + output.Int64
	}
	return limit, used, errors.Join(err, rows.Err(), rows.Close())
}

func budgetEventTx(ctx context.Context, tx *sql.Tx, limit, used int64, now time.Time) (budgetChange, error) {
	events := eventstore.Store{Now: func() time.Time { return now }}
	if limit == 0 || used < limit {
		var id int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM system_events WHERE kind='ai_budget' AND subject_id=0 AND resolved_at IS NULL`).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return budgetChange{}, nil
		}
		if err != nil {
			return budgetChange{}, err
		}
		changed, err := events.ResolveTx(ctx, tx, "ai_budget", 0)
		return budgetChange{id: id, changed: changed}, err
	}
	message := locales.Message{Code: "run.ai_daily_limit", Args: map[string]any{"tokens": used}}
	raised, err := events.RaiseTx(ctx, tx, "ai_budget", 0, message)
	if err != nil {
		return budgetChange{}, err
	}
	var id int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM system_events WHERE kind='ai_budget' AND subject_id=0 AND resolved_at IS NULL`).Scan(&id); err != nil {
		return budgetChange{}, err
	}
	changed := raised
	if !raised {
		raw, err := json.Marshal(message)
		if err != nil {
			return budgetChange{}, err
		}
		updated, err := tx.ExecContext(ctx, `UPDATE system_events SET message=? WHERE id=? AND message<>?`, string(raw), id, string(raw))
		if err != nil {
			return budgetChange{}, err
		}
		count, err := updated.RowsAffected()
		if err != nil {
			return budgetChange{}, err
		}
		changed = count > 0
	}
	return budgetChange{id: id, raised: raised, changed: changed}, nil
}

func (s *Service) After(ctx context.Context, id client.Ticket, end client.CallEnd) error {
	s.accounting.mu.Lock()
	ticket, found := s.accounting.tickets[id.ID]
	if !found {
		err := s.latchAccountingFault(errors.New("模型请求票据不存在或已消费"))
		s.accounting.mu.Unlock()
		return err
	}
	delete(s.accounting.tickets, id.ID)
	change, err := s.finishCallTx(ctx, ticket, end)
	if err != nil {
		err = s.latchAccountingFault(err)
	}
	s.accounting.mu.Unlock()
	defer ticket.finish()
	if err != nil {
		return err
	}
	s.committedAccountingEvents(ctx, &ticket.start, change)
	return nil
}

// 调用方持记账锁；第一处故障唤醒等名额的请求，让它们一起停止。
func (s *Service) latchAccountingFault(err error) error {
	if s.accounting.fault == nil {
		s.accounting.fault = storageFailure(err)
		s.slots.mu.Lock()
		if s.slots.changed != nil {
			close(s.slots.changed)
			s.slots.changed = make(chan struct{})
		}
		s.slots.mu.Unlock()
	}
	return s.accounting.fault
}

func validCallEnd(end client.CallEnd) bool {
	if end.Duration < 0 || end.HTTPStatus != 0 && (end.HTTPStatus < 100 || end.HTTPStatus > 599) || end.OK && (end.HTTPStatus < 200 || end.HTTPStatus >= 300) {
		return false
	}
	usage := end.Usage
	return !usage.Present || usage.InputTokens >= 0 && usage.OutputTokens >= 0 && usage.InputTokens <= math.MaxInt64-usage.OutputTokens && (usage.ReasoningTokens == nil || *usage.ReasoningTokens >= 0)
}

func (s *Service) finishCallTx(ctx context.Context, ticket callTicket, end client.CallEnd) (budgetChange, error) {
	if !validCallEnd(end) {
		return budgetChange{}, errors.New("模型请求用量或结果无效")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return budgetChange{}, err
	}
	defer tx.Rollback()
	snapshot, clock, now, err := s.observeNowTx(ctx, tx)
	if err != nil {
		return budgetChange{}, err
	}
	var input, output, reasoning any
	if end.Usage.Present {
		input, output, reasoning = end.Usage.InputTokens, end.Usage.OutputTokens, end.Usage.ReasoningTokens
	}
	var status, errorKind, finishReason any
	if end.HTTPStatus != 0 {
		status = end.HTTPStatus
	}
	if end.ErrorKind != "" {
		errorKind = end.ErrorKind
	}
	if end.FinishReason != "" {
		finishReason = end.FinishReason
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO ai_calls(at,provider_id,model,purpose,task_id,ok,http_status,error_kind,json_mode,input_tokens,output_tokens,reasoning_tokens,finish_reason,duration_ms) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		ticket.at, ticket.start.Provider.ID, ticket.start.Model, ticket.start.Purpose, ticket.start.TaskID, end.OK, status, errorKind, ticket.start.JSONMode, input, output, reasoning, finishReason, end.Duration.Milliseconds()); err != nil {
		return budgetChange{}, err
	}
	limit, used, err := budgetUsageTx(ctx, tx, snapshot, clock)
	if err != nil {
		return budgetChange{}, err
	}
	change, err := budgetEventTx(ctx, tx, limit, used, now)
	if err == nil {
		err = commitClock(ctx, tx, clock)
	}
	return change, err
}

func (s *Service) committedAccountingEvents(ctx context.Context, start *client.CallStart, change budgetChange) {
	var err error
	if start != nil {
		raw, _ := json.Marshal(map[string]any{"provider_id": start.Provider.ID, "task_id": start.TaskID})
		err = s.publish("ai.usage.changed", nil, raw)
	}
	if change.changed {
		err = errors.Join(err, s.publishSystemChange(ctx, change.id, change.raised))
	}
	logCallbackFailure(err)
}
