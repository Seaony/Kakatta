package modelservice

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"math"
	"unicode/utf8"

	"github.com/Seaony/Kakatta/internal/analysis/client"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/security"
)

// ValidateStored 在存储层验证结构和外键后，只读检查全部模型数据。
func ValidateStored(ctx context.Context, db *sql.DB, cipher *security.Cipher) (err error) {
	defer func() {
		if err != nil {
			if canceled := ctx.Err(); canceled != nil && !errors.Is(err, canceled) {
				err = errors.Join(err, canceled)
			}
			err = storageFailure(err)
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	if db == nil || cipher == nil {
		return errors.New("模型恢复校验缺少数据库或密钥")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := validateStoredProviders(ctx, tx, cipher); err != nil {
		return err
	}
	if err := validateStoredCompat(ctx, tx); err != nil {
		return err
	}
	if err := validateStoredCalls(ctx, tx); err != nil {
		return err
	}
	if _, _, err := loadPromptViewsTx(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func validateStoredProviders(ctx context.Context, tx *sql.Tx, cipher *security.Cipher) error {
	rows, err := tx.QueryContext(ctx, `SELECT `+providerColumns+`,p.api_key_enc IS NOT NULL`+providerJoin+` ORDER BY p.id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var p providerRecord
		var state, token sql.NullString
		// 驱动可能把空 BLOB 也读成 nil，必须另外保留 SQL 空值标记。
		var hasKey bool
		if err := rows.Scan(&p.view.ID, &p.uuid, &p.view.Name, &p.view.Preset, &p.view.BaseURL, &p.key,
			&p.view.VisionModel, &p.view.TextModel, &p.view.Enabled, &p.view.SortIndex, &state, &p.view.ErrorAt, &token, &hasKey); err != nil {
			return err
		}
		// 启动时会给合法旧行补版本；恢复校验只检查已有版本，不补写。
		if p.view.ID < 1 || p.uuid == "" || token.Valid && !validToken(token.String) || hasKey && len(p.key) == 0 {
			return errors.New("模型配置版本或密文格式损坏")
		}
		input := ProviderInput{Name: p.view.Name, Preset: p.view.Preset, BaseURL: p.view.BaseURL, VisionModel: p.view.VisionModel, TextModel: p.view.TextModel}
		normalized, _, err := normalizeProvider(input)
		if err != nil || normalized.Name != input.Name || normalized.BaseURL != input.BaseURL || normalized.VisionModel != input.VisionModel || !equalText(normalized.TextModel, input.TextModel) {
			return errors.New("模型配置内容损坏")
		}
		if state.Valid {
			if err := validateStoredModelReason(state.String); err != nil {
				return err
			}
		}
		if hasKey {
			plain, err := cipher.Open(p.key, "ai_providers", "api_key_enc", p.uuid)
			if err != nil {
				return err
			}
			invalid := bytes.ContainsAny(plain, "\r\n\x00")
			clear(plain)
			if invalid {
				return errors.New("模型密钥格式损坏")
			}
		}
	}
	return rows.Err()
}

func validateStoredModelReason(raw string) error {
	if !utf8.ValidString(raw) {
		return errors.New("模型错误原因格式损坏")
	}
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	var reason locales.Message
	if decoder.Decode(&reason) != nil || reason.Code == "" || decoder.Decode(new(any)) != io.EOF {
		return errors.New("模型错误原因格式损坏")
	}
	return nil
}

func validateStoredCompat(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT provider_id,model,json_mode,send_temperature,max_tokens_param,max_tokens_floor,updated_at FROM ai_compat`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var providerID int64
		var model string
		var at sql.NullInt64
		var value client.Compat
		if err := rows.Scan(&providerID, &model, &value.JSONMode, &value.SendTemperature, &value.MaxTokensParam, &value.MaxTokensFloor, &at); err != nil {
			return err
		}
		if !validCompat(value) {
			return errors.New("模型兼容记录损坏")
		}
	}
	return rows.Err()
}

func validateStoredCalls(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT at,provider_id,model,purpose,task_id,ok,http_status,error_kind,json_mode,input_tokens,output_tokens,reasoning_tokens,finish_reason,duration_ms FROM ai_calls`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var at, providerID, taskID, ok, status, input, output, reasoning, duration sql.NullInt64
		var model, purpose, errorKind, jsonMode, finish sql.NullString
		if err := rows.Scan(&at, &providerID, &model, &purpose, &taskID, &ok, &status, &errorKind, &jsonMode, &input, &output, &reasoning, &finish, &duration); err != nil {
			return err
		}
		if !at.Valid || !purpose.Valid || !validUsagePurpose(purpose.String) || providerID.Valid && providerID.Int64 < 1 || taskID.Valid && taskID.Int64 < 1 ||
			ok.Valid && ok.Int64 != 0 && ok.Int64 != 1 || status.Valid && (status.Int64 < 100 || status.Int64 > 599) ||
			jsonMode.Valid && compatRank(client.JSONMode(jsonMode.String)) < 0 || input.Int64 < 0 || output.Int64 < 0 || reasoning.Int64 < 0 || duration.Int64 < 0 ||
			input.Int64 > math.MaxInt64-output.Int64 {
			return errors.New("模型调用历史格式损坏")
		}
	}
	return rows.Err()
}
