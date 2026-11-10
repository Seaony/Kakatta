package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/auth"
	"github.com/Seaony/Kakatta/internal/backup"
	"github.com/Seaony/Kakatta/internal/quota"
	"github.com/Seaony/Kakatta/internal/security"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/store"
)

const runtimeClockQuery = `SELECT value,
	(SELECT value FROM meta WHERE key='quota.clock_initialized') FROM meta WHERE key=?`

func readRuntimeClock(ctx context.Context, db *sql.DB) (quota.ClockState, error) {
	if db == nil {
		return quota.ClockState{}, errors.New("恢复时钟缺少数据库")
	}
	return readRuntimeClockRow(db.QueryRowContext(ctx, runtimeClockQuery, appsettings.ClockKey))
}

func readRuntimeClockRow(row *sql.Row) (quota.ClockState, error) {
	var raw string
	var marker sql.NullString
	if err := row.Scan(&raw, &marker); err != nil {
		return quota.ClockState{}, fmt.Errorf("读取恢复时钟：%w", err)
	}
	if !marker.Valid || marker.String != "1" {
		return quota.ClockState{}, errors.New("恢复时钟初始化标记损坏")
	}
	var clock quota.ClockState
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&clock); err != nil {
		return quota.ClockState{}, errors.New("恢复时钟记录损坏")
	}
	if decoder.Decode(new(any)) != io.EOF || clock.Validate() != nil {
		return quota.ClockState{}, errors.New("恢复时钟记录损坏")
	}
	return clock, nil
}

// 这里只修改已验证的私有候选，提交后的文件安装由外层负责。
func prepareRuntimeCandidate(ctx context.Context, path string, backupKey, targetKey [backup.MasterKeyBytes]byte, hostClock quota.ClockState, env settings.Environment, now time.Time) (err error) {
	defer clear(backupKey[:])
	defer clear(targetKey[:])
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := hostClock.Validate(); err != nil {
		return err
	}
	if now.IsZero() || now.UnixMilli() <= 0 {
		return errors.New("恢复时间无效")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("恢复候选必须是普通文件")
	}
	private, err := store.Open(ctx, path)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := private.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	tx, err := private.DB().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	oldCipher, err := security.NewCipher(backupKey[:])
	if err != nil {
		return err
	}
	newCipher, err := security.NewCipher(targetKey[:])
	if err != nil {
		return err
	}
	hasAdmin, err := auth.ValidateStoredTx(ctx, tx, oldCipher)
	if err != nil {
		return err
	}
	if !hasAdmin {
		return errors.New("恢复候选缺少管理员")
	}
	snapshot, err := appsettings.ValidateStoredTx(ctx, tx, env)
	if err != nil {
		return err
	}
	var timezone string
	if err := json.Unmarshal(snapshot.Values["general.timezone"], &timezone); err != nil {
		return err
	}
	backupClock, err := readRuntimeClockRow(tx.QueryRowContext(ctx, runtimeClockQuery, appsettings.ClockKey))
	if err != nil {
		return err
	}
	clock, err := mergeRuntimeClocks(hostClock, backupClock, now, timezone)
	if err != nil {
		return err
	}
	if err := rekeyRuntimeSecrets(ctx, tx, oldCipher, newCipher, backupKey == targetKey); err != nil {
		return err
	}
	if err := appsettings.SaveClock(ctx, tx, clock); err != nil {
		return err
	}
	if err := service.SignOutRestoredAccountsTx(ctx, tx, clock.HighestUTC); err != nil {
		return err
	}
	for _, statement := range []string{
		`DELETE FROM auth_tokens`,
		`DELETE FROM quota_events`,
		`DELETE FROM meta WHERE key='auth.generation' OR key GLOB 'runtime.session.*'`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	var busy, pages, copied int
	if err := private.DB().QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &pages, &copied); err != nil {
		return err
	}
	if busy != 0 || pages != copied {
		return errors.New("恢复候选检查点没有完成")
	}
	return nil
}

func mergeRuntimeClocks(host, restored quota.ClockState, now time.Time, timezone string) (quota.ClockState, error) {
	effective := time.UnixMilli(max(host.HighestUTC, restored.HighestUTC, now.UnixMilli())).UTC()
	host, _, err := quota.AdvanceClock(host, effective, timezone)
	if err != nil {
		return quota.ClockState{}, err
	}
	restored, _, err = quota.AdvanceClock(restored, effective, timezone)
	if err != nil {
		return quota.ClockState{}, err
	}
	starts := make(map[int64]int64)
	for _, windows := range [][]quota.DayWindow{host.Windows, restored.Windows} {
		for _, window := range windows {
			if start, exists := starts[window.End]; !exists || window.Start < start {
				starts[window.End] = window.Start
			}
		}
	}
	restored.Windows = make([]quota.DayWindow, 0, len(starts))
	for end, start := range starts {
		restored.Windows = append(restored.Windows, quota.DayWindow{Start: start, End: end})
	}
	sort.Slice(restored.Windows, func(i, j int) bool { return restored.Windows[i].End < restored.Windows[j].End })
	return restored, restored.Validate()
}

func rekeyRuntimeSecrets(ctx context.Context, tx *sql.Tx, oldCipher, newCipher *security.Cipher, sameKey bool) error {
	for _, field := range []struct{ table, column, rowKey string }{
		{"admin", "totp_secret_enc", "CAST(id AS TEXT)"},
		{"proxies", "url_enc", "uuid"},
		{"ai_providers", "api_key_enc", "uuid"},
		{"channels", "secret_enc", "uuid"},
	} {
		var last int64
		first := true
		for {
			query := `SELECT id,` + field.rowKey + `,` + field.column + `,` + field.column + ` IS NULL FROM ` + field.table
			var args []any
			if !first {
				query += ` WHERE id>?`
				args = []any{last}
			}
			var id int64
			var rowKey string
			var ciphertext []byte
			var isNull bool
			err := tx.QueryRowContext(ctx, query+` ORDER BY id LIMIT 1`, args...).Scan(&id, &rowKey, &ciphertext, &isNull)
			if errors.Is(err, sql.ErrNoRows) {
				break
			}
			if err != nil {
				return err
			}
			first, last = false, id
			if isNull {
				continue
			}
			plaintext, err := oldCipher.Open(ciphertext, field.table, field.column, rowKey)
			if err != nil {
				return err
			}
			if sameKey {
				clear(plaintext)
				continue
			}
			ciphertext, err = newCipher.Seal(plaintext, field.table, field.column, rowKey)
			clear(plaintext)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE `+field.table+` SET `+field.column+`=? WHERE id=?`, ciphertext, id); err != nil {
				return err
			}
		}
	}
	return nil
}
