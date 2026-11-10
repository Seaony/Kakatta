package auth

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/Seaony/Kakatta/internal/security"
)

// ValidateStoredTx 只读认证数据，不初始化令牌、代数或会话。
func ValidateStoredTx(ctx context.Context, tx *sql.Tx, cipher *security.Cipher) (hasAdmin bool, err error) {
	if tx == nil {
		return false, ErrInvalidInput
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,username,password_hash,totp_secret_enc,
		totp_secret_enc IS NULL,totp_enabled_at,totp_last_step,created_at,password_changed_at FROM admin`)
	if err != nil {
		return false, stored(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, createdAt, passwordChangedAt int64
		var username, phc string
		var encrypted []byte
		var secretNull bool
		var enabledAt, lastStep sql.NullInt64
		if err := rows.Scan(&id, &username, &phc, &encrypted, &secretNull, &enabledAt, &lastStep, &createdAt, &passwordChangedAt); err != nil {
			return false, stored(err)
		}
		if hasAdmin || id != 1 || strings.TrimSpace(username) == "" {
			return false, ErrStorage
		}
		if err := security.ValidatePasswordHash(phc); err != nil {
			return false, stored(err)
		}
		if !enabledAt.Valid {
			if !secretNull || lastStep.Valid {
				return false, ErrStorage
			}
		} else {
			if secretNull || cipher == nil || lastStep.Valid && lastStep.Int64 < -1 {
				return false, ErrStorage
			}
			plaintext, err := cipher.Open(encrypted, "admin", "totp_secret_enc", "1")
			if err != nil {
				return false, stored(err)
			}
			err = security.ValidateTOTPSecret(string(plaintext))
			clear(plaintext)
			if err != nil {
				return false, stored(err)
			}
		}
		hasAdmin = true
	}
	if err := rows.Err(); err != nil {
		return false, stored(err)
	}
	if err := rows.Close(); err != nil {
		return false, stored(err)
	}
	var generation string
	err = tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='auth.generation'`).Scan(&generation)
	if errors.Is(err, sql.ErrNoRows) {
		// 正常启动会初始化缺失的代数，这里只报告原有数据。
		return hasAdmin, nil
	}
	if err != nil {
		return false, stored(err)
	}
	if !security.TokenMatches(generation, security.HashToken(generation)) {
		return false, ErrStorage
	}
	return hasAdmin, nil
}
