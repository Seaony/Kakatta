package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	modelservice "github.com/Seaony/Kakatta/internal/analysis/service"
	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/auth"
	"github.com/Seaony/Kakatta/internal/backup"
	"github.com/Seaony/Kakatta/internal/config"
	"github.com/Seaony/Kakatta/internal/notification"
	"github.com/Seaony/Kakatta/internal/quota"
	"github.com/Seaony/Kakatta/internal/server"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/store"
)

func graphConfig(t *testing.T) (config.Config, config.Paths, [32]byte) {
	t.Helper()
	cfg := config.Config{DataDir: t.TempDir(), Environment: settings.Environment{TZ: "UTC"}}
	paths, err := config.CreateDataDirectories(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	var key [32]byte
	for i := range key {
		key[i] = byte(i + 1)
	}
	return cfg, paths, key
}

func graphFixture(t *testing.T) *runtimeGraph {
	t.Helper()
	cfg, paths, key := graphConfig(t)
	g, err := newRuntimeGraph(t.Context(), cfg, paths, key, backup.KeyDefaultFile, "graph-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := g.close(ctx); err != nil {
			t.Error(err)
		}
	})
	return g
}

func graphAdmin(t *testing.T, g *runtimeGraph) auth.Session {
	t.Helper()
	session, err := g.authentication.Initialize(t.Context(), g.bootstrap.Token, "本地用户", "graph-test-password", "运行代测试")
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func graphRequest(t *testing.T, g *runtimeGraph, session auth.Session, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "http://kakatta.test"+path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if session.Token != "" {
		r.AddCookie(&http.Cookie{Name: "kakatta_session", Value: session.Token})
	}
	w := httptest.NewRecorder()
	g.handler.ServeHTTP(w, r)
	return w
}

func graphScalar(t *testing.T, g *runtimeGraph, query string, args ...any) int64 {
	t.Helper()
	var result int64
	if err := g.database.DB().QueryRowContext(t.Context(), query, args...).Scan(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

func graphWait(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for !ready() {
		select {
		case <-deadline.C:
			t.Fatal("运行代没有完成预期状态变化")
		case <-tick.C:
		}
	}
}

func graphStart(t *testing.T, g *runtimeGraph) {
	t.Helper()
	if err := g.core.Prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := g.core.Start(); err != nil {
		t.Fatal(err)
	}
}

func graphEvents(t *testing.T, g *runtimeGraph, session auth.Session) *bufio.Reader {
	t.Helper()
	upstream := httptest.NewServer(g.handler)
	t.Cleanup(upstream.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	t.Cleanup(cancel)
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, upstream.URL+"/api/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.AddCookie(&http.Cookie{Name: "kakatta_session", Value: session.Token})
	response, err := upstream.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { response.Body.Close() })
	if response.StatusCode != http.StatusOK {
		t.Fatal("事件流未建立", response.StatusCode)
	}
	reader := bufio.NewReader(response.Body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\n" {
			return reader
		}
	}
}

func graphEvent(t *testing.T, reader *bufio.Reader, kind string) server.Event {
	t.Helper()
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal("没有收到运行代事件", kind, err)
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event server.Event
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatal(err)
		}
		if event.Type == kind {
			return event
		}
	}
}

func graphChannel(t *testing.T, g *runtimeGraph, address string) notification.Channel {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"type": "webhook", "name": "本地回执", "secrets": map[string]string{"url": address}})
	var patch notification.ChannelPatch
	if err := json.Unmarshal(raw, &patch); err != nil {
		t.Fatal(err)
	}
	channel, err := g.notifications.CreateChannel(t.Context(), patch)
	if err != nil {
		t.Fatal(err)
	}
	return channel
}

func graphProvider(t *testing.T, g *runtimeGraph, address string) modelservice.ProviderView {
	t.Helper()
	key := "local-graph-model-key"
	result, err := g.models.CreateProvider(t.Context(), modelservice.ProviderInput{
		Name: "本地模型", BaseURL: address + "/v1", VisionModel: "graph-model", APIKey: &key, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return result.Provider
}

func TestRuntimeGraphConstructsStoppedDependenciesAndRegistersRealRoutes(t *testing.T) {
	cfg, paths, key := graphConfig(t)
	staged, err := store.Open(t.Context(), paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := staged.DB().Exec(`INSERT INTO meta(key,value) VALUES('account.cleanup.2','1')`); err != nil {
		t.Fatal(err)
	}
	if err := staged.Close(); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(paths.Browser, "2")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	kept := filepath.Join(directory, "kept")
	if err := os.WriteFile(kept, []byte("尚未准备"), 0600); err != nil {
		t.Fatal(err)
	}
	g, err := newRuntimeGraph(t.Context(), cfg, paths, key, backup.KeyExternalFile, "first-generation")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g.close(context.Background()) })
	if g.generation != "first-generation" || g.keySource != backup.KeyExternalFile || g.key != key || g.preferences == nil || g.authentication == nil || g.models == nil || g.notifications == nil || g.core == nil || g.handler == nil || g.bootstrap.Token == "" {
		t.Fatal("运行代依赖或实际密钥没有完整装配")
	}
	status, err := g.core.GetSystemStatus(t.Context())
	if err != nil || status.Scheduler.Running || status.Scheduler.Accepting || status.Browsers.Active != 0 {
		t.Fatal("构造就启动了调度", status, err)
	}
	if _, err := os.Stat(kept); err != nil || graphScalar(t, g, `SELECT count(*) FROM meta WHERE key='stats.started_at'`) != 0 || graphScalar(t, g, `SELECT count(*) FROM meta WHERE key='account.cleanup.2'`) != 1 {
		t.Fatal("构造提前执行了准备或目录清理", err)
	}
	session := graphAdmin(t, g)
	for _, path := range []string{"/api/v1/tasks", "/api/v1/channels", "/api/v1/ai/providers", "/api/v1/prompts", "/api/v1/ai/budget"} {
		if response := graphRequest(t, g, session, http.MethodGet, path, ""); response.Code != http.StatusOK {
			t.Fatal("业务路由没有接入实际服务", path, response.Code, response.Body.String())
		}
		if response := graphRequest(t, g, auth.Session{}, http.MethodGet, path, ""); response.Code != http.StatusUnauthorized {
			t.Fatal("业务路由未统一鉴权", path, response.Code)
		}
	}
	if response := graphRequest(t, g, session, http.MethodPost, "/api/v1/tasks/1/run", ""); response.Code != http.StatusServiceUnavailable {
		t.Fatal("尚未启动就接纳了运行", response.Code, response.Body.String())
	}
	for _, table := range []string{"run_records", "ai_calls", "deliveries"} {
		if graphScalar(t, g, "SELECT count(*) FROM "+table) != 0 {
			t.Fatal("构造或只读接口发生了外发", table)
		}
	}
}

func TestRuntimeGraphSettingsWakeSchedulerAndPublishAfterCommit(t *testing.T) {
	g := graphFixture(t)
	session := graphAdmin(t, g)
	local := time.Now().UTC()
	protected := time.Date(local.Year(), local.Month(), local.Day()+1, 12, 0, 0, 0, time.UTC)
	clock, _ := json.Marshal(quota.ClockState{Version: 1, HighestUTC: protected.UnixMilli(), Timezone: "UTC", Windows: []quota.DayWindow{}})
	if _, err := g.database.DB().Exec(`UPDATE meta SET value=? WHERE key=?`, string(clock), appsettings.ClockKey); err != nil {
		t.Fatal(err)
	}
	if _, err := g.preferences.Apply(t.Context(), settings.Values{"ai.daily_token_limit": json.RawMessage(`1`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.database.DB().Exec(`INSERT INTO ai_calls(at,purpose,ok,duration_ms,input_tokens,output_tokens) VALUES(?,'test',1,1,1,0)`, protected.Add(-time.Second).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	graphStart(t, g)
	graphWait(t, func() bool {
		return graphScalar(t, g, `SELECT count(*) FROM system_events WHERE kind='ai_budget' AND resolved_at IS NULL`) == 1
	})
	events := graphEvents(t, g, session)
	response := graphRequest(t, g, session, http.MethodPatch, "/api/v1/settings", `{"values":{"ai.daily_token_limit":2}}`)
	if response.Code != http.StatusOK || graphScalar(t, g, `SELECT CAST(value_json AS INTEGER) FROM settings WHERE key='ai.daily_token_limit'`) != 2 {
		t.Fatal("设置未通过实际存储提交", response.Code, response.Body.String())
	}
	graphEvent(t, events, "settings.changed")
	graphWait(t, func() bool {
		return graphScalar(t, g, `SELECT count(*) FROM system_events WHERE kind='ai_budget' AND resolved_at IS NULL`) == 0
	})
	if err := g.handler.Close(); err != nil {
		t.Fatal(err)
	}
	settingsStore := graphSettings{g}
	result, err := settingsStore.Apply(t.Context(), settings.Values{"ai.daily_token_limit": json.RawMessage(`3`)})
	if err != nil || string(result.Values["ai.daily_token_limit"]) != "3" || graphScalar(t, g, `SELECT CAST(value_json AS INTEGER) FROM settings WHERE key='ai.daily_token_limit'`) != 3 {
		t.Fatal("发布失败被伪装成设置保存失败", err)
	}
	if len(settingsStore.Definitions()) != len(g.preferences.Definitions()) {
		t.Fatal("设置包装丢失了定义")
	}
	if snapshot, err := settingsStore.Get(t.Context()); err != nil || string(snapshot.Values["ai.daily_token_limit"]) != "3" {
		t.Fatal("设置包装没有读取实际设置", err)
	}
}

func TestRuntimeGraphLoginFailurePublishesAndUsesExistingNotificationQueue(t *testing.T) {
	g := graphFixture(t)
	session := graphAdmin(t, g)
	var requests atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(upstream.Close)
	graphChannel(t, g, upstream.URL)
	graphStart(t, g)
	events := graphEvents(t, g, session)
	for number := 1; number <= 20; number++ {
		r := httptest.NewRequest(http.MethodPost, "http://kakatta.test/api/v1/auth/login", strings.NewReader(`{"username":"本地用户","password":"wrong-password"}`))
		r.RemoteAddr = "192.0.2." + strconv.Itoa(number) + ":1234"
		r.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		g.handler.ServeHTTP(response, r)
		if response.Code != http.StatusUnauthorized {
			t.Fatal("实际登录保护没有记录本次失败", number, response.Code, response.Body.String())
		}
	}
	graphEvent(t, events, "system_event.changed")
	graphWait(t, func() bool {
		return graphScalar(t, g, `SELECT count(*) FROM system_events WHERE kind='login_failures' AND notified_at IS NOT NULL`) == 1
	})
	if requests.Load() != 1 || graphScalar(t, g, `SELECT count(*) FROM deliveries WHERE kind='system' AND ok=1`) != 1 {
		t.Fatal("登录告警没有经同一队列保存真实回执", requests.Load())
	}
	if err := g.recordLoginFailure(t.Context(), server.LoginFailureEvent{}); err != nil || graphScalar(t, g, `SELECT count(*) FROM system_events WHERE kind='login_failures' AND resolved_at IS NULL`) != 0 {
		t.Fatal("完整登录成功回调没有解决原事件", err)
	}
	graphEvent(t, events, "system_event.changed")
	if _, err := g.database.DB().Exec(`CREATE TRIGGER graph_login_fail BEFORE INSERT ON system_events BEGIN SELECT RAISE(ABORT,'private database detail'); END`); err != nil {
		t.Fatal(err)
	}
	if err := g.recordLoginFailure(t.Context(), server.LoginFailureEvent{Active: true, Limit: 20, IP: "192.0.2.1"}); err == nil {
		t.Fatal("持久化失败仍放过登录保护回调")
	}
}

func TestRuntimeGraphModelErrorsUseSameNotificationAndSSE(t *testing.T) {
	g := graphFixture(t)
	session := graphAdmin(t, g)
	var requests atomic.Int64
	notifications := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(notifications.Close)
	graphChannel(t, g, notifications.URL)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer local-graph-model-key" {
			t.Error("请求没有使用本地保存的模型配置")
		}
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"invalid api key"}`)
	}))
	t.Cleanup(upstream.Close)
	graphProvider(t, g, upstream.URL)
	graphStart(t, g)
	events := graphEvents(t, g, session)
	_, err := g.models.GenerateProfile(t.Context(), modelservice.GenerateProfileInput{Keyword: "相机"})
	var failure *service.ServiceError
	if !errors.As(err, &failure) || failure.Kind != service.ErrorUnavailable || failure.Reason.Code != "analysis.client.auth" {
		t.Fatal("本地模型失败没有进入正式服务错误", err)
	}
	graphEvent(t, events, "ai.usage.changed")
	graphWait(t, func() bool {
		return graphScalar(t, g, `SELECT count(*) FROM system_events WHERE kind='ai_error' AND notified_at IS NOT NULL`) == 1
	})
	if requests.Load() != 1 || graphScalar(t, g, `SELECT count(*) FROM ai_calls WHERE purpose='profile' AND ok=0`) != 1 {
		t.Fatal("模型事件或物理请求没有走共同依赖", requests.Load())
	}
}

func TestRuntimeGraphCloseJoinsModelReceiptThenClosesDatabaseAndStream(t *testing.T) {
	g := graphFixture(t)
	var number int
	var name, databasePath string
	if err := g.database.DB().QueryRowContext(t.Context(), `PRAGMA database_list`).Scan(&number, &name, &databasePath); err != nil {
		t.Fatal(err)
	}
	session := graphAdmin(t, g)
	entered, canceled := make(chan struct{}), make(chan struct{})
	var once sync.Once
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		once.Do(func() { close(entered) })
		<-r.Context().Done()
		close(canceled)
	}))
	t.Cleanup(upstream.Close)
	graphProvider(t, g, upstream.URL)
	graphStart(t, g)
	events := graphEvents(t, g, session)
	done := make(chan error, 1)
	go func() {
		_, err := g.models.GenerateProfile(t.Context(), modelservice.GenerateProfileInput{Keyword: "相机"})
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("模型请求没有进入本地服务")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := g.close(ctx); err != nil || g.close(ctx) != nil || g.key != [32]byte{} || g.bootstrap.Token != "" || g.ctx.Err() == nil {
		t.Fatal("运行代没有完整关闭或清除私有凭据", err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("关闭丢失模型取消原因", err)
		}
	case <-time.After(time.Second):
		t.Fatal("关闭没有等模型请求返回")
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("本地HTTP请求没有回收")
	}
	if _, err := io.Copy(io.Discard, events); err != nil {
		t.Fatal("事件流没有正常结束", err)
	}
	if err := g.database.DB().Ping(); err == nil {
		t.Fatal("运行代关闭后数据库仍开放")
	}
	data, err := store.Open(t.Context(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	var count int
	if err := data.DB().QueryRow(`SELECT count(*) FROM ai_calls WHERE purpose='profile' AND error_kind='cancelled'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("数据库在模型取消回执落盘前就关闭", count, err)
	}
}

func TestRuntimeGraphCloseFailureKeepsDatabaseAndPrivateKey(t *testing.T) {
	cfg, paths, key := graphConfig(t)
	g, err := newRuntimeGraph(t.Context(), cfg, paths, key, backup.KeyDefaultFile, "failed-settlement")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		g.cancel()
		_, _ = g.database.DB().Exec(`DROP TRIGGER IF EXISTS graph_receipt_fail`)
		_ = g.handler.Close()
		_ = g.core.Close()
		_ = g.models.Close(context.Background())
		_ = g.notifications.Close(context.Background())
		_ = g.database.Close()
		clear(g.key[:])
	})
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(upstream.Close)
	channel := graphChannel(t, g, upstream.URL)
	if _, err := g.database.DB().Exec(`CREATE TRIGGER graph_receipt_fail BEFORE INSERT ON deliveries BEGIN SELECT RAISE(ABORT,'private receipt failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := g.notifications.TestChannel(t.Context(), channel.ID); err == nil || calls.Load() != 1 {
		t.Fatal("没有产生实际发送后的待补记回执", calls.Load(), err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first := g.close(ctx)
	if first == nil || g.close(ctx) != first || g.database.DB().PingContext(ctx) != nil || g.key != key {
		t.Fatal("收尾失败仍关闭数据库、清除密钥或报告成功", first)
	}
	if calls.Load() != 1 {
		t.Fatal("关闭补记再次发送了通知", calls.Load())
	}
}

func TestRuntimeGraphConstructorRejectsInvalidIdentityBeforeIOAndClosesPartialStore(t *testing.T) {
	for _, name := range []string{"取消", "缺运行代", "未知密钥来源", "图片目录失败", "模型构造失败"} {
		t.Run(name, func(t *testing.T) {
			cfg, paths, key := graphConfig(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			generation, source := "candidate", backup.KeyDefaultFile
			beforeIO := true
			switch name {
			case "取消":
				cancel()
			case "缺运行代":
				generation = ""
			case "未知密钥来源":
				source = "unknown"
			case "图片目录失败":
				paths.Images = ""
				beforeIO = false
			case "模型构造失败":
				data, err := store.Open(t.Context(), paths.Database)
				if err != nil {
					t.Fatal(err)
				}
				_, err = data.DB().Exec(`INSERT INTO ai_providers(id,uuid,name,base_url,vision_model,sort_index) VALUES(1,'old-provider','旧配置','http://127.0.0.1:1/v1','local',0); INSERT INTO meta(key,value) VALUES('provider.config.1','bad')`)
				if err != nil {
					_ = data.Close()
					t.Fatal(err)
				}
				if err := data.Close(); err != nil {
					t.Fatal(err)
				}
				beforeIO = false
			}
			g, err := newRuntimeGraph(ctx, cfg, paths, key, source, generation)
			if err == nil || g != nil {
				if g != nil {
					_ = g.close(context.Background())
				}
				t.Fatal("无效或部分失败的运行代仍返回成功", err)
			}
			if name == "取消" && !errors.Is(err, context.Canceled) {
				t.Fatal("构造丢失取消原因", err)
			}
			if errors.Is(err, errRuntimeGraphNotDrained) {
				t.Fatal("正常收尾的构造失败被误标为未排空", err)
			}
			if beforeIO {
				if _, err := os.Stat(paths.Database); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("明显无效输入仍访问数据库", err)
				}
			} else if _, err := os.Stat(paths.Database + "-wal"); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("构造失败未关闭最后的SQLite连接", err)
			}
			for i, value := range key {
				if value != byte(i+1) {
					t.Fatal("构造清除了调用方持有的密钥")
				}
			}
		})
	}
}
