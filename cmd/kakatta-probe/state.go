package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Seaony/Kakatta/internal/quota"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

// state 是要跨重启保留的东西：访问记录（算额度）、上一轮结束的时间（算最小间隔）、见过的商品（不重复打开详情）。
type state struct {
	path string
	log  func(string, map[string]any)

	Visits     []quota.Visit        `json:"visits"`
	LastRunEnd time.Time            `json:"last_run_end"`
	Seen       map[string]time.Time `json:"seen"`
	Failed     map[string]int       `json:"failed"` // 详情普通失败的次数，满 3 次不再重试（XIANYU.md 6.2 节）

	limits quota.Limits
}

// 见过的商品保留 30 天，足够判断「是不是新的」。
const seenKeep = 30 * 24 * time.Hour

func loadState(path string, limits quota.Limits) (*state, error) {
	s := &state{path: path, limits: limits, Seen: map[string]time.Time{}, Failed: map[string]int{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, s); err != nil {
		return nil, fmt.Errorf("状态文件 %s 坏了：%w", path, err)
	}
	if s.Seen == nil {
		s.Seen = map[string]time.Time{}
	}
	if s.Failed == nil {
		s.Failed = map[string]int{}
	}
	return s, nil
}

// save 先写临时文件再改名，中途断电也不会留下半个文件。
func (s *state) save() error {
	now := time.Now()
	s.Visits = quota.Prune(s.Visits, now)
	for id, t := range s.Seen {
		if now.Sub(t) > seenKeep {
			delete(s.Seen, id)
		}
	}
	for id := range s.Failed {
		if _, ok := s.Seen[id]; ok {
			delete(s.Failed, id)
		}
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Clean(s.path))
}

// Acquire 实现 web.Budget：先检查额度，够就记一次并立即保存，再让调用方访问。
func (s *state) Acquire(kind quota.Kind, what string) error {
	now := time.Now()
	if err := quota.Check(s.Visits, now, kind, s.limits); err != nil {
		return fmt.Errorf("%w：%s（%s）", web.ErrQuota, err, what)
	}
	s.Visits = append(s.Visits, quota.Visit{At: now, Kind: kind})
	if s.log != nil {
		s.log("访问", map[string]any{"种类": string(kind), "动作": what})
	}
	return s.save()
}
