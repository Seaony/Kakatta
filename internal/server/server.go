// Package server 提供网页接口、鉴权和实时事件。
package server

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"sync"
	"time"

	"github.com/Seaony/Kakatta/api"
	"github.com/Seaony/Kakatta/internal/auth"
	"github.com/Seaony/Kakatta/internal/settings"
)

type SettingsStore interface {
	Get(context.Context) (settings.Snapshot, error)
	Apply(context.Context, settings.Values) (settings.Snapshot, error)
	Definitions() []settings.Definition
}

type LoginFailureEvent struct {
	Active bool
	Limit  int
	IP     string
}

type Dependencies struct {
	Auth         *auth.Service
	Settings     SettingsStore
	Health       func(context.Context) error
	Web          fs.FS
	Now          func() time.Time
	LoginFailure func(context.Context, LoginFailureEvent) error
}

type Server struct {
	auth      *auth.Service
	settings  SettingsStore
	health    func(context.Context) error
	web       fs.FS
	now       func() time.Time
	mux       *http.ServeMux
	cross     *http.CrossOriginProtection
	limiter   *loginLimiter
	broker    *eventBroker
	closed    chan struct{}
	closeOnce sync.Once
	heartbeat time.Duration
}

type contextKey uint8

const (
	requestKey contextKey = iota
	identityKey
)

type requestState struct {
	id       string
	peer     requestPeer
	settings settings.Snapshot
}

func New(deps Dependencies) (*Server, error) {
	if deps.Auth == nil || deps.Settings == nil || deps.Health == nil || deps.LoginFailure == nil {
		return nil, errors.New("服务端依赖不完整")
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	s := &Server{
		auth: deps.Auth, settings: deps.Settings, health: deps.Health,
		web: deps.Web, now: deps.Now, mux: http.NewServeMux(),
		cross: http.NewCrossOriginProtection(), closed: make(chan struct{}),
		heartbeat: 15 * time.Second, broker: newEventBroker(),
		limiter: newLoginLimiter(deps.Now, deps.LoginFailure),
	}
	s.mux.HandleFunc("GET /api/v1/setup", s.setupStatus)
	s.mux.HandleFunc("POST /api/v1/setup", s.setup)
	s.mux.HandleFunc("POST /api/v1/auth/login", s.login)
	s.mux.HandleFunc("POST /api/v1/auth/totp", s.loginTOTP)
	s.RegisterProtected("POST /api/v1/auth/logout", http.HandlerFunc(s.logout))
	s.RegisterProtected("GET /api/v1/me", http.HandlerFunc(s.me))
	s.RegisterProtected("GET /api/v1/settings", http.HandlerFunc(s.getSettings))
	s.RegisterProtected("PATCH /api/v1/settings", http.HandlerFunc(s.patchSettings))
	s.RegisterProtected("GET /api/v1/settings/definitions", http.HandlerFunc(s.settingDefinitions))
	s.RegisterProtected("GET /api/v1/security/tokens", http.HandlerFunc(s.tokens))
	s.RegisterProtected("POST /api/v1/security/tokens", http.HandlerFunc(s.createToken))
	s.RegisterProtected("DELETE /api/v1/security/tokens/{id}", http.HandlerFunc(s.revokeToken))
	s.RegisterProtected("POST /api/v1/security/password", http.HandlerFunc(s.changePassword))
	s.RegisterProtected("POST /api/v1/security/totp/setup", http.HandlerFunc(s.beginTOTP))
	s.RegisterProtected("POST /api/v1/security/totp/enable", http.HandlerFunc(s.enableTOTP))
	s.RegisterProtected("POST /api/v1/security/totp/disable", http.HandlerFunc(s.disableTOTP))
	s.RegisterProtected("GET /api/v1/events", http.HandlerFunc(s.events))
	s.RegisterProtected("GET /api/v1/openapi.json", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(api.JSON())
	}))
	s.mux.HandleFunc("/", s.static)
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	state := &requestState{id: rand.Text()}
	r = r.WithContext(context.WithValue(r.Context(), requestKey, state))
	w.Header().Set("X-Request-Id", state.id)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	if r.Body != nil && r.Body != http.NoBody {
		// 返回后标准库还会排空请求体，期限要保留到整个请求结束。
		controller := http.NewResponseController(w)
		if err := controller.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			w.Header().Set("Connection", "close")
			WriteError(w, r, http.StatusServiceUnavailable, message("http.unavailable"), nil)
			return
		}
	}
	select {
	case <-s.closed:
		WriteError(w, r, http.StatusServiceUnavailable, message("http.unavailable"), nil)
		return
	default:
	}
	if r.URL.Path == "/healthz" {
		s.healthCheck(w, r)
		return
	}
	values, err := s.settings.Get(r.Context())
	if err != nil {
		WriteError(w, r, http.StatusServiceUnavailable, message("http.unavailable"), nil)
		return
	}
	var trusted []string
	if err := json.Unmarshal(values.Values["security.trusted_proxies"], &trusted); err != nil {
		WriteError(w, r, http.StatusServiceUnavailable, message("http.unavailable"), nil)
		return
	}
	state.settings = values
	state.peer, err = resolvePeer(r, trusted)
	if err != nil {
		WriteError(w, r, http.StatusBadRequest, message("http.invalid_forwarded"), nil)
		return
	}
	check := r.Clone(r.Context())
	check.Host = state.peer.Host
	if len(r.Header.Values("Origin")) > 1 || len(r.Header.Values("Sec-Fetch-Site")) > 1 || s.cross.Check(check) != nil {
		WriteError(w, r, http.StatusForbidden, message("http.forbidden"), nil)
		return
	}
	s.mux.ServeHTTP(w, r)
}

// RegisterProtected 让后续业务接口走同一套鉴权和跨站检查。
func (s *Server) RegisterProtected(pattern string, handler http.Handler) {
	s.mux.Handle(pattern, s.authenticated(handler))
}

func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		close(s.closed)
		s.broker.close()
	})
	return nil
}

func (s *Server) healthCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		WriteError(w, r, http.StatusMethodNotAllowed, message("http.method_not_allowed"), nil)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	status, value := http.StatusOK, "ok"
	if s.health(ctx) != nil {
		status, value = http.StatusServiceUnavailable, "unavailable"
	}
	WriteJSON(w, r, status, struct {
		Status string `json:"status"`
	}{value})
}

func IdentityFromContext(ctx context.Context) (auth.Identity, bool) {
	identity, ok := ctx.Value(identityKey).(auth.Identity)
	return identity, ok
}

func RequestID(ctx context.Context) string {
	if state, ok := ctx.Value(requestKey).(*requestState); ok {
		return state.id
	}
	return ""
}

func stateOf(r *http.Request) *requestState {
	state, _ := r.Context().Value(requestKey).(*requestState)
	return state
}
