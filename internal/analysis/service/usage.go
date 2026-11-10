package modelservice

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"slices"
	"strconv"
	"time"

	"github.com/Seaony/Kakatta/internal/limits"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/quota"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/settings"
)

func (s *Service) BudgetStatus(ctx context.Context) (BudgetStatus, error) {
	ctx, done, err := s.beginJob(ctx)
	if err != nil {
		return BudgetStatus{}, err
	}
	defer done()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return BudgetStatus{}, storageFailure(err)
	}
	defer tx.Rollback()
	snapshot, clock, now, err := s.observeNowTx(ctx, tx)
	if err != nil {
		return BudgetStatus{}, storageFailure(err)
	}
	limit, used, err := budgetUsageTx(ctx, tx, snapshot, clock)
	if err != nil {
		return BudgetStatus{}, storageFailure(err)
	}
	start, err := clock.DayStart()
	if err != nil {
		return BudgetStatus{}, storageFailure(err)
	}
	result := BudgetStatus{Limit: limit, ProtectedUsed: used, ProtectedStart: start.UnixMilli(), EffectiveNow: now.UnixMilli(), Blocked: limit > 0 && used >= limit}
	if result.Blocked {
		result.NextCheckAt, err = nextBudgetCheckTx(ctx, tx, snapshot, clock)
		if err != nil {
			return BudgetStatus{}, storageFailure(err)
		}
	}
	return result, storageFailure(tx.Commit())
}

func nextBudgetCheckTx(ctx context.Context, tx *sql.Tx, snapshot settings.Snapshot, clock quota.ClockState) (*int64, error) {
	// 只在窗口起点可能前进的时刻重算，旧时区午夜前不提前放行。
	for remaining := len(clock.Windows) + 2; remaining > 0; remaining-- {
		start, err := clock.DayStart()
		if err != nil {
			return nil, err
		}
		location, _ := time.LoadLocation(clock.Timezone)
		currentStart, currentEnd := quota.DayBounds(time.UnixMilli(clock.HighestUTC).In(location))
		boundary := currentEnd.UnixMilli()
		if start.Before(currentStart) {
			boundary = 0
			for _, window := range clock.Windows {
				if window.Start == start.UnixMilli() && window.End > clock.HighestUTC && (boundary == 0 || window.End < boundary) {
					boundary = window.End
				}
			}
		}
		if boundary <= clock.HighestUTC {
			return nil, errors.New("模型额度窗口没有向前推进")
		}
		clock, _, err = quota.AdvanceClock(clock, time.UnixMilli(boundary), clock.Timezone)
		if err != nil {
			return nil, err
		}
		limit, used, err := budgetUsageTx(ctx, tx, snapshot, clock)
		if err != nil {
			return nil, err
		}
		if used < limit {
			return &boundary, nil
		}
	}
	return nil, errors.New("无法确定模型额度窗口的结束时间")
}

func (s *Service) Usage(ctx context.Context, query UsageQuery) (UsageReport, error) {
	ctx, done, err := s.beginJob(ctx)
	if err != nil {
		return UsageReport{}, err
	}
	defer done()
	if !slices.Contains([]UsageWindow{UsageToday, Usage7Days, Usage30Days}, query.Window) || query.TaskID != nil && (*query.TaskID < 1 || *query.TaskID > limits.MaxSafeInteger) || query.Purpose != nil && !validUsagePurpose(*query.Purpose) {
		return UsageReport{}, &service.ServiceError{Kind: service.ErrorBadRequest, Reason: locales.Message{Code: "service.invalid_query", Args: map[string]any{}}, Fields: []settings.Issue{}}
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return UsageReport{}, storageFailure(err)
	}
	defer tx.Rollback()
	_, clock, now, err := s.observeNowTx(ctx, tx)
	if err != nil {
		return UsageReport{}, storageFailure(err)
	}
	location, _ := time.LoadLocation(clock.Timezone)
	days := 0
	if query.Window == Usage7Days {
		days = 6
	} else if query.Window == Usage30Days {
		days = 29
	}
	local := now.In(location)
	date := time.Date(local.Year(), local.Month(), local.Day()-days, 0, 0, 0, 0, time.UTC)
	anchor := time.Date(date.Year(), date.Month(), date.Day(), 12, 0, 0, 0, location)
	year, month, day := anchor.Date()
	if time.Date(year, month, day, 0, 0, 0, 0, time.UTC).Before(date) {
		// 整天被时区跳过时，取该日期之后第一个存在的日界。
		anchor = anchor.Add(24 * time.Hour)
	}
	from, _ := quota.DayBounds(anchor)
	result := UsageReport{From: from.UnixMilli(), Until: now.UnixMilli(), Timezone: clock.Timezone, ByTask: []TaskUsage{}, ByPurpose: []PurposeUsage{}}
	result.RetainedFrom, err = usageRetainedFromTx(ctx, tx)
	if err != nil {
		return UsageReport{}, storageFailure(err)
	}
	result.Incomplete = result.RetainedFrom == nil || *result.RetainedFrom > result.From
	if err = readUsageTx(ctx, tx, query, &result); err != nil {
		return UsageReport{}, storageFailure(err)
	}
	return result, storageFailure(tx.Commit())
}

func validUsagePurpose(purpose string) bool {
	return slices.Contains([]string{"title_screen", "analysis", "profile", "market", "test", "probe"}, purpose)
}

func usageRetainedFromTx(ctx context.Context, tx *sql.Tx) (*int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT key,value FROM meta WHERE key IN ('stats.started_at','stats.coverage.ai_calls')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var started bool
	var latest int64
	for rows.Next() {
		var key, raw string
		if err := rows.Scan(&key, &raw); err != nil {
			return nil, err
		}
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value <= 0 || strconv.FormatInt(value, 10) != raw {
			return nil, errors.New("模型用量覆盖记录损坏")
		}
		started = started || key == "stats.started_at"
		latest = max(latest, value)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if !started {
		return nil, nil
	}
	return &latest, nil
}

type usageRecord struct {
	at, taskID, ok, duration, input, output, reasoning sql.NullInt64
	purpose, taskName                                  sql.NullString
}

type usageAccumulator struct {
	total                  UsageTotal
	knownOK, knownDuration int64
	incomplete             bool
}

func (a *usageAccumulator) add(row usageRecord) error {
	a.total.Calls++
	if row.ok.Valid {
		a.knownOK++
		if row.ok.Int64 == 1 {
			a.total.OKCalls++
		}
	}
	if row.duration.Valid {
		a.knownDuration++
	}
	if !row.input.Valid || !row.output.Valid {
		a.total.MissingUsageCalls++
	}
	a.incomplete = a.incomplete || !row.ok.Valid || !row.duration.Valid || !row.input.Valid || !row.output.Valid
	for _, value := range []struct {
		target *int64
		amount int64
	}{
		{&a.total.InputTokens, row.input.Int64},
		{&a.total.OutputTokens, row.output.Int64},
		{&a.total.DurationMS, row.duration.Int64},
	} {
		if *value.target > math.MaxInt64-value.amount {
			return errors.New("模型用量累计溢出")
		}
		*value.target += value.amount
	}
	if a.total.InputTokens > math.MaxInt64-a.total.OutputTokens {
		return errors.New("模型token累计溢出")
	}
	if row.reasoning.Valid {
		if a.total.ReasoningTokens == nil {
			a.total.ReasoningTokens = new(int64)
		}
		if *a.total.ReasoningTokens > math.MaxInt64-row.reasoning.Int64 {
			return errors.New("模型reasoning token累计溢出")
		}
		*a.total.ReasoningTokens += row.reasoning.Int64
	}
	return nil
}

func (a *usageAccumulator) finish() UsageTotal {
	result := a.total
	if result.Calls > 0 && a.knownOK == result.Calls {
		rate := float64(result.OKCalls) / float64(result.Calls)
		result.SuccessRate = &rate
	}
	if a.knownDuration > 0 {
		average := float64(result.DurationMS) / float64(a.knownDuration)
		result.AverageDurationMS = &average
	}
	return result
}

func readUsageTx(ctx context.Context, tx *sql.Tx, query UsageQuery, report *UsageReport) error {
	sqlText := `SELECT a.at,a.task_id,a.purpose,a.ok,a.duration_ms,a.input_tokens,a.output_tokens,a.reasoning_tokens,t.name FROM ai_calls a LEFT JOIN tasks t ON t.id=a.task_id WHERE (a.at IS NULL OR (a.at>=? AND a.at<?))`
	args := []any{report.From, report.Until}
	if query.TaskID != nil {
		sqlText += ` AND a.task_id=?`
		args = append(args, *query.TaskID)
	}
	if query.Purpose != nil {
		sqlText += ` AND a.purpose=?`
		args = append(args, *query.Purpose)
	}
	rows, err := tx.QueryContext(ctx, sqlText+` ORDER BY a.id`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	var total usageAccumulator
	tasks := map[int64]*usageAccumulator{}
	taskNames := map[int64]*string{}
	purposes := map[string]*usageAccumulator{}
	for rows.Next() {
		var row usageRecord
		if err := rows.Scan(&row.at, &row.taskID, &row.purpose, &row.ok, &row.duration, &row.input, &row.output, &row.reasoning, &row.taskName); err != nil {
			return err
		}
		if !row.at.Valid || !row.purpose.Valid || !validUsagePurpose(row.purpose.String) || row.taskID.Valid && row.taskID.Int64 < 1 || row.ok.Valid && row.ok.Int64 != 0 && row.ok.Int64 != 1 || row.duration.Int64 < 0 || row.input.Int64 < 0 || row.output.Int64 < 0 || row.reasoning.Int64 < 0 {
			return errors.New("模型用量记录损坏")
		}
		taskID := row.taskID.Int64
		if tasks[taskID] == nil {
			tasks[taskID] = &usageAccumulator{}
			if row.taskName.Valid {
				taskNames[taskID] = &row.taskName.String
			}
		}
		if purposes[row.purpose.String] == nil {
			purposes[row.purpose.String] = &usageAccumulator{}
		}
		for _, accumulator := range []*usageAccumulator{&total, tasks[taskID], purposes[row.purpose.String]} {
			if err := accumulator.add(row); err != nil {
				return err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	report.Total = total.finish()
	report.Incomplete = report.Incomplete || total.incomplete
	var taskIDs []int64
	for id := range tasks {
		taskIDs = append(taskIDs, id)
	}
	slices.Sort(taskIDs)
	for _, id := range taskIDs {
		group := TaskUsage{Total: tasks[id].finish()}
		if id != 0 {
			group.TaskID, group.TaskName = &id, taskNames[id]
			group.Deleted = group.TaskName == nil
		}
		report.ByTask = append(report.ByTask, group)
	}
	for _, purpose := range []string{"title_screen", "analysis", "profile", "market", "test", "probe"} {
		if group := purposes[purpose]; group != nil {
			report.ByPurpose = append(report.ByPurpose, PurposeUsage{Purpose: purpose, Total: group.finish()})
		}
	}
	return nil
}
