package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/quota"
)

type overviewWindow struct {
	from, to                 time.Time
	previousFrom, previousTo time.Time
	activity                 []ActivityBucket
}

// GetOverview 使用同一数据库快照；运行态在取连接前一次读取，避免单连接自等。
func (s *Service) GetOverview(ctx context.Context, period string) (OverviewView, error) {
	if period != "today" && period != "7d" && period != "30d" {
		return OverviewView{}, queryError()
	}
	if s.db == nil || s.settings == nil {
		return OverviewView{}, storageFailure(errors.New("概览缺少数据库或设置"))
	}
	s.initRuntime()
	runtimes := s.taskRuntimeSnapshots()
	scheduler, browsers := s.runtimeStatusSnapshot()
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return OverviewView{}, storageFailure(err)
	}
	defer tx.Rollback()
	preferences, err := s.settings.SnapshotTx(ctx, tx)
	if err != nil {
		return OverviewView{}, storageFailure(err)
	}
	now, err = readObservedNowTx(ctx, tx, now)
	if err != nil {
		return OverviewView{}, storageFailure(err)
	}
	var timezone string
	if err := json.Unmarshal(preferences.Values["general.timezone"], &timezone); err != nil {
		return OverviewView{}, storageFailure(err)
	}
	var globalPages int64
	if err := json.Unmarshal(preferences.Values["crawl.global_pages"], &globalPages); err != nil || globalPages < 1 {
		return OverviewView{}, storageFailure(errors.New("页面额度配置损坏"))
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return OverviewView{}, storageFailure(err)
	}
	window := overviewPeriod(period, now.In(location))
	view := OverviewView{
		Period: period, Timezone: timezone, FromAt: window.from.UnixMilli(), ToAt: window.to.UnixMilli(), AsOf: now.UnixMilli(),
		Activity: window.activity, Tasks: []TaskOverview{}, GradeDistribution: overviewGradeCounts(),
		Exceptions: ExceptionOverview{UnresolvedEvents: []SystemEvent{}},
	}
	if view.Coverage, err = readOverviewCoverageTx(ctx, tx, view.FromAt, view.AsOf); err != nil {
		return OverviewView{}, storageFailure(err)
	}
	accounts, err := overviewAccountsTx(ctx, tx)
	if err != nil {
		return OverviewView{}, storageFailure(err)
	}
	view.Accounts.Total = int64(len(accounts))
	for _, account := range accounts {
		if account.state == AccountActive {
			view.Accounts.Active++
		}
	}
	tasks, err := overviewTasksTx(ctx, tx)
	if err != nil {
		return OverviewView{}, storageFailure(err)
	}
	from := view.FromAt
	if !window.previousFrom.IsZero() && window.previousFrom.UnixMilli() < from {
		from = window.previousFrom.UnixMilli()
	}
	runs, err := overviewRunsTx(ctx, tx, from, view.AsOf)
	if err != nil {
		return OverviewView{}, storageFailure(err)
	}
	findings, err := overviewFindingsTx(ctx, tx, from, view.AsOf)
	if err != nil {
		return OverviewView{}, storageFailure(err)
	}
	view.Cards = overviewCardsFor(window.from.UnixMilli(), view.AsOf, runs, findings)
	if view.Cards.PriceDropCount, err = overviewPriceDropsTx(ctx, tx, view.FromAt, view.AsOf); err != nil {
		return OverviewView{}, storageFailure(err)
	}
	for i := range view.Activity {
		bucket := &view.Activity[i]
		for _, run := range runs {
			if run.started >= bucket.FromAt && run.started < bucket.ToAt && run.started < view.AsOf {
				bucket.NewCount += run.new
				bucket.SeenCount += run.scanned - run.new
			}
		}
	}
	if view.Coverage.FromAt != nil && !window.previousFrom.IsZero() && *view.Coverage.FromAt <= window.previousFrom.UnixMilli() {
		previous := overviewCardsFor(window.previousFrom.UnixMilli(), window.previousTo.UnixMilli(), runs, findings)
		previous.PriceDropCount, err = overviewPriceDropsTx(ctx, tx, window.previousFrom.UnixMilli(), window.previousTo.UnixMilli())
		if err != nil {
			return OverviewView{}, storageFailure(err)
		}
		view.Comparison = &OverviewComparison{
			FromAt: window.previousFrom.UnixMilli(), ToAt: window.previousTo.UnixMilli(),
			Scanned:     overviewMetric(view.Cards.ScannedCount, previous.ScannedCount),
			New:         overviewMetric(view.Cards.NewCount, previous.NewCount),
			Recommended: overviewMetric(view.Cards.RecommendedCount, previous.RecommendedCount),
			PriceDrop:   overviewMetric(view.Cards.PriceDropCount, previous.PriceDropCount),
		}
	}
	runsByTask := map[int64][]overviewRun{}
	for _, run := range runs {
		if run.task.Valid {
			runsByTask[run.task.Int64] = append(runsByTask[run.task.Int64], run)
		}
	}
	findingsByTask := map[int64][]overviewFinding{}
	for _, finding := range findings {
		findingsByTask[finding.task] = append(findingsByTask[finding.task], finding)
	}
	for _, task := range tasks {
		runtime := runtimes[task.ID]
		if runtime.Phase == "" {
			runtime = TaskRuntime{TaskID: task.ID, Phase: TaskIdle}
		}
		if runtime.Phase == TaskIdle {
			preflight, err := s.preflightTx(ctx, tx, task, TriggerAuto)
			if err != nil {
				return OverviewView{}, storageFailure(err)
			}
			runtime.Blocked = displayPreflightBlock(preflight)
		}
		item := overviewTask(task, runtime, accounts, runsByTask[task.ID], findingsByTask[task.ID], view.FromAt, view.AsOf)
		if runtime.Phase == TaskIdle && runtime.Blocked == nil && task.Enabled && task.AccountID != nil {
			ready := view.AsOf
			item.NextRunAt = &ready
		}
		resourceBusy := browsers.Active >= globalPages
		for _, session := range browsers.Sessions {
			if session.Phase != SessionQueued && task.AccountID != nil && session.AccountID == *task.AccountID {
				resourceBusy = true
			}
		}
		// 资源何时释放没有有限保证，不能把计划或额度时间当作实际开始时间。
		if !scheduler.Accepting || resourceBusy || runtime.Phase == TaskQueued {
			item.NextRunAt = nil
		}
		market, err := loadMarketTx(ctx, tx, task, view.AsOf, timezone)
		if err != nil {
			return OverviewView{}, storageFailure(err)
		}
		item.Trend = market.TaskTrend
		if item.Trend == nil {
			item.Trend = []analysis.DailyPrice{}
		}
		for _, group := range market.Groups {
			if group.Selected && group.Cheapest != nil && (item.MinPrice == nil || group.Cheapest.Price < *item.MinPrice) {
				value := group.Cheapest.Price
				item.MinPrice = &value
			}
		}
		view.Tasks = append(view.Tasks, item)
	}
	view.GradeDistribution = overviewDistribution(findings, view.FromAt, view.AsOf)
	if view.AI, err = overviewAITx(ctx, tx, view.FromAt, view.AsOf); err != nil {
		return OverviewView{}, storageFailure(err)
	}
	if view.Notifications, err = overviewNotificationsTx(ctx, tx, view.FromAt, view.AsOf); err != nil {
		return OverviewView{}, storageFailure(err)
	}
	if view.Exceptions.UnresolvedEvents, err = overviewEventsTx(ctx, tx); err != nil {
		return OverviewView{}, storageFailure(err)
	}
	for _, run := range runs {
		if run.started < view.FromAt || !run.ended.Valid || run.outcome != RunFailed {
			continue
		}
		switch run.failure.String {
		case "risk_verify", "risk_deny", "risk_env":
			view.Exceptions.RiskFailureCount++
		case "network":
			view.Exceptions.NetworkFailureCount++
		}
	}
	if err := tx.Commit(); err != nil {
		return OverviewView{}, storageFailure(err)
	}
	return view, nil
}

func overviewPeriod(period string, now time.Time) overviewWindow {
	start, end := quota.DayBounds(now)
	window := overviewWindow{from: start, to: now, activity: []ActivityBucket{}}
	days := 1
	if period == "7d" {
		days = 7
	}
	if period == "30d" {
		days = 30
	}
	date := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	first := date.AddDate(0, 0, 1-days)
	window.from = overviewDateStart(first, now.Location())
	if period != "30d" {
		shift := -1
		if period == "7d" {
			shift = -7
		}
		window.previousFrom = overviewDateStart(first.AddDate(0, 0, shift), now.Location())
		previousDate := date.AddDate(0, 0, shift)
		window.previousTo = overviewWallTime(previousDate, now.Hour(), now.Minute(), now.Second(), now.Nanosecond(), now.Location())
		if !window.previousTo.After(window.previousFrom) {
			window.previousFrom, window.previousTo = time.Time{}, time.Time{}
		}
	}
	if period == "today" {
		for cursor := start; cursor.Before(end); {
			next := cursor.Add(time.Hour)
			if next.After(end) {
				next = end
			}
			window.activity = append(window.activity, ActivityBucket{FromAt: cursor.UnixMilli(), ToAt: next.UnixMilli()})
			cursor = next
		}
	} else {
		for offset := 0; offset < days; offset++ {
			day := first.AddDate(0, 0, offset)
			from, to := overviewDateStart(day, now.Location()), overviewDateStart(day.AddDate(0, 0, 1), now.Location())
			if to.After(from) {
				window.activity = append(window.activity, ActivityBucket{FromAt: from.UnixMilli(), ToAt: to.UnixMilli()})
			}
		}
	}
	return window
}

func overviewDateStart(date time.Time, location *time.Location) time.Time {
	return overviewWallTime(date, 0, 0, 0, 0, location)
}

// 昨天的同一钟点可能不存在或出现两次；取首个不早于目标民用时刻的真实瞬间。
func overviewWallTime(date time.Time, hour, minute, second, nano int, location *time.Location) time.Time {
	wall := time.Date(date.Year(), date.Month(), date.Day(), hour, minute, second, nano, time.UTC)
	approximate := time.Date(date.Year(), date.Month(), date.Day(), hour, minute, second, nano, location)
	cursor, limit := approximate.Add(-48*time.Hour), approximate.Add(48*time.Hour)
	var first time.Time
	for cursor.Before(limit) {
		local := cursor.In(location)
		_, offset := local.Zone()
		_, end := local.ZoneBounds()
		if !end.IsZero() && !end.After(cursor) {
			end = time.Date(cursor.UTC().Year()+1, time.January, 1, 0, 0, 0, 0, time.UTC)
		}
		if end.IsZero() || end.After(limit) {
			end = limit
		}
		candidate := wall.Add(-time.Duration(offset) * time.Second)
		if candidate.Before(cursor) {
			candidate = cursor
		}
		if candidate.Before(end) {
			v := candidate.In(location)
			civil := time.Date(v.Year(), v.Month(), v.Day(), v.Hour(), v.Minute(), v.Second(), v.Nanosecond(), time.UTC)
			if !civil.Before(wall) && (first.IsZero() || candidate.Before(first)) {
				first = candidate
			}
		}
		cursor = end
	}
	return first.In(location)
}

func readOverviewCoverageTx(ctx context.Context, tx *sql.Tx, from, now int64) (CoverageView, error) {
	result := CoverageView{ToAt: now, IsPartial: true}
	rows, err := tx.QueryContext(ctx, `SELECT key,value FROM meta WHERE key='stats.started_at' OR key IN ('stats.coverage.findings','stats.coverage.run_records','stats.coverage.task_sightings','stats.coverage.ai_calls','stats.coverage.deliveries','stats.coverage.notification_intents')`)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	var started bool
	var latest int64
	for rows.Next() {
		var key, raw string
		if err := rows.Scan(&key, &raw); err != nil {
			return result, err
		}
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value <= 0 || strconv.FormatInt(value, 10) != raw {
			return result, errors.New("统计覆盖记录损坏")
		}
		if key == "stats.started_at" {
			started = true
		}
		if value > latest {
			latest = value
		}
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	if started {
		result.FromAt = &latest
		result.IsPartial = latest > from
	}
	return result, nil
}

type overviewAccount struct {
	state  AccountState
	reason *locales.Message
}

func overviewAccountsTx(ctx context.Context, tx *sql.Tx) (map[int64]overviewAccount, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,state,state_reason FROM accounts`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]overviewAccount{}
	for rows.Next() {
		var id int64
		var account overviewAccount
		var raw sql.NullString
		if err := rows.Scan(&id, &account.state, &raw); err != nil {
			return nil, err
		}
		if raw.Valid {
			account.reason = new(locales.Message)
			if err := json.Unmarshal([]byte(raw.String), account.reason); err != nil {
				return nil, err
			}
		}
		out[id] = account
	}
	return out, rows.Err()
}

func overviewTasksTx(ctx context.Context, tx *sql.Tx) ([]Task, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM tasks ORDER BY sort_index,id`)
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
	out := make([]Task, 0, len(ids))
	for _, id := range ids {
		task, err := loadTaskTx(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, task)
	}
	return out, nil
}

type overviewRun struct {
	task         sql.NullInt64
	started      int64
	ended        sql.NullInt64
	outcome      RunOutcome
	failure      sql.NullString
	scanned, new int64
}

func overviewRunsTx(ctx context.Context, tx *sql.Tx, from, to int64) ([]overviewRun, error) {
	rows, err := tx.QueryContext(ctx, `SELECT task_id,started_at,ended_at,outcome,failure_kind,COALESCE(scanned_count,0),COALESCE(new_count,0) FROM run_records WHERE started_at>=? AND started_at<?`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []overviewRun{}
	for rows.Next() {
		var run overviewRun
		if err := rows.Scan(&run.task, &run.started, &run.ended, &run.outcome, &run.failure, &run.scanned, &run.new); err != nil {
			return nil, err
		}
		if run.scanned < 0 || run.new < 0 || run.new > run.scanned {
			return nil, errors.New("运行统计计数不一致")
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

type overviewFinding struct {
	id, task, found        int64
	item                   string
	baseline, recommended  bool
	decision               analysis.Decision
	reject, grade          sql.NullString
	score, analyzed, layer sql.NullInt64
}

func overviewFindingsTx(ctx context.Context, tx *sql.Tx, from, to int64) ([]overviewFinding, error) {
	recommended := `(` + findingVisibleSQL + `) AND (` + findingRecommendationBudgetSQL + `) AND (f.decision='matched' OR (f.decision='scored' AND (f.grade IN ('steal','good') OR (f.score IS NULL AND f.grade IS NULL))))`
	rows, err := tx.QueryContext(ctx, `SELECT f.id,f.task_id,f.item_id,f.found_at,f.is_baseline,f.decision,f.reject_kind,f.score,f.grade,f.analyzed_at,f.decided_layer,CASE WHEN `+recommended+` THEN 1 ELSE 0 END FROM findings f JOIN listings l ON l.item_id=f.item_id JOIN tasks t ON t.id=f.task_id WHERE f.found_at<? AND (f.found_at>=? OR f.analyzed_at>=?)`, to, from, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []overviewFinding{}
	for rows.Next() {
		var finding overviewFinding
		if err := rows.Scan(&finding.id, &finding.task, &finding.item, &finding.found, &finding.baseline, &finding.decision, &finding.reject, &finding.score, &finding.grade, &finding.analyzed, &finding.layer, &finding.recommended); err != nil {
			return nil, err
		}
		out = append(out, finding)
	}
	return out, rows.Err()
}

func overviewCardsFor(from, to int64, runs []overviewRun, findings []overviewFinding) OverviewCards {
	var out OverviewCards
	for _, run := range runs {
		if run.started >= from && run.started < to {
			out.ScannedCount += run.scanned
			out.NewCount += run.new
		}
	}
	for _, finding := range findings {
		if finding.found < from || finding.found >= to || !finding.recommended {
			continue
		}
		out.RecommendedCount++
		if finding.decision == analysis.DecisionMatched {
			out.RecommendedKeywordCount++
		} else {
			out.RecommendedAICount++
		}
	}
	return out
}

func overviewPriceDropsTx(ctx context.Context, tx *sql.Tx, from, to int64) (int64, error) {
	var count int64
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM notification_intents WHERE kind='price_drop' AND state='sent' AND sent_at>=? AND sent_at<?`, from, to).Scan(&count)
	return count, err
}

func overviewMetric(current, previous int64) MetricComparison {
	metric := MetricComparison{PreviousCount: previous, Delta: current - previous}
	if previous >= 50 {
		ratio := float64(metric.Delta) / float64(previous)
		metric.Ratio = &ratio
	}
	return metric
}

func overviewTask(task Task, runtime TaskRuntime, accounts map[int64]overviewAccount, runs []overviewRun, findings []overviewFinding, from, to int64) TaskOverview {
	item := TaskOverview{TaskID: task.ID, Name: task.Name, JudgeMode: task.JudgeMode, State: OverviewMonitoring, Trend: []analysis.DailyPrice{}}
	switch {
	case runtime.Phase == TaskRunning || runtime.Phase == TaskFinalizing:
		item.State = OverviewChecking
	case !task.Enabled:
		item.State = OverviewPaused
	case task.AccountID == nil:
		item.State = OverviewPaused
		item.Reason = &locales.Message{Code: "run.no_account", Args: map[string]any{}}
	case accounts[*task.AccountID].state != AccountActive:
		item.State = OverviewPaused
		account := accounts[*task.AccountID]
		item.Reason = account.reason
		if item.Reason == nil {
			code := map[AccountState]string{
				AccountSignedOut: "run.account_signed_out", AccountExpired: "run.account_expired",
				AccountNeedsVerification: "run.account_needs_verification", AccountDenied: "run.account_denied",
				AccountCooling: "run.account_cooling",
			}[account.state]
			if code == "" {
				code = "run.no_account"
			}
			item.Reason = &locales.Message{Code: code, Args: map[string]any{}}
		}
	case task.FailurePausedUntil != nil && *task.FailurePausedUntil > to || task.LastRunOutcome != nil && *task.LastRunOutcome == "failed":
		item.State = OverviewError
		item.Reason = task.LastError
	}
	if runtime.Blocked != nil {
		item.Reason = &runtime.Blocked.Reason
		item.NextRunAt = runtime.Blocked.NextAllowedAt
	}
	var successes, completed, durationCount int64
	var duration float64
	for _, run := range runs {
		if !run.task.Valid || run.task.Int64 != task.ID || run.started < from || run.started >= to {
			continue
		}
		item.ScannedCount += run.scanned
		if run.ended.Valid && (run.outcome == RunSuccess || run.outcome == RunFailed) {
			completed++
			if run.outcome == RunSuccess {
				successes++
			}
			if run.ended.Int64 >= run.started {
				duration += float64(run.ended.Int64 - run.started)
				durationCount++
			}
		}
	}
	if completed > 0 {
		ratio := float64(successes) / float64(completed)
		item.SuccessRate = &ratio
	}
	if durationCount > 0 {
		mean := duration / float64(durationCount)
		item.AverageDurationMS = &mean
	}
	for _, finding := range findings {
		if finding.task == task.ID && finding.found >= from && finding.found < to && !finding.baseline {
			item.NewFindingCount++
			if finding.recommended {
				item.RecommendedCount++
			}
		}
	}
	if item.NewFindingCount > 0 {
		ratio := float64(item.RecommendedCount) / float64(item.NewFindingCount)
		item.RecommendationRate = &ratio
	}
	return item
}

func overviewGradeCounts() []GradeCount {
	out := make([]GradeCount, 0, 8)
	for _, kind := range []string{"steal", "good", "fair", "pricey", "avoid", "unpriced", "unmatched", "junk"} {
		out = append(out, GradeCount{Kind: kind})
	}
	return out
}

func overviewDistribution(findings []overviewFinding, from, to int64) []GradeCount {
	latest := map[string]overviewFinding{}
	for _, finding := range findings {
		if overviewFindingGrade(finding) == "" || !finding.analyzed.Valid || finding.analyzed.Int64 >= to {
			continue
		}
		old, found := latest[finding.item]
		if !found || finding.analyzed.Int64 > old.analyzed.Int64 || finding.analyzed.Int64 == old.analyzed.Int64 && finding.id > old.id {
			latest[finding.item] = finding
		}
	}
	out := overviewGradeCounts()
	for _, finding := range latest {
		if finding.analyzed.Int64 < from {
			continue
		}
		kind := overviewFindingGrade(finding)
		for i := range out {
			if out[i].Kind == kind {
				out[i].Count++
				break
			}
		}
	}
	return out
}

func overviewFindingGrade(f overviewFinding) string {
	if f.decision == analysis.DecisionScored {
		if f.grade.Valid {
			switch f.grade.String {
			case "steal", "good", "fair", "pricey", "avoid":
				return f.grade.String
			}
		}
		if !f.grade.Valid && !f.score.Valid {
			return "unpriced"
		}
	}
	if f.decision == analysis.DecisionRejected && f.layer.Valid && f.layer.Int64 >= 2 && f.reject.Valid {
		switch f.reject.String {
		case "unmatched", "junk":
			return f.reject.String
		}
	}
	return ""
}

func overviewAITx(ctx context.Context, tx *sql.Tx, from, to int64) (AIOverview, error) {
	var result AIOverview
	var known, successes int64
	var average sql.NullFloat64
	err := tx.QueryRowContext(ctx, `SELECT count(*),count(ok),COALESCE(sum(CASE WHEN ok=1 THEN 1 ELSE 0 END),0),avg(duration_ms),COALESCE(sum(COALESCE(input_tokens,0)+COALESCE(output_tokens,0)),0) FROM ai_calls WHERE at>=? AND at<?`, from, to).Scan(&result.Count, &known, &successes, &average, &result.Tokens)
	if err != nil {
		return result, err
	}
	if result.Count > 0 && known == result.Count {
		ratio := float64(successes) / float64(result.Count)
		result.SuccessRate = &ratio
	}
	if average.Valid {
		result.AverageDurationMS = &average.Float64
	}
	return result, nil
}

func overviewNotificationsTx(ctx context.Context, tx *sql.Tx, from, to int64) (NotificationOverview, error) {
	var result NotificationOverview
	var known, successes int64
	err := tx.QueryRowContext(ctx, `SELECT count(*),count(ok),COALESCE(sum(CASE WHEN ok=1 THEN 1 ELSE 0 END),0) FROM deliveries WHERE at>=? AND at<?`, from, to).Scan(&result.Count, &known, &successes)
	if err != nil {
		return result, err
	}
	if result.Count > 0 && known == result.Count {
		ratio := float64(successes) / float64(result.Count)
		result.SuccessRate = &ratio
	}
	return result, nil
}

func overviewEventsTx(ctx context.Context, tx *sql.Tx) ([]SystemEvent, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,kind,subject_id,message,created_at,resolved_at,notified_at FROM system_events WHERE resolved_at IS NULL ORDER BY created_at DESC,id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []SystemEvent{}
	for rows.Next() {
		var event SystemEvent
		var kind, message sql.NullString
		var subject, created, resolved, notified sql.NullInt64
		if err := rows.Scan(&event.ID, &kind, &subject, &message, &created, &resolved, &notified); err != nil {
			return nil, err
		}
		if kind.Valid {
			value := SystemEventKind(kind.String)
			event.Kind = &value
		}
		if subject.Valid {
			value := subject.Int64
			event.SubjectID = &value
		}
		if message.Valid {
			event.Message = new(locales.Message)
			if err := json.Unmarshal([]byte(message.String), event.Message); err != nil {
				return nil, fmt.Errorf("系统事件原因无法读取：%w", err)
			}
		}
		if created.Valid {
			value := created.Int64
			event.CreatedAt = &value
		}
		if resolved.Valid {
			value := resolved.Int64
			event.ResolvedAt = &value
		}
		if notified.Valid {
			value := notified.Int64
			event.NotifiedAt = &value
		}
		result = append(result, event)
	}
	return result, rows.Err()
}
