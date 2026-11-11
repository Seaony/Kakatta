package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// appendRunStepTx 超过前一百条以后只保存失败，序号只由本入口分配。
func appendRunStepTx(ctx context.Context, tx *sql.Tx, step RunStep) (bool, error) {
	if step.RunID <= 0 || step.At == nil || step.Status == nil || step.Message == nil {
		return false, runError(ErrorInvalid, "runs.invalid_step")
	}
	if *step.Status != StepOK && *step.Status != StepWarn && *step.Status != StepFail {
		return false, runError(ErrorInvalid, "runs.invalid_step")
	}
	var count, last int64
	if err := tx.QueryRowContext(ctx, "SELECT count(*),COALESCE(max(seq),0) FROM run_steps WHERE run_id=?", step.RunID).Scan(&count, &last); err != nil {
		return false, err
	}
	if count >= 100 && *step.Status != StepFail {
		return false, nil
	}
	message, err := logMessageJSON(step.Message)
	if err != nil {
		return false, err
	}
	detail, err := logMessageJSON(step.Detail)
	if err != nil {
		return false, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO run_steps(run_id,seq,at,status,message,detail) VALUES(?,?,?,?,?,?)",
		step.RunID, last+1, step.At, step.Status, message, detail)
	return err == nil, err
}

type runStepCursor struct {
	Seq int64 `json:"seq"`
}

func (s *Service) listRunStepsTx(ctx context.Context, tx *sql.Tx, runID int64, q ListQuery) (Page[RunStep], error) {
	binding := struct {
		RunID int64 `json:"run_id"`
	}{RunID: runID}
	var anchor runStepCursor
	if err := decodeCursor(q.Cursor, "runs.steps", binding, &anchor); err != nil {
		return Page[RunStep]{}, err
	}
	if q.Cursor != "" && anchor.Seq <= 0 {
		return Page[RunStep]{}, queryError()
	}
	snapshot, _, now, err := s.observeNowTx(ctx, tx)
	if err != nil {
		return Page[RunStep]{}, err
	}
	var days int64
	if err := json.Unmarshal(snapshot.Values["retention.run_steps_days"], &days); err != nil || days < 1 {
		return Page[RunStep]{}, errors.New("逐步日志保留期无效")
	}
	cutoff := cleanupBefore(now.UnixMilli(), days*24*time.Hour.Milliseconds())
	where := " WHERE run_id=? AND (at IS NULL OR at>=?)"
	args := []any{runID, cutoff}
	result := Page[RunStep]{Items: []RunStep{}}
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM run_steps"+where, args...).Scan(&result.Total); err != nil {
		return Page[RunStep]{}, err
	}
	if q.Cursor != "" {
		where += " AND seq>?"
		args = append(args, anchor.Seq)
	}
	args = append(args, q.Limit+1)
	rows, err := tx.QueryContext(ctx, "SELECT run_id,seq,at,status,message,detail FROM run_steps"+where+" ORDER BY seq ASC LIMIT ?", args...)
	if err != nil {
		return Page[RunStep]{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var step RunStep
		var message, detail sql.NullString
		if err := rows.Scan(&step.RunID, &step.Seq, &step.At, &step.Status, &message, &detail); err != nil {
			return Page[RunStep]{}, err
		}
		step.Message, err = logMessage(message)
		if err != nil {
			return Page[RunStep]{}, err
		}
		step.Detail, err = logMessage(detail)
		if err != nil {
			return Page[RunStep]{}, err
		}
		result.Items = append(result.Items, step)
	}
	if err := rows.Err(); err != nil {
		return Page[RunStep]{}, err
	}
	if int64(len(result.Items)) > q.Limit {
		result.Items = result.Items[:q.Limit]
		last := result.Items[len(result.Items)-1]
		cursor, err := encodeCursor("runs.steps", binding, runStepCursor{Seq: last.Seq})
		if err != nil {
			return Page[RunStep]{}, err
		}
		result.NextCursor = &cursor
	}
	return result, nil
}

func (s *Service) ListRunSteps(ctx context.Context, runID int64, q ListQuery) (Page[RunStep], error) {
	if runID <= 0 {
		return Page[RunStep]{}, queryError()
	}
	q, err := normalizeListQuery(q)
	if err != nil {
		return Page[RunStep]{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Page[RunStep]{}, storageFailure(err)
	}
	defer tx.Rollback()
	var found int
	if err := tx.QueryRowContext(ctx, "SELECT 1 FROM run_records WHERE id=?", runID).Scan(&found); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Page[RunStep]{}, runError(ErrorNotFound, "runs.not_found")
		}
		return Page[RunStep]{}, storageFailure(err)
	}
	result, err := s.listRunStepsTx(ctx, tx, runID, q)
	if err != nil {
		return Page[RunStep]{}, storageFailure(err)
	}
	if err := tx.Commit(); err != nil {
		return Page[RunStep]{}, storageFailure(err)
	}
	return result, nil
}
