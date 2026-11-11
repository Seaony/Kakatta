package remote

import (
	"context"
	"errors"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/coder/websocket"
)

const previewInterval = time.Second

var previewPage = template.Must(template.ParseFS(assets, "assets/preview.html"))
var errPreviewFrame = errors.New("读不到备用画面")

type vncStartError struct {
	reason string
	cause  error
}

func (e *vncStartError) Error() string { return previewText(locales.ZhCN, e.reason, nil) }
func (e *vncStartError) Unwrap() error { return e.cause }

type readOnlyPreview struct {
	reason     string
	noticeCode string
	capture    func(context.Context) ([]byte, error)
	mu         sync.Mutex
	next       time.Time
	last       []byte
	lastErr    error
}

// StartWithFallback 只在 x11vnc 启动失败时，改用当前浏览器的只读画面。
func StartWithFallback(ctx context.Context, display string, capture func(context.Context) ([]byte, error)) (*Session, error) {
	return startWithFallback(ctx, display, capture, Start)
}

func StartWithFallbackLog(ctx context.Context, display string, capture func(context.Context) ([]byte, error), output io.Writer) (*Session, error) {
	return startWithFallback(ctx, display, capture, func(ctx context.Context, display string) (*Session, error) {
		return StartWithLog(ctx, display, output)
	})
}

func startWithFallback(ctx context.Context, display string, capture func(context.Context) ([]byte, error), start func(context.Context, string) (*Session, error)) (*Session, error) {
	view, err := start(ctx, display)
	var failure *vncStartError
	if view != nil || capture == nil || !errors.As(err, &failure) {
		return view, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	slog.Warn("远程画面切换到只读备用", "原因", failure.Error())
	lifetime, cancel := context.WithCancel(ctx)
	return &Session{ctx: lifetime, cancel: cancel, preview: &readOnlyPreview{reason: failure.reason, capture: capture}}, nil
}

func (p *readOnlyPreview) image(ctx context.Context) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	now := time.Now()
	if now.Before(p.next) {
		return p.last, p.lastErr
	}
	p.next = now.Add(previewInterval)
	frame, err := p.capture(ctx)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || len(frame) == 0 || len(frame) > 2<<20 {
		p.last, p.lastErr = nil, errPreviewFrame
	} else {
		p.last, p.lastErr = frame, nil
	}
	return p.last, p.lastErr
}

type previewCopy struct {
	Lang                                locales.Language
	Title, Heading, Help, Alt           string
	Connecting, Connected, Disconnected string
}

func previewText(lang locales.Language, code string, args map[string]any) string {
	text, _ := locales.Format(lang, locales.Message{Code: "remote.preview." + code, Args: args})
	return text
}

func (s *Session) previewDocument(w http.ResponseWriter, r *http.Request) {
	lang := locales.Select("", r.Header.Get("Accept-Language"))
	var document struct {
		Current previewCopy
		Copies  []previewCopy
	}
	for _, copyLang := range []locales.Language{locales.ZhCN, locales.En} {
		text := func(code string) string { return previewText(copyLang, code, nil) }
		copy := previewCopy{
			Lang: copyLang, Title: text("title"), Heading: text("heading"), Alt: text("alt"),
			Help:       previewText(copyLang, "help", map[string]any{"reason": text(s.preview.reason)}),
			Connecting: text("connecting"), Connected: text("connected"), Disconnected: text("disconnected"),
		}
		if s.preview.noticeCode != "" {
			copy.Help = text(s.preview.noticeCode)
		}
		document.Copies = append(document.Copies, copy)
		if copyLang == lang {
			document.Current = copy
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = previewPage.Execute(w, document)
}

func (s *Session) servePreview(ctx context.Context, w http.ResponseWriter, r *http.Request, onAccepted func(context.Context) error) {
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	s.mu.Lock()
	if s.closed || ctx.Err() != nil {
		s.mu.Unlock()
		ws.CloseNow()
		return
	}
	s.readers++
	s.active.Add(1)
	s.mu.Unlock()
	defer func() {
		ws.CloseNow()
		s.mu.Lock()
		s.readers--
		if s.readers == 0 {
			s.preview.mu.Lock()
			s.preview.last, s.preview.lastErr = nil, nil
			s.preview.mu.Unlock()
		}
		s.mu.Unlock()
		s.active.Done()
	}()

	// 备用连接拒绝客户端发来的数据，只发送截图。
	ctx = ws.CloseRead(ctx)
	ticker := time.NewTicker(previewInterval)
	defer ticker.Stop()
	for {
		frame, err := s.preview.image(ctx)
		if err != nil {
			return
		}
		if len(frame) != 0 {
			if onAccepted != nil {
				if err := onAccepted(ctx); err != nil {
					return
				}
				onAccepted = nil
			}
			writeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			err := ws.Write(writeCtx, websocket.MessageBinary, frame)
			cancel()
			if err != nil {
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
