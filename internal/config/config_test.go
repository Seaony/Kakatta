package config

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func environment(values map[string]string) Lookup {
	return func(key string) (string, bool) { value, ok := values[key]; return value, ok }
}

func TestLoad(t *testing.T) {
	cfg, err := Load([]string{"--data", "local data", "--listen", "127.0.0.1:8080"}, environment(map[string]string{
		"TZ": "Asia/Singapore", "KAKATTA_TRUSTED_PROXIES": "",
	}))
	if err != nil || !filepath.IsAbs(cfg.DataDir) || cfg.Environment.TZ != "Asia/Singapore" ||
		cfg.Environment.TrustedProxies == nil || *cfg.Environment.TrustedProxies != "" {
		t.Fatalf("参数读取错误：%+v %v", cfg, err)
	}
	for _, args := range [][]string{{"--data", ""}, {"--listen", "8080"}, {"--listen", "localhost:"}, {"extra"}, {"--unknown"}} {
		if _, err := Load(args, environment(nil)); err == nil {
			t.Errorf("接受了错误参数：%v", args)
		}
	}
}

func TestPreparePaths(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	paths, err := CreateDataDirectories(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{data, paths.Browser, paths.Images, paths.Backups} {
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
			t.Fatalf("目录未正确建立：%s %v", path, err)
		}
	}
	outside := t.TempDir()
	linked := filepath.Join(t.TempDir(), "data")
	if err := os.Symlink(outside, linked); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateDataDirectories(linked); err == nil {
		t.Fatal("接受了链接数据目录")
	}
	if _, err := os.Stat(filepath.Join(outside, "browser")); !os.IsNotExist(err) {
		t.Fatal("拒绝链接前已写入外部目录")
	}
}

func TestNewMasterKeyPersistsAndIsNeverReplaced(t *testing.T) {
	paths, err := CreateDataDirectories(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first, err := LoadMasterKey(paths, environment(nil))
	if err != nil || len(first) != 32 {
		t.Fatalf("未生成密钥：%v", err)
	}
	second, err := LoadMasterKey(paths, environment(nil))
	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("重启更换了密钥：%v", err)
	}
	info, _ := os.Stat(paths.Key)
	if info.Mode().Perm() != 0600 {
		t.Fatal("默认密钥权限错误")
	}
	if err := os.WriteFile(paths.Database, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(paths.Key); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadMasterKey(paths, environment(nil)); err == nil {
		t.Fatal("已有数据库时补造了新密钥")
	}
	if _, err := os.Stat(paths.Key); !os.IsNotExist(err) {
		t.Fatal("拒绝启动后留下了新密钥")
	}
}

func TestExplicitKeyDoesNotFallback(t *testing.T) {
	paths, _ := CreateDataDirectories(t.TempDir())
	key := bytes.Repeat([]byte{0x9f}, 32)
	keyFile := filepath.Join(t.TempDir(), "docker-secret")
	if err := os.WriteFile(keyFile, key, 0444); err != nil {
		t.Fatal(err)
	}
	for _, env := range []map[string]string{
		{"KAKATTA_SECRET_KEY": base64.StdEncoding.EncodeToString(key)},
		{"KAKATTA_SECRET_KEY_FILE": keyFile},
	} {
		got, err := LoadMasterKey(paths, environment(env))
		if err != nil || !bytes.Equal(got, key) {
			t.Fatalf("显式来源失败：%v", err)
		}
	}
	for _, env := range []map[string]string{
		{"KAKATTA_SECRET_KEY": "", "KAKATTA_SECRET_KEY_FILE": keyFile},
		{"KAKATTA_SECRET_KEY": "secret-invalid-key"},
		{"KAKATTA_SECRET_KEY_FILE": ""},
		{"KAKATTA_SECRET_KEY_FILE": filepath.Join(t.TempDir(), "secret-invalid-key")},
		{"KAKATTA_SECRET_KEY_FILE": paths.DataDir},
	} {
		_, err := LoadMasterKey(paths, environment(env))
		if err == nil || strings.Contains(err.Error(), "secret-invalid-key") {
			t.Fatalf("显式错误回退或泄露输入：%v", err)
		}
	}
	if _, err := os.Stat(paths.Key); !os.IsNotExist(err) {
		t.Fatal("显式来源写出了默认密钥")
	}
}

func TestBadDefaultKeyFailsClosed(t *testing.T) {
	for _, size := range []int{0, 31, 33, 100} {
		t.Run(strings.Repeat("x", size), func(t *testing.T) {
			paths, _ := CreateDataDirectories(t.TempDir())
			if err := os.WriteFile(paths.Key, make([]byte, size), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadMasterKey(paths, environment(nil)); err == nil {
				t.Fatal("接受了错误长度的默认密钥")
			}
		})
	}
	paths, _ := CreateDataDirectories(t.TempDir())
	if err := os.WriteFile(paths.Key, make([]byte, 32), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(paths.Key, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadMasterKey(paths, environment(nil)); err == nil {
		t.Fatal("接受了权限过宽的默认密钥")
	}
	if err := os.Remove(paths.Key); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing", paths.Key); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadMasterKey(paths, environment(nil)); err == nil {
		t.Fatal("接受了默认密钥链接")
	}
}

func TestConcurrentCreationUsesOneKey(t *testing.T) {
	paths, _ := CreateDataDirectories(t.TempDir())
	const count = 8
	keys := make([][]byte, count)
	errs := make([]error, count)
	var group sync.WaitGroup
	for i := range count {
		group.Go(func() { keys[i], errs[i] = LoadMasterKey(paths, environment(nil)) })
	}
	group.Wait()
	for i := range count {
		if errs[i] != nil || !bytes.Equal(keys[0], keys[i]) {
			t.Fatalf("并发创建不一致：%v", errs[i])
		}
	}
	leftovers, err := filepath.Glob(filepath.Join(paths.DataDir, ".secret-key-*"))
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("临时密钥未清理：%v %v", leftovers, err)
	}
}
