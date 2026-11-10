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

	"github.com/Seaony/Kakatta/internal/locales"
)

var runExportHeader = []string{
	"record_type", "run_id", "task_id", "task_name", "trigger", "account_id", "account_name", "proxy_name",
	"started_at", "ended_at", "outcome", "failure_kind", "repeat_count", "page_count", "scanned_count", "new_count",
	"layer1_rejected", "layer2_count", "layer2_rejected", "detail_count", "seller_page_count", "analyzed_count",
	"recommended_count", "notified_count", "warnings", "seq", "at", "status", "message", "detail",
}

func (s *Service) ExportRunLogs(ctx context.Context, query RunQuery, language locales.Language, dst io.Writer) (ExportInfo, error) {
	if dst == nil || language != locales.ZhCN && language != locales.En {
		return ExportInfo{}, queryError()
	}
	query.ListQuery = ListQuery{}
	query, err := normalizeRunQuery(query)
	if err != nil {
		return ExportInfo{}, err
	}
	var info ExportInfo
	err = s.exportSnapshot(ctx, func(ctx context.Context, tx *sql.Tx) error {
		location, now, err := s.exportTimeTx(ctx, tx)
		if err != nil {
			return err
		}
		info = ExportInfo{Filename: "kakatta-runs-" + now.Format("2006-01-02") + ".txt", MediaType: "text/plain; charset=utf-8"}
		writer := exportWriter{ctx, dst}
		if err := writeExportTSVRow(writer, runExportHeader); err != nil {
			return err
		}
		var anchor *runCursor
		for {
			where, args := runQueryWhere(query)
			if anchor != nil {
				if anchor.StartedAt == nil {
					where += " AND r.started_at IS NULL AND r.id<?"
					args = append(args, anchor.ID)
				} else {
					where += " AND (r.started_at<? OR (r.started_at=? AND r.id<?) OR r.started_at IS NULL)"
					args = append(args, *anchor.StartedAt, *anchor.StartedAt, anchor.ID)
				}
			}
			columns, selectionArgs := runQueryColumns(query)
			selectionArgs = append(selectionArgs, args...)
			rows, err := tx.QueryContext(ctx, "SELECT "+columns+" FROM run_records r WHERE "+where+" ORDER BY r.started_at DESC NULLS LAST,r.id DESC LIMIT ?", append(selectionArgs, exportBatchSize)...)
			if err != nil {
				return err
			}
			batch := []RunRecord{}
			for rows.Next() {
				run, err := scanRun(rows)
				if err != nil {
					rows.Close()
					return err
				}
				batch = append(batch, run)
			}
			err = errors.Join(rows.Err(), rows.Close())
			if err != nil {
				return err
			}
			if len(batch) == 0 {
				return nil
			}
			for _, run := range batch {
				fields, err := runExportFields(visibleRun(run), language, location)
				if err != nil {
					return err
				}
				if err := writeExportTSVRow(writer, fields); err != nil {
					return err
				}
				info.Rows++
				if err := exportRunSteps(ctx, tx, run.ID, language, location, writer, &info.Rows); err != nil {
					return err
				}
			}
			last := batch[len(batch)-1]
			anchor = &runCursor{ID: last.ID, StartedAt: last.StartedAt}
		}
	})
	if err != nil {
		return ExportInfo{}, storageFailure(err)
	}
	return info, nil
}

func exportRunSteps(ctx context.Context, tx *sql.Tx, runID int64, language locales.Language, location *time.Location, writer io.Writer, count *int64) error {
	var after int64
	for {
		rows, err := tx.QueryContext(ctx, "SELECT run_id,seq,at,status,message,detail FROM run_steps WHERE run_id=? AND seq>? ORDER BY seq ASC LIMIT ?", runID, after, exportBatchSize)
		if err != nil {
			return err
		}
		batch := []RunStep{}
		for rows.Next() {
			var step RunStep
			var message, detail sql.NullString
			if err := rows.Scan(&step.RunID, &step.Seq, &step.At, &step.Status, &message, &detail); err != nil {
				rows.Close()
				return err
			}
			step.Message, err = logMessage(message)
			if err == nil {
				step.Detail, err = logMessage(detail)
			}
			if err != nil {
				rows.Close()
				return err
			}
			batch = append(batch, step)
		}
		err = errors.Join(rows.Err(), rows.Close())
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}
		for _, step := range batch {
			fields := make([]string, len(runExportHeader))
			fields[0], fields[1], fields[25] = "step", strconv.FormatInt(runID, 10), strconv.FormatInt(step.Seq, 10)
			fields[26], fields[27] = exportTimestamp(step.At, location), exportOptional(step.Status)
			fields[28], err = exportMessage(language, step.Message)
			if err == nil {
				fields[29], err = exportMessage(language, step.Detail)
			}
			if err != nil {
				return err
			}
			if err := writeExportTSVRow(writer, fields); err != nil {
				return err
			}
			*count += 1
		}
		after = batch[len(batch)-1].Seq
	}
}

func runExportFields(run RunView, language locales.Language, location *time.Location) ([]string, error) {
	fields := make([]string, len(runExportHeader))
	fields[0], fields[1], fields[2], fields[3] = "run", strconv.FormatInt(run.ID, 10), exportOptional(run.TaskID), run.TaskName
	fields[4], fields[5], fields[6], fields[7] = string(run.Trigger), exportOptional(run.AccountID), exportOptional(run.AccountName), exportOptional(run.ProxyName)
	fields[8], fields[9] = exportTimestamp(run.StartedAt, location), exportTimestamp(run.EndedAt, location)
	fields[10], fields[11], fields[12] = exportOptional(run.Outcome), exportOptional(run.FailureKind), strconv.FormatInt(run.RepeatCount, 10)
	for i, value := range []*int64{run.PageCount, run.ScannedCount, run.NewCount, run.Layer1Rejected, run.Layer2Count,
		run.Layer2Rejected, run.DetailCount, run.SellerPageCount, run.AnalyzedCount, run.RecommendedCount, run.NotifiedCount} {
		fields[13+i] = exportOptional(value)
	}
	warnings := []string{}
	for _, warning := range run.Warnings {
		text, err := exportMessage(language, &warning)
		if err != nil {
			return nil, err
		}
		warnings = append(warnings, text)
	}
	raw, err := json.Marshal(warnings)
	if err != nil {
		return nil, err
	}
	fields[24] = string(raw)
	fields[28], err = exportMessage(language, run.Message)
	return fields, err
}

var exportTSVEscape = strings.NewReplacer("\\", "\\\\", "\t", "\\t", "\r", "\\r", "\n", "\\n", "\u2028", "\\u2028", "\u2029", "\\u2029")

func writeExportTSVRow(writer io.Writer, fields []string) error {
	if err := validExportFields(fields); err != nil {
		return err
	}
	escaped := make([]string, len(fields))
	for i, field := range fields {
		escaped[i] = exportTSVEscape.Replace(field)
	}
	_, err := io.WriteString(writer, strings.Join(escaped, "\t")+"\n")
	return err
}
