package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Seaony/Kakatta/internal/config"
	"github.com/Seaony/Kakatta/internal/security"
	"github.com/Seaony/Kakatta/internal/settings"
)

func TestCheckReadsSavedTimezoneAndProxyWithoutChangingProtection(t *testing.T) {
	data := t.TempDir()
	paths := prepareOfflineRuntime(t, data, settings.Environment{})
	db, err := sql.Open("sqlite", paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	key, err := os.ReadFile(paths.Key)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := security.NewCipher(key)
	clear(key)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := cipher.Seal([]byte("http://local-user:local-password@127.0.0.1:18761"), "proxies", "url_enc", "check-proxy")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO proxies(uuid,name,url_enc) VALUES('check-proxy','本地检查',?)`, encrypted); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO settings(key,value_json) VALUES('general.timezone','"America/New_York"') ON CONFLICT(key) DO UPDATE SET value_json=excluded.value_json`); err != nil {
		t.Fatal(err)
	}
	var before, after string
	if err := db.QueryRow(`SELECT value FROM meta WHERE key='quota.clock'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"meta", "settings", "proxies"} {
		for _, operation := range []string{"INSERT", "UPDATE", "DELETE"} {
			if _, err := db.Exec(`CREATE TRIGGER check_no_` + operation + `_` + table + ` BEFORE ` + operation + ` ON ` + table + ` BEGIN SELECT RAISE(ABORT,'自检不能写库'); END`); err != nil {
				t.Fatal(err)
			}
		}
	}
	zone, proxies, err := checkSavedConfiguration(t.Context(), config.Config{DataDir: data}, func(string) (string, bool) { return "", false })
	if err != nil || zone != "America/New_York" || len(proxies) != 1 {
		t.Fatalf("未只读加载实际配置：时区=%s，代理数=%d，错误=%v", zone, len(proxies), err)
	}
	if password, _ := proxies[0].User.Password(); password != "local-password" || proxies[0].Hostname() != "127.0.0.1" {
		t.Fatal("代理配置没有从原密钥完整读取")
	}
	if err := db.QueryRow(`SELECT value FROM meta WHERE key='quota.clock'`).Scan(&after); err != nil || before != after {
		t.Fatal("自检修改了额度保护时钟")
	}
}

func TestCheckDoesNotInitializeMissingDatabaseOrKey(t *testing.T) {
	data := t.TempDir()
	zone, proxies, err := checkSavedConfiguration(t.Context(), config.Config{DataDir: data, Environment: settings.Environment{TZ: "UTC"}}, nil)
	if err != nil || zone != "UTC" || len(proxies) != 0 {
		t.Fatalf("全新配置自检错误：%s，%d，%v", zone, len(proxies), err)
	}
	entries, err := os.ReadDir(data)
	if err != nil || len(entries) != 0 {
		t.Fatal("读取默认设置创建了数据库或密钥")
	}
}

func TestCheckReportsCorruptSettingsBeforeStartingBrowser(t *testing.T) {
	data := t.TempDir()
	if err := os.WriteFile(filepath.Join(data, "kakatta.db"), []byte("不是数据库"), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run(t.Context(), []string{"check", "--data", data}, nil, &output, func(string) (string, bool) { return "", false }); err == nil {
		t.Fatal("损坏数据库仍通过自检")
	}
	var check struct {
		ID     string
		Passed bool
		Action string
	}
	if err := json.Unmarshal(output.Bytes(), &check); err != nil || check.ID != "settings" || check.Passed || check.Action == "" {
		t.Fatalf("缺少明确失败与处理办法：%s", output.Bytes())
	}
	if _, err := os.Stat(filepath.Join(data, "secret.key")); !os.IsNotExist(err) {
		t.Fatal("损坏数据库自检重建了主密钥")
	}
}

func TestOccupiedPortLeavesReadableStartupFailureWithoutCredentials(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	data := t.TempDir()
	err = serve(context.Background(), config.Config{DataDir: data, Listen: listener.Addr().String(), Environment: settings.Environment{TZ: "UTC"}},
		func(key string) (string, bool) {
			return "http://private-user:private-password@127.0.0.1:1234", key == "HTTP_PROXY"
		},
		slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err == nil || !strings.Contains(err.Error(), "端口是否已被占用") || !strings.Contains(err.Error(), "--listen") {
		t.Fatalf("端口错误缺少处理办法：%v", err)
	}
	files, err := filepath.Glob(filepath.Join(data, "diagnostics", "service", "events-*.jsonl"))
	if err != nil || len(files) != 1 {
		t.Fatal("启动失败没有落盘日志")
	}
	logged, err := os.ReadFile(files[0])
	if err != nil || !bytes.Contains(logged, []byte("service_stopped")) || !bytes.Contains(logged, []byte("environment")) || !bytes.Contains(logged, []byte("端口是否已被占用")) {
		t.Fatalf("无法从本机文件查明启动失败：%s，%v", logged, err)
	}
	if bytes.Contains(logged, []byte("private-password")) || bytes.Contains(logged, []byte("private-user")) {
		t.Fatal("环境记录保存了代理凭据")
	}
}
