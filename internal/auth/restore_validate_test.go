package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/security"
	"github.com/Seaony/Kakatta/internal/store"
)

const restorePHC = "$argon2id$v=19$m=65536,t=3,p=4$AAECAwQFBgcICQoLDA0ODw$vUrOhcKV+zjYROFncankOSUoKsmCyGVr5Vou9s8g/Vg"

func restoreValidationDB(t *testing.T) (*sql.DB, *security.Cipher) {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "restore.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if db.DB().Stats().MaxOpenConnections != 1 {
		t.Fatal("测试必须使用单连接数据库")
	}
	key := security.NewMasterKey()
	cipher, err := security.NewCipher(key)
	clear(key)
	if err != nil {
		t.Fatal(err)
	}
	return db.DB(), cipher
}

func restoreValidationExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func restoreValidationAdmin(t *testing.T, db *sql.DB) {
	t.Helper()
	restoreValidationExec(t, db, `INSERT INTO admin(id,username,password_hash,created_at,password_changed_at) VALUES(1,?,?,?,?)`, "用户:旧账号", restorePHC, int64(-1000), int64(-2000))
}

func restoreValidationTx(t *testing.T, db *sql.DB) (context.Context, *sql.Tx) {
	t.Helper()
	restoreValidationExec(t, db, `PRAGMA query_only=ON`)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tx.Rollback() })
	return ctx, tx
}

func TestValidateStoredTxAllowsZeroAdminAndNeverInitializes(t *testing.T) {
	db, cipher := restoreValidationDB(t)
	for _, table := range []string{"admin", "auth_tokens", "meta"} {
		for _, action := range []string{"INSERT", "UPDATE", "DELETE"} {
			query := fmt.Sprintf(`CREATE TRIGGER refuse_%s_%s BEFORE %s ON %s BEGIN SELECT RAISE(ABORT,'禁止校验写入'); END`, table, action, action, table)
			restoreValidationExec(t, db, query)
		}
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO meta(key,value) VALUES('probe','1')`); err == nil || !strings.Contains(err.Error(), "禁止校验写入") {
		t.Fatal("禁止写入的触发器没有生效")
	}
	ctx, tx := restoreValidationTx(t, db)
	var before int64
	if err := tx.QueryRowContext(ctx, `SELECT total_changes()`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if hasAdmin, err := ValidateStoredTx(ctx, tx, cipher); err != nil || hasAdmin {
		t.Fatalf("零管理员的真实状态未保留：%v %v", hasAdmin, err)
	}
	var admins, tokens, generations, after int64
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM admin),(SELECT count(*) FROM auth_tokens),(SELECT count(*) FROM meta WHERE key='auth.generation'),total_changes()`).Scan(&admins, &tokens, &generations, &after); err != nil {
		t.Fatal(err)
	}
	if admins != 0 || tokens != 0 || generations != 0 || before != after {
		t.Fatal("纯校验写入了管理员、令牌或代数")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('probe','1')`); err == nil {
		t.Fatal("事务没有真正限制写入")
	}
	if err := tx.Commit(); err != nil {
		t.Fatal("校验器擅自结束了事务", err)
	}
}

func TestValidateStoredTxAcceptsAdminAndHistoricalSessions(t *testing.T) {
	db, cipher := restoreValidationDB(t)
	restoreValidationAdmin(t, db)
	generation, _ := security.NewToken()
	restoreValidationExec(t, db, `INSERT INTO meta(key,value) VALUES('auth.generation',?)`, generation)
	for i, kind := range []Kind{WebSession, APIToken} {
		_, hash := security.NewToken()
		restoreValidationExec(t, db, `INSERT INTO auth_tokens(kind,token_hash,name,created_at,last_used_at,expires_at,revoked_at) VALUES(?,?,?,NULL,NULL,0,1)`, kind, hash[:], fmt.Sprintf("历史%d", i))
	}
	ctx, tx := restoreValidationTx(t, db)
	if hasAdmin, err := ValidateStoredTx(ctx, tx, cipher); err != nil || !hasAdmin {
		t.Fatalf("合法管理员或历史会话被拒绝：%v %v", hasAdmin, err)
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM auth_tokens`).Scan(&count); err != nil || count != 2 {
		t.Fatal("校验器清理了历史会话")
	}
}

func TestValidateStoredTxRejectsDamagedPasswordHash(t *testing.T) {
	for i, phc := range []string{"", restorePHC + "=", restorePHC[:len(restorePHC)-1], strings.Replace(restorePHC, "m=65536", "m=4294967295", 1), strings.Replace(restorePHC, "p=4", "p=1", 1), strings.Replace(restorePHC, "v=19", "v=16", 1)} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			db, cipher := restoreValidationDB(t)
			restoreValidationAdmin(t, db)
			restoreValidationExec(t, db, `UPDATE admin SET password_hash=?`, phc)
			ctx, tx := restoreValidationTx(t, db)
			hasAdmin, err := ValidateStoredTx(ctx, tx, cipher)
			if hasAdmin || !errors.Is(err, ErrStorage) || !errors.Is(err, security.ErrPasswordHash) || err.Error() != ErrStorage.Error() {
				t.Fatalf("坏PHC未被安全拒绝：%v %v", hasAdmin, err)
			}
		})
	}
}

func TestValidateStoredTxGenerationIsOptionalButMustBeCanonical(t *testing.T) {
	valid, _ := security.NewToken()
	for _, generation := range []*string{nil, &valid, new(string), restoreString(valid + "="), restoreString("!" + valid[1:]), restoreString(strings.Repeat("a", 43))} {
		db, cipher := restoreValidationDB(t)
		restoreValidationAdmin(t, db)
		if generation != nil {
			restoreValidationExec(t, db, `INSERT INTO meta(key,value) VALUES('auth.generation',?)`, *generation)
		}
		ctx, tx := restoreValidationTx(t, db)
		hasAdmin, err := ValidateStoredTx(ctx, tx, cipher)
		wantValid := generation == nil || generation == &valid
		if wantValid && (err != nil || !hasAdmin) || !wantValid && (hasAdmin || !errors.Is(err, ErrStorage)) {
			t.Fatalf("代数格式校验错误：有效=%v %v", wantValid, err)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
	}
}

func restoreString(value string) *string { return &value }

func TestValidateStoredTxTOTPAssociationAndCiphertext(t *testing.T) {
	for _, name := range []string{"off", "on", "legacy_null_step", "legacy_minus_one", "future_step", "step_below_minimum", "missing_secret", "missing_enabled_at", "off_with_step", "empty_ciphertext", "wrong_aad", "wrong_key", "bad_secret", "damaged_tag", "missing_cipher"} {
		t.Run(name, func(t *testing.T) {
			db, cipher := restoreValidationDB(t)
			restoreValidationAdmin(t, db)
			secret := security.NewTOTPSecret()
			if name == "bad_secret" {
				secret = strings.ToLower("GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ")
			}
			rowKey := "1"
			if name == "wrong_aad" {
				rowKey = "2"
			}
			sealed, err := cipher.Seal([]byte(secret), "admin", "totp_secret_enc", rowKey)
			if err != nil {
				t.Fatal(err)
			}
			if name == "damaged_tag" {
				sealed[len(sealed)-1] ^= 1
			}
			if name != "off" {
				restoreValidationExec(t, db, `UPDATE admin SET totp_secret_enc=?,totp_enabled_at=?,totp_last_step=?`, sealed, int64(1<<63-1), int64(1<<63-1))
			}
			switch name {
			case "on":
				restoreValidationExec(t, db, `UPDATE admin SET totp_last_step=1`)
			case "legacy_null_step":
				restoreValidationExec(t, db, `UPDATE admin SET totp_last_step=NULL`)
			case "legacy_minus_one":
				restoreValidationExec(t, db, `UPDATE admin SET totp_last_step=-1`)
			case "step_below_minimum":
				restoreValidationExec(t, db, `UPDATE admin SET totp_last_step=-2`)
			case "missing_secret":
				restoreValidationExec(t, db, `UPDATE admin SET totp_secret_enc=NULL`)
			case "missing_enabled_at":
				restoreValidationExec(t, db, `UPDATE admin SET totp_enabled_at=NULL`)
			case "off_with_step":
				restoreValidationExec(t, db, `UPDATE admin SET totp_secret_enc=NULL,totp_enabled_at=NULL`)
			case "empty_ciphertext":
				restoreValidationExec(t, db, `UPDATE admin SET totp_secret_enc=?`, []byte{})
			case "wrong_key":
				cipher, err = security.NewCipher(security.NewMasterKey())
				if err != nil {
					t.Fatal(err)
				}
			case "missing_cipher":
				cipher = nil
			}
			ctx, tx := restoreValidationTx(t, db)
			hasAdmin, err := ValidateStoredTx(ctx, tx, cipher)
			wantValid := name == "off" || name == "on" || name == "legacy_null_step" || name == "legacy_minus_one" || name == "future_step"
			if wantValid && (err != nil || !hasAdmin) || !wantValid && (hasAdmin || !errors.Is(err, ErrStorage)) {
				t.Fatalf("TOTP校验错误：有效=%v %v", wantValid, err)
			}
			if err != nil && strings.Contains(err.Error(), secret) {
				t.Fatal("校验错误泄露了TOTP明文")
			}
		})
	}
}

func TestValidateStoredTxRejectsExtraAdminAndBlankUsername(t *testing.T) {
	for _, name := range []string{"blank", "wrong_id", "multiple"} {
		t.Run(name, func(t *testing.T) {
			db, cipher := restoreValidationDB(t)
			restoreValidationAdmin(t, db)
			if name == "blank" {
				restoreValidationExec(t, db, `UPDATE admin SET username='  '`)
			} else {
				restoreValidationExec(t, db, `PRAGMA ignore_check_constraints=ON`)
				if name == "wrong_id" {
					restoreValidationExec(t, db, `UPDATE admin SET id=2`)
				} else {
					restoreValidationExec(t, db, `INSERT INTO admin(id,username,password_hash,created_at,password_changed_at) VALUES(2,'other',?,0,0)`, restorePHC)
				}
			}
			ctx, tx := restoreValidationTx(t, db)
			if hasAdmin, err := ValidateStoredTx(ctx, tx, cipher); hasAdmin || !errors.Is(err, ErrStorage) {
				t.Fatalf("非法管理员记录被接受：%v %v", hasAdmin, err)
			}
		})
	}
}

func TestValidateStoredTxPropagatesTransactionAndContextErrors(t *testing.T) {
	db, cipher := restoreValidationDB(t)
	ctx, tx := restoreValidationTx(t, db)
	if hasAdmin, err := ValidateStoredTx(ctx, nil, cipher); hasAdmin || !errors.Is(err, ErrInvalidInput) {
		t.Fatal("空事务没有返回错误")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if hasAdmin, err := ValidateStoredTx(cancelled, tx, cipher); hasAdmin || !errors.Is(err, context.Canceled) {
		t.Fatalf("取消错误被吞：%v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if hasAdmin, err := ValidateStoredTx(ctx, tx, cipher); hasAdmin || !errors.Is(err, sql.ErrTxDone) {
		t.Fatalf("已结束事务的错误被吞：%v", err)
	}
}
