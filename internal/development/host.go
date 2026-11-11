//go:build kakatta_dev

package development

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Seaony/Kakatta/internal/devfixture"
)

type Config struct {
	Mode, Root, State, Data, Listen, BackendListen, ProxyListen, Air, Fixture string
	Poll                                                                      bool
	Output                                                                    io.Writer
}

type Status struct {
	Mode            string         `json:"mode"`
	Phase           string         `json:"phase"`
	Generation      int64          `json:"generation"`
	SchedulerPaused bool           `json:"scheduler_paused"`
	BackendReady    bool           `json:"backend_ready"`
	LastError       string         `json:"last_error"`
	Logs            []Log          `json:"logs"`
	Fixture         map[string]any `json:"fixture,omitempty"`
}

type Host struct {
	config             Config
	mu, outputMu       sync.Mutex
	phase, lastError   string
	generation, seq    int64
	paused             bool
	backendReady       bool
	logs               []Log
	secrets            []string
	token, fixturePath string
	fixture            *devfixture.Fixture
	fixtureServer      *http.Server
	fixtureURL         string
	backend, air       *child
	activeBinary       string
	seenCandidate      string
	start              func(string, []string, string, io.Writer) (*child, error)
	stop               chan struct{}
}

func New(config Config) (*Host, error) {
	if config.Mode != "fake" && config.Mode != "real" {
		return nil, errors.New("开发模式只能是 fake 或 real")
	}
	for _, target := range []*string{&config.Root, &config.State, &config.Data} {
		absolute, err := filepath.Abs(*target)
		if err != nil {
			return nil, err
		}
		*target = absolute
	}
	if !loopbackAddress(config.BackendListen) || config.Mode == "fake" && (!loopbackAddress(config.Listen) || config.ProxyListen != "") {
		return nil, errors.New("假站点模式只监听本机回环地址，后端始终只监听回环地址")
	}
	if err := os.MkdirAll(config.State, 0700); err != nil {
		return nil, err
	}
	token := rand.Text()
	return &Host{config: config, phase: "starting", paused: config.Mode == "real", token: token,
		secrets: []string{token}, logs: []Log{}, start: startChild, stop: make(chan struct{}, 1)}, nil
}

func loopbackAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	ip := net.ParseIP(host)
	return err == nil && ip != nil && ip.IsLoopback()
}

func (h *Host) RequestStop() {
	select {
	case h.stop <- struct{}{}:
	default:
	}
}

func (h *Host) setPhase(phase string, err error) {
	h.mu.Lock()
	h.phase = phase
	if err != nil {
		h.lastError = h.safe(err.Error())
	} else {
		h.lastError = ""
	}
	h.mu.Unlock()
	if err != nil {
		h.log("manager", err.Error())
	}
}

func (h *Host) setBackendReady(ready bool) {
	h.mu.Lock()
	h.backendReady = ready
	h.mu.Unlock()
}

func (h *Host) Run(ctx context.Context) error {
	listener, err := net.Listen("tcp", h.config.Listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	if err := writePrivate(filepath.Join(h.config.State, "control-token"), []byte(h.token)); err != nil {
		return err
	}
	if h.config.ProxyListen != "" {
		proxyListener, err := net.Listen("tcp", h.config.ProxyListen)
		if err != nil {
			return err
		}
		target, _ := url.Parse(h.backendURL())
		proxy := httputil.NewSingleHostReverseProxy(target)
		proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, "开发后端暂未就绪", http.StatusServiceUnavailable)
		}
		proxyServer := &http.Server{Handler: proxy, ReadHeaderTimeout: 5 * time.Second}
		go proxyServer.Serve(proxyListener)
		defer proxyServer.Close()
	}
	if err := h.prepareFixture(); err != nil {
		return err
	}
	defer h.closeFixture()
	server := &http.Server{Handler: h.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go server.Serve(listener)
	defer server.Close()
	_ = os.Remove(filepath.Join(h.config.State, "candidate.json"))
	if err := os.Remove(filepath.Join(h.config.State, "stop-build")); err != nil && !os.IsNotExist(err) {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	configPath := filepath.Join(h.config.State, "air.toml")
	if err := writePrivate(configPath, []byte(airConfig(h.config.Root, h.config.State, executable, h.config.Poll))); err != nil {
		return err
	}
	h.air, err = h.start(h.config.Air, []string{"-c", configPath}, h.config.Root, &logWriter{host: h, stream: "air"})
	if err != nil {
		return err
	}
	h.setPhase("building", nil)
	h.log("manager", "开发管理器已启动，等待后端编译；按 Ctrl+C 安全退出")
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	contextDone := ctx.Done()
	stopping := false
	airExited := false
	for {
		select {
		case <-contextDone:
			contextDone = nil
			h.RequestStop()
		case <-h.stop:
			stopping = true
			h.setPhase("stopping", nil)
			if err := h.stopBackend(); err != nil {
				h.setPhase("shutdown_blocked", fmt.Errorf("%w；尚未退出，当前运行结束后再按 Ctrl+C 重试", err))
				continue
			}
			if err := h.finishBuild(); err != nil {
				h.setPhase("shutdown_blocked", err)
				continue
			}
			if err := stopIdleChild(h.air, 5*time.Second); err != nil {
				h.setPhase("shutdown_blocked", err)
				continue
			}
			h.log("manager", "后端与编译监听已退出，正在关闭本地夹具")
			return nil
		case <-ticker.C:
			if h.backend != nil {
				select {
				case <-h.backend.done:
					h.setBackendReady(false)
					h.backend = nil
					h.setPhase("error", errors.New("开发后端已退出；修正代码后保存可重新启动"))
				default:
				}
			}
			select {
			case <-h.air.done:
				if !airExited {
					h.setPhase("error", errors.New("编译监听已退出；请按 Ctrl+C 安全退出后重新启动"))
					airExited = true
				}
			default:
			}
			if !stopping {
				h.checkCandidate()
			}
		}
	}
}

func (h *Host) prepareFixture() error {
	if h.config.Fixture != "" {
		h.fixturePath = h.config.Fixture
		raw, err := os.ReadFile(h.fixturePath)
		if err != nil {
			return err
		}
		var metadata devfixture.Metadata
		if err := json.Unmarshal(raw, &metadata); err != nil {
			return errors.New("本地夹具文件格式无效")
		}
		h.fixtureURL = metadata.ControlURL
		if !localURL(h.fixtureURL) {
			return errors.New("夹具控制地址必须是本地回环 HTTP 地址")
		}
		h.secrets = append(h.secrets, metadata.ModelKey)
		return nil
	}
	if h.config.Mode != "fake" {
		return nil
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	h.fixtureURL = "http://" + listener.Addr().String()
	h.fixture = devfixture.New(context.Background(), h.fixtureURL)
	h.fixture.EnableDevelopmentAutoLogin()
	fixtureHandler := h.fixture.Handler()
	h.fixtureServer = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get(controlHeader)), []byte(h.token)) != 1 {
			http.Error(w, "本地夹具只接受管理进程控制", http.StatusForbidden)
			return
		}
		fixtureHandler.ServeHTTP(w, r)
	}), ReadHeaderTimeout: 5 * time.Second}
	go h.fixtureServer.Serve(listener)
	metadata := h.fixture.Metadata()
	h.secrets = append(h.secrets, metadata.ModelKey)
	raw, err := json.Marshal(metadata)
	if err != nil {
		h.closeFixture()
		return err
	}
	h.fixturePath = filepath.Join(h.config.State, "fixture.json")
	if err := writePrivate(h.fixturePath, raw); err != nil {
		h.closeFixture()
		return err
	}
	return nil
}

func (h *Host) closeFixture() {
	if h.fixtureServer != nil {
		_ = h.fixtureServer.Close()
	}
	if h.fixture != nil {
		h.fixture.Close()
	}
}

func (h *Host) checkCandidate() {
	raw, err := os.ReadFile(filepath.Join(h.config.State, "candidate.json"))
	if err != nil {
		return
	}
	var next candidate
	if json.Unmarshal(raw, &next) != nil || next.Generation == "" || next.Generation == h.seenCandidate {
		return
	}
	h.seenCandidate = next.Generation
	if next.Error != "" {
		h.setPhase("reload_failed", errors.New(next.Error))
		return
	}
	if filepath.Dir(next.Binary) != filepath.Join(h.config.State, "candidates") {
		h.setPhase("reload_failed", errors.New("编译候选路径无效"))
		return
	}
	info, err := os.Lstat(next.Binary)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		h.setPhase("reload_failed", errors.New("编译候选不可执行"))
		return
	}
	h.setPhase("reloading", nil)
	if err := h.stopBackend(); err != nil {
		_ = os.Remove(next.Binary)
		h.setPhase("reload_failed", err)
		return
	}
	args := []string{"dev", "--mode", h.config.Mode, "--data", h.config.Data, "--listen", h.config.BackendListen, "--control-token-file", filepath.Join(h.config.State, "control-token")}
	if h.fixturePath != "" {
		args = append(args, "--fixture", h.fixturePath)
	}
	h.setBackendReady(false)
	h.backend, err = h.start(next.Binary, args, h.config.Root, &logWriter{host: h, stream: "backend"})
	if err != nil {
		_ = os.Remove(next.Binary)
		h.setPhase("error", err)
		return
	}
	if h.activeBinary != "" && h.activeBinary != next.Binary {
		_ = os.Remove(h.activeBinary)
	}
	h.activeBinary = next.Binary
	if err := h.waitReady(); err != nil {
		h.setPhase("error", err)
		return
	}
	h.mu.Lock()
	h.generation++
	h.mu.Unlock()
	h.setPhase("running", nil)
}

func stopIdleChild(process *child, wait time.Duration) error {
	if process == nil {
		return nil
	}
	if err := process.interrupt(); err != nil {
		return err
	}
	select {
	case <-process.done:
		return nil
	case <-time.After(wait):
		return errors.New("子进程尚未退出，保留管理器继续等待；不会强制结束")
	}
}
