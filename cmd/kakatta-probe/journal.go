package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// journal 把每一步写成一行 JSON，同时在终端打印一行简短的说明。不能写入接口正文和 Cookie。
type journal struct {
	mu sync.Mutex
	f  *os.File
}

func openJournal(path string) (*journal, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &journal{f: f}, nil
}

func (j *journal) Close() error { return j.f.Close() }

// log 写一条记录。fields 里的键原样写进 JSON，time 和 event 两个键留给记录本身。
func (j *journal) log(event string, fields map[string]any) {
	now := time.Now()
	rec := map[string]any{"time": now.Format(time.RFC3339Nano), "event": event}
	for k, v := range fields {
		rec[k] = v
	}
	line, err := json.Marshal(rec)
	if err != nil {
		line, _ = json.Marshal(map[string]any{"time": rec["time"], "event": event, "记录失败": err.Error()})
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.f.Write(append(line, '\n'))
	fmt.Fprintf(os.Stderr, "%s %s%s\n", now.Format("01-02 15:04:05"), event, brief(fields))
}

// with 返回一个总带上 extra 字段的记录函数，比如每条都带上第几轮。
func (j *journal) with(extra map[string]any) func(string, map[string]any) {
	return func(event string, fields map[string]any) {
		all := make(map[string]any, len(fields)+len(extra))
		for k, v := range extra {
			all[k] = v
		}
		for k, v := range fields {
			all[k] = v
		}
		j.log(event, all)
	}
}

func brief(fields map[string]any) string {
	if len(fields) == 0 {
		return ""
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, " %s=%v", k, fields[k])
	}
	return b.String()
}
