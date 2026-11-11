package experimentstate

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func committedState(t *testing.T) ([]byte, State) {
	t.Helper()
	raw, err := os.ReadFile("testdata/visits-5709c5c.json")
	if err != nil {
		t.Fatal(err)
	}
	state, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	return raw, state
}

func TestCommittedVisitFormatPreservesProtectionAndQuota(t *testing.T) {
	_, state := committedState(t)
	now := time.Date(2035, 11, 10, 12, 0, 0, 0, time.UTC)
	if state.Version != Version || len(state.Visits) != 3 || state.Visits[0].Kind != "search" || state.Visits[2].Kind != "detail" || !state.HighestAt.Equal(now) || state.GapSeconds != 180 || !state.CooldownUntil.Equal(now.Add(2*time.Hour)) || state.VerifyLevel != 1 {
		t.Fatalf("旧版本记录被遗漏或清零：%+v", state)
	}
	if blocked := state.protection(now.Add(-time.Hour)); blocked == nil || blocked.Kind != "cooldown" || blocked.Until == nil || !blocked.Until.Equal(state.CooldownUntil) {
		t.Fatalf("倒退时钟改变了旧冷却保护：%+v", blocked)
	}
}

func TestReadProtectionUsesAllRecordsWithoutChangingThem(t *testing.T) {
	data := t.TempDir()
	_, first := committedState(t)
	for id, state := range map[string]State{"1": first, "9": first} {
		dir := filepath.Join(data, "experiment", id)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if id == "9" {
			state.DeniedUntil = state.HighestAt.Add(24 * time.Hour)
		}
		raw, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "visits.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	lastPath := filepath.Join(data, "experiment", "9", "visits.json")
	before, err := os.ReadFile(lastPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, observed := range []time.Time{first.HighestAt.Add(-time.Hour), first.HighestAt.Add(time.Hour), first.HighestAt.Add(23 * time.Hour)} {
		block, err := ReadProtection(data, observed)
		if err != nil || block == nil || block.Kind != "denied" || block.Until == nil || !block.Until.Equal(first.HighestAt.Add(24*time.Hour)) {
			t.Fatalf("没有取所有实验账号里最晚的保护：%+v %v", block, err)
		}
	}
	if block, err := ReadProtection(data, first.HighestAt.Add(24*time.Hour)); err != nil || block != nil {
		t.Fatalf("保护到期后没有恢复：%+v %v", block, err)
	}
	if after, err := os.ReadFile(lastPath); err != nil || !bytes.Equal(before, after) {
		t.Fatal("正式服务只读检查改写了实验记录")
	}
	if err := os.Remove(lastPath); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadProtection(data, first.HighestAt.Add(48*time.Hour)); err == nil {
		t.Fatal("记录丢失不能当作额度恢复")
	}
}

func TestReadProtectionKeepsPendingStateAndRejectsInvalidRecords(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*State)
		kind string
	}{
		{"运行中", func(s *State) { s.InProgress = true }, "in_progress"},
		{"待验证", func(s *State) { s.VerifyPending = true }, "verification"},
		{"未来格式", func(s *State) { s.Version++ }, ""},
		{"缺时钟", func(s *State) { s.HighestAt = time.Time{} }, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := t.TempDir()
			_, state := committedState(t)
			now := state.HighestAt.Add(48 * time.Hour)
			test.edit(&state)
			raw, err := json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(data, "experiment", "1")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "visits.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			block, err := ReadProtection(data, now)
			if test.kind == "" {
				if err == nil {
					t.Fatal("损坏或未来记录被接受")
				}
			} else if err != nil || block == nil || block.Kind != test.kind || block.Until != nil {
				t.Fatalf("未解决的实验状态不能按时间自动放行：%+v %v", block, err)
			}
		})
	}
	data := t.TempDir()
	dir := filepath.Join(data, "experiment-fixture", "1")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "visits.json"), []byte("坏的假页记录"), 0600); err != nil {
		t.Fatal(err)
	}
	if block, err := ReadProtection(data, time.Now()); err != nil || block != nil {
		t.Fatalf("本地假页记录不应影响正式账号：%+v %v", block, err)
	}
}
