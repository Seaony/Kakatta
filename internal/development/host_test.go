//go:build kakatta_dev

package development

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestDevelopmentChild(t *testing.T) {
	if len(os.Args) < 4 || os.Args[len(os.Args)-3] != "development-child" {
		return
	}
	path, label := os.Args[len(os.Args)-2], os.Args[len(os.Args)-1]
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		os.Exit(3)
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	fmt.Fprintf(file, "start %s %d %d\n", label, os.Getpid(), os.Getppid())
	if label == "exit" {
		file.Close()
		os.Exit(1)
	}
	<-signals
	fmt.Fprintf(file, "interrupt %s\n", label)
	file.Close()
	os.Exit(0)
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("没有等到预期的开发进程状态")
}

func testHost(t *testing.T, backend string) *Host {
	t.Helper()
	h, err := New(Config{Mode: "fake", Root: t.TempDir(), State: t.TempDir(), Data: t.TempDir(), Listen: "127.0.0.1:0", BackendListen: backend, Air: "fake-air"})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func assertBackendReady(t *testing.T, h *Host, want bool) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18081/__dev/status", nil)
	w := httptest.NewRecorder()
	h.Handler().ServeHTTP(w, req)
	var status struct {
		BackendReady *bool `json:"backend_ready"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &status) != nil || status.BackendReady == nil || *status.BackendReady != want {
		t.Fatalf("后端就绪状态不正确：期望%t HTTP%d %s", want, w.Code, w.Body.String())
	}
}

func TestReloadAndShutdownKeepBackendUntilDrainSucceeds(t *testing.T) {
	var response atomic.Int64
	response.Store(http.StatusOK)
	var waitMS atomic.Int64
	waitMS.Store(3000)
	var cancels atomic.Int64
	var rejectHandshake atomic.Bool
	var rejectedHandshakes atomic.Int64
	var gateMu sync.Mutex
	var gate chan struct{}
	entered := make(chan struct{}, 8)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/__dev/backend/status":
			if rejectHandshake.Load() {
				rejectedHandshakes.Add(1)
				http.Error(w, "握手暂时失败", 503)
				return
			}
			writeJSON(w, backendStatus{ReloadWaitMS: waitMS.Load()})
		case "/__dev/backend/prepare-reload":
			gateMu.Lock()
			current := gate
			gateMu.Unlock()
			entered <- struct{}{}
			if current != nil {
				select {
				case <-current:
				case <-r.Context().Done():
					return
				}
			}
			w.WriteHeader(int(response.Load()))
			writeJSON(w, map[string]bool{"ready": true})
		case "/__dev/backend/cancel-reload":
			cancels.Add(1)
			writeJSON(w, map[string]bool{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer backend.Close()
	h := testHost(t, strings.TrimPrefix(backend.URL, "http://"))
	events := filepath.Join(t.TempDir(), "processes")
	executable, _ := os.Executable()
	var children []*child
	var backendCount int
	h.start = func(name string, args []string, dir string, output io.Writer) (*child, error) {
		label := "air"
		if name != "fake-air" {
			backendCount++
			label = fmt.Sprintf("backend%d", backendCount)
		}
		p, err := startChild(executable, []string{"-test.run=^TestDevelopmentChild$", "--", "development-child", events, label}, dir, output)
		if err != nil {
			return nil, err
		}
		children = append(children, p)
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			raw, _ := os.ReadFile(events)
			if strings.Contains(string(raw), "start "+label+" ") {
				return p, nil
			}
			time.Sleep(5 * time.Millisecond)
		}
		return p, fmt.Errorf("假子进程未启动：%s", label)
	}
	finished := make(chan error, 1)
	go func() { finished <- h.Run(context.Background()); close(finished) }()
	t.Cleanup(func() {
		response.Store(200)
		gateMu.Lock()
		if gate != nil {
			close(gate)
			gate = nil
		}
		gateMu.Unlock()
		h.RequestStop()
		select {
		case <-finished:
		case <-time.After(4 * time.Second):
			for _, p := range children {
				_ = p.cmd.Process.Kill()
			}
		}
	})
	phase := func(want string) bool { h.mu.Lock(); defer h.mu.Unlock(); return h.phase == want }
	waitFor(t, func() bool { return phase("building") })
	assertBackendReady(t, h, false)
	metadataBefore, err := os.ReadFile(h.fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{h.fixturePath, filepath.Join(h.config.State, "control-token")} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("开发凭据文件权限无效：%s %v", path, err)
		}
	}
	publish := func(id, buildError string) {
		t.Helper()
		next := candidate{Generation: id, Error: buildError}
		if buildError == "" {
			next.Binary = filepath.Join(h.config.State, "candidates", "backend-"+id)
			if err := os.MkdirAll(filepath.Dir(next.Binary), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(next.Binary, []byte("候选"), 0700); err != nil {
				t.Fatal(err)
			}
		}
		raw, _ := json.Marshal(next)
		if err := writePrivate(filepath.Join(h.config.State, "candidate.json"), raw); err != nil {
			t.Fatal(err)
		}
	}
	publish("one", "")
	waitFor(t, func() bool { return phase("running") })
	assertBackendReady(t, h, true)
	publish("bad-build", "编译失败 API_KEY=do-not-expose")
	waitFor(t, func() bool { return phase("reload_failed") })
	assertBackendReady(t, h, true)
	rejectHandshake.Store(true)
	publish("bad-handshake", "")
	waitFor(t, func() bool { return rejectedHandshakes.Load() == 1 && phase("reload_failed") })
	assertBackendReady(t, h, true)
	rejectHandshake.Store(false)
	response.Store(503)
	publish("two", "")
	waitFor(t, func() bool { return cancels.Load() == 1 && phase("reload_failed") })
	assertBackendReady(t, h, true)
	raw, _ := os.ReadFile(events)
	if strings.Contains(string(raw), "interrupt backend") || backendCount != 1 {
		t.Fatalf("失败编译或排空向旧后端发了信号：%s", raw)
	}
	response.Store(200)
	gateMu.Lock()
	gate = make(chan struct{})
	gateMu.Unlock()
	publish("three", "")
	<-entered
	waitFor(t, func() bool { return phase("reloading") })
	raw, _ = os.ReadFile(events)
	if strings.Contains(string(raw), "interrupt backend") {
		t.Fatal("排空尚未完成就终止了后端")
	}
	gateMu.Lock()
	close(gate)
	gate = nil
	gateMu.Unlock()
	waitFor(t, func() bool { h.mu.Lock(); defer h.mu.Unlock(); return h.generation == 2 && h.phase == "running" })
	metadataAfter, _ := os.ReadFile(h.fixturePath)
	if string(metadataAfter) != string(metadataBefore) {
		t.Fatal("重载改变了夹具地址")
	}
	response.Store(409)
	h.RequestStop()
	waitFor(t, func() bool { return phase("shutdown_blocked") })
	assertBackendReady(t, h, true)
	raw, _ = os.ReadFile(events)
	if strings.Contains(string(raw), "interrupt backend2") || strings.Contains(string(raw), "interrupt air") {
		t.Fatal("退出排空失败时停止了旧后端或编译监听")
	}
	response.Store(200)
	h.RequestStop()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("正常退出没有收齐子进程")
	}
	assertBackendReady(t, h, false)
	raw, _ = os.ReadFile(events)
	text := string(raw)
	if strings.Index(text, "interrupt backend2") > strings.Index(text, "interrupt air") {
		t.Fatalf("编译监听先于后端关闭：%s", text)
	}
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		fields := strings.Fields(line)
		if fields[0] != "start" {
			continue
		}
		pid, _ := strconv.Atoi(fields[2])
		parent, _ := strconv.Atoi(fields[3])
		if parent != os.Getpid() {
			t.Fatalf("后端不是管理器的直接子进程：%s", line)
		}
		if err := syscall.Kill(pid, 0); err == nil {
			t.Fatalf("进程 %d 没有退出", pid)
		}
	}
}

func TestExitedBackendKeepsStatusAndLogsAvailable(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "后端启动失败", 503) }))
	defer backend.Close()
	h := testHost(t, strings.TrimPrefix(backend.URL, "http://"))
	events := filepath.Join(t.TempDir(), "events")
	executable, _ := os.Executable()
	h.start = func(name string, args []string, dir string, output io.Writer) (*child, error) {
		label := "air"
		if name != "fake-air" {
			label = "exit"
		}
		return startChild(executable, []string{"-test.run=^TestDevelopmentChild$", "--", "development-child", events, label}, dir, output)
	}
	finished := make(chan error, 1)
	go func() { finished <- h.Run(context.Background()); close(finished) }()
	t.Cleanup(func() {
		h.RequestStop()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("失败后端测试没有安全退出管理器")
		}
	})
	waitFor(t, func() bool { h.mu.Lock(); defer h.mu.Unlock(); return h.phase == "building" })
	path := filepath.Join(h.config.State, "candidates", "backend-exits")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("启动失败的候选"), 0700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(candidate{Generation: "exits", Binary: path})
	if err := writePrivate(filepath.Join(h.config.State, "candidate.json"), raw); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { h.mu.Lock(); defer h.mu.Unlock(); return h.phase == "error" })
	assertBackendReady(t, h, false)
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18081/__dev/status", nil)
	w := httptest.NewRecorder()
	h.Handler().ServeHTTP(w, req)
	var status Status
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &status) != nil || status.LastError == "" || len(status.Logs) == 0 {
		t.Fatalf("退出后端让管理状态或排错日志不可读：HTTP%d %s", w.Code, w.Body.String())
	}
}

func TestControlChecksOriginsAndRedactsBoundedLogs(t *testing.T) {
	h := testHost(t, "127.0.0.1:8080")
	writer := &logWriter{host: h, stream: "backend"}
	_, _ = writer.Write([]byte("API_KEY=secret-value token="))
	_, _ = writer.Write([]byte(h.token + "\n"))
	_, _ = writer.Write([]byte(strings.Repeat("x", 10000) + "\n"))
	for i := 0; i < 320; i++ {
		h.log("air", fmt.Sprintf("line %d", i))
	}
	if len(h.logs) != 300 {
		t.Fatal("日志没有限制条数")
	}
	_, _ = writer.Write([]byte("API_KEY=secret-value token=" + h.token + "\n"))
	for _, input := range []struct {
		method, host, origin string
		want                 int
	}{
		{http.MethodGet, "127.0.0.1:18081", "", 200},
		{http.MethodGet, "evil.example:18081", "", 403},
		{http.MethodPost, "127.0.0.1:18081", "http://evil.example", 403},
		{http.MethodPost, "127.0.0.1:18081", "", 403},
		{http.MethodPost, "127.0.0.1:18081", "http://127.0.0.1:5173", 400},
	} {
		path := "/__dev/status"
		if input.method == http.MethodPost {
			path = "/__dev/scheduler"
		}
		req := httptest.NewRequest(input.method, "http://"+input.host+path, strings.NewReader(`{}`))
		req.Header.Set("Origin", input.origin)
		w := httptest.NewRecorder()
		h.Handler().ServeHTTP(w, req)
		if w.Code != input.want {
			t.Fatalf("本地控制约束失效：%+v HTTP%d", input, w.Code)
		}
		if strings.Contains(w.Body.String(), h.token) || strings.Contains(w.Body.String(), "secret-value") {
			t.Fatal("控制响应泄漏了日志凭据")
		}
	}
	if localURL("https://example.com") || localURL("http://user:pass@127.0.0.1:80") || localURL("http://192.168.1.5:80") {
		t.Fatal("夹具控制允许了非本地目标")
	}
}

func TestConfigurationKeepsFakeModeAndBackendOnLoopback(t *testing.T) {
	for _, config := range []Config{
		{Mode: "fake", Listen: "0.0.0.0:18081", BackendListen: "127.0.0.1:18080"},
		{Mode: "real", Listen: "0.0.0.0:18081", BackendListen: "0.0.0.0:8080"},
	} {
		config.Root, config.State, config.Data = t.TempDir(), t.TempDir(), t.TempDir()
		if _, err := New(config); err == nil {
			t.Fatal("开发控制扩大了监听范围")
		}
	}
	config := airConfig("/repo", "/repo/build/_dev", "/repo/devhost", true)
	if !strings.Contains(config, `entrypoint = ["/usr/bin/true"]`) || !strings.Contains(config, "poll = true") {
		t.Fatal("编译监听启动了后端或没有容器轮询")
	}
}

func TestPrepareTimeoutDoesNotSignalBackend(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/__dev/backend/status":
			writeJSON(w, backendStatus{ReloadWaitMS: 30})
		case "/__dev/backend/prepare-reload":
			<-r.Context().Done()
		default:
			writeJSON(w, map[string]bool{"ok": true})
		}
	}))
	defer backend.Close()
	h := testHost(t, strings.TrimPrefix(backend.URL, "http://"))
	executable, _ := os.Executable()
	events := filepath.Join(t.TempDir(), "events")
	p, err := startChild(executable, []string{"-test.run=^TestDevelopmentChild$", "--", "development-child", events, "backend"}, t.TempDir(), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { p.interrupt(); <-p.done }()
	h.backend = p
	waitFor(t, func() bool { raw, _ := os.ReadFile(events); return strings.Contains(string(raw), "start backend") })
	if err := h.stopBackend(); err == nil {
		t.Fatal("排空超时被当成成功")
	}
	raw, _ := os.ReadFile(events)
	if strings.Contains(string(raw), "interrupt") {
		t.Fatal("排空超时向后端发了信号")
	}
}

func TestLocalControlsForwardOnlyPrivateTokenAndFixtureFaults(t *testing.T) {
	var token string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(controlHeader) != token {
			http.Error(w, "控制凭据错误", 403)
			return
		}
		if r.URL.Path != "/__dev/backend/scheduler" {
			http.NotFound(w, r)
			return
		}
		var command struct {
			Paused bool `json:"paused"`
		}
		if json.NewDecoder(r.Body).Decode(&command) != nil || !command.Paused {
			http.Error(w, "状态错误", 400)
			return
		}
		writeJSON(w, map[string]bool{"paused": true})
	}))
	defer backend.Close()
	h := testHost(t, strings.TrimPrefix(backend.URL, "http://"))
	token = h.token
	if err := h.prepareFixture(); err != nil {
		t.Fatal(err)
	}
	defer h.closeFixture()
	request := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:18081"+path, strings.NewReader(body))
		req.Header.Set("Origin", "http://127.0.0.1:5173")
		w := httptest.NewRecorder()
		h.Handler().ServeHTTP(w, req)
		return w
	}
	if result := request("/__dev/scheduler", `{"paused":true}`); result.Code != 200 || strings.Contains(result.Body.String(), token) {
		t.Fatalf("调度转发失败或泄漏凭据：%s", result.Body.String())
	}
	for _, mode := range []string{"expired", "risk", "missing", "api_error", "normal"} {
		result := request("/__dev/fault", `{"mode":"`+mode+`"}`)
		if result.Code != 200 || h.fixture.State()["fault_mode"] != mode {
			t.Fatalf("夹具故障没有转发生效：%s %s", mode, result.Body.String())
		}
	}
}

func TestBackendWaitAcceptsLongRepresentableDurations(t *testing.T) {
	var wait atomic.Int64
	wait.Store(int64((48 * time.Hour) / time.Millisecond))
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, backendStatus{ReloadWaitMS: wait.Load()}) }))
	defer backend.Close()
	h := testHost(t, strings.TrimPrefix(backend.URL, "http://"))
	if _, err := h.getBackendStatus(); err != nil {
		t.Fatalf("合法的长运行超时被管理器拦截：%v", err)
	}
	wait.Store(int64((1<<63-1)/time.Millisecond) + 1)
	if _, err := h.getBackendStatus(); err == nil {
		t.Fatal("无法表示的超时没有被拒绝")
	}
}

func TestSecondManagerDoesNotOverwriteLiveControlToken(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	h := testHost(t, "127.0.0.1:8080")
	h.config.Listen = listener.Addr().String()
	path := filepath.Join(h.config.State, "control-token")
	if err := os.WriteFile(path, []byte("existing-control-token"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := h.Run(context.Background()); err == nil {
		t.Fatal("第二个管理器占用了已有端口")
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != "existing-control-token" {
		t.Fatal("失败启动覆盖了已有管理器的控制凭据")
	}
}

func TestStoppedBuildDoesNotCompileOrPublish(t *testing.T) {
	state := t.TempDir()
	if err := os.WriteFile(filepath.Join(state, "stop-build"), []byte("stopping"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "candidate.json"), []byte("保留上次候选"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Build(context.Background(), t.TempDir(), state, io.Discard); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(filepath.Join(state, "candidate.json")); err != nil || string(raw) != "保留上次候选" {
		t.Fatal("停止后的编译覆盖了候选")
	}
	if _, err := os.Stat(filepath.Join(state, "candidates")); !os.IsNotExist(err) {
		t.Fatal("停止后的编译仍创建候选目录")
	}
}
