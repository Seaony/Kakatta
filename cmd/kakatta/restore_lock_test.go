//go:build linux || darwin

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/auth"
	"github.com/Seaony/Kakatta/internal/config"
	"github.com/Seaony/Kakatta/internal/security"
	"github.com/Seaony/Kakatta/internal/store"
)

const restoreLockPHC = "$argon2id$v=19$m=65536,t=3,p=4$AAECAwQFBgcICQoLDA0ODw$vUrOhcKV+zjYROFncankOSUoKsmCyGVr5Vou9s8g/Vg"

func maintenanceRestoreDB(t *testing.T) (string, *sql.DB) {
	t.Helper()
	data := t.TempDir()
	db, err := store.Open(t.Context(), filepath.Join(data, "kakatta.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	key := security.NewMasterKey()
	cipher, err := security.NewCipher(key)
	clear(key)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := cipher.Seal([]byte(security.NewTOTPSecret()), "admin", "totp_secret_enc", "1")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	if _, err := db.DB().ExecContext(t.Context(), `INSERT INTO admin(id,username,password_hash,totp_secret_enc,totp_enabled_at,totp_last_step,created_at,password_changed_at) VALUES(1,'用户',?,?,?,1,?,?)`, restoreLockPHC, sealed, now, now, now); err != nil {
		t.Fatal(err)
	}
	generation, _ := security.NewToken()
	if _, err := db.DB().ExecContext(t.Context(), `INSERT INTO meta(key,value) VALUES('auth.generation',?)`, generation); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []auth.Kind{auth.WebSession, auth.APIToken} {
		_, hash := security.NewToken()
		var expires any
		if kind == auth.WebSession {
			expires = now + (30 * 24 * time.Hour).Milliseconds()
		}
		if _, err := db.DB().ExecContext(t.Context(), `INSERT INTO auth_tokens(kind,token_hash,name,created_at,last_used_at,expires_at) VALUES(?,?,'本地测试',?,?,?)`, kind, hash[:], now, now, expires); err != nil {
			t.Fatal(err)
		}
	}
	return data, db.DB()
}

func maintenanceRestoreState(t *testing.T, db *sql.DB) string {
	t.Helper()
	var saved [][][]any
	for _, query := range []string{
		`SELECT * FROM admin ORDER BY id`,
		`SELECT * FROM auth_tokens ORDER BY id`,
		`SELECT * FROM meta ORDER BY key`,
	} {
		rows, err := db.QueryContext(t.Context(), query)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		var table [][]any
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			table = append(table, values)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		saved = append(saved, table)
	}
	raw, err := json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func maintenanceRestoreCLI(t *testing.T, command, data string) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRestoreMaintenanceCLIHelper$")
	cmd.Env = append(os.Environ(), "KAKATTA_RESTORE_CLI_TEST=1", "KAKATTA_RESTORE_CLI_COMMAND="+command, "KAKATTA_RESTORE_CLI_DATA="+data)
	cmd.Stdin = strings.NewReader("new-password\n")
	return cmd.CombinedOutput()
}

func TestRestoreMaintenanceCLIHelper(t *testing.T) {
	if os.Getenv("KAKATTA_RESTORE_CLI_TEST") != "1" {
		return
	}
	command := os.Getenv("KAKATTA_RESTORE_CLI_COMMAND")
	os.Args = []string{"kakatta", command, "--data", os.Getenv("KAKATTA_RESTORE_CLI_DATA")}
	if command == "reset-password" {
		os.Args = append(os.Args, "--disable-totp")
	}
	main()
}

func TestRestoreExclusiveLockBlocksMaintenanceCLIWithoutDBChanges(t *testing.T) {
	for _, command := range []string{"reset-password", "disable-totp"} {
		t.Run(command, func(t *testing.T) {
			data, db := maintenanceRestoreDB(t)
			before := maintenanceRestoreState(t, db)
			lock, err := config.AcquireRestoreLock(data, true)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Close()
			output, err := maintenanceRestoreCLI(t, command, data)
			if err == nil || !strings.Contains(string(output), "正在维护或恢复") {
				t.Fatalf("CLI没有被排他锁拦住：%s %v", output, err)
			}
			if maintenanceRestoreState(t, db) != before {
				t.Fatal("被恢复锁拦住的CLI改动了认证数据")
			}
		})
	}
}

func TestRestoreJournalBlocksMaintenanceCLIWithoutDBChanges(t *testing.T) {
	for _, kind := range []string{"pending", "committed", "malformed", "directory", "dangling"} {
		t.Run(kind, func(t *testing.T) {
			data, db := maintenanceRestoreDB(t)
			before := maintenanceRestoreState(t, db)
			path := filepath.Join(data, config.RestoreStateName)
			var err error
			switch kind {
			case "directory":
				err = os.Mkdir(path, 0700)
			case "dangling":
				err = os.Symlink(filepath.Join(data, "missing"), path)
			default:
				err = os.WriteFile(path, []byte(kind), 0000)
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, command := range []string{"reset-password", "disable-totp"} {
				output, err := maintenanceRestoreCLI(t, command, data)
				if err == nil || !strings.Contains(string(output), "恢复尚未完成") {
					t.Fatalf("CLI自行解释或忽略恢复日志：%s %v", output, err)
				}
				if maintenanceRestoreState(t, db) != before {
					t.Fatal("恢复日志存在时CLI改动了认证数据")
				}
			}
		})
	}
}

func TestRestoreGuardsRunBeforeMaintenanceDatabaseInspection(t *testing.T) {
	for _, mode := range []string{"exclusive", "journal"} {
		t.Run(mode, func(t *testing.T) {
			data := t.TempDir()
			want := "恢复尚未完成"
			if mode == "exclusive" {
				lock, err := config.AcquireRestoreLock(data, true)
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
				want = "正在维护或恢复"
			} else if err := os.WriteFile(filepath.Join(data, config.RestoreStateName), nil, 0600); err != nil {
				t.Fatal(err)
			}
			output, err := maintenanceRestoreCLI(t, "disable-totp", data)
			if err == nil || !strings.Contains(string(output), want) {
				t.Fatalf("维护命令先检查了数据库：%s %v", output, err)
			}
			if _, err := os.Lstat(filepath.Join(data, "kakatta.db")); !os.IsNotExist(err) {
				t.Fatal("被拦CLI创建了数据库", err)
			}
		})
	}
}

func TestRestoreSharedLockKeepsMaintenanceCLIUsableWithServerLock(t *testing.T) {
	for _, command := range []string{"reset-password", "disable-totp"} {
		t.Run(command, func(t *testing.T) {
			data, db := maintenanceRestoreDB(t)
			server, err := config.AcquireServerLock(data)
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			shared, err := config.AcquireRestoreLock(data, false)
			if err != nil {
				t.Fatal(err)
			}
			defer shared.Close()
			output, err := maintenanceRestoreCLI(t, command, data)
			if err != nil || !strings.Contains(string(output), "已") || strings.Contains(string(output), "new-password") {
				t.Fatalf("服务持锁时CLI不可用或泄露密码：%s %v", output, err)
			}
			var phc string
			var secret []byte
			var enabledAt, step sql.NullInt64
			if err := db.QueryRowContext(t.Context(), `SELECT password_hash,totp_secret_enc,totp_enabled_at,totp_last_step FROM admin`).Scan(&phc, &secret, &enabledAt, &step); err != nil {
				t.Fatal(err)
			}
			if secret != nil || enabledAt.Valid || step.Valid {
				t.Fatal("维护命令未清空两步验证")
			}
			if command == "reset-password" {
				if ok, err := security.VerifyPassword(t.Context(), "new-password", phc); err != nil || !ok {
					t.Fatal("密码没有正确重置", err)
				}
				var active int
				if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM auth_tokens WHERE revoked_at IS NULL`).Scan(&active); err != nil || active != 0 {
					t.Fatal("重置没有吊销全部令牌", err)
				}
			} else if phc != restoreLockPHC {
				t.Fatal("关闭两步验证改动了密码")
			}
		})
	}
}

type restoreMaintenanceReader func([]byte) (int, error)

func (f restoreMaintenanceReader) Read(p []byte) (int, error) { return f(p) }

func TestRestoreSharedLockHeldUntilMaintenanceCompletes(t *testing.T) {
	data, _ := maintenanceRestoreDB(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var started, finished sync.Once
	unblock := func() { finished.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	input := strings.NewReader("new-password\n")
	reader := restoreMaintenanceReader(func(p []byte) (int, error) {
		started.Do(func() { close(entered) })
		<-release
		return input.Read(p)
	})
	done := make(chan error, 1)
	var output bytes.Buffer
	go func() { done <- run(t.Context(), []string{"reset-password", "--data", data}, reader, &output, nil) }()
	select {
	case <-entered:
	case err := <-done:
		t.Fatal("维护命令尚未读取密码就退出", err)
	case <-time.After(5 * time.Second):
		t.Fatal("维护命令没有进入密码读取")
	}
	if lock, err := config.AcquireRestoreLock(data, true); lock != nil || err == nil {
		if lock != nil {
			lock.Close()
		}
		t.Fatal("维护命令过早释放共享锁")
	}
	unblock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("维护命令未结束")
	}
	lock, err := config.AcquireRestoreLock(data, true)
	if err != nil {
		t.Fatal("维护结束后未释放共享锁", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
}
