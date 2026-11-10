package modelservice

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"unicode/utf8"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/analysis/client"
	"github.com/Seaony/Kakatta/internal/analysis/prompts"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/settings"
)

func promptStorageFailure(ctx context.Context, err error) error {
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return storageFailure(err)
}

func promptValidationError(err error) error {
	var invalid *analysis.Error
	if !errors.As(err, &invalid) {
		return storageFailure(err)
	}
	field := "content"
	if invalid.Message.Code == "analysis.prompts.invalid_input" {
		if invalid.Message.Args["field"] != "kind" && invalid.Message.Args["field"] != "content" {
			return storageFailure(err)
		}
		field = invalid.Message.Args["field"].(string)
	}
	return &service.ServiceError{Kind: service.ErrorInvalid, Reason: invalid.Message,
		Fields: []settings.Issue{{Key: field, Reason: invalid.Message}}}
}

func loadPromptViewsTx(ctx context.Context, tx *sql.Tx) ([]PromptView, prompts.Snapshot, error) {
	rows, err := tx.QueryContext(ctx, `SELECT key,content,updated_at FROM prompts`)
	if err != nil {
		return nil, prompts.Snapshot{}, err
	}
	defer rows.Close()
	overrides := map[client.Kind]string{}
	updated := map[client.Kind]*int64{}
	for rows.Next() {
		var kind client.Kind
		var content string
		var at *int64
		if err := rows.Scan(&kind, &content, &at); err != nil {
			return nil, prompts.Snapshot{}, err
		}
		if !utf8.ValidString(content) {
			return nil, prompts.Snapshot{}, errors.New("保存的提示词不是有效文字")
		}
		overrides[kind], updated[kind] = content, at
	}
	if err := rows.Err(); err != nil {
		return nil, prompts.Snapshot{}, err
	}
	snapshot, err := prompts.Resolve(overrides)
	if err != nil {
		return nil, prompts.Snapshot{}, err
	}
	definitions, err := prompts.Definitions()
	if err != nil {
		return nil, prompts.Snapshot{}, err
	}
	views := make([]PromptView, 0, len(definitions))
	for _, definition := range definitions {
		_, customized := overrides[definition.Kind]
		views = append(views, PromptView{Kind: definition.Kind, Content: snapshot.Contents[definition.Kind],
			Default: definition.Default, Customized: customized, RequiredKeys: definition.RequiredKeys,
			Schema: definition.Schema, AssetVersion: snapshot.AssetVersion, UpdatedAt: updated[definition.Kind]})
	}
	return views, snapshot, nil
}

func (s *Service) ListPrompts(ctx context.Context) ([]PromptView, error) {
	ctx, done, err := s.beginJob(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, promptStorageFailure(ctx, err)
	}
	defer tx.Rollback()
	views, _, err := loadPromptViewsTx(ctx, tx)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return nil, promptStorageFailure(ctx, err)
	}
	return views, nil
}

func (s *Service) PromptSnapshot(ctx context.Context) (prompts.Snapshot, error) {
	ctx, done, err := s.beginJob(ctx)
	if err != nil {
		return prompts.Snapshot{}, err
	}
	defer done()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return prompts.Snapshot{}, promptStorageFailure(ctx, err)
	}
	defer tx.Rollback()
	_, snapshot, err := loadPromptViewsTx(ctx, tx)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return prompts.Snapshot{}, promptStorageFailure(ctx, err)
	}
	return snapshot, nil
}

func (s *Service) SavePrompt(ctx context.Context, kind client.Kind, content string) (PromptView, error) {
	return s.changePrompt(ctx, kind, &content)
}

func (s *Service) RestorePrompt(ctx context.Context, kind client.Kind) (PromptView, error) {
	return s.changePrompt(ctx, kind, nil)
}

func (s *Service) changePrompt(ctx context.Context, kind client.Kind, content *string) (PromptView, error) {
	ctx, done, err := s.beginJob(ctx)
	if err != nil {
		return PromptView{}, err
	}
	defer done()
	if content != nil && !utf8.ValidString(*content) {
		return PromptView{}, promptValidationError(&analysis.Error{Message: locales.Message{Code: "analysis.prompts.invalid_input", Args: map[string]any{"field": "content"}}})
	}
	definitions, err := prompts.Definitions()
	if err != nil {
		return PromptView{}, storageFailure(err)
	}
	known := false
	for _, definition := range definitions {
		if definition.Kind == kind {
			known = true
			if content == nil {
				content = &definition.Default
			}
			break
		}
	}
	if !known {
		return PromptView{}, promptValidationError(&analysis.Error{Message: locales.Message{Code: "analysis.prompts.invalid_input", Args: map[string]any{"field": "kind"}}})
	}
	prepared, err := prompts.PrepareSave(kind, *content)
	if err != nil {
		return PromptView{}, promptValidationError(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PromptView{}, promptStorageFailure(ctx, err)
	}
	defer tx.Rollback()
	var previous string
	err = tx.QueryRowContext(ctx, `SELECT content FROM prompts WHERE key=?`, kind).Scan(&previous)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return PromptView{}, promptStorageFailure(ctx, err)
	}
	changed := prepared == nil && exists || prepared != nil && (!exists || previous != *prepared)
	if changed {
		if prepared == nil {
			_, err = tx.ExecContext(ctx, `DELETE FROM prompts WHERE key=?`, kind)
		} else {
			_, err = tx.ExecContext(ctx, `INSERT INTO prompts(key,content,updated_at) VALUES(?,?,?)
				ON CONFLICT(key) DO UPDATE SET content=excluded.content,updated_at=excluded.updated_at`, kind, *prepared, s.now().UnixMilli())
		}
		if err != nil {
			return PromptView{}, promptStorageFailure(ctx, err)
		}
	}
	views, _, err := loadPromptViewsTx(ctx, tx)
	if err != nil {
		return PromptView{}, promptStorageFailure(ctx, err)
	}
	var selected PromptView
	for _, view := range views {
		if view.Kind == kind {
			selected = view
			break
		}
	}
	if selected.Kind == "" {
		return PromptView{}, storageFailure(errors.New("提示词资产缺少已知用途"))
	}
	if err := tx.Commit(); err != nil {
		return PromptView{}, promptStorageFailure(ctx, err)
	}
	if changed {
		data, _ := json.Marshal(map[string]client.Kind{"kind": kind})
		logCallbackFailure(s.publish("ai.prompts.changed", nil, data))
	}
	return selected, nil
}
