package experimentstate

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

type Protection struct {
	Kind  string
	Until *time.Time
}

// ReadProtection 只读实验记录；正式服务不能替实验工具改写额度或结束会话。
func ReadProtection(dataDir string, now time.Time) (*Protection, error) {
	root := filepath.Join(dataDir, "experiment")
	if info, err := os.Lstat(root); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("检查实验账号目录：%w", err)
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrInvalid
	}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取实验账号目录：%w", err)
	}
	var blocked *Protection
	for _, entry := range entries {
		id, err := strconv.Atoi(entry.Name())
		if err != nil || id < 1 || id > 12767 || strconv.Itoa(id) != entry.Name() {
			continue
		}
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil, ErrInvalid
		}
		dir := filepath.Join(root, entry.Name())
		raw, err := os.ReadFile(filepath.Join(dir, "visits.json"))
		if os.IsNotExist(err) && InitialLogsOnly(dir, ProfileDir(dataDir, id, false), filepath.Join(dataDir, "browser", entry.Name())) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("读取实验账号%d保护记录：%w", id, err)
		}
		state, err := Decode(raw)
		if err != nil {
			return nil, fmt.Errorf("读取实验账号%d保护记录：%w", id, err)
		}
		selectProtection(&blocked, state.protection(now))
	}
	return blocked, nil
}

func (s State) protection(observed time.Time) *Protection {
	now := observed
	if s.HighestAt.After(now) {
		now = s.HighestAt
	}
	if s.InProgress {
		return &Protection{Kind: "in_progress"}
	}
	if s.VerifyPending {
		return &Protection{Kind: "verification"}
	}
	var blocked *Protection
	for _, deadline := range []struct {
		kind string
		at   time.Time
	}{{"denied", s.DeniedUntil}, {"cooldown", s.CooldownUntil}, {"gap", s.EndedAt.Add(time.Duration(s.GapSeconds) * time.Second)}} {
		if now.Before(deadline.at) {
			at := deadline.at.UTC()
			selectProtection(&blocked, &Protection{Kind: deadline.kind, Until: &at})
		}
	}
	return blocked
}

func selectProtection(current **Protection, next *Protection) {
	if next != nil && (*current == nil || (*current).Until != nil && (next.Until == nil || next.Until.After(*(*current).Until))) {
		*current = next
	}
}
