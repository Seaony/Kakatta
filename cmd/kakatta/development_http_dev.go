//go:build kakatta_dev

package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Seaony/Kakatta/internal/auth"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/server"
	"github.com/Seaony/Kakatta/internal/settings"
)

type developmentHandler struct {
	manager        *runtimeManager
	autoLogin      bool
	control        string
	switching      atomic.Bool
	controlMu      sync.Mutex
	previousPaused bool
	authMu         sync.Mutex
	authGeneration string
	session        auth.Session
}

func newDevelopmentHandler(manager *runtimeManager, autoLogin bool, control string) http.Handler {
	return &developmentHandler{manager: manager, autoLogin: autoLogin, control: control}
}

func (d *developmentHandler) graph() *runtimeGraph {
	d.manager.mu.Lock()
	defer d.manager.mu.Unlock()
	return d.manager.graph
}

func (d *developmentHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !developmentLoopback(r.Host) || !developmentLoopback(r.RemoteAddr) {
		http.Error(w, "开发入口仅接受回环访问", http.StatusForbidden)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/__dev/backend/") {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Kakatta-Development-Control")), []byte(d.control)) != 1 {
			http.Error(w, "开发管理凭据无效", http.StatusUnauthorized)
			return
		}
		d.controlRequest(w, r)
		return
	}
	if d.switching.Load() {
		server.WriteJSON(w, r, http.StatusServiceUnavailable, server.ErrorEnvelope{Error: locales.Message{Code: "http.unavailable", Args: map[string]any{}}, Fields: []settings.Issue{}, RequestID: "development-reload"})
		return
	}
	if d.autoLogin && r.Header.Get("Authorization") == "" {
		g := d.graph()
		if g == nil {
			http.Error(w, "开发后端正在启动", http.StatusServiceUnavailable)
			return
		}
		d.authMu.Lock()
		var err error
		if d.authGeneration != g.generation || d.session.Token == "" {
			d.session, err = g.authentication.DevelopmentSession(r.Context())
			d.authGeneration = g.generation
		} else {
			_, err = g.authentication.AuthenticateReadOnly(r.Context(), d.session.Token, auth.WebSession)
			if errors.Is(err, auth.ErrUnauthenticated) {
				d.session, err = g.authentication.DevelopmentSession(r.Context())
			}
		}
		token := d.session.Token
		d.authMu.Unlock()
		if err != nil {
			http.Error(w, "无法建立本机开发会话", http.StatusServiceUnavailable)
			return
		}
		r = r.Clone(r.Context())
		r.Header.Del("Cookie")
		r.AddCookie(&http.Cookie{Name: "kakatta_session", Value: token})
	}
	d.manager.ServeHTTP(w, r)
}

func (d *developmentHandler) controlRequest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	g := d.graph()
	if g == nil {
		http.Error(w, "开发后端正在启动", http.StatusServiceUnavailable)
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/__dev/backend/status":
		wait, err := developmentReloadWait(r.Context(), g)
		if err != nil {
			http.Error(w, "无法读取开发运行超时", http.StatusServiceUnavailable)
			return
		}
		developmentJSON(w, map[string]any{"scheduler_paused": g.core.DevelopmentSchedulerPaused(), "reload_wait_ms": wait.Milliseconds()})
	case r.Method == http.MethodPost && r.URL.Path == "/__dev/backend/scheduler":
		d.controlMu.Lock()
		defer d.controlMu.Unlock()
		if d.switching.Load() {
			http.Error(w, "正在切换代码，暂不能改变调度", http.StatusConflict)
			return
		}
		var body struct {
			Paused *bool `json:"paused"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&body) != nil || body.Paused == nil || decoder.Decode(new(any)) != io.EOF {
			http.Error(w, "调度状态无效", http.StatusBadRequest)
			return
		}
		g.core.SetDevelopmentSchedulerPaused(*body.Paused)
		developmentJSON(w, map[string]bool{"scheduler_paused": *body.Paused})
	case r.Method == http.MethodPost && r.URL.Path == "/__dev/backend/prepare-reload":
		d.controlMu.Lock()
		defer d.controlMu.Unlock()
		if d.switching.Load() {
			developmentJSON(w, map[string]bool{"ready": true})
			return
		}
		d.manager.switchMu.Lock()
		defer d.manager.switchMu.Unlock()
		g = d.graph()
		wait, err := developmentReloadWait(r.Context(), g)
		if err != nil {
			http.Error(w, "无法读取开发运行超时", http.StatusServiceUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), wait)
		defer cancel()
		resume, err := g.core.PrepareDevelopmentReload(ctx)
		if err != nil {
			http.Error(w, "当前运行尚未安全结束，旧服务继续运行", http.StatusConflict)
			return
		}
		d.previousPaused = g.core.DevelopmentSchedulerPaused()
		g.core.SetDevelopmentSchedulerPaused(true)
		d.switching.Store(true)
		if err := d.manager.pause(ctx); err != nil {
			resume()
			g.core.SetDevelopmentSchedulerPaused(d.previousPaused)
			d.manager.resume(g)
			d.switching.Store(false)
			http.Error(w, "网页请求尚未收尾，旧服务继续运行", http.StatusConflict)
			return
		}
		// 自动和网页准入都已关闭，释放业务屏障，让正式停机能继续收尾。
		resume()
		developmentJSON(w, map[string]bool{"ready": true})
	case r.Method == http.MethodPost && r.URL.Path == "/__dev/backend/cancel-reload":
		d.controlMu.Lock()
		defer d.controlMu.Unlock()
		if d.switching.Load() {
			g.core.SetDevelopmentSchedulerPaused(d.previousPaused)
			d.manager.resume(g)
			d.switching.Store(false)
		}
		developmentJSON(w, map[string]bool{"ready": false})
	default:
		http.NotFound(w, r)
	}
}

func developmentReloadWait(ctx context.Context, g *runtimeGraph) (time.Duration, error) {
	snapshot, err := g.preferences.Get(ctx)
	if err != nil {
		return 0, err
	}
	var minutes int64
	if err := json.Unmarshal(snapshot.Values["crawl.run_timeout_minutes"], &minutes); err != nil || minutes <= 0 || minutes > int64((1<<63-1)/time.Minute) {
		return 0, errors.New("运行超时无效")
	}
	return time.Duration(minutes) * time.Minute, nil
}

func developmentJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(value)
}
