package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/auth"
	"github.com/Seaony/Kakatta/internal/config"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/security"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/store"
)

type observedLog struct {
	mu      sync.Mutex
	lines   bytes.Buffer
	address chan string
	token   string
}

func (w *observedLog) Write(line []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lines.Write(line)
	var entry struct{ Token, Listen string }
	if json.Unmarshal(line, &entry) == nil {
		if entry.Token != "" {
			w.token = entry.Token
		}
		if entry.Listen != "" {
			w.address <- entry.Listen
		}
	}
	return len(line), nil
}

func TestServeStartsWithEmbeddedPageAndShutsDown(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	logs := &observedLog{address: make(chan string, 1)}
	finished := make(chan error, 1)
	data := t.TempDir()
	trusted := "127.0.0.0/8"
	go func() {
		finished <- serve(ctx, config.Config{DataDir: data, Listen: "127.0.0.1:0", Environment: settings.Environment{TZ: "UTC", TrustedProxies: &trusted}},
			func(string) (string, bool) { return "", false }, slog.New(slog.NewJSONHandler(logs, nil)))
	}()
	var address string
	select {
	case address = <-logs.address:
	case err := <-finished:
		t.Fatalf("服务未启动：%v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("服务启动超时")
	}
	client := &http.Client{Timeout: 3 * time.Second}
	for _, test := range []struct {
		path     string
		status   int
		contains string
	}{
		{"/healthz", 200, `"ok"`}, {"/api/v1/me", 401, "auth.unauthenticated"}, {"/", 200, `<div id="root">`},
	} {
		response, err := client.Get("http://" + address + test.path)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != test.status || !strings.Contains(string(body), test.contains) {
			t.Fatalf("%s：状态%d，响应%s，错误%v", test.path, response.StatusCode, body, err)
		}
	}
	logs.mu.Lock()
	token := logs.token
	logs.mu.Unlock()
	if token == "" {
		t.Fatal("缺少首次初始化令牌")
	}
	body, _ := json.Marshal(map[string]string{"token": token, "username": "测试用户", "password": "local-test-password"})
	response, err := client.Post("http://"+address+"/api/v1/setup", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 || len(response.Cookies()) != 1 || response.Cookies()[0].Name != "kakatta_session" {
		t.Fatalf("初始化未发正式会话：%d", response.StatusCode)
	}
	for number := 1; number <= 20; number++ {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+address+"/api/v1/auth/login", strings.NewReader(`{"username":"测试用户","password":"wrong-password"}`))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Forwarded-For", fmt.Sprintf("192.0.2.%d", number))
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("第%d次错误密码未正常计入：%d", number, response.StatusCode)
		}
	}
	database, err := store.Open(t.Context(), filepath.Join(data, "kakatta.db"))
	if err != nil {
		t.Fatal(err)
	}
	var raw string
	err = database.DB().QueryRow(`SELECT message FROM system_events WHERE kind='login_failures' AND resolved_at IS NULL`).Scan(&raw)
	database.Close()
	var reason locales.Message
	if err != nil || json.Unmarshal([]byte(raw), &reason) != nil || reason.Code != "security.login_failures" || reason.Args["count"] != float64(20) || reason.Args["ip"] != "192.0.2.20" {
		t.Fatalf("登录保护丢失次数或真实来源：%s %v", raw, err)
	}
	for _, language := range []locales.Language{locales.ZhCN, locales.En} {
		if _, err := locales.Format(language, reason); err != nil {
			t.Fatalf("登录告警无法按事件事实翻译：%v", err)
		}
	}
	cancel()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("停止后服务没有退出")
	}
	logs.mu.Lock()
	defer logs.mu.Unlock()
	if strings.Count(logs.lines.String(), `"token":`) != 1 || strings.Contains(logs.lines.String(), "local-test-password") {
		t.Fatal("初始化日志次数错误或泄露了密码")
	}
}

func TestMaintenanceWithoutMasterKeyRevokesSessions(t *testing.T) {
	data := t.TempDir()
	db, err := store.Open(t.Context(), filepath.Join(data, "kakatta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cipher, err := security.NewCipher(security.NewMasterKey())
	if err != nil {
		t.Fatal(err)
	}
	service, bootstrap, err := auth.New(t.Context(), auth.Dependencies{DB: db.DB(), Cipher: cipher})
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.Initialize(t.Context(), bootstrap.Token, "用户", "old-password", "本地测试")
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run(t.Context(), []string{"reset-password", "--data", data, "--disable-totp"}, strings.NewReader("  new-password  \n"), &output, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "new-password") || strings.Contains(output.String(), bootstrap.Token) {
		t.Fatal("维护输出泄露凭据")
	}
	if _, err := service.Authenticate(t.Context(), session.Token, auth.WebSession); err == nil {
		t.Fatal("旧会话未吊销")
	}
	if _, err := service.PasswordLogin(t.Context(), "用户", "  new-password  ", "本地测试"); err != nil {
		t.Fatalf("密码前后空白被改动：%v", err)
	}
	if err := run(t.Context(), []string{"disable-totp", "--data", data}, strings.NewReader(""), &output, nil); err != nil {
		t.Fatal(err)
	}
	if err := run(t.Context(), []string{"reset-password", "--data", filepath.Join(t.TempDir(), "missing")}, strings.NewReader("password\n"), &output, nil); err == nil {
		t.Fatal("维护命令为不存在的数据目录创建了数据库")
	}
}

func TestPasswordInputAndHelp(t *testing.T) {
	for _, input := range []string{"two words\r\n", "two words\n", "two words"} {
		password, err := readPassword(strings.NewReader(input), io.Discard)
		if err != nil || string(password) != "two words" {
			t.Fatalf("标准输入读取错误：%v", err)
		}
	}
	if _, err := readPassword(strings.NewReader(strings.Repeat("x", (1<<20)+1)), io.Discard); err == nil {
		t.Fatal("接受了过长输入")
	}
	var output bytes.Buffer
	if err := run(t.Context(), []string{"--help"}, nil, &output, nil); err != nil || !strings.Contains(output.String(), "reset-password") {
		t.Fatalf("帮助未列出维护命令：%v", err)
	}
}
