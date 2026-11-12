package auth

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLegacyFirstRunFileRemovedWithoutChangingAccounts(t *testing.T) {
	for _, initialized := range []bool{false, true} {
		t.Run(map[bool]string{false: "新实例", true: "已有管理员"}[initialized], func(t *testing.T) {
			f := newFixture(t, initialized)
			var current Session
			var version [32]byte
			if initialized {
				first := f.session(t)
				f.enable(t, first.Identity)
				current = f.session(t)
				admin, err := f.s.admin(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				version = admin.version()
			}
			count := f.count(t, "auth_tokens")
			data := t.TempDir()
			path := filepath.Join(data, "初始化令牌.txt")
			if err := os.WriteFile(path, []byte("obsolete-local-value"), 0600); err != nil {
				t.Fatal(err)
			}
			reopened, err := New(t.Context(), Dependencies{DB: f.db, Cipher: f.cipher, Now: f.clock.now, DataDir: data})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatal("旧文件未清理", err)
			}
			if f.count(t, "auth_tokens") != count {
				t.Fatal("清理旧文件修改了原会话")
			}
			status, err := reopened.Setup(t.Context())
			if err != nil || status.Initialized != initialized {
				t.Fatal("清理旧文件改变了初始化状态", err)
			}
			if initialized {
				after, err := reopened.admin(t.Context())
				if err != nil || after.version() != version {
					t.Fatal("清理旧文件修改了账号或两步验证", err)
				}
				if _, err := reopened.Authenticate(t.Context(), current.Token, WebSession); err != nil {
					t.Fatal(err)
				}
			} else if _, err := reopened.Initialize(t.Context(), "owner", "password", "browser"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLegacyFileCleanupDoesNotFollowLink(t *testing.T) {
	f := newFixture(t, false)
	data := t.TempDir()
	victim := filepath.Join(t.TempDir(), "keep")
	if err := os.WriteFile(victim, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(data, "初始化令牌.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := New(t.Context(), Dependencies{DB: f.db, Cipher: f.cipher, DataDir: data}); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(victim); err != nil || string(raw) != "keep" {
		t.Fatal("清理旧文件损坏了链接目标", err)
	}
}
