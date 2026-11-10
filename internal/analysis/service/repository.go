package modelservice

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis/client"
	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/eventstore"
	"github.com/Seaony/Kakatta/internal/locales"
)

var _ client.Repository = (*Service)(nil)

const providerColumns = `p.id,p.uuid,p.name,p.preset,p.base_url,p.api_key_enc,p.vision_model,p.text_model,p.enabled,p.sort_index,p.error_state,p.error_at,m.value`
const providerJoin = ` FROM ai_providers p LEFT JOIN meta m ON m.key='provider.config.'||p.id`

type providerRecord struct {
	view  ProviderView
	uuid  string
	key   []byte
	token string
}

func providerTokenKey(id int64) string { return "provider.config." + strconv.FormatInt(id, 10) }

func randomToken() string {
	var data [32]byte
	rand.Read(data[:])
	return hex.EncodeToString(data[:])
}

func validToken(token string) bool {
	data, err := hex.DecodeString(token)
	return err == nil && len(data) == 32
}

func initializeProviderTokens(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT p.id,m.value`+providerJoin+` ORDER BY p.id`)
	if err != nil {
		return err
	}
	var missing []int64
	for rows.Next() {
		var id int64
		var token sql.NullString
		if err = rows.Scan(&id, &token); err != nil {
			break
		}
		if token.Valid && !validToken(token.String) {
			err = errors.New("模型配置版本损坏")
			break
		}
		if !token.Valid {
			missing = append(missing, id)
		}
	}
	if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, id := range missing {
		if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?)`, providerTokenKey(id), randomToken()); err != nil {
			return err
		}
	}
	return nil
}

func scanProvider(row interface{ Scan(...any) error }) (providerRecord, error) {
	var p providerRecord
	var state, token sql.NullString
	err := row.Scan(&p.view.ID, &p.uuid, &p.view.Name, &p.view.Preset, &p.view.BaseURL, &p.key,
		&p.view.VisionModel, &p.view.TextModel, &p.view.Enabled, &p.view.SortIndex, &state, &p.view.ErrorAt, &token)
	if err != nil {
		return p, err
	}
	if !token.Valid || !validToken(token.String) || p.uuid == "" || p.view.ID < 1 {
		return p, errors.New("模型配置版本缺失或损坏")
	}
	input := ProviderInput{Name: p.view.Name, Preset: p.view.Preset, BaseURL: p.view.BaseURL, VisionModel: p.view.VisionModel, TextModel: p.view.TextModel}
	normalized, _, err := normalizeProvider(input)
	if err != nil || normalized.BaseURL != p.view.BaseURL || normalized.Name != p.view.Name || normalized.VisionModel != p.view.VisionModel || !equalText(normalized.TextModel, p.view.TextModel) || p.key != nil && len(p.key) == 0 {
		return p, errors.New("模型配置内容损坏")
	}
	p.token = token.String
	if state.Valid {
		var reason locales.Message
		decoder := json.NewDecoder(bytes.NewBufferString(state.String))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&reason); err != nil {
			return p, err
		}
		if decoder.Decode(new(any)) != io.EOF || reason.Code == "" {
			return p, errors.New("模型错误原因损坏")
		}
		p.view.ErrorState = &reason
	}
	return p, nil
}

func loadProviderTx(ctx context.Context, tx *sql.Tx, id int64) (providerRecord, error) {
	if id < 1 {
		return providerRecord{}, sql.ErrNoRows
	}
	return scanProvider(tx.QueryRowContext(ctx, `SELECT `+providerColumns+providerJoin+` WHERE p.id=?`, id))
}

func (s *Service) requestProvider(p providerRecord) (client.Provider, error) {
	result := client.Provider{Ref: client.ProviderRef{ID: p.view.ID, ConfigToken: p.token}, Name: p.view.Name,
		BaseURL: p.view.BaseURL, VisionModel: p.view.VisionModel, Enabled: p.view.Enabled, SortIndex: p.view.SortIndex, ErrorState: p.view.ErrorState}
	if p.view.TextModel != nil {
		result.TextModel = *p.view.TextModel
	}
	if len(p.key) > 0 {
		plain, err := s.cipher.Open(p.key, "ai_providers", "api_key_enc", p.uuid)
		if err != nil {
			return client.Provider{}, err
		}
		result.APIKey = string(plain)
	}
	return result, nil
}

func (s *Service) providerView(p providerRecord) (ProviderView, error) {
	value, err := s.requestProvider(p)
	if err != nil {
		return ProviderView{}, err
	}
	p.view.APIKeySet = value.APIKey != ""
	return p.view, nil
}

func (s *Service) Providers(ctx context.Context) ([]client.Provider, error) {
	ctx, done, err := s.beginJob(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	records, err := s.readProviders(ctx)
	if err != nil {
		return nil, storageFailure(err)
	}
	result := make([]client.Provider, 0, len(records))
	for _, record := range records {
		provider, err := s.requestProvider(record)
		if err != nil {
			return nil, storageFailure(err)
		}
		result = append(result, provider)
	}
	return result, nil
}

func (s *Service) Provider(ctx context.Context, id int64) (client.Provider, error) {
	ctx, done, err := s.beginJob(ctx)
	if err != nil {
		return client.Provider{}, err
	}
	defer done()
	record, err := scanProvider(s.db.QueryRowContext(ctx, `SELECT `+providerColumns+providerJoin+` WHERE p.id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return client.Provider{}, client.ErrConfigChanged
	}
	if err != nil {
		return client.Provider{}, storageFailure(err)
	}
	value, err := s.requestProvider(record)
	return value, storageFailure(err)
}

func (s *Service) readProviders(ctx context.Context) ([]providerRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+providerColumns+providerJoin+` ORDER BY p.sort_index,p.id`)
	if err != nil {
		return nil, err
	}
	result := []providerRecord{}
	for rows.Next() {
		record, e := scanProvider(rows)
		if e != nil {
			err = e
			break
		}
		result = append(result, record)
	}
	return result, errors.Join(err, rows.Err(), rows.Close())
}

func matchProviderTx(ctx context.Context, tx *sql.Tx, ref client.ProviderRef) (providerRecord, error) {
	p, err := loadProviderTx(ctx, tx, ref.ID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (ref.ConfigToken == "" || p.token != ref.ConfigToken) {
		return providerRecord{}, client.ErrConfigChanged
	}
	return p, err
}

func (s *Service) ChangeProvider(ctx context.Context, ref client.ProviderRef, change client.ProviderChange) error {
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
	p, err := matchProviderTx(ctx, tx, ref)
	if err != nil {
		if err == client.ErrConfigChanged {
			return err
		}
		return storageFailure(err)
	}
	_, clock, now, err := s.observeNowTx(ctx, tx)
	if err != nil {
		return storageFailure(err)
	}
	events := eventstore.Store{Now: func() time.Time { return now }}
	var eventID int64
	var raised, eventChanged, providerChanged bool
	switch change.Kind {
	case "blocked":
		if change.Reason == nil || !blockingReason(change.Reason.Code) {
			return invalidProvider("error_state")
		}
		raw, e := json.Marshal(change.Reason)
		if e != nil {
			return storageFailure(e)
		}
		message := locales.Message{Code: "ai.provider_error", Args: map[string]any{"provider": p.view.Name, "reason": *change.Reason}}
		if _, err = tx.ExecContext(ctx, `UPDATE ai_providers SET error_state=?,error_at=? WHERE id=?`, string(raw), now.UnixMilli(), p.view.ID); err == nil {
			raised, err = events.RaiseTx(ctx, tx, "ai_error", p.view.ID, message)
		}
		if err == nil {
			err = tx.QueryRowContext(ctx, `SELECT id FROM system_events WHERE kind='ai_error' AND subject_id=? AND resolved_at IS NULL`, p.view.ID).Scan(&eventID)
		}
		if err == nil && !raised {
			raw, err = json.Marshal(message)
			if err == nil {
				_, err = tx.ExecContext(ctx, `UPDATE system_events SET message=? WHERE id=?`, string(raw), eventID)
			}
		}
		providerChanged, eventChanged = true, true
	case "request_success", "test_success", "probe_success":
		if change.Kind != "request_success" {
			_, err = tx.ExecContext(ctx, `UPDATE ai_providers SET error_state=NULL,error_at=NULL WHERE id=?`, p.view.ID)
			providerChanged = p.view.ErrorState != nil || p.view.ErrorAt != nil
		}
		if err == nil {
			eventID, err = resolveProviderErrorTx(ctx, tx, p.view.ID, now)
			eventChanged = eventID > 0
		}
	default:
		return invalidProvider("change")
	}
	if err == nil {
		err = appsettings.SaveClock(ctx, tx, clock)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return storageFailure(err)
	}
	if providerChanged {
		err = s.publishProviderChange(p.view.ID)
	}
	if eventChanged {
		err = errors.Join(err, s.publishSystemChange(ctx, eventID, raised))
	}
	return storageFailure(err)
}

func blockingReason(code string) bool {
	switch code {
	case "analysis.client.auth", "analysis.client.balance", "analysis.client.daily_quota", "analysis.client.not_found", "analysis.client.unsupported_image", "analysis.client.thinking_too_long":
		return true
	}
	return false
}
