package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
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

	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/auth"
	"github.com/Seaony/Kakatta/internal/backup"
	"github.com/Seaony/Kakatta/internal/config"
	"github.com/Seaony/Kakatta/internal/security"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/store"
	"modernc.org/sqlite"
)

func prepareOfflineRuntime(t *testing.T, dir string, environment settings.Environment) config.Paths {
	t.Helper()
	paths, err := config.CreateDataDirectories(dir)
	if err != nil {
		t.Fatal(err)
	}
	key, err := config.LoadMasterKey(paths, func(string) (string, bool) { return "", false })
	clear(key)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(t.Context(), paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	prefs, err := appsettings.New(t.Context(), db.DB(), environment, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prefs.Apply(t.Context(), settings.Values{"general.update_check": json.RawMessage(`false`)}); err != nil {
		t.Fatal(err)
	}
	return paths
}

func runtimeFixture(t *testing.T, source backup.KeySource) (*runtimeManager, auth.Session) {
	t.Helper()
	cfg := config.Config{DataDir: t.TempDir(), Listen: "127.0.0.1:0", Environment: settings.Environment{TZ: "UTC"}}
	paths := prepareOfflineRuntime(t, cfg.DataDir, cfg.Environment)
	key, _, _, err := (runtimeKeyConfig{}).load(paths)
	if err != nil {
		t.Fatal(err)
	}
	var keyFile string
	if source == backup.KeyExternalFile {
		keyFile = filepath.Join(t.TempDir(), "external.key")
		if err := os.WriteFile(keyFile, key[:], 0600); err != nil {
			t.Fatal(err)
		}
	}
	m, err := newRuntimeManager(t.Context(), cfg, paths, key, source, keyFile, slog.New(slog.NewTextHandler(io.Discard, nil)))
	clear(key[:])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := m.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	session, err := m.graph.authentication.Initialize(t.Context(), m.graph.bootstrap.Token, "本地用户", "local-password", "本地测试")
	if err != nil {
		t.Fatal(err)
	}
	return m, session
}

func runtimeRequest(m *runtimeManager, session auth.Session, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://kakatta.test"+path, strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(&http.Cookie{Name: "kakatta_session", Value: session.Token})
	w := httptest.NewRecorder()
	m.ServeHTTP(w, r)
	return w
}

func runtimePlan(t *testing.T, source, target *runtimeManager) backup.RestorePlan {
	t.Helper()
	dir, err := os.MkdirTemp(target.paths.DataDir, ".test-candidate-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "candidate.db")
	if err := source.graph.database.Backup(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	return backup.RestorePlan{DatabasePath: path, MasterKey: source.graph.key, KeySource: target.graph.keySource, ExpectedGeneration: target.graph.generation, Authorize: func(context.Context) error { return nil }}
}

func TestRuntimeInstallReplacesWholeGraphAndPreservesExternalKey(t *testing.T) {
	for _, keySource := range []backup.KeySource{backup.KeyDefaultFile, backup.KeyEnvironment, backup.KeyExternalFile} {
		t.Run(string(keySource), func(t *testing.T) {
			source, _ := runtimeFixture(t, backup.KeyDefaultFile)
			target, oldSession := runtimeFixture(t, keySource)
			if _, err := source.graph.database.DB().Exec(`INSERT INTO tasks(name,keyword,enabled,max_pages,interval_minutes,judge_mode,notify_policy,sort_index,requirement,profile_json) VALUES('来自备份','本地商品',0,1,30,'keyword','recommended',0,'','{}')`); err != nil {
				t.Fatal(err)
			}
			old := target.graph
			oldKey := old.key
			defaultKey, err := os.ReadFile(target.paths.Key)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(target.paths.Browser, "old-login"), []byte("旧登录"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(target.paths.Images, "old-image"), []byte("旧缓存"), 0600); err != nil {
				t.Fatal(err)
			}
			plan := runtimePlan(t, source, target)
			if err := target.Install(t.Context(), plan); err != nil {
				t.Fatal(err)
			}
			g := target.graph
			if g == old || g.generation == old.generation || old.database.DB().Ping() == nil || old.key != [32]byte{} {
				t.Fatal("旧运行代没有完全关闭或代次没有切换")
			}
			wantKey := oldKey
			if keySource == backup.KeyDefaultFile {
				wantKey = source.graph.key
			}
			if g.key != wantKey || g.keySource != keySource {
				t.Fatal("新库没有绑定实际目标密钥")
			}
			var count int
			if err := g.database.DB().QueryRow(`SELECT count(*) FROM tasks WHERE name='来自备份'`).Scan(&count); err != nil || count != 1 {
				t.Fatal("没有切换到备份业务数据", err)
			}
			for _, path := range []string{filepath.Join(target.paths.Browser, "old-login"), filepath.Join(target.paths.Images, "old-image"), filepath.Join(target.paths.DataDir, config.RestoreStateName)} {
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("恢复后残留旧资源", path, err)
				}
			}
			if keySource != backup.KeyDefaultFile {
				stored, err := os.ReadFile(target.paths.Key)
				if err != nil || !bytes.Equal(stored, defaultKey) {
					t.Fatal("外部密钥来源仍改动默认密钥", err)
				}
			}
			if response := runtimeRequest(target, oldSession, http.MethodGet, "/api/v1/me", ""); response.Code != http.StatusUnauthorized {
				t.Fatal("恢复后旧会话仍有效", response.Code)
			}
			if _, err := g.authentication.PasswordLogin(t.Context(), "本地用户", "local-password", "本地测试"); err != nil {
				t.Fatal("无法用备份中的密码重新登录", err)
			}
		})
	}
}

func TestRuntimeRestoreConfirmationDrainsRequestsWithoutWaitingForItself(t *testing.T) {
	source, _ := runtimeFixture(t, backup.KeyDefaultFile)
	target, session := runtimeFixture(t, backup.KeyDefaultFile)
	var archive bytes.Buffer
	if err := source.backups.Export(t.Context(), "archive-password", &archive); err != nil {
		t.Fatal(err)
	}
	owner := string(session.Identity.Kind) + ":" + strconv.FormatInt(session.Identity.TokenID, 10)
	preview, err := target.backups.PrepareRestore(t.Context(), bytes.NewReader(archive.Bytes()), int64(archive.Len()), "archive-password", owner)
	if err != nil {
		t.Fatal(err)
	}
	entered, canceled, drained := make(chan struct{}), make(chan struct{}), make(chan struct{})
	release := make(chan struct{})
	unhold := sync.OnceFunc(func() { close(release) })
	defer unhold()
	target.graph.handler.RegisterProtected("GET /test/held", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(canceled)
		<-release
		close(drained)
	}))
	held := make(chan *httptest.ResponseRecorder, 1)
	go func() { held <- runtimeRequest(target, session, http.MethodGet, "/test/held", "") }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("旧请求未进入运行代")
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- runtimeRequest(target, session, http.MethodPost, "/api/v1/backup/confirm", `{"ticket":"`+preview.Ticket+`"}`)
	}()
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("切换没有取消旧请求")
	}
	busy := runtimeRequest(target, session, http.MethodGet, "/api/v1/me", "")
	var reply struct {
		RequestID string `json:"request_id"`
	}
	if busy.Code != http.StatusServiceUnavailable || json.Unmarshal(busy.Body.Bytes(), &reply) != nil || reply.RequestID == "" || reply.RequestID != busy.Header().Get("X-Request-Id") {
		t.Fatal("等待旧请求结束时没有按统一格式拒绝新请求", busy.Code, busy.Body.String())
	}
	unhold()
	select {
	case response := <-done:
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"restored":true`) {
			t.Fatal("真实确认未完成整代恢复", response.Code, response.Body.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("确认请求等待自身，或旧请求没有排空")
	}
	select {
	case <-drained:
	default:
		t.Fatal("确认返回时旧请求仍在使用旧库")
	}
	<-held
}

func TestRuntimeRejectedAuthorizationKeepsOriginalGraph(t *testing.T) {
	source, _ := runtimeFixture(t, backup.KeyDefaultFile)
	target, session := runtimeFixture(t, backup.KeyDefaultFile)
	old := target.graph
	plan := runtimePlan(t, source, target)
	var calls atomic.Int64
	plan.Authorize = func(context.Context) error { calls.Add(1); return auth.ErrUnauthenticated }
	if err := target.Install(t.Context(), plan); !errors.Is(err, auth.ErrUnauthenticated) || calls.Load() != 1 || target.graph != old || old.ctx.Err() != nil {
		t.Fatal("失效身份改变了原运行代", err)
	}
	if response := runtimeRequest(target, session, http.MethodGet, "/api/v1/me", ""); response.Code != http.StatusOK {
		t.Fatal("拒绝恢复后没有重新接纳原请求", response.Code)
	}
	plan.ExpectedGeneration = "old-generation"
	if err := target.Install(t.Context(), plan); !errors.Is(err, backup.ErrRestoreChanged) || calls.Load() != 1 {
		t.Fatal("运行代已变仍尝试恢复或鉴权", err)
	}
}

func TestRuntimeSnapshotOnlyAcceptsTheBoundGeneration(t *testing.T) {
	m, _ := runtimeFixture(t, backup.KeyDefaultFile)
	generation := m.graph.generation
	stale := context.WithValue(t.Context(), runtimeBackupGenerationKey{}, generation+"-previous")
	if err := m.WithSnapshot(stale, func(backup.SnapshotSource) error {
		t.Fatal("旧代请求进入了当前数据库的快照回调")
		return nil
	}); !errors.Is(err, backup.ErrRestoreChanged) {
		t.Fatal("未拒绝旧代请求", err)
	}
	current := context.WithValue(t.Context(), runtimeBackupGenerationKey{}, generation)
	if err := m.WithSnapshot(current, func(source backup.SnapshotSource) error {
		if source.Generation != generation || source.MasterKey != m.graph.key || source.Store != m.graph.database {
			t.Fatal("快照没有从同一运行代取库和密钥")
		}
		return source.Store.DB().PingContext(current)
	}); err != nil {
		t.Fatal(err)
	}
}

type runtimeBlockedResponse struct {
	*httptest.ResponseRecorder
	entered   chan struct{}
	unblock   func()
	writeDone <-chan struct{}
}

func (w *runtimeBlockedResponse) Write([]byte) (int, error) {
	close(w.entered)
	<-w.writeDone
	return 0, os.ErrDeadlineExceeded
}

func (w *runtimeBlockedResponse) SetWriteDeadline(time.Time) error {
	w.unblock()
	return nil
}

func TestRuntimePauseInterruptsBlockedResponse(t *testing.T) {
	m, session := runtimeFixture(t, backup.KeyDefaultFile)
	writeDone := make(chan struct{})
	w := &runtimeBlockedResponse{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), writeDone: writeDone,
		unblock: sync.OnceFunc(func() { close(writeDone) })}
	defer w.unblock()
	m.graph.handler.RegisterProtected("GET /test/blocked-response", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("正在传输的旧响应"))
	}))
	r := httptest.NewRequest(http.MethodGet, "http://kakatta.test/test/blocked-response", nil)
	r.AddCookie(&http.Cookie{Name: "kakatta_session", Value: session.Token})
	done := make(chan struct{})
	go func() { defer close(done); m.ServeHTTP(w, r) }()
	select {
	case <-w.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("响应未进入阻塞写入")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := m.pause(ctx); err != nil {
		t.Fatal("暂停只取消上下文，没有中断网络写入", err)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("暂停返回后仍有旧请求")
	}
}

func TestRuntimeInstallRollsBackWhenNewGraphCannotOpen(t *testing.T) {
	source, _ := runtimeFixture(t, backup.KeyDefaultFile)
	target, session := runtimeFixture(t, backup.KeyDefaultFile)
	old := target.graph
	oldKey := old.key
	oldProfile := filepath.Join(target.paths.Browser, "old-login")
	if err := os.WriteFile(oldProfile, []byte("旧登录"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := old.database.DB().Exec(`INSERT INTO meta(key,value) VALUES('test.host','保留旧库')`); err != nil {
		t.Fatal(err)
	}
	if _, err := source.graph.database.DB().Exec(`INSERT INTO ai_providers(id,uuid,name,base_url,vision_model,sort_index,enabled) VALUES(1,'local-provider','本地模型','http://127.0.0.1:1/v1','local',0,0)`); err != nil {
		t.Fatal(err)
	}
	// 直接调用切换入口，在新库已经换入后令模型初始化写入失败。
	if _, err := source.graph.database.DB().Exec(`CREATE TRIGGER reject_graph_configuration BEFORE INSERT ON meta WHEN NEW.key LIKE 'provider.config.%' BEGIN SELECT RAISE(ABORT,'blocked new graph'); END`); err != nil {
		t.Fatal(err)
	}
	plan := runtimePlan(t, source, target)
	var databaseError *sqlite.Error
	if err := target.Install(t.Context(), plan); !errors.As(err, &databaseError) || !strings.Contains(databaseError.Error(), "blocked new graph") {
		t.Fatal("没有在新图准备阶段触发失败", err)
	}
	if target.graph == old || target.graph.key != oldKey || old.database.DB().Ping() == nil {
		t.Fatal("回滚没有用旧密钥重新打开旧库")
	}
	var saved string
	if err := target.graph.database.DB().QueryRow(`SELECT value FROM meta WHERE key='test.host'`).Scan(&saved); err != nil || saved != "保留旧库" {
		t.Fatal("回滚丢失了原业务数据", saved, err)
	}
	profile, err := os.ReadFile(oldProfile)
	if err != nil || string(profile) != "旧登录" {
		t.Fatal("回滚没有还原旧浏览器目录", err)
	}
	if key, err := os.ReadFile(target.paths.Key); err != nil || !bytes.Equal(key, oldKey[:]) {
		t.Fatal("回滚没有还原磁盘上的旧密钥", err)
	}
	if _, err := os.Lstat(filepath.Join(target.paths.DataDir, config.RestoreStateName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("回滚没有清掉已处理的恢复记录", err)
	}
	if response := runtimeRequest(target, session, http.MethodGet, "/api/v1/me", ""); response.Code != http.StatusOK {
		t.Fatal("旧会话不能在回滚后的图继续使用", response.Code)
	}
}

func TestRuntimeInstallRejectsMaintenanceLockAndExternalKeyInsideBrowser(t *testing.T) {
	source, _ := runtimeFixture(t, backup.KeyDefaultFile)
	target, _ := runtimeFixture(t, backup.KeyExternalFile)
	old := target.graph
	plan := runtimePlan(t, source, target)
	plan.Authorize = func(context.Context) error { t.Fatal("先碰了鉴权或旧运行代"); return nil }
	lock, err := config.AcquireRestoreLock(target.paths.DataDir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := target.Install(t.Context(), plan); err == nil || target.graph != old || old.ctx.Err() != nil {
		t.Fatal("维护命令持锁时仍切换旧运行代", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(target.paths.Browser, "key")
	if err := os.Rename(target.keyFile, inside); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(inside, target.keyFile); err != nil {
		t.Fatal(err)
	}
	if err := target.Install(t.Context(), plan); err == nil || target.graph != old || old.ctx.Err() != nil {
		t.Fatal("外部密钥目标位于浏览器目录内仍允许覆盖", err)
	}
	if key, err := os.ReadFile(inside); err != nil || !bytes.Equal(key, old.key[:]) {
		t.Fatal("重叠密钥被改动", err)
	}
}

func TestRuntimeExternalKeyPathCannotPassThroughManagedDirectory(t *testing.T) {
	data, external := t.TempDir(), t.TempDir()
	managed := filepath.Join(data, "browser")
	if err := os.Mkdir(managed, 0700); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(external, "key")
	if err := os.WriteFile(key, make([]byte, backup.MasterKeyBytes), 0600); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(managed, "link")
	through := filepath.Join(external, "through")
	safe := filepath.Join(external, "safe")
	for path, destination := range map[string]string{inside: key, through: inside, safe: "key"} {
		if err := os.Symlink(destination, path); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{inside, through} {
		if err := checkExternalKeyPaths(path, data, "browser"); err == nil {
			t.Fatal("主密钥依赖将被搬走的路径仍允许恢复", path)
		}
	}
	if err := checkExternalKeyPaths(safe, data, "browser"); err != nil {
		t.Fatal("外部正常链接被拒绝", err)
	}
}

func TestServeRejectsDamagedRecoveryBeforeCreatingResources(t *testing.T) {
	data := t.TempDir()
	if err := os.WriteFile(filepath.Join(data, config.RestoreStateName), []byte(`{"phase":"pending"}`), 0600); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := serve(t.Context(), config.Config{DataDir: data, Listen: "invalid"}, nil, logger); !errors.Is(err, errRestoreFilesState) {
		t.Fatal("未先识别损坏的恢复记录", err)
	}
	for _, name := range []string{"browser", "cache", "backups", "secret.key", "kakatta.db"} {
		if _, err := os.Lstat(filepath.Join(data, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("恢复失败前创建了服务资源", name, err)
		}
	}
}

func TestServeCleansInterruptedBackupWorkAndProtectsConfiguredKey(t *testing.T) {
	m, _ := runtimeFixture(t, backup.KeyDefaultFile)
	if err := m.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(m.paths.DataDir, backup.WorkDirName)
	interrupted, err := os.MkdirTemp(work, ".backup-restore-")
	if err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(interrupted, "configured-key")
	if err := os.WriteFile(key, make([]byte, backup.MasterKeyBytes), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "key-link")
	if err := os.Symlink(key, link); err != nil {
		t.Fatal(err)
	}
	userFile := filepath.Join(m.paths.DataDir, ".backup-restore-user-file")
	if err := os.WriteFile(userFile, []byte("用户资料"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := m.cfg
	cfg.Listen = "invalid"
	lookup := func(name string) (string, bool) { return link, name == "KAKATTA_SECRET_KEY_FILE" }
	if err := serve(t.Context(), cfg, lookup, m.logger); err == nil || !strings.Contains(err.Error(), "外部主密钥路径") {
		t.Fatal("启动没有先保护明确配置的密钥", err)
	}
	if info, err := os.Stat(key); err != nil || info.Size() != backup.MasterKeyBytes {
		t.Fatal("启动清扫改动了外部主密钥", err)
	}
	lookup = func(string) (string, bool) { return "", false }
	var listenErr *net.OpError
	if err := serve(t.Context(), cfg, lookup, m.logger); !errors.As(err, &listenErr) {
		t.Fatal("完成清扫后没有继续启动", err)
	}
	if _, err := os.Lstat(work); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("重启后仍留有过期的备份工作目录", err)
	}
	if raw, err := os.ReadFile(userFile); err != nil || string(raw) != "用户资料" {
		t.Fatal("启动清扫触碰了非本应用工作文件", err)
	}
}

func TestLoadRuntimeKeyReadsConfiguredSourceOnce(t *testing.T) {
	paths, err := config.CreateDataDirectories(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	raw := security.NewMasterKey()
	defer clear(raw)
	calls := map[string]int{}
	cfg := readRuntimeKeyConfig(func(name string) (string, bool) {
		calls[name]++
		return base64.StdEncoding.EncodeToString(raw), name == "KAKATTA_SECRET_KEY" && calls[name] == 1
	})
	key, source, file, err := cfg.load(paths)
	defer clear(key[:])
	if err != nil || source != backup.KeyEnvironment || file != "" || !bytes.Equal(key[:], raw) || calls["KAKATTA_SECRET_KEY"] != 1 || calls["KAKATTA_SECRET_KEY_FILE"] != 1 {
		t.Fatal("来源与实际加载密钥不在同一次读取中确定", err)
	}
}
