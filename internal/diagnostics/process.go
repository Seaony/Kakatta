package diagnostics

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
)

const maxProcessBytes = 1 << 20

type processLog struct {
	mu       sync.Mutex
	recorder *Recorder
	path     string
	file     *os.File
	pending  []byte
	longLine bool
	closed   bool
}

func (r *Recorder) ProcessLog(name string) (io.WriteCloser, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.err != nil {
		return nil, errors.Join(errors.New("诊断记录不可写"), r.err)
	}
	if name != "chrome" && name != "xvfb" && name != "x11vnc" {
		return nil, errors.New("未知的浏览器诊断进程")
	}
	path := filepath.Join(r.dir, name+".log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil, r.failed(err)
	}
	writer := &processLog{recorder: r, path: path, file: f}
	r.outputs = append(r.outputs, writer)
	return writer, nil
}

func (w *processLog) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, errors.New("进程诊断日志已关闭")
	}
	count := len(data)
	for len(data) > 0 {
		line, rest, newline := bytes.Cut(data, []byte{'\n'})
		data = rest
		if !w.longLine && len(w.pending)+len(line) <= maxEventSize {
			w.pending = append(w.pending, line...)
		} else {
			w.longLine = true
			w.pending = nil
		}
		if newline {
			if err := w.flush(); err != nil {
				return count - len(data), err
			}
		}
	}
	return count, nil
}

func (w *processLog) flush() error {
	line := RedactText(string(w.pending)) + "\n"
	if w.longLine {
		line = "[过长的进程输出已省略]\n"
	}
	w.pending, w.longLine = nil, false
	info, err := w.file.Stat()
	if err == nil && info.Size()+int64(len(line)) > maxProcessBytes {
		err = w.file.Close()
		if err == nil {
			if removeErr := os.Remove(w.path + ".1"); removeErr != nil && !os.IsNotExist(removeErr) {
				err = removeErr
			}
		}
		if err == nil {
			err = os.Rename(w.path, w.path+".1")
		}
		if err == nil {
			var next *os.File
			next, err = os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
			if err == nil {
				w.file = next
			}
		}
	}
	if err == nil {
		_, err = io.WriteString(w.file, line)
	}
	if err == nil {
		err = w.file.Sync()
	}
	if err != nil {
		w.recorder.mu.Lock()
		err = w.recorder.failed(err)
		w.recorder.mu.Unlock()
	}
	return err
}

func (w *processLog) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	var err error
	if len(w.pending) > 0 || w.longLine {
		err = w.flush()
	}
	closeErr := w.file.Close()
	if closeErr != nil {
		w.recorder.mu.Lock()
		w.recorder.failed(closeErr)
		w.recorder.mu.Unlock()
	}
	return errors.Join(err, closeErr)
}
