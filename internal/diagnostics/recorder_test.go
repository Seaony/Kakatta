package diagnostics

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func openTestRecorder(t *testing.T) *Recorder {
	t.Helper()
	r, err := Open(t.TempDir(), "Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil && r.Err() == nil {
			t.Error(err)
		}
	})
	return r
}

func TestDailyEventsPersistSequenceTimezoneAndRecentHistory(t *testing.T) {
	r := openTestRecorder(t)
	first := time.Date(2026, 11, 11, 15, 59, 0, 0, time.UTC)
	r.now = func() time.Time { return first }
	fields := map[string]any{"steps": []any{"搜索"}, "password": "private-password"}
	if err := r.Event("阶段", fields); err != nil {
		t.Fatal(err)
	}
	if fields["password"] != "private-password" {
		t.Fatal("脱敏修改了调用方数据")
	}
	first = first.Add(2 * time.Minute)
	for index := range 54 {
		if err := r.Event("详情", map[string]any{"index": index}); err != nil {
			t.Fatal(err)
		}
	}
	copy := r.Events()
	copy[len(copy)-1].Fields["index"] = -1
	if len(copy) != 50 || copy[0].Sequence != 6 || copy[len(copy)-1].Sequence != 55 || r.Events()[49].Fields["index"] != float64(53) {
		t.Fatalf("近期事件或独立副本不正确：%+v", copy)
	}
	for _, day := range []string{"2026-11-11", "2026-11-12"} {
		data, err := os.ReadFile(filepath.Join(r.dir, "events-"+day+".jsonl"))
		if err != nil || !bytes.Contains(data, []byte("+08:00")) || bytes.Contains(data, []byte("private-password")) {
			t.Fatalf("按本地日写盘或脱敏失败：%s %v", day, err)
		}
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(r.dir, "Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.Event("重新启动", nil); err != nil || reopened.Events()[49].Sequence != 56 {
		t.Fatalf("重启恢复序号失败：%v %+v", err, reopened.Events())
	}
}

func TestEventRetentionRemovesOldestDaysAndCapsTotalSize(t *testing.T) {
	r := openTestRecorder(t)
	r.now = func() time.Time { return time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC) }
	old := filepath.Join(r.dir, "events-2026-11-01.jsonl")
	large := filepath.Join(r.dir, "events-2026-11-24.jsonl")
	if err := os.WriteFile(old, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(large)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxLogBytes); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := r.Event("保留新事件", nil); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{old, large} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("未清理旧日志：%s %v", path, err)
		}
	}
	if r.Events()[0].Sequence != 1 {
		t.Fatal("清理重置了序号")
	}
}

func TestBundlePreservesProductsAndMasksCredentialsAcrossAllFiles(t *testing.T) {
	r := openTestRecorder(t)
	if err := r.Event("解析失败", map[string]any{
		"step": "详情", "url": "https://www.example.test/item?id=123&token=private-url",
		"nested": map[string]any{"API_KEY": "private-api", "password": "private-password"},
	}); err != nil {
		t.Fatal(err)
	}
	page := Page{
		Screenshot: []byte{0xff, 0xd8, 0xff, 0xd9}, URL: "https://www.example.test/item?id=123&token=private-url#private-fragment",
		Title: "商品详情", HTML: `<html><head><title>本地商品</title></head><body><input name="password" value="private-input"><input name="phone" value="13812345678"><script>{"token":"private-script"}</script><div>相机 138 元，型号 123456789，商品描述保持原样</div></body></html>`,
		Errors: map[string]string{"screenshot": "本地错误"},
	}
	raw := []byte(`{"data":{"mobile":"13812345678","idCard":"110101199001011234","access_token":"private-access","cookie":"private-cookie","itemDO":{"title":"相机 138 元","desc":"123456789和原文保持不变"}},"ret":["SUCCESS"`)
	path, err := r.Bundle("parse", page, raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Summary(map[string]any{"API_KEY": "private-summary", "bundles": []string{path}}); err != nil {
		t.Fatal(err)
	}
	var all []byte
	if err := filepath.WalkDir(r.dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		all = append(all, data...)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private-url", "private-fragment", "private-api", "private-password", "private-input", "private-script", "private-access", "private-cookie", "private-summary", "13812345678", "110101199001011234"} {
		if bytes.Contains(all, []byte(forbidden)) {
			t.Fatalf("诊断资料泄露敏感字段：%s", forbidden)
		}
	}
	for _, expected := range []string{"相机 138 元", "123456789和原文保持不变", "商品描述保持原样"} {
		if !bytes.Contains(all, []byte(expected)) {
			t.Fatalf("商品原文丢失：%s", expected)
		}
	}
	data, err := os.ReadFile(filepath.Join(path, "response.json"))
	if err != nil || !bytes.HasSuffix(data, []byte(`"ret":["SUCCESS"`)) {
		t.Fatal("半截响应未保留失败位置", err)
	}
	clean, err := r.Bundle("element", page, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(clean, "response.json")); !os.IsNotExist(err) {
		t.Fatal("没有失败响应时写入了正文", err)
	}
	empty, err := r.Bundle("parse", Page{}, []byte{})
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(empty, "response.json")); err != nil || len(data) != 0 {
		t.Fatal("明确空正文未保留为空文件", err)
	}
}

func TestBundleAndSummaryCapsKeepNewestPaths(t *testing.T) {
	r := openTestRecorder(t)
	r.now = func() time.Time { return time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC) }
	var last string
	for index := range 12 {
		var err error
		last, err = r.Bundle("error", Page{Title: "详情"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.Summary(map[string]any{"run": index}); err != nil {
			t.Fatal(err)
		}
	}
	bundles, err := os.ReadDir(filepath.Join(r.dir, "bundles"))
	if err != nil || len(bundles) != maxBundles {
		t.Fatalf("诊断包数量不受限：%d %v", len(bundles), err)
	}
	if _, err := os.Stat(last); err != nil {
		t.Fatal("最新诊断包被当成旧包删除", err)
	}
	for index := range 21 {
		if err := r.Summary(map[string]any{"run": index}); err != nil {
			t.Fatal(err)
		}
	}
	summaries, err := os.ReadDir(filepath.Join(r.dir, "summaries"))
	if err != nil || len(summaries) != 30 {
		t.Fatalf("摘要数量不受限：%d %v", len(summaries), err)
	}
}

func TestWriteFailureIsReturnedAndLatched(t *testing.T) {
	r := openTestRecorder(t)
	if err := os.Remove(r.dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.dir, []byte("not-a-directory"), 0600); err != nil {
		t.Fatal(err)
	}
	err := r.Event("无法写入", nil)
	if err == nil || r.Err() == nil || r.Event("不能伪装成功", nil) == nil || r.Close() == nil {
		t.Fatal("写盘失败被静默忽略", err)
	}
}

func TestProcessLogsHandleSplitSecretsRotationAndFinalLine(t *testing.T) {
	r := openTestRecorder(t)
	w, err := r.ProcessLog("chrome")
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"[local] Authorization: Bearer private-", "authorization\n", "[local] Cookie: first=private-one; second=private-two\n", `{"password":"private-`, `password"}`, "\nhttps://user:private-proxy@proxy.example.test:7890/path?token=private-query\n"} {
		if _, err := w.Write([]byte(part)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(r.dir, "chrome.log"))
	if strings.Contains(string(data), "private-") || !bytes.Contains(data, []byte(redacted)) {
		t.Fatalf("跨Write凭据未脱敏：%s", data)
	}
	if err := w.Close(); err != nil {
		t.Fatal("关闭不是幂等", err)
	}
	second, err := r.ProcessLog("xvfb")
	if err != nil {
		t.Fatal(err)
	}
	for range 40 {
		if _, err := second.Write([]byte(strings.Repeat("x", 60<<10) + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	second.Write([]byte("last-line"))
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", ".1"} {
		info, err := os.Stat(filepath.Join(r.dir, "xvfb.log"+suffix))
		if err != nil || info.Size() > maxProcessBytes {
			t.Fatalf("进程日志未限制大小：%+v %v", info, err)
		}
	}
	data, _ = os.ReadFile(filepath.Join(r.dir, "xvfb.log"))
	if !bytes.HasSuffix(data, []byte("last-line\n")) {
		t.Fatal("关闭时丢失最后一行")
	}
}

func TestConcurrentEventsHaveOnePersistentSequence(t *testing.T) {
	r := openTestRecorder(t)
	var jobs sync.WaitGroup
	for range 8 {
		jobs.Add(1)
		go func() {
			defer jobs.Done()
			for range 8 {
				if err := r.Event("并发事件", map[string]any{"ok": true}); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	jobs.Wait()
	data, _ := os.ReadFile(filepath.Join(r.dir, "events-"+r.now().In(r.loc).Format("2006-01-02")+".jsonl"))
	for index, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
		var event Event
		if err := json.Unmarshal(line, &event); err != nil || event.Sequence != uint64(index+1) {
			t.Fatal("并发日志重号或损坏", index, err, event)
		}
	}
}

func TestRestartSeparatesInterruptedLastLineBeforeAppending(t *testing.T) {
	r := openTestRecorder(t)
	if err := r.Event("强杀前", nil); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(r.dir, "events-"+r.now().In(r.loc).Format("2006-01-02")+".jsonl")
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte(`{"sequence":2,"event":"半截`))
	f.Close()
	second, err := Open(r.dir, "Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := second.Event("重启后", nil); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
	var final Event
	if len(lines) != 3 || json.Unmarshal(lines[2], &final) != nil || final.Event != "重启后" || final.Sequence != 2 {
		t.Fatal("重启后的事件接到半行后面，无法再次读取", string(data))
	}
}
