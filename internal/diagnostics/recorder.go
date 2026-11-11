package diagnostics

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	keepDays     = 14
	maxLogBytes  = 16 << 20
	maxEventSize = 64 << 10
	maxBundles   = 10
	maxPackBytes = 12 << 20
	maxPackTotal = 64 << 20
)

type Event struct {
	Sequence uint64         `json:"sequence"`
	At       time.Time      `json:"at"`
	Event    string         `json:"event"`
	Fields   map[string]any `json:"fields,omitempty"`
}

type Page struct {
	Screenshot []byte            `json:"-"`
	HTML       string            `json:"-"`
	URL        string            `json:"url"`
	Title      string            `json:"title"`
	Errors     map[string]string `json:"errors,omitempty"`
	LoginPage  bool              `json:"login_page"`
}

type Recorder struct {
	mu       sync.Mutex
	dir      string
	loc      *time.Location
	now      func() time.Time
	sequence uint64
	events   []Event
	err      error
	closed   bool
	outputs  []*processLog
}

func Open(dir, timezone string) (*Recorder, error) {
	loc, err := time.LoadLocation(timezone)
	if err != nil || timezone == "" {
		return nil, errors.New("诊断日志时区无效")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("创建诊断日志目录：%w", err)
	}
	r := &Recorder{dir: dir, loc: loc, now: time.Now}
	data, err := os.ReadFile(filepath.Join(dir, "sequence.json"))
	if err == nil {
		var state struct {
			Version  int    `json:"version"`
			Sequence uint64 `json:"sequence"`
		}
		if json.Unmarshal(data, &state) != nil || state.Version != 1 {
			return nil, errors.New("诊断日志序号文件损坏或版本不受支持")
		}
		r.sequence = state.Sequence
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("读取诊断日志序号：%w", err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "events-*.jsonl"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	for _, path := range files {
		if err := r.readEvents(path); err != nil {
			return nil, err
		}
	}
	if err := r.pruneLogs(0); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Recorder) readEvents(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 4096), maxEventSize+1)
	for scan.Scan() {
		var event Event
		if json.Unmarshal(scan.Bytes(), &event) != nil || event.Sequence == 0 {
			continue
		}
		r.sequence = max(r.sequence, event.Sequence)
		r.remember(event)
	}
	if err := scan.Err(); err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		return err
	}
	last := []byte{0}
	if _, err := f.ReadAt(last, info.Size()-1); err != nil {
		return err
	}
	if last[0] != '\n' {
		// 强杀可能留下半行，先分隔开，不能把下次事件接到坏行后面。
		appendFile, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return err
		}
		_, writeErr := appendFile.Write([]byte{'\n'})
		return errors.Join(writeErr, appendFile.Sync(), appendFile.Close())
	}
	return nil
}

func (r *Recorder) remember(event Event) {
	r.events = append(r.events, event)
	if len(r.events) > 50 {
		r.events = append([]Event(nil), r.events[len(r.events)-50:]...)
	}
}

func (r *Recorder) Event(name string, fields map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errors.New("诊断日志已关闭")
	}
	if r.err != nil {
		return r.err
	}
	data, err := json.Marshal(fields)
	if err != nil {
		return r.failed(err)
	}
	var clean map[string]any
	if err := json.Unmarshal(redactJSON(data), &clean); err != nil {
		return r.failed(err)
	}
	event := Event{Sequence: r.sequence + 1, At: r.now().In(r.loc), Event: RedactText(name), Fields: clean}
	line, err := json.Marshal(event)
	if err != nil || len(line)+1 > maxEventSize {
		return r.failed(errors.Join(errors.New("诊断事件超过单条大小上限"), err))
	}
	if err := r.pruneLogs(int64(len(line) + 1)); err != nil {
		return r.failed(err)
	}
	state, _ := json.Marshal(map[string]any{"version": 1, "sequence": event.Sequence})
	if err := atomicFile(filepath.Join(r.dir, "sequence.json"), state); err != nil {
		return r.failed(err)
	}
	r.sequence = event.Sequence
	path := filepath.Join(r.dir, "events-"+event.At.Format("2006-01-02")+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return r.failed(err)
	}
	_, writeErr := f.Write(append(line, '\n'))
	err = errors.Join(writeErr, f.Sync(), f.Close())
	if err != nil {
		return r.failed(err)
	}
	r.remember(event)
	return nil
}

func (r *Recorder) Events() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	data, _ := json.Marshal(r.events)
	var copy []Event
	json.Unmarshal(data, &copy)
	return copy
}

func (r *Recorder) failed(err error) error {
	if err != nil && r.err == nil {
		r.err = fmt.Errorf("写入本机诊断资料失败：%w", err)
	}
	return r.err
}

func (r *Recorder) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

func (r *Recorder) Close() error {
	r.mu.Lock()
	r.closed = true
	outputs := append([]*processLog(nil), r.outputs...)
	r.mu.Unlock()
	for _, output := range outputs {
		output.Close()
	}
	return r.Err()
}

func (r *Recorder) pruneLogs(extra int64) error {
	files, err := filepath.Glob(filepath.Join(r.dir, "events-*.jsonl"))
	if err != nil {
		return err
	}
	sort.Strings(files)
	var total int64
	for _, path := range files {
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		total += info.Size()
	}
	cutoff := r.now().In(r.loc).AddDate(0, 0, 1-keepDays).Format("2006-01-02")
	for _, path := range files {
		day := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "events-"), ".jsonl")
		if day >= cutoff && total+extra <= maxLogBytes {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		total -= info.Size()
	}
	return nil
}

func atomicFile(path string, data []byte) (err error) {
	f, err := os.CreateTemp(filepath.Dir(path), ".write-")
	if err != nil {
		return err
	}
	defer func() {
		f.Close()
		os.Remove(f.Name())
	}()
	if _, err := io.Copy(f, bytes.NewReader(data)); err != nil {
		return err
	}
	if err := errors.Join(f.Sync(), f.Close()); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
