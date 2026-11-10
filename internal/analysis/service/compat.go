package modelservice

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Seaony/Kakatta/internal/analysis/client"
)

func compatRank(mode client.JSONMode) int {
	switch mode {
	case client.JSONSchema:
		return 0
	case client.JSONObject:
		return 1
	case client.JSONText:
		return 2
	}
	return -1
}

func validCompat(value client.Compat) bool {
	return compatRank(value.JSONMode) >= 0 && (value.MaxTokensParam == "max_tokens" || value.MaxTokensParam == "max_completion_tokens") && (value.MaxTokensFloor == nil || *value.MaxTokensFloor >= 0)
}

func matchCompatTx(ctx context.Context, tx *sql.Tx, key client.CompatKey) error {
	p, err := matchProviderTx(ctx, tx, key.Provider)
	if err != nil {
		return err
	}
	if p.view.BaseURL != key.BaseURL || key.Model != p.view.VisionModel && (p.view.TextModel == nil || key.Model != *p.view.TextModel) {
		return client.ErrConfigChanged
	}
	return nil
}

func readCompatTx(ctx context.Context, tx *sql.Tx, key client.CompatKey) (client.Compat, error) {
	var value client.Compat
	err := tx.QueryRowContext(ctx, `SELECT json_mode,send_temperature,max_tokens_param,max_tokens_floor FROM ai_compat WHERE provider_id=? AND model=?`,
		key.Provider.ID, key.Model).Scan(&value.JSONMode, &value.SendTemperature, &value.MaxTokensParam, &value.MaxTokensFloor)
	if errors.Is(err, sql.ErrNoRows) {
		return client.DefaultCompat(), nil
	}
	if err == nil && !validCompat(value) {
		err = errors.New("模型兼容记录损坏")
	}
	return value, err
}

func (s *Service) LoadCompat(ctx context.Context, key client.CompatKey) (client.Compat, error) {
	ctx, done, err := s.beginJob(ctx)
	if err != nil {
		return client.Compat{}, err
	}
	defer done()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return client.Compat{}, storageFailure(err)
	}
	defer tx.Rollback()
	if err := matchCompatTx(ctx, tx, key); err != nil {
		if err == client.ErrConfigChanged {
			return client.Compat{}, err
		}
		return client.Compat{}, storageFailure(err)
	}
	value, err := readCompatTx(ctx, tx, key)
	return value, storageFailure(err)
}

func mergeCompat(previous, requested client.Compat) client.Compat {
	result := previous
	if compatRank(requested.JSONMode) > compatRank(result.JSONMode) {
		result.JSONMode = requested.JSONMode
	}
	result.SendTemperature = result.SendTemperature && requested.SendTemperature
	if requested.MaxTokensParam == "max_completion_tokens" {
		result.MaxTokensParam = requested.MaxTokensParam
	}
	if requested.MaxTokensFloor != nil && (result.MaxTokensFloor == nil || *result.MaxTokensFloor < *requested.MaxTokensFloor) {
		result.MaxTokensFloor = requested.MaxTokensFloor
	}
	if result.MaxTokensFloor != nil {
		floor := *result.MaxTokensFloor
		result.MaxTokensFloor = &floor
	}
	return result
}

func (s *Service) SaveCompat(ctx context.Context, key client.CompatKey, requested client.Compat) (client.Compat, error) {
	ctx, done, err := s.beginJob(ctx)
	if err != nil {
		return client.Compat{}, err
	}
	defer done()
	if !validCompat(requested) {
		return client.Compat{}, invalidProvider("compat")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return client.Compat{}, storageFailure(err)
	}
	defer tx.Rollback()
	if err := matchCompatTx(ctx, tx, key); err != nil {
		if err == client.ErrConfigChanged {
			return client.Compat{}, err
		}
		return client.Compat{}, storageFailure(err)
	}
	previous, err := readCompatTx(ctx, tx, key)
	if err != nil {
		return client.Compat{}, storageFailure(err)
	}
	value := mergeCompat(previous, requested)
	_, clock, now, err := s.observeNowTx(ctx, tx)
	if err == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO ai_compat(provider_id,model,json_mode,send_temperature,max_tokens_param,max_tokens_floor,updated_at) VALUES(?,?,?,?,?,?,?)
			ON CONFLICT(provider_id,model) DO UPDATE SET json_mode=excluded.json_mode,send_temperature=excluded.send_temperature,max_tokens_param=excluded.max_tokens_param,max_tokens_floor=excluded.max_tokens_floor,updated_at=excluded.updated_at`,
			key.Provider.ID, key.Model, value.JSONMode, value.SendTemperature, value.MaxTokensParam, value.MaxTokensFloor, now.UnixMilli())
	}
	if err == nil {
		err = commitClock(ctx, tx, clock)
	}
	if err != nil {
		return client.Compat{}, storageFailure(err)
	}
	return value, nil
}
