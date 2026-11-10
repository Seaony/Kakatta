package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"time"

	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/settings"
)

type taskImportOptions struct{ Channels *[]TaskChannelRef }

func (s *Service) taskTransferContext(parent context.Context) (context.Context, func(), error) {
	release, err := s.beginMutation()
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithCancelCause(parent)
	stop := context.AfterFunc(s.ctx, func() { cancel(context.Cause(s.ctx)) })
	if s.ctx.Err() != nil {
		cancel(context.Cause(s.ctx))
	}
	return ctx, func() { stop(); cancel(nil); release() }, nil
}

// ExportTasks 的写入目标是本地临时文件，下载在快照归还后开始。
func (s *Service) ExportTasks(parent context.Context, dst io.Writer) error {
	ctx, release, err := s.taskTransferContext(parent)
	if err != nil {
		return err
	}
	defer release()
	if dst == nil || s.readSnapshot == nil {
		return transferUnavailable()
	}
	return storageFailure(s.readSnapshot(ctx, func(tx *sql.Tx) error {
		now, err := readObservedNowTx(ctx, tx, s.now())
		if err != nil {
			return err
		}
		snapshot, err := s.settings.SnapshotTx(ctx, tx)
		if err != nil {
			return err
		}
		var zone string
		if err := json.Unmarshal(snapshot.Values["general.timezone"], &zone); err != nil {
			return err
		}
		location, err := time.LoadLocation(zone)
		if err != nil {
			return err
		}
		timestamp, _ := json.Marshal(now.In(location).Format(time.RFC3339))
		write := func(raw []byte) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			n, err := dst.Write(raw)
			if err == nil && n != len(raw) {
				err = io.ErrShortWrite
			}
			return err
		}
		if err := write(append(append([]byte(`{"format":"kakatta.tasks","version":1,"exported_at":`), timestamp...), []byte(`,"tasks":[`)...)); err != nil {
			return err
		}
		first := true
		var sortIndex, id int64
		for {
			rows, err := tx.QueryContext(ctx, `SELECT id,sort_index FROM tasks WHERE ?=1 OR sort_index>? OR (sort_index=? AND id>?) ORDER BY sort_index,id LIMIT 100`, firstPage(first, id), sortIndex, sortIndex, id)
			if err != nil {
				return err
			}
			type position struct{ id, sort int64 }
			page := []position{}
			for rows.Next() {
				var p position
				if err = rows.Scan(&p.id, &p.sort); err != nil {
					rows.Close()
					return err
				}
				page = append(page, p)
			}
			err = rows.Err()
			closeErr := rows.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
			if len(page) == 0 {
				break
			}
			for _, p := range page {
				task, err := loadTaskTx(ctx, tx, p.id)
				if err != nil {
					return err
				}
				raw, err := exportTaskTx(ctx, tx, task)
				if err != nil {
					return err
				}
				if !first {
					if err = write([]byte(",")); err != nil {
						return err
					}
				}
				first = false
				if err = write(raw); err != nil {
					return err
				}
				sortIndex, id = p.sort, p.id
			}
		}
		return write([]byte("]}\n"))
	}))
}

func firstPage(first bool, id int64) int {
	if first && id == 0 {
		return 1
	}
	return 0
}
func transferUnavailable() error {
	return &ServiceError{Kind: ErrorUnavailable, Reason: locales.Message{Code: "tasks.transfer_unavailable", Args: map[string]any{}}, Fields: []settings.Issue{}}
}

func exportTaskTx(ctx context.Context, tx *sql.Tx, task Task) ([]byte, error) {
	encoded, err := json.Marshal(task.TaskFields)
	if err != nil {
		return nil, err
	}
	all := TaskPatch{}
	if err = json.Unmarshal(encoded, &all); err != nil {
		return nil, err
	}
	fields := TaskPatch{}
	for _, key := range []string{"name", "keyword", "enabled", "min_price", "max_price", "personal_only", "free_shipping_only", "publish_window", "max_pages", "interval_minutes", "crawl_overrides", "judge_mode", "requirement", "profile", "budget_min", "budget_max", "match_keywords", "exclude_keywords", "analyze_on_first_run", "notify_policy", "notify_price_drop"} {
		fields[key] = all[key]
	}
	for _, price := range []struct {
		key   string
		value *int64
	}{{"min_price", task.MinPrice}, {"max_price", task.MaxPrice}, {"budget_min", task.BudgetMin}, {"budget_max", task.BudgetMax}} {
		fields[price.key] = json.RawMessage("null")
		if price.value != nil {
			fields[price.key] = json.RawMessage(taskMoneyDecimal(*price.value))
		}
	}
	fields["region"], err = json.Marshal(map[string]*string{"province": task.RegionProvince, "city": task.RegionCity, "district": task.RegionDistrict})
	if err != nil {
		return nil, err
	}
	var profile string
	if err = tx.QueryRowContext(ctx, `SELECT profile_json FROM tasks WHERE id=?`, task.ID).Scan(&profile); err != nil {
		return nil, err
	}
	fields["profile"] = json.RawMessage(profile)
	refs := []TaskChannelRef{}
	rows, err := tx.QueryContext(ctx, `SELECT c.type,c.name FROM channels c JOIN task_channels tc ON tc.channel_id=c.id WHERE tc.task_id=? ORDER BY c.id`, task.ID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var ref TaskChannelRef
		if err = rows.Scan(&ref.Type, &ref.Name); err != nil {
			rows.Close()
			return nil, err
		}
		refs = append(refs, ref)
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	fields["channels"], err = json.Marshal(refs)
	if err != nil {
		return nil, err
	}
	return json.Marshal(fields)
}

func taskMoneyDecimal(cents int64) string {
	sign := ""
	if cents < 0 {
		sign = "-"
		cents = -cents
	}
	value := sign + strconv.FormatInt(cents/100, 10)
	if cents%100 == 0 {
		return value
	}
	fraction := strconv.FormatInt(cents%100+100, 10)[1:]
	if fraction[1] == '0' {
		fraction = fraction[:1]
	}
	return value + "." + fraction
}

func (s *Service) ImportTasks(parent context.Context, staged io.ReadSeeker) (*TaskImportReport, error) {
	ctx, release, err := s.taskTransferContext(parent)
	if err != nil {
		return nil, err
	}
	defer release()
	if staged == nil {
		return nil, transferInvalidFile()
	}
	if _, err = staged.Seek(0, io.SeekStart); err != nil {
		return nil, storageFailure(err)
	}
	total, err := scanTaskImport(ctx, staged, nil)
	if err != nil {
		return nil, err
	}
	report := &TaskImportReport{Total: total, Unprocessed: total, Items: []TaskImportItem{}}
	if _, err = staged.Seek(0, io.SeekStart); err != nil {
		setTaskImportFailure(report, err)
		return report, storageFailure(err)
	}
	_, err = scanTaskImport(ctx, staged, func(index int64, raw json.RawMessage) error {
		item := TaskImportItem{Index: index, Corrections: []settings.Issue{}, Warnings: []settings.Issue{}, Fields: []settings.Issue{}}
		patch, options, decodeErr := decodeImportedTask(raw, &item.Corrections)
		var result TaskSaveResult
		if decodeErr == nil {
			var corrections []settings.Issue
			result, corrections, decodeErr = s.createTask(ctx, patch, options)
			item.Corrections = append(item.Corrections, corrections...)
			item.Warnings = append(item.Warnings, result.Warnings...)
		}
		report.Unprocessed--
		if decodeErr == nil {
			item.Status = "imported"
			id := result.Task.ID
			item.TaskID = &id
			report.Imported++
		} else {
			var business *ServiceError
			if errors.As(decodeErr, &business) {
				message := business.Reason
				item.Reason = &message
				item.Fields = append(item.Fields, business.Fields...)
			} else {
				reason := taskImportFailureReason(decodeErr)
				item.Reason = &reason
			}
			if errors.As(decodeErr, &business) && (business.Kind == ErrorInvalid || business.Kind == ErrorBadRequest) {
				item.Status = "skipped"
				report.Skipped++
			} else {
				item.Status = "failed"
				report.Failed++
				report.Items = append(report.Items, item)
				return decodeErr
			}
		}
		report.Items = append(report.Items, item)
		return nil
	})
	if err != nil {
		setTaskImportFailure(report, err)
		return report, err
	}
	report.Complete = true
	return report, nil
}
func taskImportFailureReason(err error) locales.Message {
	var business *ServiceError
	if errors.As(err, &business) {
		return business.Reason
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return locales.Message{Code: "tasks.import_cancelled", Args: map[string]any{}}
	}
	return locales.Message{Code: "service.storage_failure", Args: map[string]any{}}
}
func setTaskImportFailure(report *TaskImportReport, err error) {
	reason := taskImportFailureReason(err)
	report.Failure = &reason
	report.Complete = false
}
func transferInvalidFile() error {
	return &ServiceError{Kind: ErrorBadRequest, Reason: locales.Message{Code: "tasks.import_invalid_file", Args: map[string]any{}}, Fields: []settings.Issue{}}
}
func importCorrection(issues *[]settings.Issue, key, code string) {
	*issues = append(*issues, settings.Issue{Key: key, Reason: locales.Message{Code: "tasks.import_" + code, Args: map[string]any{}}})
}
