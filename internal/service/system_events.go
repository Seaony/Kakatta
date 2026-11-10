package service

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Seaony/Kakatta/internal/locales"
)

type systemEventAnchor struct {
	CreatedAt *int64 `json:"created_at"`
	ID        int64  `json:"id"`
}

func (s *Service) ListSystemEvents(ctx context.Context, query SystemEventQuery) (Page[SystemEvent], error) {
	page := Page[SystemEvent]{Items: []SystemEvent{}}
	var err error
	query.ListQuery, err = normalizeListQuery(query.ListQuery)
	if err != nil {
		return page, err
	}
	binding := query
	binding.ListQuery = ListQuery{}
	var anchor systemEventAnchor
	if err := decodeCursor(query.Cursor, "system.events", binding, &anchor); err != nil {
		return page, err
	}
	if query.Cursor != "" && anchor.ID <= 0 {
		return page, queryError()
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return page, storageFailure(err)
	}
	defer tx.Rollback()
	where := "1=1"
	if query.Resolved != nil {
		if *query.Resolved {
			where = "resolved_at IS NOT NULL"
		} else {
			where = "resolved_at IS NULL"
		}
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM system_events WHERE `+where).Scan(&page.Total); err != nil {
		return page, storageFailure(err)
	}
	args := []any{}
	if query.Cursor != "" {
		if anchor.CreatedAt == nil {
			where += " AND created_at IS NULL AND id < ?"
			args = append(args, anchor.ID)
		} else {
			where += " AND (created_at < ? OR created_at IS NULL OR (created_at = ? AND id < ?))"
			args = append(args, *anchor.CreatedAt, *anchor.CreatedAt, anchor.ID)
		}
	}
	args = append(args, query.Limit+1)
	rows, err := tx.QueryContext(ctx, `SELECT id,kind,subject_id,message,created_at,resolved_at,notified_at FROM system_events WHERE `+where+` ORDER BY created_at DESC NULLS LAST,id DESC LIMIT ?`, args...)
	if err != nil {
		return page, storageFailure(err)
	}
	for rows.Next() {
		event, err := scanSystemEvent(rows)
		if err != nil {
			rows.Close()
			return Page[SystemEvent]{Items: []SystemEvent{}}, storageFailure(err)
		}
		page.Items = append(page.Items, event)
	}
	readErr := rows.Err()
	closeErr := rows.Close()
	if readErr != nil {
		return Page[SystemEvent]{Items: []SystemEvent{}}, storageFailure(readErr)
	}
	if closeErr != nil {
		return Page[SystemEvent]{Items: []SystemEvent{}}, storageFailure(closeErr)
	}
	if int64(len(page.Items)) > query.Limit {
		page.Items = page.Items[:query.Limit]
		last := page.Items[len(page.Items)-1]
		cursor, err := encodeCursor("system.events", binding, systemEventAnchor{CreatedAt: last.CreatedAt, ID: last.ID})
		if err != nil {
			return Page[SystemEvent]{Items: []SystemEvent{}}, err
		}
		page.NextCursor = &cursor
	}
	if err := tx.Commit(); err != nil {
		return Page[SystemEvent]{Items: []SystemEvent{}}, storageFailure(err)
	}
	return page, nil
}

func scanSystemEvent(row interface{ Scan(...any) error }) (SystemEvent, error) {
	var event SystemEvent
	var raw sql.NullString
	if err := row.Scan(&event.ID, &event.Kind, &event.SubjectID, &raw, &event.CreatedAt, &event.ResolvedAt, &event.NotifiedAt); err != nil {
		return event, err
	}
	if raw.Valid {
		var message *locales.Message
		if err := logJSON(raw.String, &message); err != nil {
			return SystemEvent{}, err
		}
		if message == nil || message.Code == "" {
			return SystemEvent{}, errors.New("系统事件的原因记录损坏")
		}
		if message.Args == nil {
			message.Args = map[string]any{}
		}
		event.Message = message
	}
	return event, nil
}
