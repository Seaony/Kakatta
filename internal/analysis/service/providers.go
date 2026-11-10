package modelservice

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/eventstore"
	"github.com/Seaony/Kakatta/internal/limits"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/quota"
)

func (s *Service) ListProviders(ctx context.Context) ([]ProviderView, error) {
	ctx, done, err := s.beginJob(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	records, err := s.readProviders(ctx)
	if err != nil {
		return nil, storageFailure(err)
	}
	result := make([]ProviderView, 0, len(records))
	for _, record := range records {
		view, err := s.providerView(record)
		if err != nil {
			return nil, storageFailure(err)
		}
		result = append(result, view)
	}
	return result, nil
}

func (s *Service) GetProvider(ctx context.Context, id int64) (ProviderView, error) {
	ctx, done, err := s.beginJob(ctx)
	if err != nil {
		return ProviderView{}, err
	}
	defer done()
	record, err := scanProvider(s.db.QueryRowContext(ctx, `SELECT `+providerColumns+providerJoin+` WHERE p.id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ProviderView{}, missingProvider()
	}
	if err != nil {
		return ProviderView{}, storageFailure(err)
	}
	view, err := s.providerView(record)
	return view, storageFailure(err)
}

func normalizeProvider(input ProviderInput) (ProviderInput, []locales.Message, error) {
	input.Name, input.VisionModel = strings.TrimSpace(input.Name), strings.TrimSpace(input.VisionModel)
	if input.Name == "" {
		return input, nil, invalidProvider("name")
	}
	if input.VisionModel == "" {
		return input, nil, invalidProvider("vision_model")
	}
	if input.Preset != nil && !slices.Contains([]string{"openai", "deepseek", "modelscope", "siliconflow", "openrouter", "aliyun", "anthropic", "ollama", "custom"}, *input.Preset) {
		return input, nil, invalidProvider("preset")
	}
	if input.APIKey != nil && strings.ContainsAny(*input.APIKey, "\r\n\x00") {
		return input, nil, invalidProvider("api_key")
	}
	if input.TextModel != nil {
		model := strings.TrimSpace(*input.TextModel)
		input.TextModel = nil
		if model != "" {
			input.TextModel = &model
		}
	}
	base := strings.TrimRight(strings.TrimSpace(input.BaseURL), "/")
	var warnings []locales.Message
	if strings.HasSuffix(base, "/chat/completions") {
		base = strings.TrimRight(strings.TrimSuffix(base, "/chat/completions"), "/")
		warnings = append(warnings, locales.Message{Code: "ai.url_normalized", Args: map[string]any{}})
	}
	address, err := url.Parse(base)
	if err != nil || address.Hostname() == "" || (address.Scheme != "http" && address.Scheme != "https") || address.User != nil || address.RawQuery != "" || address.ForceQuery || strings.Contains(base, "#") || address.Opaque != "" {
		return input, nil, invalidProvider("base_url")
	}
	input.BaseURL = base
	return input, warnings, nil
}

func newProviderUUID() string {
	var data [16]byte
	rand.Read(data[:])
	data[6], data[8] = data[6]&0x0f|0x40, data[8]&0x3f|0x80
	value := hex.EncodeToString(data[:])
	return value[:8] + "-" + value[8:12] + "-" + value[12:16] + "-" + value[16:20] + "-" + value[20:]
}

func (s *Service) encryptProviderKey(input *string, uuid string, previous []byte) ([]byte, error) {
	if input == nil {
		return previous, nil
	}
	if *input == "" {
		return nil, nil
	}
	return s.cipher.Seal([]byte(*input), "ai_providers", "api_key_enc", uuid)
}

func (s *Service) CreateProvider(ctx context.Context, input ProviderInput) (ProviderSaveResult, error) {
	ctx, done, err := s.beginJob(ctx)
	if err != nil {
		return ProviderSaveResult{}, err
	}
	defer done()
	input, warnings, err := normalizeProvider(input)
	if err != nil {
		return ProviderSaveResult{}, err
	}
	uuid := newProviderUUID()
	key, err := s.encryptProviderKey(input.APIKey, uuid, nil)
	if err != nil {
		return ProviderSaveResult{}, storageFailure(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ProviderSaveResult{}, storageFailure(err)
	}
	defer tx.Rollback()
	_, clock, _, err := s.observeNowTx(ctx, tx)
	if err != nil {
		return ProviderSaveResult{}, storageFailure(err)
	}
	var last sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(sort_index) FROM ai_providers`).Scan(&last); err != nil {
		return ProviderSaveResult{}, storageFailure(err)
	}
	sortIndex := int64(0)
	if last.Valid {
		sortIndex = last.Int64 + 1
	}
	insert, err := tx.ExecContext(ctx, `INSERT INTO ai_providers(uuid,name,preset,base_url,api_key_enc,vision_model,text_model,enabled,sort_index) VALUES(?,?,?,?,?,?,?,?,?)`,
		uuid, input.Name, input.Preset, input.BaseURL, key, input.VisionModel, input.TextModel, input.Enabled, sortIndex)
	if err != nil {
		return ProviderSaveResult{}, storageFailure(err)
	}
	id, err := insert.LastInsertId()
	if err == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?)`, providerTokenKey(id), randomToken())
	}
	if err != nil {
		return ProviderSaveResult{}, storageFailure(err)
	}
	p, err := loadProviderTx(ctx, tx, id)
	if err != nil {
		return ProviderSaveResult{}, storageFailure(err)
	}
	view, err := s.providerView(p)
	if err != nil {
		return ProviderSaveResult{}, storageFailure(err)
	}
	if err := commitClock(ctx, tx, clock); err != nil {
		return ProviderSaveResult{}, storageFailure(err)
	}
	return ProviderSaveResult{Provider: view, Warnings: append([]locales.Message{}, warnings...)}, storageFailure(s.publishProviderChange(id))
}

func (s *Service) UpdateProvider(ctx context.Context, id int64, input ProviderInput) (ProviderSaveResult, error) {
	ctx, done, err := s.beginJob(ctx)
	if err != nil {
		return ProviderSaveResult{}, err
	}
	defer done()
	input, warnings, err := normalizeProvider(input)
	if err != nil {
		return ProviderSaveResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ProviderSaveResult{}, storageFailure(err)
	}
	defer tx.Rollback()
	p, err := loadProviderTx(ctx, tx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return ProviderSaveResult{}, missingProvider()
	}
	if err != nil {
		return ProviderSaveResult{}, storageFailure(err)
	}
	_, clock, now, err := s.observeNowTx(ctx, tx)
	if err != nil {
		return ProviderSaveResult{}, storageFailure(err)
	}
	key, err := s.encryptProviderKey(input.APIKey, p.uuid, p.key)
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE ai_providers SET name=?,preset=?,base_url=?,api_key_enc=?,vision_model=?,text_model=?,enabled=?,error_state=NULL,error_at=NULL WHERE id=?`,
			input.Name, input.Preset, input.BaseURL, key, input.VisionModel, input.TextModel, input.Enabled, id)
	}
	if err == nil && (p.view.BaseURL != input.BaseURL || p.view.VisionModel != input.VisionModel || !equalText(p.view.TextModel, input.TextModel)) {
		_, err = tx.ExecContext(ctx, `DELETE FROM ai_compat WHERE provider_id=?`, id)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE meta SET value=? WHERE key=?`, randomToken(), providerTokenKey(id))
	}
	var eventID int64
	if err == nil {
		eventID, err = resolveProviderErrorTx(ctx, tx, id, now)
	}
	if err != nil {
		return ProviderSaveResult{}, storageFailure(err)
	}
	p, err = loadProviderTx(ctx, tx, id)
	if err != nil {
		return ProviderSaveResult{}, storageFailure(err)
	}
	view, err := s.providerView(p)
	if err != nil {
		return ProviderSaveResult{}, storageFailure(err)
	}
	if err := commitClock(ctx, tx, clock); err != nil {
		return ProviderSaveResult{}, storageFailure(err)
	}
	err = s.publishProviderChange(id)
	if eventID > 0 {
		err = errors.Join(err, s.publishSystemChange(ctx, eventID, false))
	}
	return ProviderSaveResult{Provider: view, Warnings: append([]locales.Message{}, warnings...)}, storageFailure(err)
}

func (s *Service) SetProviderEnabled(ctx context.Context, id int64, enabled bool) (ProviderView, error) {
	ctx, done, err := s.beginJob(ctx)
	if err != nil {
		return ProviderView{}, err
	}
	defer done()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ProviderView{}, storageFailure(err)
	}
	defer tx.Rollback()
	if _, err := loadProviderTx(ctx, tx, id); errors.Is(err, sql.ErrNoRows) {
		return ProviderView{}, missingProvider()
	} else if err != nil {
		return ProviderView{}, storageFailure(err)
	}
	_, clock, now, err := s.observeNowTx(ctx, tx)
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE ai_providers SET enabled=?,error_state=NULL,error_at=NULL WHERE id=?`, enabled, id)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE meta SET value=? WHERE key=?`, randomToken(), providerTokenKey(id))
	}
	var eventID int64
	if err == nil {
		eventID, err = resolveProviderErrorTx(ctx, tx, id, now)
	}
	if err != nil {
		return ProviderView{}, storageFailure(err)
	}
	p, err := loadProviderTx(ctx, tx, id)
	if err != nil {
		return ProviderView{}, storageFailure(err)
	}
	view, err := s.providerView(p)
	if err != nil {
		return ProviderView{}, storageFailure(err)
	}
	if err := commitClock(ctx, tx, clock); err != nil {
		return ProviderView{}, storageFailure(err)
	}
	err = s.publishProviderChange(id)
	if eventID > 0 {
		err = errors.Join(err, s.publishSystemChange(ctx, eventID, false))
	}
	return view, storageFailure(err)
}

func (s *Service) ReorderProviders(ctx context.Context, ids []int64) ([]ProviderView, error) {
	ctx, done, err := s.beginJob(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	for _, id := range ids {
		if id < 1 || id > limits.MaxSafeInteger {
			return nil, invalidProvider("ids")
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, storageFailure(err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT `+providerColumns+providerJoin)
	if err != nil {
		return nil, storageFailure(err)
	}
	records := map[int64]providerRecord{}
	for rows.Next() {
		record, e := scanProvider(rows)
		if e != nil {
			err = e
			break
		}
		records[record.view.ID] = record
	}
	if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
		return nil, storageFailure(err)
	}
	if len(records) != len(ids) {
		return nil, invalidProvider("ids")
	}
	result := make([]ProviderView, 0, len(ids))
	for index, id := range ids {
		record, found := records[id]
		if !found {
			return nil, invalidProvider("ids")
		}
		delete(records, id)
		record.view.SortIndex = int64(index)
		view, err := s.providerView(record)
		if err != nil {
			return nil, storageFailure(err)
		}
		result = append(result, view)
	}
	_, clock, _, err := s.observeNowTx(ctx, tx)
	if err != nil {
		return nil, storageFailure(err)
	}
	for index, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE ai_providers SET sort_index=? WHERE id=?`, index, id); err != nil {
			return nil, storageFailure(err)
		}
	}
	if err := commitClock(ctx, tx, clock); err != nil {
		return nil, storageFailure(err)
	}
	return result, storageFailure(s.publishProviderChange(0))
}

func (s *Service) DeleteProvider(ctx context.Context, id int64) error {
	ctx, done, err := s.beginJob(ctx)
	if err != nil {
		return err
	}
	defer done()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storageFailure(err)
	}
	defer tx.Rollback()
	if _, err := loadProviderTx(ctx, tx, id); errors.Is(err, sql.ErrNoRows) {
		return missingProvider()
	} else if err != nil {
		return storageFailure(err)
	}
	_, clock, now, err := s.observeNowTx(ctx, tx)
	var eventID int64
	if err == nil {
		eventID, err = resolveProviderErrorTx(ctx, tx, id, now)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `DELETE FROM ai_providers WHERE id=?`, id)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `DELETE FROM meta WHERE key=?`, providerTokenKey(id))
	}
	if err != nil {
		return storageFailure(err)
	}
	if err := commitClock(ctx, tx, clock); err != nil {
		return storageFailure(err)
	}
	err = s.publishProviderChange(id)
	if eventID > 0 {
		err = errors.Join(err, s.publishSystemChange(ctx, eventID, false))
	}
	return storageFailure(err)
}

func equalText(a, b *string) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func commitClock(ctx context.Context, tx *sql.Tx, clock quota.ClockState) error {
	if err := appsettings.SaveClock(ctx, tx, clock); err != nil {
		return err
	}
	return tx.Commit()
}

func resolveProviderErrorTx(ctx context.Context, tx *sql.Tx, id int64, now time.Time) (int64, error) {
	var eventID int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM system_events WHERE kind='ai_error' AND subject_id=? AND resolved_at IS NULL`, id).Scan(&eventID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	_, err = (eventstore.Store{Now: func() time.Time { return now }}).ResolveTx(ctx, tx, "ai_error", id)
	return eventID, err
}
