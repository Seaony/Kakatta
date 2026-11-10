package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/limits"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/store"
)

func newTaskTransferHarness(t *testing.T) taskHarness {
	t.Helper()
	database, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	clock := &atomic.Int64{}
	clock.Store(time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC).UnixMilli())
	now := func() time.Time { return time.UnixMilli(clock.Load()) }
	preferences, err := appsettings.New(t.Context(), database.DB(), settings.Environment{TZ: "UTC"}, now)
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{db: database.DB(), settings: preferences, now: now, readSnapshot: database.ReadSnapshot}
	s.initRuntime()
	t.Cleanup(func() { s.Close() })
	return taskHarness{t: t, ctx: t.Context(), s: s, clock: clock}
}
func importTaskText(tasks string) string {
	return `{"format":"kakatta.tasks","version":1,"tasks":[` + tasks + `]}`
}
func importTasksForTest(t *testing.T, h taskHarness, tasks string) *TaskImportReport {
	t.Helper()
	report, err := h.s.ImportTasks(h.ctx, strings.NewReader(importTaskText(tasks)))
	if err != nil {
		t.Fatal(err)
	}
	if report == nil || !report.Complete || report.Total != report.Imported+report.Skipped+report.Failed+report.Unprocessed {
		t.Fatalf("报告不完整：%+v", report)
	}
	return report
}
func importedTask(t *testing.T, h taskHarness, item TaskImportItem) Task {
	t.Helper()
	if item.TaskID == nil {
		t.Fatalf("没有实际保存编号：%+v", item)
	}
	tx, err := h.s.db.BeginTx(h.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	task, err := loadTaskTx(h.ctx, tx, *item.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	return task
}
func issueCode(issues []settings.Issue, code string) bool {
	for _, issue := range issues {
		if issue.Reason.Code == code {
			return true
		}
	}
	return false
}

func TestTaskImportPreflightWritesNothing(t *testing.T) {
	cases := []string{
		`{"tasks":[{"keyword":"valid"}],"format":"wrong","version":1}`,
		`{"tasks":[{"keyword":"valid"}],"format":"kakatta.tasks","version":2}`,
		`{"format":"kakatta.tasks","version":1,"tasks":[{"keyword":"valid"},`,
		importTaskText(`{"keyword":"valid"}`) + `{}`,
		`{"format":"kakatta.tasks","version":1,"tasks":[],"tasks":[]}`,
		`{"format":"kakatta.tasks","tasks":[]}`,
		`{"format":"kakatta.tasks","version":0,"tasks":[]}`,
		`{"format":"kakatta.tasks","version":1,"tasks":[{"keyword":"valid"}],"unknown":true}`,
		`{"format":"kakatta.tasks","version":1,"tasks":[],"exported_at":null}`,
		importTaskText("{\"keyword\":\"bad\xff\"}"),
	}
	for index, input := range cases {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			h := newTaskTransferHarness(t)
			report, err := h.s.ImportTasks(h.ctx, strings.NewReader(input))
			if err == nil || report != nil || h.count(`SELECT COUNT(*) FROM tasks`) != 0 {
				t.Fatalf("坏文件出现写入或成功：%+v %v", report, err)
			}
		})
	}
}

func TestTaskImportItemsFailIndependently(t *testing.T) {
	invalid := []string{
		`null`, `"text"`, `{"keyword":""}`, `{"keyword":"` + strings.Repeat("鱼", 51) + `"}`,
		`{"keyword":"a","enabled":null}`, `{"keyword":"a","max_pages":"0"}`,
		`{"keyword":"a","interval_minutes":0}`, `{"keyword":"a","interval_minutes":30.0}`,
		`{"keyword":"a","min_price":-1}`, `{"keyword":"a","min_price":20,"max_price":10}`,
		`{"keyword":"a","min_price":1.005}`, `{"keyword":"a","min_price":"1"}`,
		`{"keyword":"a","publish_window":"future"}`, `{"keyword":"a","region":{"province":"不存在"}}`,
		`{"keyword":"a","channels":null}`, `{"keyword":"a","region":null}`,
		`{"keyword":"a","profile":{"musts":[{"text":"x","source":null}]}}`,
		`{"keyword":"a","profile":{"musts":[{"text":"x","rev":-1,"source":"user"}]},"judge_mode":"ai"}`,
		`{"keyword":"a","crawl_overrides":{"max_details_per_run":"0"}}`,
		`{"keyword":"a","crawl_overrides":{"page_wait_seconds":{"min":null,"max":2}}}`,
		`{"keyword":"a","keyword":"b"}`,
	}
	for index, input := range invalid {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			h := newTaskTransferHarness(t)
			report := importTasksForTest(t, h, `{"keyword":"before"},`+input+`,{"keyword":"after"}`)
			if report.Imported != 2 || report.Skipped != 1 || report.Failed != 0 || len(report.Items) != 3 || report.Items[1].Reason == nil || h.count(`SELECT COUNT(*) FROM tasks`) != 2 {
				t.Fatalf("单项错误影响相邻任务：%+v", report)
			}
		})
	}
}

func TestTaskImportUsesCommonCreationAndChannelPresence(t *testing.T) {
	h := newTaskTransferHarness(t)
	h.exec(`INSERT INTO channels(id,uuid,type,name,enabled,config_json) VALUES(1,'one','bark','same',1,'{}'),(2,'two','telegram','same',0,'{}')`)
	h.exec(`INSERT INTO accounts(id,name,state,created_at) VALUES(1,'later','active',2),(2,'earlier','active',1),(3,'signed','signed_out',0)`)
	old := h.create(map[string]any{"name": "same", "enabled": false})
	report := importTasksForTest(t, h, `{"keyword":"first","name":"same","enabled":false,"account_id":3,"account_pinned":true}, {"keyword":"second","channels":[]}, {"keyword":"third","channels":[{"type":"telegram","name":"same"},{"type":"missing","name":"same"},{"type":"telegram","name":"same"}]}`)
	if report.Imported != 3 || h.count(`SELECT COUNT(*) FROM tasks`) != 4 {
		t.Fatalf("覆盖已有同名任务：%+v", report)
	}
	tasks := []Task{}
	for _, item := range report.Items {
		tasks = append(tasks, importedTask(t, h, item))
	}
	if tasks[0].Enabled || tasks[0].AccountPinned || tasks[0].AccountID == nil || *tasks[0].AccountID != 1 || !reflect.DeepEqual(tasks[0].ChannelIDs, []int64{1}) || len(tasks[1].ChannelIDs) != 0 || !reflect.DeepEqual(tasks[2].ChannelIDs, []int64{2}) {
		t.Fatalf("默认/空渠道/禁用渠道或自动账号错误：%+v", tasks)
	}
	for index, task := range tasks {
		if task.SearchGeneration != 1 || !task.BaselinePending || task.NextRunAt == nil || *task.NextRunAt != h.clock.Load() || task.LastRunAt != nil || task.SortIndex != old.SortIndex+int64(index)+1 {
			t.Fatalf("绕过共同新建流程：%+v", task)
		}
	}
	if !issueCode(report.Items[2].Corrections, "tasks.import_channel_unmatched") || !issueCode(report.Items[2].Corrections, "tasks.import_duplicate_channel") {
		t.Fatalf("未报告失配/重复渠道：%+v", report.Items[2])
	}
}

func TestTaskImportRepairsProfileAndPreservesDistinctRevisions(t *testing.T) {
	h := newTaskTransferHarness(t)
	report := importTasksForTest(t, h, `{"keyword":"camera","judge_mode":"ai","profile":{"next_no":2,"unknown":123,"specs":[{"name":"config","options":["x"],"values":["x"],"extra":true},{"name":""}],"musts":[{"id":"m7","text":"user","source":"user","rev":9},{"id":"m01","text":"ai","source":"ai","rev":31},{"id":"m8","text":"unknown","source":"unknown","rev":41}],"bonuses":[{"id":"b9","text":"bonus","source":"user","rev":21},{"id":"b9","text":"duplicate","source":"user","rev":22},{"text":"missing","source":"user"}],"risks":[{"id":"c17","name":"custom","attitude":"reject","source":"user","rev":51},{"id":"cFoo","name":"unknown source","attitude":"confirm","source":"unknown","edited":true,"rev":52},{"id":"c17","name":"duplicate custom","attitude":"reject","source":"ai","rev":53},{"id":"seller_dealer","attitude":"bad","source":"system","rev":61},{"id":"seller_dealer","attitude":"ignore","source":"system","rev":62},{"id":"unknown","name":"drop"},{"id":"c99","name":""}],"allowed_kinds":["sale","future"]},"notify_policy":"recommended"}`)
	if report.Imported != 1 {
		t.Fatalf("有效内容被丢弃：%+v", report)
	}
	task := importedTask(t, h, report.Items[0])
	profile := task.Profile
	if task.JudgeMode != analysis.ModeAI || task.NotifyPolicy != "good" || len(profile.Specs) != 1 || profile.Specs[0].Source != analysis.SourceAI || len(profile.Musts) != 1 || len(profile.Bonuses) != 5 || len(profile.Risks) != 4 || !reflect.DeepEqual(profile.AllowedKinds, []analysis.Kind{analysis.KindSale}) {
		t.Fatalf("清单修复错误：%+v", profile)
	}
	expected := []struct {
		text string
		rev  int64
	}{{"ai", 31}, {"unknown", 41}, {"bonus", 21}, {"duplicate", 22}, {"missing", 1}}
	seen := map[string]bool{"m7": true}
	for index, item := range profile.Bonuses {
		if item.Text != expected[index].text || item.Rev != expected[index].rev || !strings.HasPrefix(item.ID, "b") || seen[item.ID] {
			t.Fatalf("搬移、分号或版本对应错误：%+v", profile)
		}
		seen[item.ID] = true
	}
	if profile.Musts[0].ID != "m7" || profile.Musts[0].Rev != 9 || profile.Risks[0].Rev != 51 || profile.Risks[0].Attitude != analysis.Reject || profile.Risks[1].Rev != 52 || profile.Risks[1].Attitude != analysis.Mind || profile.Risks[2].Rev != 53 || profile.Risks[2].Attitude != analysis.Mind || profile.Risks[3].Rev != 61 {
		t.Fatalf("合法版本/用户档位或不可信来源错误：%+v", profile)
	}
	if profile.NextNo <= 17 || !issueCode(report.Items[0].Corrections, "tasks.import_unknown_field") || !issueCode(report.Items[0].Corrections, "tasks.import_profile_must_moved") || !issueCode(report.Items[0].Warnings, "run.no_ai_provider") {
		t.Fatalf("缺修复记录或无模型提示：%+v", report.Items[0])
	}
	var watermark string
	if err := h.s.db.QueryRowContext(h.ctx, `SELECT value FROM meta WHERE key=?`, taskProfileWatermarkPrefix+fmt.Sprint(task.ID)).Scan(&watermark); err != nil || watermark != fmt.Sprint(profile.NextNo) {
		t.Fatalf("共同创建未持久化水位：%s %v", watermark, err)
	}
}

func TestTaskImportFieldRepairsAndRecommendations(t *testing.T) {
	h := newTaskTransferHarness(t)
	report := importTasksForTest(t, h, `{"keyword":"`+strings.Repeat("鱼", 35)+`","name":"","max_pages":0,"budget_min":20,"budget_max":10,"judge_mode":"ai","profile":{"specs":[],"musts":[]},"notify_policy":"good","crawl_overrides":{"unknown":null,"interval_jitter_percent":0,"max_details_per_run":0,"page_wait_seconds":{"min":3,"max":2},"action_wait_seconds":{"min":1,"max":2,"future":true}}}, {"keyword":"wide","max_pages":4,"interval_minutes":1}`)
	if report.Imported != 2 {
		t.Fatalf("可修复任务被跳过：%+v", report)
	}
	task := importedTask(t, h, report.Items[0])
	if task.Name != strings.Repeat("鱼", 30) || task.MaxPages != 1 || task.BudgetMin != nil || task.BudgetMax != nil || task.JudgeMode != analysis.ModeKeyword || task.NotifyPolicy != "recommended" || string(task.CrawlOverrides["interval_jitter_percent"]) != "0" || len(task.CrawlOverrides) != 2 {
		t.Fatalf("字段修复不完整：%+v", task)
	}
	if !issueCode(report.Items[1].Warnings, "settings.outside_recommendation") {
		t.Fatalf("合法超推荐值没有提示：%+v", report.Items[1])
	}
}

func TestTaskImportPartialStorageFailureKeepsCommittedTasks(t *testing.T) {
	h := newTaskTransferHarness(t)
	h.exec(`CREATE TRIGGER fail_import BEFORE INSERT ON tasks WHEN NEW.keyword='fail' BEGIN SELECT RAISE(ABORT,'local failure'); END`)
	report, err := h.s.ImportTasks(h.ctx, strings.NewReader(importTaskText(`{"keyword":"saved"},{"keyword":"fail"},{"keyword":"unprocessed"}`)))
	if err == nil || report == nil || report.Complete || report.Imported != 1 || report.Failed != 1 || report.Skipped != 0 || report.Unprocessed != 1 || report.Failure == nil || h.count(`SELECT COUNT(*) FROM tasks WHERE keyword='saved' AND baseline_pending=1`) != 1 || len(report.Items) != 2 || report.Items[1].Status != "failed" {
		t.Fatalf("中途失败丢成果或计数：%+v %v", report, err)
	}
}

func TestTaskImportCancellationKeepsCounts(t *testing.T) {
	h := newTaskTransferHarness(t)
	ctx, cancel := context.WithCancel(h.ctx)
	h.s.publish = func(string, *locales.Message, json.RawMessage) error { cancel(); return nil }
	report, err := h.s.ImportTasks(ctx, strings.NewReader(importTaskText(`{"keyword":"saved"},{"keyword":"left"},{"keyword":"left2"}`)))
	if !errors.Is(err, context.Canceled) || report == nil || report.Imported != 1 || report.Failed != 0 || report.Unprocessed != 2 || len(report.Items) != 1 || h.count(`SELECT COUNT(*) FROM tasks`) != 1 {
		t.Fatalf("取消后继续写或计数错：%+v %v", report, err)
	}
	report, err = h.s.ImportTasks(ctx, strings.NewReader(importTaskText(`{"keyword":"never"}`)))
	if report != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("取消后的预检应零写入：%+v %v", report, err)
	}
}

func TestTaskExportAllPagesExactMoneyAndRawProfile(t *testing.T) {
	h := newTaskTransferHarness(t)
	h.exec(`INSERT INTO channels(id,uuid,type,name,enabled,config_json) VALUES(1,'secret-uuid','bark','local-channel',1,'{"token":"secret-channel-token"}')`)
	for index := 0; index < 251; index++ {
		h.create(map[string]any{"name": fmt.Sprintf("任务%d", index), "min_price": int64(123), "max_price": limits.MaxSafeInteger})
	}
	raw := `{"summary":"原始","next_no":19,"specs":[],"musts":[{"id":"m7","text":"条件","source":"user","edited":true,"rev":11}],"bonuses":[],"risks":[],"allowed_kinds":[],"future":{"number":9007199254740991}}`
	h.exec(`UPDATE tasks SET profile_json=?,account_pinned=1 WHERE id=1`, raw)
	var output bytes.Buffer
	if err := h.s.ExportTasks(h.ctx, &output); err != nil {
		t.Fatal(err)
	}
	var file struct {
		Format  string                       `json:"format"`
		Version int                          `json:"version"`
		Tasks   []map[string]json.RawMessage `json:"tasks"`
	}
	if err := json.Unmarshal(output.Bytes(), &file); err != nil {
		t.Fatal(err)
	}
	if file.Format != "kakatta.tasks" || file.Version != 1 || len(file.Tasks) != 251 {
		t.Fatalf("漏导出分页：%s %d", file.Format, len(file.Tasks))
	}
	for index, task := range file.Tasks {
		var name string
		json.Unmarshal(task["name"], &name)
		if name != fmt.Sprintf("任务%d", index) || string(task["min_price"]) != "1.23" || string(task["max_price"]) != "90071992547409.91" {
			t.Fatalf("顺序或金额变形：%d %+v", index, task)
		}
		for _, key := range []string{"id", "account_id", "account_pinned", "channel_ids", "search_generation", "baseline_pending", "next_run_at", "last_run_outcome"} {
			if _, ok := task[key]; ok {
				t.Fatalf("泄露内部字段 %s", key)
			}
		}
	}
	if strings.Contains(output.String(), "secret-channel-token") || strings.Contains(output.String(), "secret-uuid") || !strings.Contains(output.String(), `"channels":[{"type":"bark","name":"local-channel"}]`) {
		t.Fatalf("渠道投影缺失或泄露凭据：%s", output.String()[:min(500, output.Len())])
	}
	var want, got any
	json.Unmarshal([]byte(raw), &want)
	json.Unmarshal(file.Tasks[0]["profile"], &got)
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("profile没有原样投影：%s", file.Tasks[0]["profile"])
	}
}

func TestTaskImportMoneyIsExactAndSafe(t *testing.T) {
	cases := []struct {
		raw  string
		want int64
		ok   bool
	}{{"1.23", 123, true}, {"1.2300", 123, true}, {"123e-2", 123, true}, {"1e2", 10000, true}, {"0e999999999999999999", 0, true}, {"-0.00", 0, true}, {"90071992547409.91", limits.MaxSafeInteger, true}, {"90071992547409.92", 0, false}, {"1.005", 0, false}, {"1e-999999999999", 0, false}, {"1e999999999999", 0, false}, {`"1"`, 0, false}, {"-1", 0, false}}
	for _, c := range cases {
		t.Run(c.raw, func(t *testing.T) {
			value, err := parseTaskImportMoney(json.RawMessage(c.raw))
			if (err == nil) != c.ok || c.ok && value != c.want {
				t.Fatalf("金额不精确：%d %v", value, err)
			}
		})
	}
	h := newTaskTransferHarness(t)
	report := importTasksForTest(t, h, `{"keyword":"price","min_price":1.2300,"max_price":90071992547409.91,"budget_min":0,"budget_max":123e-2}`)
	task := importedTask(t, h, report.Items[0])
	if *task.MinPrice != 123 || *task.MaxPrice != limits.MaxSafeInteger || *task.BudgetMin != 0 || *task.BudgetMax != 123 {
		t.Fatalf("边界转换不精确：%+v", task)
	}
}

type transferBlockingWriter struct {
	started chan struct{}
	unblock chan struct{}
	once    sync.Once
}

func (w *transferBlockingWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.started); <-w.unblock })
	return len(p), nil
}
func TestTaskExportCloseCancelsAndJoinsSnapshot(t *testing.T) {
	h := newTaskTransferHarness(t)
	h.create(nil)
	writer := &transferBlockingWriter{started: make(chan struct{}), unblock: make(chan struct{})}
	exportDone := make(chan error, 1)
	go func() { exportDone <- h.s.ExportTasks(h.ctx, writer) }()
	<-writer.started
	closeDone := make(chan error, 1)
	go func() { closeDone <- h.s.Close() }()
	<-h.s.ctx.Done()
	select {
	case err := <-closeDone:
		t.Fatalf("关闭没有等待本地导出：%v", err)
	default:
	}
	close(writer.unblock)
	if err := <-exportDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("关闭未取消导出：%v", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
}
func TestTaskExportRequiresSnapshotAndPropagatesWriterError(t *testing.T) {
	h := newTaskTransferHarness(t)
	snapshot := h.s.readSnapshot
	h.s.readSnapshot = nil
	var out bytes.Buffer
	var business *ServiceError
	if err := h.s.ExportTasks(h.ctx, &out); !errors.As(err, &business) || business.Kind != ErrorUnavailable || out.Len() != 0 {
		t.Fatalf("缺快照依赖却成功：%v", err)
	}
	h.s.readSnapshot = snapshot
	failure := errors.New("local write failed")
	if err := h.s.ExportTasks(h.ctx, taskTransferErrorWriter{failure}); !errors.Is(err, failure) {
		t.Fatalf("丢失writer错误：%v", err)
	}
}

type taskTransferErrorWriter struct{ err error }

func (w taskTransferErrorWriter) Write([]byte) (int, error) { return 0, w.err }

type oneByteTransferReader struct{ io.Reader }

func (r oneByteTransferReader) Read(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	return r.Reader.Read(p)
}
func TestTaskImportStreamingUnicodeAndHeaderOrder(t *testing.T) {
	raw := `{"tasks":[{"keyword":"相机🐈"},{"keyword":"第二"}],"exported_at":"任意元数据","version":1,"format":"kakatta.tasks"}`
	count, err := scanTaskImport(t.Context(), oneByteTransferReader{strings.NewReader(raw)}, nil)
	if err != nil || count != 2 {
		t.Fatalf("分块UTF8或头顺序失败：%d %v", count, err)
	}
}

func TestTaskExportSnapshotIsConsistentDuringWrite(t *testing.T) {
	h := newTaskTransferHarness(t)
	for index := 0; index < 105; index++ {
		h.create(map[string]any{"keyword": fmt.Sprintf("old%d", index)})
	}
	var out bytes.Buffer
	writer := &taskTransferMutatingWriter{Writer: &out, mutate: func() {
		h.exec(`UPDATE tasks SET keyword='changed' WHERE id=105`)
		h.create(map[string]any{"keyword": "new"})
	}}
	if err := h.s.ExportTasks(h.ctx, writer); err != nil {
		t.Fatal(err)
	}
	var file struct {
		Tasks []struct {
			Keyword string `json:"keyword"`
		} `json:"tasks"`
	}
	json.Unmarshal(out.Bytes(), &file)
	if len(file.Tasks) != 105 || file.Tasks[104].Keyword != "old104" {
		t.Fatalf("跨页读到不一致版本：%+v", file.Tasks)
	}
}

type taskTransferMutatingWriter struct {
	io.Writer
	once   sync.Once
	mutate func()
}

func (w *taskTransferMutatingWriter) Write(p []byte) (int, error) {
	w.once.Do(w.mutate)
	return w.Writer.Write(p)
}

func TestTaskImportDefaultsNullAndKnownSourceStrictness(t *testing.T) {
	h := newTaskTransferHarness(t)
	h.exec(`INSERT INTO ai_providers(uuid,name,base_url,vision_model,enabled,sort_index,error_state) VALUES('fake','unavailable','http://127.0.0.1:1','vision',1,0,'invalid')`)
	if _, err := h.s.settings.Apply(h.ctx, settings.Values{"crawl.max_pages": json.RawMessage(`2`), "crawl.task_interval_minutes": json.RawMessage(`45`)}); err != nil {
		t.Fatal(err)
	}
	report := importTasksForTest(t, h, `{"keyword":"default"}, {"keyword":"null","min_price":null,"max_price":null,"budget_min":null,"budget_max":null,"region":{"province":null,"city":null,"district":null,"unknown":true},"personal_only":false,"free_shipping_only":false,"analyze_on_first_run":false,"notify_price_drop":false,"judge_mode":"ai","profile":{"specs":[{"name":"memory","options":["16"],"values":["16"],"source":"user"}],"musts":[],"bonuses":[],"risks":[{"id":"c4","name":"user edited","source":"ai","edited":true,"attitude":"reject","rev":6}]}}`)
	if report.Imported != 2 {
		t.Fatalf("新建默认或显式空值失败：%+v", report)
	}
	first := importedTask(t, h, report.Items[0])
	second := importedTask(t, h, report.Items[1])
	if first.MaxPages != 2 || first.IntervalMinutes != 45 || first.JudgeMode != analysis.ModeKeyword || second.MinPrice != nil || second.MaxPrice != nil || second.BudgetMin != nil || second.BudgetMax != nil || second.PersonalOnly || second.AnalyzeOnFirstRun || second.NotifyPriceDrop || second.JudgeMode != analysis.ModeAI || len(second.Profile.Risks) != 1 || second.Profile.Risks[0].Attitude != analysis.Reject || second.Profile.Risks[0].Rev != 6 {
		t.Fatalf("缺省/null/false/编辑后的严格档位未保留：%+v %+v", first, second)
	}
	if !issueCode(report.Items[1].Warnings, "run.no_ai_provider") {
		t.Fatalf("error_state 服务商被当可用：%+v", report.Items[1])
	}
}

type taskTransferSeekBlocker struct {
	*strings.Reader
	seekCount int
	started   chan struct{}
	unblock   chan struct{}
}

func (r *taskTransferSeekBlocker) Seek(offset int64, whence int) (int64, error) {
	r.seekCount++
	if r.seekCount == 2 {
		close(r.started)
		<-r.unblock
	}
	return r.Reader.Seek(offset, whence)
}
func TestTaskImportCloseCancelsAndJoinsAfterPreflight(t *testing.T) {
	h := newTaskTransferHarness(t)
	reader := &taskTransferSeekBlocker{Reader: strings.NewReader(importTaskText(`{"keyword":"never"}`)), started: make(chan struct{}), unblock: make(chan struct{})}
	type outcome struct {
		report *TaskImportReport
		err    error
	}
	importDone := make(chan outcome, 1)
	go func() { report, err := h.s.ImportTasks(h.ctx, reader); importDone <- outcome{report, err} }()
	<-reader.started
	closeDone := make(chan error, 1)
	go func() { closeDone <- h.s.Close() }()
	<-h.s.ctx.Done()
	select {
	case err := <-closeDone:
		t.Fatalf("关闭未等已登记导入：%v", err)
	default:
	}
	close(reader.unblock)
	result := <-importDone
	if !errors.Is(result.err, context.Canceled) || result.report == nil || result.report.Imported != 0 || result.report.Unprocessed != 1 || result.report.Failed != 0 {
		t.Fatalf("关闭后仍写入或丢失报告：%+v %v", result.report, result.err)
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	if h.count(`SELECT COUNT(*) FROM tasks`) != 0 {
		t.Fatal("取消后写入了任务")
	}
}

type taskTransferFailSecondSeek struct{ *strings.Reader }

func (r *taskTransferFailSecondSeek) Seek(offset int64, whence int) (int64, error) {
	if r.Reader.Len() == 0 {
		return 0, io.ErrClosedPipe
	}
	return r.Reader.Seek(offset, whence)
}
func TestTaskImportSeekFailureHasTruthfulReport(t *testing.T) {
	h := newTaskTransferHarness(t)
	report, err := h.s.ImportTasks(h.ctx, &taskTransferFailSecondSeek{strings.NewReader(importTaskText(`{"keyword":"never"}`))})
	if !errors.Is(err, io.ErrClosedPipe) || report == nil || report.Total != 1 || report.Unprocessed != 1 || report.Imported != 0 || report.Complete || report.Failure == nil || h.count(`SELECT COUNT(*) FROM tasks`) != 0 {
		t.Fatalf("预检后seek失败报告错误：%+v %v", report, err)
	}
}

func TestTaskImportLargeBalancedDecimalExponent(t *testing.T) {
	value := "0." + strings.Repeat("0", 1000001) + "123e1000002"
	cents, err := parseTaskImportMoney(json.RawMessage(value))
	if err != nil || cents != 123 {
		t.Fatalf("合法长小数与指数相消算错：%d %v", cents, err)
	}
}
