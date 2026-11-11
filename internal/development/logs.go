//go:build kakatta_dev

package development

import (
	"bytes"
	"strings"
	"sync"
	"time"

	"github.com/Seaony/Kakatta/internal/diagnostics"
)

type Log struct {
	Seq    int64  `json:"seq"`
	At     int64  `json:"at"`
	Stream string `json:"stream"`
	Text   string `json:"text"`
}

type logWriter struct {
	host     *Host
	stream   string
	mu       sync.Mutex
	pending  []byte
	dropping bool
}

func (w *logWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(data)
	for len(data) > 0 {
		end := bytes.IndexByte(data, '\n')
		part := data
		if end >= 0 {
			part = data[:end]
		}
		if len(w.pending)+len(part) > 8192 {
			w.pending, w.dropping = nil, true
		}
		if !w.dropping {
			w.pending = append(w.pending, part...)
		}
		if end < 0 {
			break
		}
		w.flushLocked()
		data = data[end+1:]
	}
	return n, nil
}

func (w *logWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.pending) > 0 || w.dropping {
		w.flushLocked()
	}
}

func (w *logWriter) flushLocked() {
	if w.dropping {
		w.host.log(w.stream, "日志过长，已省略这一行")
	} else {
		w.host.log(w.stream, string(w.pending))
	}
	w.pending, w.dropping = nil, false
}

func (h *Host) safe(text string) string {
	for _, secret := range h.secrets {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "[已隐去]")
		}
	}
	return diagnostics.RedactText(text)
}

func (h *Host) log(stream, text string) {
	text = h.safe(strings.TrimSpace(text))
	if text == "" {
		return
	}
	h.mu.Lock()
	h.seq++
	h.logs = append(h.logs, Log{Seq: h.seq, At: time.Now().UnixMilli(), Stream: stream, Text: text})
	if len(h.logs) > 300 {
		h.logs = append([]Log{}, h.logs[len(h.logs)-300:]...)
	}
	h.mu.Unlock()
	if h.config.Output != nil {
		h.outputMu.Lock()
		_, _ = h.config.Output.Write([]byte("[" + stream + "] " + text + "\n"))
		h.outputMu.Unlock()
	}
}
