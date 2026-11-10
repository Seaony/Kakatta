package modelservice

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"maps"
	"strings"
	"unicode/utf8"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/analysis/prompts"
	"github.com/Seaony/Kakatta/internal/limits"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/settings"
)

var _ API = (*Service)(nil)

func (s *Service) GenerateProfile(ctx context.Context, input GenerateProfileInput) (GeneratedDraft, error) {
	ctx, finish, err := s.beginJob(ctx)
	if err != nil {
		return GeneratedDraft{}, err
	}
	defer finish()
	var taskID *int64
	if input.TaskID != nil {
		id := *input.TaskID
		if id < limits.MinSafeInteger || id > limits.MaxSafeInteger {
			return GeneratedDraft{}, generationInputError("task_id", locales.Message{Code: "settings.overflow", Args: map[string]any{}})
		}
		if id < 1 {
			return GeneratedDraft{}, generationInputError("task_id", locales.Message{Code: "settings.positive_id_required", Args: map[string]any{}})
		}
		taskID = &id
	}
	keyword := strings.TrimSpace(input.Keyword)
	if n := utf8.RuneCountInString(keyword); !utf8.ValidString(keyword) || n < 1 || n > 50 {
		return GeneratedDraft{}, generationInputError("keyword", locales.Message{Code: "tasks.text_length", Args: map[string]any{"min": 1, "max": 50}})
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return GeneratedDraft{}, generationFailure(ctx, err)
	}
	defer tx.Rollback()
	old, current, warnings, err := service.PrepareProfileGenerationTx(ctx, tx, taskID, input.Current)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return GeneratedDraft{}, generationFailure(ctx, err)
	}
	snapshot, err := s.PromptSnapshot(ctx)
	if err != nil {
		return GeneratedDraft{}, generationFailure(ctx, err)
	}
	request, err := prompts.BuildProfile(snapshot, taskID, keyword, input.Requirement, current)
	if err != nil {
		return GeneratedDraft{}, generationFailure(ctx, err)
	}
	result, err := s.Complete(ctx, request)
	if err != nil {
		return GeneratedDraft{}, generationFailure(ctx, err)
	}
	generated, parseWarnings, err := analysis.ParseGeneratedProfile([]byte(result.Text))
	if err != nil {
		return GeneratedDraft{}, generationFailure(ctx, err)
	}
	warnings = append(warnings, parseWarnings...)
	profile, suggestions, mergeWarnings, err := analysis.MergeGeneratedProfile(current, generated, keyword, input.Requirement)
	if err != nil {
		return GeneratedDraft{}, generationFailure(ctx, err)
	}
	warnings = append(warnings, mergeWarnings...)
	// 中间草稿不算一次已保存修改，最终版本仍对照真实旧清单。
	profile, revisionWarnings, err := analysis.ReviseProfile(old, profile)
	if err != nil {
		return GeneratedDraft{}, generationFailure(ctx, err)
	}
	warnings = append(warnings, revisionWarnings...)
	if err := ctx.Err(); err != nil {
		return GeneratedDraft{}, err
	}
	view, discarded := ConvertSuggestions(suggestions)
	if len(discarded) != 0 {
		slog.WarnContext(ctx, "已去掉无效预算建议", "fields", discarded)
	}
	return GeneratedDraft{Profile: profile, Suggestions: view, Warnings: warnings, Request: result}, nil
}

func generationInputError(field string, reason locales.Message) error {
	return &service.ServiceError{Kind: service.ErrorInvalid, Reason: locales.Message{Code: "tasks.invalid", Args: map[string]any{}},
		Fields: []settings.Issue{{Key: field, Reason: reason}}}
}

func generationFailure(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var invalid *analysis.Error
	if errors.As(err, &invalid) {
		reason := locales.Message{Code: invalid.Message.Code, Args: maps.Clone(invalid.Message.Args)}
		return &storageError{business: &service.ServiceError{Kind: service.ErrorUnavailable, Reason: reason, Fields: []settings.Issue{}}, cause: err}
	}
	return requestFailure(err)
}
