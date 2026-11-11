package auth

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func tokenFileService(t *testing.T, f *fixture, data string) (*Service, Bootstrap, error) {
	t.Helper()
	return New(t.Context(), Dependencies{DB: f.db, Cipher: f.cipher, Now: f.clock.now, DataDir: data})
}

func readInitialTokenFile(t *testing.T, data string) string {
	t.Helper()
	file := filepath.Join(data, "初始化令牌.txt")
	info, err := os.Lstat(file)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		t.Fatal("初始化令牌文件必须是权限0600的普通文件", err)
	}
	text, err := os.ReadFile(file)
	if err != nil || len(text) != 44 || text[len(text)-1] != '\n' {
		t.Fatal("初始化令牌文件缺失或格式错误", err)
	}
	return strings.TrimSpace(string(text))
}

func TestInitialTokenFileNormalInitialization(t *testing.T) {
	f := newFixture(t, false)
	data := t.TempDir()
	s, boot, err := tokenFileService(t, f, data)
	if err != nil {
		t.Fatal(err)
	}
	token := readInitialTokenFile(t, data)
	if token != boot.Token {
		t.Fatal("文件与本次日志使用的初始化令牌不一致")
	}
	_, err = s.Initialize(t.Context(), "wrong", "owner", "password", "browser")
	requireError(t, err, ErrInvalidCredentials)
	if readInitialTokenFile(t, data) != token {
		t.Fatal("错误凭据删除了有效初始化令牌")
	}
	f.exec(t, "CREATE TRIGGER fail_file_session BEFORE INSERT ON auth_tokens BEGIN SELECT RAISE(ABORT, 'test failure'); END")
	_, err = s.Initialize(t.Context(), token, "owner", "password", "browser")
	requireError(t, err, ErrStorage)
	if f.count(t, "admin") != 0 || readInitialTokenFile(t, data) != token {
		t.Fatal("初始化事务失败不能留下管理员或删除令牌文件")
	}
	f.exec(t, "DROP TRIGGER fail_file_session")
	session, err := s.Initialize(t.Context(), token, "owner", "password", "browser")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(data, "初始化令牌.txt")); !os.IsNotExist(err) {
		t.Fatal("创建管理员成功后没有删除初始化令牌文件", err)
	}
	reopened, next, err := tokenFileService(t, f, data)
	if err != nil || next.Token != "" || next.ExpiresAt != nil {
		t.Fatal("已有管理员时不能重发初始化凭据", err)
	}
	if _, err := reopened.Authenticate(t.Context(), session.Token, WebSession); err != nil {
		t.Fatal("重启后旧登录会话失效", err)
	}
}

func TestInitialTokenFileRestartAtomicallyReplacesOldFile(t *testing.T) {
	f := newFixture(t, false)
	data := t.TempDir()
	_, first, err := tokenFileService(t, f, data)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(data, "初始化令牌.txt")
	oldInfo, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(data, "old-token-copy")
	if err := os.Link(file, alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(file, 0644); err != nil {
		t.Fatal(err)
	}
	s, next, err := tokenFileService(t, f, data)
	if err != nil {
		t.Fatal(err)
	}
	if next.Token == first.Token || readInitialTokenFile(t, data) != next.Token {
		t.Fatal("未初始化时重启没有写入本次独立令牌")
	}
	newInfo, err := os.Stat(file)
	previous, readErr := os.ReadFile(alias)
	if err != nil || readErr != nil || os.SameFile(oldInfo, newInfo) || string(previous) != first.Token+"\n" {
		t.Fatal("令牌文件没有原子替换，或跟随硬链接改动了旧文件", err, readErr)
	}
	_, err = s.Initialize(t.Context(), first.Token, "owner", "password", "browser")
	requireError(t, err, ErrInvalidCredentials)
	entries, err := os.ReadDir(data)
	if err != nil || len(entries) != 2 {
		t.Fatal("原子写入留下了临时文件", err)
	}
}

func TestInitialTokenFileExistingAdminCleanupPreservesCredentials(t *testing.T) {
	f := newFixture(t, true)
	session := f.session(t)
	f.enable(t, session.Identity)
	before, err := f.s.admin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	count := f.count(t, "auth_tokens")
	data := t.TempDir()
	if err := os.WriteFile(filepath.Join(data, "初始化令牌.txt"), []byte("stale-synthetic-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, boot, err := tokenFileService(t, f, data)
	if err != nil || boot.Token != "" {
		t.Fatal("已有管理员时清理遗留文件失败", err)
	}
	if _, err := os.Lstat(filepath.Join(data, "初始化令牌.txt")); !os.IsNotExist(err) {
		t.Fatal("重启没有删除遗留初始化令牌", err)
	}
	after, err := s.admin(t.Context())
	if err != nil || before.version() != after.version() || f.count(t, "auth_tokens") != count {
		t.Fatal("清理初始化文件修改了密码、两步验证或会话", err)
	}
	if _, err := s.Authenticate(t.Context(), session.Token, WebSession); err != nil {
		t.Fatal("清理遗留文件使旧会话失效", err)
	}
	login, err := s.PasswordLogin(t.Context(), "owner", "old password", "browser")
	if err != nil || login.Challenge == "" || login.Session != nil {
		t.Fatal("旧密码和两步验证行为改变", err)
	}
}

func TestInitialTokenFileRejectsLinksAndDirectories(t *testing.T) {
	for _, initialized := range []bool{false, true} {
		for _, kind := range []string{"link", "directory"} {
			t.Run(kind+map[bool]string{false: "/new", true: "/initialized"}[initialized], func(t *testing.T) {
				f := newFixture(t, initialized)
				data := t.TempDir()
				file := filepath.Join(data, "初始化令牌.txt")
				victim := filepath.Join(t.TempDir(), "private-location-sentinel")
				if err := os.WriteFile(victim, []byte("private-content-sentinel"), 0600); err != nil {
					t.Fatal(err)
				}
				if kind == "link" {
					if err := os.Symlink(victim, file); err != nil {
						t.Fatal(err)
					}
				} else if err := os.Mkdir(file, 0700); err != nil {
					t.Fatal(err)
				}
				_, _, err := tokenFileService(t, f, data)
				if !errors.Is(err, ErrStorage) || !strings.Contains(err.Error(), "初始化令牌文件") || strings.Contains(err.Error(), data) || strings.Contains(err.Error(), "private-") {
					t.Fatal("不安全文件必须分类报错，且不带路径或原始内容", err)
				}
				text, err := os.ReadFile(victim)
				if err != nil || string(text) != "private-content-sentinel" {
					t.Fatal("处理初始化文件跟随了符号链接", err)
				}
			})
		}
	}
}

func TestInitialTokenFileCleanupFailureDoesNotFailCommittedInitialization(t *testing.T) {
	f := newFixture(t, false)
	data := t.TempDir()
	s, boot, err := tokenFileService(t, f, data)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(data, "初始化令牌.txt")
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(file, 0700); err != nil {
		t.Fatal(err)
	}
	session, err := s.Initialize(t.Context(), boot.Token, "owner", "password", "browser")
	if err != nil || session.Token == "" || f.count(t, "admin") != 1 {
		t.Fatal("删除失败不能把已提交的初始化显示成失败", err)
	}
	_, err = s.Initialize(t.Context(), boot.Token, "other", "password", "browser")
	requireError(t, err, ErrAlreadyInitialized)
	_, _, err = tokenFileService(t, f, data)
	if !errors.Is(err, ErrStorage) {
		t.Fatal("重启未报告遗留初始化文件的清理故障", err)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("stale-synthetic-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	reopened, _, err := tokenFileService(t, f, data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Authenticate(t.Context(), session.Token, WebSession); err != nil {
		t.Fatal("清障重启没有保留首次登录会话", err)
	}
}

func TestInitialTokenFileWritePermissionFailureStopsStartup(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("需要普通用户的目录权限检查")
	}
	f := newFixture(t, false)
	data := t.TempDir()
	if err := os.Chmod(data, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(data, 0700) })
	_, _, err := tokenFileService(t, f, data)
	if !errors.Is(err, ErrStorage) || !strings.Contains(err.Error(), "初始化令牌文件") {
		t.Fatal("令牌文件无法安全写入时不能假装启动成功", err)
	}
}

func TestInitialTokenFileRejectsLinkedDataDirectory(t *testing.T) {
	f := newFixture(t, false)
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "linked-data")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	_, _, err := tokenFileService(t, f, link)
	if !errors.Is(err, ErrStorage) {
		t.Fatal("初始化令牌不能跟随数据目录链接写入", err)
	}
	entries, err := os.ReadDir(target)
	if err != nil || len(entries) != 0 {
		t.Fatal("初始化令牌写入了链接指向的目录", err)
	}
}
