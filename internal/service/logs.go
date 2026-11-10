package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/settings"
)

const runColumns = "r.id,r.task_id,r.task_name,r.trigger,r.account_id,r.account_name,r.proxy_name,r.started_at,r.ended_at,r.outcome,r.failure_kind,r.message,r.repeat_count,r.page_count,r.scanned_count,r.new_count,r.layer1_rejected,r.layer2_count,r.layer2_rejected,r.detail_count,r.seller_page_count,r.analyzed_count,r.recommended_count,r.notified_count,r.warnings_json"

type runScanner interface{ Scan(...any) error }

func runError(kind ErrorKind, code string) error {
	return &ServiceError{Kind: kind, Reason: locales.Message{Code: code, Args: map[string]any{}}, Fields: []settings.Issue{}}
}

func logJSON(raw string, target any) error {
	if !utf8.ValidString(raw) {
		return errors.New("运行日志不是有效的文本")
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("运行日志包含多份数据")
	}
	return nil
}

func logMessage(raw sql.NullString) (*locales.Message, error) {
	if !raw.Valid {
		return nil, nil
	}
	var message locales.Message
	if err := logJSON(raw.String, &message); err != nil {
		return nil, err
	}
	if message.Code == "" {
		return nil, errors.New("运行日志缺少原因代码")
	}
	if message.Args == nil {
		message.Args = map[string]any{}
	}
	return &message, nil
}

func logMessageJSON(message *locales.Message) (any, error) {
	if message == nil {
		return nil, nil
	}
	if message.Code == "" || !utf8.ValidString(message.Code) {
		return nil, runError(ErrorInvalid, "runs.invalid_record")
	}
	raw, err := json.Marshal(message)
	if err != nil {
		return nil, err
	}
	return string(raw), nil
}

func normalizeRunWarnings(warnings []locales.Message) ([]locales.Message, error) {
	result := []locales.Message{}
	seen := map[string]bool{}
	for _, warning := range warnings {
		raw, err := logMessageJSON(&warning)
		if err != nil {
			return nil, err
		}
		key := raw.(string)
		if seen[key] {
			continue
		}
		seen[key] = true
		var copy locales.Message
		if err := logJSON(key, &copy); err != nil {
			return nil, err
		}
		result = append(result, copy)
	}
	return result, nil
}

func scanRun(row runScanner) (RunRecord, error) {
	var run RunRecord
	var outcome RunOutcome
	var message, warnings sql.NullString
	err := row.Scan(&run.ID, &run.TaskID, &run.TaskName, &run.Trigger, &run.AccountID,
		&run.AccountName, &run.ProxyName, &run.StartedAt, &run.EndedAt, &outcome,
		&run.FailureKind, &message, &run.RepeatCount, &run.PageCount, &run.ScannedCount,
		&run.NewCount, &run.Layer1Rejected, &run.Layer2Count, &run.Layer2Rejected,
		&run.DetailCount, &run.SellerPageCount, &run.AnalyzedCount, &run.RecommendedCount,
		&run.NotifiedCount, &warnings)
	if err != nil {
		return RunRecord{}, err
	}
	run.Outcome = &outcome
	run.Message, err = logMessage(message)
	if err != nil {
		return RunRecord{}, err
	}
	run.Warnings = []locales.Message{}
	if warnings.Valid {
		if err := logJSON(warnings.String, &run.Warnings); err != nil {
			return RunRecord{}, err
		}
		if run.Warnings == nil {
			run.Warnings = []locales.Message{}
		}
		for i := range run.Warnings {
			if run.Warnings[i].Code == "" {
				return RunRecord{}, errors.New("运行警告缺少原因代码")
			}
			if run.Warnings[i].Args == nil {
				run.Warnings[i].Args = map[string]any{}
			}
		}
	}
	return run, nil
}

func visibleRun(run RunRecord) RunView {
	if run.EndedAt == nil {
		run.Outcome, run.FailureKind, run.Message = nil, nil, nil
	}
	return run
}

func loadRunTx(ctx context.Context, tx *sql.Tx, id int64) (RunRecord, error) {
	run, err := scanRun(tx.QueryRowContext(ctx, "SELECT "+runColumns+" FROM run_records r WHERE r.id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return RunRecord{}, runError(ErrorNotFound, "runs.not_found")
	}
	return run, err
}

func insertRunTx(ctx context.Context, tx *sql.Tx, run RunRecord) (RunRecord, error) {
	message, err := logMessageJSON(run.Message)
	if err != nil {
		return RunRecord{}, err
	}
	run.Warnings, err = normalizeRunWarnings(run.Warnings)
	if err != nil {
		return RunRecord{}, err
	}
	warnings, err := json.Marshal(run.Warnings)
	if err != nil {
		return RunRecord{}, err
	}
	result, err := tx.ExecContext(ctx, "INSERT INTO run_records(task_id,task_name,trigger,account_id,account_name,proxy_name,started_at,ended_at,outcome,failure_kind,message,repeat_count,page_count,scanned_count,new_count,layer1_rejected,layer2_count,layer2_rejected,detail_count,seller_page_count,analyzed_count,recommended_count,notified_count,warnings_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
		run.TaskID, run.TaskName, run.Trigger, run.AccountID, run.AccountName, run.ProxyName,
		run.StartedAt, run.EndedAt, run.Outcome, run.FailureKind, message, run.RepeatCount,
		run.PageCount, run.ScannedCount, run.NewCount, run.Layer1Rejected, run.Layer2Count,
		run.Layer2Rejected, run.DetailCount, run.SellerPageCount, run.AnalyzedCount,
		run.RecommendedCount, run.NotifiedCount, string(warnings))
	if err != nil {
		return RunRecord{}, err
	}
	run.ID, err = result.LastInsertId()
	return run, err
}

// beginRunTx 只保存开始记录，资源标记和账号门禁由外层事务一起提交。
func beginRunTx(ctx context.Context, tx *sql.Tx, run RunRecord) (RunRecord, error) {
	if run.TaskName == "" || run.StartedAt == nil {
		return RunRecord{}, runError(ErrorInvalid, "runs.invalid_record")
	}
	outcome, failure := RunFailed, FailureRestart
	run.ID, run.EndedAt, run.Message, run.RepeatCount = 0, nil, nil, 1
	run.Outcome, run.FailureKind = &outcome, &failure
	return insertRunTx(ctx, tx, run)
}

// finishRunTx 不改任务或账号；调用方在同一事务里结算它们。
func finishRunTx(ctx context.Context, tx *sql.Tx, run RunRecord) error {
	if run.ID <= 0 || run.EndedAt == nil || run.Outcome == nil {
		return runError(ErrorInvalid, "runs.invalid_record")
	}
	if *run.Outcome != RunSuccess && *run.Outcome != RunFailed && *run.Outcome != RunSkipped {
		return runError(ErrorInvalid, "runs.invalid_record")
	}
	previous, err := loadRunTx(ctx, tx, run.ID)
	if err != nil {
		return err
	}
	if previous.EndedAt != nil {
		return runError(ErrorConflict, "runs.changed")
	}
	if previous.StartedAt != nil && *run.EndedAt < *previous.StartedAt {
		return runError(ErrorInvalid, "runs.invalid_record")
	}
	message, err := logMessageJSON(run.Message)
	if err != nil {
		return err
	}
	warnings, err := normalizeRunWarnings(run.Warnings)
	if err != nil {
		return err
	}
	rawWarnings, err := json.Marshal(warnings)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "UPDATE run_records SET ended_at=?,outcome=?,failure_kind=?,message=?,page_count=?,scanned_count=?,new_count=?,layer1_rejected=?,layer2_count=?,layer2_rejected=?,detail_count=?,seller_page_count=?,analyzed_count=?,recommended_count=?,notified_count=?,warnings_json=? WHERE id=? AND ended_at IS NULL",
		run.EndedAt, run.Outcome, run.FailureKind, message, run.PageCount, run.ScannedCount,
		run.NewCount, run.Layer1Rejected, run.Layer2Count, run.Layer2Rejected, run.DetailCount,
		run.SellerPageCount, run.AnalyzedCount, run.RecommendedCount, run.NotifiedCount,
		string(rawWarnings), run.ID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return runError(ErrorConflict, "runs.changed")
	}
	if *run.Outcome == RunSkipped {
		_, err = tx.ExecContext(ctx, "INSERT INTO run_skip_events(run_id,seq,at) VALUES(?,1,?)", run.ID, *run.EndedAt)
	}
	return err
}

func recordSkipTx(ctx context.Context, tx *sql.Tx, run RunRecord, at int64) (RunRecord, error) {
	if run.TaskName == "" || run.Message == nil || run.Message.Code == "" {
		return RunRecord{}, runError(ErrorInvalid, "runs.invalid_record")
	}
	if run.TaskID != nil {
		previous, err := scanRun(tx.QueryRowContext(ctx, "SELECT "+runColumns+" FROM run_records r WHERE r.task_id=? ORDER BY r.id DESC LIMIT 1", *run.TaskID))
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return RunRecord{}, err
		}
		if err == nil && previous.EndedAt != nil && previous.Outcome != nil && *previous.Outcome == RunSkipped &&
			previous.Message != nil && previous.Message.Code == run.Message.Code {
			message, err := logMessageJSON(run.Message)
			if err != nil {
				return RunRecord{}, err
			}
			previous.RepeatCount++
			end := max(*previous.EndedAt, at)
			previous.EndedAt, previous.Message, previous.FailureKind = &end, run.Message, run.FailureKind
			_, err = tx.ExecContext(ctx, "UPDATE run_records SET ended_at=?,repeat_count=?,message=?,failure_kind=? WHERE id=?",
				end, previous.RepeatCount, message, run.FailureKind, previous.ID)
			if err != nil {
				return RunRecord{}, err
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO run_skip_events(run_id,seq,at) VALUES(?,?,?)", previous.ID, previous.RepeatCount, at); err != nil {
				return RunRecord{}, err
			}
			return previous, nil
		}
	}
	outcome := RunSkipped
	run.ID, run.StartedAt, run.EndedAt, run.Outcome, run.RepeatCount = 0, &at, &at, &outcome, 1
	run, err := insertRunTx(ctx, tx, run)
	if err != nil {
		return RunRecord{}, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO run_skip_events(run_id,seq,at) VALUES(?,?,?)", run.ID, 1, at)
	return run, err
}

type runCursor struct {
	StartedAt *int64 `json:"started_at"`
	ID        int64  `json:"id"`
}

func normalizeRunQuery(q RunQuery) (RunQuery, error) {
	var err error
	q.ListQuery, err = normalizeListQuery(q.ListQuery)
	if err != nil {
		return RunQuery{}, err
	}
	if q.TaskID != nil && *q.TaskID <= 0 || !utf8.ValidString(q.Q) ||
		q.FromAt != nil && q.ToAt != nil && *q.FromAt >= *q.ToAt {
		return RunQuery{}, queryError()
	}
	if q.Outcome != nil && *q.Outcome != RunSuccess && *q.Outcome != RunFailed && *q.Outcome != RunSkipped {
		return RunQuery{}, queryError()
	}
	if q.FailureKind != nil {
		switch *q.FailureKind {
		case FailureRiskVerify, FailureRiskDeny, FailureRiskEnv, FailureLogin, FailureNetwork, FailurePage,
			FailureTimeout, FailureQuota, FailureConfig, FailureCancelled, FailureRestart:
		default:
			return RunQuery{}, queryError()
		}
	}
	q.Q = strings.TrimSpace(q.Q)
	return q, nil
}

func runQueryWhere(q RunQuery) (string, []any) {
	parts, args := []string{"1=1"}, []any{}
	if q.TaskID != nil {
		parts, args = append(parts, "r.task_id=?"), append(args, *q.TaskID)
	}
	if q.Outcome != nil {
		parts, args = append(parts, "r.ended_at IS NOT NULL AND r.outcome=?"), append(args, *q.Outcome)
	}
	if q.FailureKind != nil {
		parts, args = append(parts, "r.ended_at IS NOT NULL AND r.failure_kind=?"), append(args, *q.FailureKind)
	}
	if q.FromAt != nil || q.ToAt != nil {
		events, eventArgs := runTimeBounds(q, "e.at")
		ordinary, ordinaryArgs := runTimeBounds(q, "r.started_at")
		parts = append(parts, "((r.ended_at IS NOT NULL AND r.outcome='skipped' AND EXISTS(SELECT 1 FROM run_skip_events e WHERE e.run_id=r.id AND "+events+")) OR ((r.ended_at IS NULL OR r.outcome<>'skipped') AND "+ordinary+"))")
		args = append(args, eventArgs...)
		args = append(args, ordinaryArgs...)
	}
	if q.Q != "" {
		parts = append(parts, "(instr(lower(r.task_name),lower(?))>0 OR instr(lower(COALESCE(r.account_name,'')),lower(?))>0 OR instr(lower(COALESCE(r.proxy_name,'')),lower(?))>0 OR instr(lower(COALESCE(r.message,'')),lower(?))>0)")
		args = append(args, q.Q, q.Q, q.Q, q.Q)
	}
	return strings.Join(parts, " AND "), args
}

func runTimeBounds(q RunQuery, column string) (string, []any) {
	parts, args := []string{}, []any{}
	if q.FromAt != nil {
		parts, args = append(parts, column+">=?"), append(args, *q.FromAt)
	}
	if q.ToAt != nil {
		parts, args = append(parts, column+"<?"), append(args, *q.ToAt)
	}
	return strings.Join(parts, " AND "), args
}

// 时间筛选只投影窗口内的重复次数，不改折叠记录本身或排序锚点。
func runQueryColumns(q RunQuery) (string, []any) {
	if q.FromAt == nil && q.ToAt == nil {
		return runColumns, nil
	}
	window, args := runTimeBounds(q, "e.at")
	repeated := "CASE WHEN r.ended_at IS NOT NULL AND r.outcome='skipped' THEN (SELECT count(*) FROM run_skip_events e WHERE e.run_id=r.id AND " + window + ") ELSE r.repeat_count END"
	return strings.Replace(runColumns, "r.repeat_count", repeated, 1), args
}

func listRunsTx(ctx context.Context, tx *sql.Tx, q RunQuery) (Page[RunView], error) {
	binding := q
	binding.ListQuery = ListQuery{}
	var anchor runCursor
	if err := decodeCursor(q.Cursor, "runs", binding, &anchor); err != nil {
		return Page[RunView]{}, err
	}
	if q.Cursor != "" && anchor.ID <= 0 {
		return Page[RunView]{}, queryError()
	}
	where, args := runQueryWhere(q)
	result := Page[RunView]{Items: []RunView{}}
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM run_records r WHERE "+where, args...).Scan(&result.Total); err != nil {
		return Page[RunView]{}, err
	}
	if q.Cursor != "" {
		if anchor.StartedAt == nil {
			where += " AND r.started_at IS NULL AND r.id<?"
			args = append(args, anchor.ID)
		} else {
			where += " AND (r.started_at<? OR (r.started_at=? AND r.id<?) OR r.started_at IS NULL)"
			args = append(args, *anchor.StartedAt, *anchor.StartedAt, anchor.ID)
		}
	}
	args = append(args, q.Limit+1)
	columns, selectionArgs := runQueryColumns(q)
	selectionArgs = append(selectionArgs, args...)
	rows, err := tx.QueryContext(ctx, "SELECT "+columns+" FROM run_records r WHERE "+where+" ORDER BY r.started_at DESC NULLS LAST,r.id DESC LIMIT ?", selectionArgs...)
	if err != nil {
		return Page[RunView]{}, err
	}
	defer rows.Close()
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return Page[RunView]{}, err
		}
		result.Items = append(result.Items, visibleRun(run))
	}
	if err := rows.Err(); err != nil {
		return Page[RunView]{}, err
	}
	if int64(len(result.Items)) > q.Limit {
		result.Items = result.Items[:q.Limit]
		last := result.Items[len(result.Items)-1]
		cursor, err := encodeCursor("runs", binding, runCursor{StartedAt: last.StartedAt, ID: last.ID})
		if err != nil {
			return Page[RunView]{}, err
		}
		result.NextCursor = &cursor
	}
	return result, nil
}

func (s *Service) ListRuns(ctx context.Context, q RunQuery) (Page[RunView], error) {
	q, err := normalizeRunQuery(q)
	if err != nil {
		return Page[RunView]{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Page[RunView]{}, storageFailure(err)
	}
	defer tx.Rollback()
	result, err := listRunsTx(ctx, tx, q)
	if err != nil {
		return Page[RunView]{}, storageFailure(err)
	}
	if err := tx.Commit(); err != nil {
		return Page[RunView]{}, storageFailure(err)
	}
	return result, nil
}

func (s *Service) GetRun(ctx context.Context, id int64) (RunDetail, error) {
	if id <= 0 {
		return RunDetail{}, queryError()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RunDetail{}, storageFailure(err)
	}
	defer tx.Rollback()
	run, err := loadRunTx(ctx, tx, id)
	if err != nil {
		return RunDetail{}, storageFailure(err)
	}
	steps, err := listRunStepsTx(ctx, tx, id, ListQuery{Limit: 50})
	if err != nil {
		return RunDetail{}, storageFailure(err)
	}
	if err := tx.Commit(); err != nil {
		return RunDetail{}, storageFailure(err)
	}
	return RunDetail{Run: visibleRun(run), Steps: steps}, nil
}

func (s *Service) GetRunLogOverview(ctx context.Context) (RunLogOverview, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RunLogOverview{}, storageFailure(err)
	}
	defer tx.Rollback()
	observed := time.Now()
	if s.now != nil {
		observed = s.now()
	}
	now, err := readObservedNowTx(ctx, tx, observed)
	if err != nil {
		return RunLogOverview{}, storageFailure(err)
	}
	asOf := now.UnixMilli()
	const hourMS int64 = 60 * 60 * 1000
	result := RunLogOverview{FromAt: asOf - 24*hourMS, ToAt: asOf, AsOf: asOf, Buckets: make([]RunHourBucket, 24)}
	result.Coverage, err = readRunLogCoverageTx(ctx, tx, result.FromAt, asOf)
	if err != nil {
		return RunLogOverview{}, storageFailure(err)
	}
	for i := range result.Buckets {
		start := result.FromAt + int64(i)*hourMS
		result.Buckets[i] = RunHourBucket{FromAt: start, ToAt: start + hourMS}
	}
	rows, err := tx.QueryContext(ctx, "SELECT outcome,started_at,ended_at FROM run_records WHERE ended_at IS NOT NULL AND started_at>=? AND started_at<? AND outcome IN ('success','failed')", result.FromAt, result.ToAt)
	if err != nil {
		return RunLogOverview{}, storageFailure(err)
	}
	var duration float64
	for rows.Next() {
		var outcome RunOutcome
		var started, ended int64
		if err := rows.Scan(&outcome, &started, &ended); err != nil {
			rows.Close()
			return RunLogOverview{}, storageFailure(err)
		}
		if ended < started {
			rows.Close()
			return RunLogOverview{}, runError(ErrorUnavailable, "service.storage_failure")
		}
		bucket := &result.Buckets[(started-result.FromAt)/hourMS]
		if outcome == RunSuccess {
			result.SuccessCount++
			bucket.SuccessCount++
		} else {
			result.FailedCount++
			bucket.FailedCount++
		}
		duration += float64(ended) - float64(started)
	}
	rowErr := rows.Err()
	rows.Close()
	if rowErr != nil {
		return RunLogOverview{}, storageFailure(rowErr)
	}
	skips, err := tx.QueryContext(ctx, "SELECT e.at FROM run_skip_events e JOIN run_records r ON r.id=e.run_id WHERE r.outcome='skipped' AND r.ended_at IS NOT NULL AND e.at>=? AND e.at<?", result.FromAt, result.ToAt)
	if err != nil {
		return RunLogOverview{}, storageFailure(err)
	}
	for skips.Next() {
		var at int64
		if err := skips.Scan(&at); err != nil {
			skips.Close()
			return RunLogOverview{}, storageFailure(err)
		}
		result.SkippedCount++
		result.Buckets[(at-result.FromAt)/hourMS].SkippedCount++
	}
	rowErr = skips.Err()
	skips.Close()
	if rowErr != nil {
		return RunLogOverview{}, storageFailure(rowErr)
	}
	completed := result.SuccessCount + result.FailedCount
	result.TotalCount = completed + result.SkippedCount
	if completed > 0 {
		rate, average := float64(result.SuccessCount)/float64(completed), duration/float64(completed)
		result.SuccessRate, result.AverageDurationMS = &rate, &average
	}
	if err := tx.Commit(); err != nil {
		return RunLogOverview{}, storageFailure(err)
	}
	return result, nil
}

func readRunLogCoverageTx(ctx context.Context, tx *sql.Tx, from, now int64) (CoverageView, error) {
	result := CoverageView{ToAt: now, IsPartial: true}
	rows, err := tx.QueryContext(ctx, "SELECT key,value FROM meta WHERE key IN ('stats.started_at','stats.coverage.run_records','stats.coverage.run_skip_events')")
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
		at, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || at <= 0 || strconv.FormatInt(at, 10) != raw {
			return result, errors.New("运行统计覆盖记录损坏")
		}
		started = started || key == "stats.started_at"
		latest = max(latest, at)
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	if started {
		result.FromAt, result.IsPartial = &latest, latest > from
	}
	return result, nil
}
