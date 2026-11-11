package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/diagnostics"
	"github.com/Seaony/Kakatta/internal/experimentstate"
)

func TestCommittedExperimentFormatsRemainReadable(t *testing.T) {
	settingsRaw, err := os.ReadFile("../../internal/experimentstate/testdata/settings-5709c5c.json")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "settings.json")
	if err = os.WriteFile(path, settingsRaw, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := loadSettings(path)
	if err != nil || s.Version != 1 {
		t.Fatalf("旧版无版本号设置不能读取：%+v %v", s, err)
	}
	want := testSettings()
	if !reflect.DeepEqual(s, want) {
		t.Fatalf("旧版设置发生改变：%+v，期望%+v", s, want)
	}
	if after, err := os.ReadFile(path); err != nil || !bytes.Equal(after, settingsRaw) {
		t.Fatal("兼容读取覆盖了作者的旧设置")
	}
	visitsRaw, err := os.ReadFile("../../internal/experimentstate/testdata/visits-5709c5c.json")
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	dir := filepath.Join(base, "experiment", "1")
	if err = os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "visits.json"), visitsRaw, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "initialized"), []byte("1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := experimentstate.Decode(visitsRaw)
	if err != nil {
		t.Fatal(err)
	}
	j, err := openJournalAt(dir, experimentstate.ProfileDir(base, 1, false), s, func() time.Time { return before.HighestAt })
	if err != nil {
		t.Fatalf("当前已提交版本生成的访问记录不能升级：%v", err)
	}
	defer j.unlock()
	if !reflect.DeepEqual(j.state, before) {
		t.Fatalf("升级清掉了额度或账号保护：%+v，原来%+v", j.state, before)
	}
}

func TestSettingsWriteVersionAndRejectFutureVersionWithoutOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if _, err := loadSettings(path); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err = json.Unmarshal(raw, &fields); err != nil || fields["version"] != float64(1) {
		t.Fatalf("新建设置没有版本号：%s %v", raw, err)
	}
	for _, version := range []any{0, 999} {
		fields["version"] = version
		bad, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, bad, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = loadSettings(path); err == nil {
			t.Fatalf("不支持的设置版本被接受：%v", version)
		}
		if after, err := os.ReadFile(path); err != nil || !bytes.Equal(bad, after) {
			t.Fatal("读取失败仍覆盖了原设置")
		}
	}
}

func TestFutureVisitFormatPreservesFilesAndRequestsNewerProgram(t *testing.T) {
	raw, err := os.ReadFile("../../internal/experimentstate/testdata/visits-5709c5c.json")
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err = json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	fields["version"] = 999
	raw, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	dir := filepath.Join(base, "state")
	if err = os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "visits.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if j, err := openJournal(dir, filepath.Join(base, "profile"), testSettings()); err == nil {
		j.unlock()
		t.Fatal("未来格式仍被旧程序打开")
	} else if !strings.Contains(err.Error(), "请更新程序") || strings.Contains(err.Error(), "48 小时") {
		t.Fatalf("版本不兼容不能引导用户等待后清除保护：%v", err)
	}
	if after, err := os.ReadFile(path); err != nil || !bytes.Equal(raw, after) {
		t.Fatal("版本不兼容时改写了原访问记录")
	}
}

func TestStartupDiagnosticDirectoryDoesNotPreventFirstJournal(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "experiment", "1")
	logs := filepath.Join(dir, "logs")
	if err := os.MkdirAll(filepath.Join(logs, "summaries", "summary-20351110-120000-12345"), 0700); err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string]string{
		"sequence.json": "1", "events-2035-11-10.jsonl": "{}\n", "summaries/summary-20351110-120000-12345/summary.json": "{}",
	} {
		if err := os.WriteFile(filepath.Join(logs, path), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	j, err := openJournal(dir, experimentstate.ProfileDir(base, 1, false), testSettings())
	if err != nil {
		t.Fatalf("修好设置后仍被首次日志目录挡住：%v", err)
	}
	j.unlock()
	if err = os.Remove(j.path); err != nil {
		t.Fatal(err)
	}
	if next, err := openJournal(dir, j.profile, testSettings()); err == nil {
		next.unlock()
		t.Fatal("已有初始化标记时缺失记录不能视为初次启动")
	}
}

func TestSelfCheckProcessLogsAllowFirstJournal(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "experiment", "1")
	logs := filepath.Join(dir, "logs")
	recorder, err := diagnostics.Open(logs, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { recorder.Close() })
	if err := recorder.Event("环境自检", map[string]any{"通过": true}); err != nil {
		t.Fatal(err)
	}
	line := strings.Repeat("local process check ", 1700) + "\n"
	for _, name := range []string{"chrome", "xvfb", "x11vnc"} {
		output, err := recorder.ProcessLog(name)
		if err != nil {
			t.Fatal(err)
		}
		for range 34 {
			if _, err := output.Write([]byte(line)); err != nil {
				t.Fatal(err)
			}
		}
		if err := output.Close(); err != nil {
			t.Fatal(err)
		}
		for _, suffix := range []string{".log", ".log.1"} {
			if info, err := os.Stat(filepath.Join(logs, name+suffix)); err != nil || !info.Mode().IsRegular() {
				t.Fatalf("未生成真实自检进程日志%s%s：%v", name, suffix, err)
			}
		}
	}
	if err := recorder.Summary(map[string]any{"version": 1, "mode": "preflight", "passed": true}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	j, err := openJournal(dir, experimentstate.ProfileDir(base, 1, false), testSettings())
	if err != nil {
		t.Fatalf("自检日志阻止首次扫码初始化：%v", err)
	}
	j.unlock()
}

func TestStartupDiagnosticDirectoryCannotResetExistingProtection(t *testing.T) {
	for _, damage := range []string{"多余文件", "新浏览器", "旧浏览器", "日志符号链接", "日志内未知文件", "进程日志目录", "进程日志符号链接", "额外轮转文件"} {
		t.Run(damage, func(t *testing.T) {
			base := t.TempDir()
			dir := filepath.Join(base, "experiment", "1")
			profile := experimentstate.ProfileDir(base, 1, false)
			logs := filepath.Join(dir, "logs")
			if err := os.MkdirAll(logs, 0700); err != nil {
				t.Fatal(err)
			}
			switch damage {
			case "多余文件":
				if err := os.WriteFile(filepath.Join(dir, "unknown"), []byte("old"), 0600); err != nil {
					t.Fatal(err)
				}
			case "新浏览器", "旧浏览器":
				path := profile
				if damage == "旧浏览器" {
					path = filepath.Join(base, "browser", "1")
				}
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "日志符号链接":
				if err := os.Remove(logs); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), logs); err != nil {
					t.Fatal(err)
				}
			case "日志内未知文件":
				if err := os.WriteFile(filepath.Join(logs, "unknown"), []byte("old"), 0600); err != nil {
					t.Fatal(err)
				}
			case "进程日志目录":
				if err := os.Mkdir(filepath.Join(logs, "chrome.log"), 0700); err != nil {
					t.Fatal(err)
				}
			case "进程日志符号链接":
				if err := os.Symlink(filepath.Join(t.TempDir(), "outside.log"), filepath.Join(logs, "x11vnc.log")); err != nil {
					t.Fatal(err)
				}
			case "额外轮转文件":
				if err := os.WriteFile(filepath.Join(logs, "chrome.log.2"), []byte("old"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if j, err := openJournal(dir, profile, testSettings()); err == nil {
				j.unlock()
				t.Fatal("不能借诊断目录重置已有账号保护")
			}
		})
	}
}
