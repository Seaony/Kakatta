package remote

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/json"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/coder/websocket"
)

//go:embed assets
var assets embed.FS

// ControlPage 是单次实验工具的控制页，接口由实验工具自己提供。
func ControlPage() []byte {
	data, _ := assets.ReadFile("assets/control.html")
	style, _ := assets.ReadFile("assets/control.css")
	script, _ := assets.ReadFile("assets/control.js")
	return []byte(strings.NewReplacer("{{style}}", string(style), "{{script}}", string(script)).Replace(string(data)))
}

// ControlPagePolicy 只允许控制页里固定的样式和脚本，不开放任意内联内容。
func ControlPagePolicy() string {
	style, _ := assets.ReadFile("assets/control.css")
	script, _ := assets.ReadFile("assets/control.js")
	hash := func(data []byte) string {
		sum := sha256.Sum256(data)
		return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	}
	return "default-src 'none'; script-src " + hash(script) + "; style-src " + hash(style) + "; img-src data:; connect-src 'self'; frame-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"
}

// Handler 的每一条请求都检查现有会话，空检查器不会放行。
// 挂到子路径时由调用方使用 http.StripPrefix 去掉前缀。
func (s *Session) Handler(authorized func(*http.Request) bool) http.Handler {
	return s.HandlerWithViewer(authorized, nil)
}

// HandlerWithViewer 在升级成功后、转发画面前确认接手，失败只关闭当前连接。
func (s *Session) HandlerWithViewer(authorized func(*http.Request) bool, onAccepted func(context.Context) error) http.Handler {
	files, _ := fs.Sub(assets, "assets")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /vnc", func(w http.ResponseWriter, r *http.Request) { s.serveVNC(w, r, onAccepted) })
	mux.HandleFunc("GET /viewport", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		s.mu.Lock()
		defer s.mu.Unlock()
		json.NewEncoder(w).Encode(s.viewport)
	})
	fileServer := http.FileServerFS(files)
	if s.preview == nil {
		mux.HandleFunc("GET /{$}", viewerDocument)
		mux.Handle("GET /", fileServer)
	} else {
		mux.HandleFunc("GET /{$}", s.previewDocument)
		mux.Handle("GET /preview.js", fileServer)
		mux.Handle("GET /viewer.css", fileServer)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data: blob:; connect-src 'self'; frame-ancestors 'self'; base-uri 'none'; form-action 'self'")
		if authorized == nil || !authorized(r) {
			http.Error(w, viewerText(locales.Select("", r.Header.Get("Accept-Language")), "unauthorized"), http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func originAllowed(r *http.Request) bool {
	origin, err := url.Parse(r.Header.Get("Origin"))
	return err == nil && (origin.Scheme == "http" || origin.Scheme == "https") && strings.EqualFold(origin.Host, r.Host) && origin.User == nil && origin.Path == "" && origin.RawQuery == "" && origin.Fragment == ""
}

func (s *Session) serveVNC(w http.ResponseWriter, r *http.Request, onAccepted func(context.Context) error) {
	if !originAllowed(r) {
		http.Error(w, viewerText(locales.Select("", r.Header.Get("Accept-Language")), "origin_forbidden"), http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stopSession := context.AfterFunc(s.ctx, cancel)
	defer stopSession()
	if s.ctx.Err() != nil {
		cancel()
	}
	if s.preview != nil {
		s.servePreview(ctx, w, r, onAccepted)
		return
	}
	upstream, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp4", s.address)
	if err != nil {
		http.Error(w, viewerText(locales.Select("", r.Header.Get("Accept-Language")), "closed"), http.StatusServiceUnavailable)
		return
	}
	defer upstream.Close()
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	if !s.register(upstream) {
		s.closeViewer(ws)
		return
	}
	defer func() {
		upstream.Close()
		s.closeViewer(ws)
		s.unregister(upstream)
	}()
	if onAccepted != nil {
		if err := onAccepted(ctx); err != nil {
			return
		}
	}
	// 主循环先发送会话结束标记再关连接，不能让取消先掐断握手。
	client := websocket.NetConn(context.WithoutCancel(ctx), ws, websocket.MessageBinary)
	finished := make(chan struct{}, 2)
	go func() { io.Copy(upstream, client); finished <- struct{}{} }()
	go func() { io.Copy(client, upstream); finished <- struct{}{} }()
	completed := 0
	select {
	case <-finished:
		completed++
	case <-ctx.Done():
	}
	upstream.Close()
	s.closeViewer(ws)
	cancel()
	for completed < 2 {
		<-finished
		completed++
	}
}

func (s *Session) closeViewer(ws *websocket.Conn) {
	s.mu.Lock()
	normal := s.normalEnd
	s.mu.Unlock()
	if normal {
		ws.Close(websocket.StatusNormalClosure, "kakatta-session-ended")
	} else {
		ws.CloseNow()
	}
}
