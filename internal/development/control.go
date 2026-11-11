//go:build kakatta_dev

package development

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const controlHeader = "X-Kakatta-Development-Control"

var controlClient = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

type backendStatus struct {
	SchedulerPaused bool  `json:"scheduler_paused"`
	ReloadWaitMS    int64 `json:"reload_wait_ms"`
}

func (h *Host) backendURL() string { return "http://" + h.config.BackendListen }

func (h *Host) backendRequest(ctx context.Context, method, path string, body any, result any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, h.backendURL()+"/__dev/backend/"+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set(controlHeader, h.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := controlClient.Do(req)
	if err != nil {
		return fmt.Errorf("开发后端控制请求未完成：%w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("开发后端拒绝操作（HTTP %d），原运行继续保留", response.StatusCode)
	}
	if result != nil {
		decoder := json.NewDecoder(io.LimitReader(response.Body, 65536))
		if decoder.Decode(result) != nil || decoder.Decode(new(any)) != io.EOF {
			return errors.New("开发后端控制响应无效")
		}
	}
	return nil
}

func (h *Host) getBackendStatus() (backendStatus, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var status backendStatus
	err := h.backendRequest(ctx, http.MethodGet, "status", nil, &status)
	if err == nil && (status.ReloadWaitMS <= 0 || status.ReloadWaitMS > int64((1<<63-1)/time.Millisecond)) {
		err = errors.New("开发后端没有提供有效的排空等待上限")
	}
	if err == nil {
		h.mu.Lock()
		h.paused = status.SchedulerPaused
		h.backendReady = true
		h.mu.Unlock()
	}
	return status, err
}

func (h *Host) stopBackend() error {
	if h.backend == nil {
		h.setBackendReady(false)
		return nil
	}
	select {
	case <-h.backend.done:
		h.setBackendReady(false)
		h.backend = nil
		return nil
	default:
	}
	status, err := h.getBackendStatus()
	if err != nil {
		return err
	}
	wait := time.Duration(status.ReloadWaitMS) * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	var result struct {
		Ready bool `json:"ready"`
	}
	err = h.backendRequest(ctx, http.MethodPost, "prepare-reload", nil, &result)
	if err != nil || !result.Ready {
		release, done := context.WithTimeout(context.Background(), 2*time.Second)
		defer done()
		_ = h.backendRequest(release, http.MethodPost, "cancel-reload", nil, nil)
		if err == nil {
			err = errors.New("开发后端尚未排空，原运行继续保留")
		}
		return err
	}
	h.setBackendReady(false)
	if err := stopIdleChild(h.backend, wait); err != nil {
		return err
	}
	h.backend = nil
	return nil
}

func (h *Host) waitReady() error {
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := h.getBackendStatus(); err == nil {
			return nil
		}
		select {
		case <-h.backend.done:
			h.setBackendReady(false)
			return errors.New("新后端启动时退出，请检查后端日志")
		case <-time.After(100 * time.Millisecond):
		}
	}
	return errors.New("新后端仍未就绪，已保留进程供排错；按 Ctrl+C 安全退出")
}

func (h *Host) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /__dev/status", func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		status := Status{Mode: h.config.Mode, Phase: h.phase, Generation: h.generation, SchedulerPaused: h.paused, BackendReady: h.backendReady, LastError: h.lastError, Logs: append([]Log{}, h.logs...)}
		h.mu.Unlock()
		if h.fixture != nil {
			status.Fixture = h.fixture.State()
		}
		writeJSON(w, status)
	})
	mux.HandleFunc("POST /__dev/scheduler", func(w http.ResponseWriter, r *http.Request) {
		var command struct {
			Paused *bool `json:"paused"`
		}
		if !readJSON(w, r, &command) {
			return
		}
		if command.Paused == nil {
			http.Error(w, "请指定调度状态", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if err := h.backendRequest(ctx, http.MethodPost, "scheduler", command, nil); err != nil {
			http.Error(w, h.safe(err.Error()), http.StatusServiceUnavailable)
			return
		}
		h.mu.Lock()
		h.paused = *command.Paused
		h.mu.Unlock()
		writeJSON(w, map[string]bool{"paused": *command.Paused})
	})
	mux.HandleFunc("POST /__dev/fault", func(w http.ResponseWriter, r *http.Request) {
		var command struct {
			Mode string `json:"mode"`
		}
		if !readJSON(w, r, &command) {
			return
		}
		switch command.Mode {
		case "normal", "expired", "risk", "missing", "api_error":
		default:
			http.Error(w, "假站点故障模式无效", http.StatusBadRequest)
			return
		}
		if h.fixtureURL == "" {
			http.Error(w, "真实模式没有本地故障夹具", http.StatusConflict)
			return
		}
		raw, _ := json.Marshal(map[string]string{"action": "fault", "mode": command.Mode})
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(h.fixtureURL, "/")+"/control", bytes.NewReader(raw))
		if err != nil {
			http.Error(w, "本地夹具控制地址无效", http.StatusServiceUnavailable)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(controlHeader, h.token)
		response, err := controlClient.Do(req)
		if err != nil {
			http.Error(w, "本地夹具暂未就绪", http.StatusServiceUnavailable)
			return
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			http.Error(w, "本地夹具未接受故障模式", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !localHost(r.Host) || r.Method != http.MethodGet && !h.localOrigin(r.Header.Get("Origin")) {
			http.Error(w, "开发控制只接受本地网页操作", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func localHost(hostport string) bool {
	host, port, err := net.SplitHostPort(hostport)
	return err == nil && (host == "127.0.0.1" || host == "localhost" || host == "::1") && (port == "5173" || port == "18081")
}

func localURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	ip := net.ParseIP(u.Hostname())
	return u.Hostname() == "localhost" || ip != nil && ip.IsLoopback()
}

func (h *Host) localOrigin(raw string) bool {
	origin, err := url.Parse(raw)
	return err == nil && origin.Scheme == "http" && localHost(origin.Host) && origin.Path == "" && origin.RawQuery == "" && origin.Fragment == "" && origin.User == nil
}

func readJSON(w http.ResponseWriter, r *http.Request, into any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(into) != nil || decoder.Decode(new(any)) != io.EOF {
		http.Error(w, "开发控制参数无效", http.StatusBadRequest)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(value)
}
