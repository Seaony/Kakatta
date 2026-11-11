package browser

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func writeProfileFixture(t *testing.T, dir, name string, data []byte) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareProfilePreservesPreferencesAndOnlyClearsSessionLogs(t *testing.T) {
	dir := t.TempDir()
	writeProfileFixture(t, dir, "Default/Preferences", []byte(`{"large":9007199254740993,"precise":1.234567890123456789e+30,"session":{"restore_on_startup":5,"keep":{"enabled":true}},"other":[1,2]}`))
	kept := []string{"Local State", "Default/Cookies", "Default/Network/Cookies", "Default/Local Storage/value", "Default/Session Storage/value", "Default/IndexedDB/value", "Default/History", "Default/Sessions_Backup/value"}
	for _, name := range kept {
		writeProfileFixture(t, dir, name, []byte("原有数据 "+name))
	}
	for _, name := range []string{"Default/Sessions/Session_local", "Default/Sessions_Encrypted/Session_local"} {
		writeProfileFixture(t, dir, name, []byte("恢复日志"))
	}
	if err := prepareProfile(dir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "Default", "Preferences"))
	if err != nil {
		t.Fatal(err)
	}
	var preferences, session map[string]json.RawMessage
	if err := json.Unmarshal(data, &preferences); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(preferences["session"], &session); err != nil {
		t.Fatal(err)
	}
	if string(session["restore_on_startup"]) != "1" || string(session["keep"]) != `{"enabled":true}` || string(preferences["large"]) != "9007199254740993" || string(preferences["precise"]) != "1.234567890123456789e+30" || string(preferences["other"]) != "[1,2]" {
		t.Fatal("合并启动策略改变了原有设置或数字", string(data))
	}
	for _, name := range kept {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(data) != "原有数据 "+name {
			t.Fatal("清理碰到了恢复日志以外的配置", name, err)
		}
	}
	for _, name := range []string{"Sessions", "Sessions_Encrypted"} {
		if _, err := os.Lstat(filepath.Join(dir, "Default", name)); !os.IsNotExist(err) {
			t.Fatal("旧页面恢复日志没有清除", name, err)
		}
	}
}

func TestPrepareProfileCreatesSessionPreferences(t *testing.T) {
	dir := t.TempDir()
	if err := prepareProfile(dir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "Default", "Preferences"))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Session struct {
			Restore int `json:"restore_on_startup"`
		} `json:"session"`
	}
	if err := json.Unmarshal(data, &got); err != nil || got.Session.Restore != 1 {
		t.Fatal("新配置没有启用会话 Cookie 恢复", string(data), err)
	}
}

func TestPrepareProfileRejectsBrokenPreferencesWithoutOverwriting(t *testing.T) {
	for _, source := range []string{`{"session":`, `null`, `[]`, `{"session":null}`, `{"session":[]}`, `{"session":"broken"}`, `{"session":false}`} {
		t.Run(source, func(t *testing.T) {
			dir := t.TempDir()
			writeProfileFixture(t, dir, "Default/Preferences", []byte(source))
			writeProfileFixture(t, dir, "Default/Sessions/Session_local", []byte("恢复日志"))
			if err := prepareProfile(dir); err == nil {
				t.Fatal("无效配置不能被覆盖后继续启动")
			}
			data, err := os.ReadFile(filepath.Join(dir, "Default", "Preferences"))
			if err != nil || !bytes.Equal(data, []byte(source)) {
				t.Fatal("原有坏配置被悄悄覆盖", err)
			}
			data, err = os.ReadFile(filepath.Join(dir, "Default", "Sessions", "Session_local"))
			if err != nil || string(data) != "恢复日志" {
				t.Fatal("配置未通过校验就删除了恢复日志", err)
			}
		})
	}
}

func TestPrepareProfileSessionCleanupFailureRefusesStartup(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("需要普通用户的目录权限检查")
	}
	dir := t.TempDir()
	writeProfileFixture(t, dir, "Default/Preferences", []byte(`{"session":{"restore_on_startup":1}}`))
	blocked := filepath.Join(dir, "Default", "Sessions_Encrypted", "blocked")
	writeProfileFixture(t, dir, "Default/Sessions_Encrypted/blocked/Session_local", []byte("恢复日志"))
	if err := os.Chmod(blocked, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(blocked, 0700) })
	if err := prepareProfile(dir); err == nil {
		t.Fatal("恢复日志无法删除时不能继续启动")
	}
}
