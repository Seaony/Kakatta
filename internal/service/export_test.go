package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/limits"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/store"
)

func exportFixture(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	database, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "exports.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	now := time.Date(2030, 11, 11, 16, 30, 0, 0, time.UTC)
	preferences, err := appsettings.New(t.Context(), database.DB(), settings.Environment{TZ: "Asia/Tokyo"}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{db: database.DB(), readSnapshot: database.ReadSnapshot, settings: preferences, now: func() time.Time { return now }}
	t.Cleanup(func() { _ = s.Close() })
	return s, database
}

func exportReadCSV(t *testing.T, raw []byte) [][]string {
	t.Helper()
	if !bytes.HasPrefix(raw, []byte{0xef, 0xbb, 0xbf}) || bytes.Count(raw, []byte{0xef, 0xbb, 0xbf}) != 1 {
		t.Fatal("CSV没有且仅有一次BOM")
	}
	rows, err := csv.NewReader(bytes.NewReader(raw[3:])).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rows[0], findingExportHeader) {
		t.Fatal("CSV列顺序错误", rows[0])
	}
	return rows
}

type exportHookWriter struct {
	bytes.Buffer
	hook func() error
}

func (w *exportHookWriter) Write(p []byte) (int, error) {
	if w.hook != nil {
		hook := w.hook
		w.hook = nil
		if err := hook(); err != nil {
			return 0, err
		}
	}
	return w.Buffer.Write(p)
}

func TestExportFindingsCSVAllRowsUseOneSnapshotAndListOrder(t *testing.T) {
	s, database := exportFixture(t)
	db := database.DB()
	taskID := queryTask(t, db, nil)
	otherTask := queryTask(t, db, nil)
	for i := range 405 {
		item := fmt.Sprintf("item-%03d", i)
		queryListing(t, db, item, item, queryPtr(int64(10000+i)))
		var score *int64
		var grade *analysis.Grade
		if i < 301 {
			score, grade = queryPtr(int64(i%3+80)), queryPtr(analysis.GradeGood)
		}
		queryFinding(t, db, taskID, item, analysis.DecisionScored, grade, score, int64(i%4))
	}
	queryFinding(t, db, otherTask, "item-000", analysis.DecisionScored, queryPtr(analysis.GradeSteal), queryPtr(int64(99)), 1000)
	for _, sort := range []string{"score", "latest"} {
		t.Run(sort, func(t *testing.T) {
			query := FindingQuery{TaskID: &taskID, View: "all", Sort: sort, ListQuery: ListQuery{Limit: 73}}
			want := []string{}
			for {
				page, err := s.ListFindings(t.Context(), query)
				if err != nil {
					t.Fatal(err)
				}
				for _, view := range page.Items {
					want = append(want, view.Listing.Title)
				}
				if page.NextCursor == nil {
					break
				}
				query.Cursor = *page.NextCursor
			}
			query.Limit, query.Cursor = 1, "界面分页不能影响全量导出"
			var writer exportHookWriter
			if sort == "latest" {
				writer.hook = func() error {
					tx, err := db.BeginTx(t.Context(), nil)
					if err != nil {
						return err
					}
					defer tx.Rollback()
					for _, statement := range []string{
						`UPDATE listings SET title='已变化',price=1`,
						`UPDATE findings SET found_at=99999,score=100`,
						`DELETE FROM findings WHERE item_id='item-404'`,
						`INSERT INTO listings(item_id,title,desktop_url,first_seen_at,last_seen_at) VALUES('new','新记录','https://example.invalid/new',1,1)`,
					} {
						if _, err := tx.ExecContext(t.Context(), statement); err != nil {
							return err
						}
					}
					if _, err := tx.ExecContext(t.Context(), `INSERT INTO findings(task_id,item_id,search_generation,found_at,is_baseline,decision,stage,is_read) VALUES(?,'new',1,99999,0,'matched','done',0)`, taskID); err != nil {
						return err
					}
					return tx.Commit()
				}
			}
			info, err := s.ExportFindingsCSV(t.Context(), taskID, query, &writer)
			if err != nil || info.Rows != 405 || info.Filename != "查询任务-2030-11-12.csv" || info.MediaType != "text/csv; charset=utf-8" {
				t.Fatal("全量导出错误", info, err)
			}
			rows := exportReadCSV(t, writer.Bytes())
			got := []string{}
			for _, row := range rows[1:] {
				got = append(got, row[0])
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatal("导出遗漏、重复、混入新快照或排序不同", len(got), len(want))
			}
		})
	}
}

func TestExportFindingsCSVFieldsPreserveTextMoneyAndKnownAvoid(t *testing.T) {
	s, database := exportFixture(t)
	db := database.DB()
	taskID := queryTask(t, db, nil)
	title := " \t＝HYPERLINK(\"a\",\"b\")\r孤回车\r\n第二\n行😀"
	queryListing(t, db, "fields", title, queryPtr(limits.MaxSafeInteger))
	queryExec(t, db, `UPDATE listings SET seller_nick=' @卖家',area='+地区' WHERE item_id='fields'`)
	id := queryFinding(t, db, taskID, "fields", analysis.DecisionScored, queryPtr(analysis.GradeAvoid), nil, 0)
	reason := locales.Message{Code: "analysis.title_unmatched", Args: map[string]any{"reason": "=理由,\"原文\"\r孤\n换行"}}
	raw, _ := json.Marshal(reason)
	queryExec(t, db, `UPDATE findings SET reason=?,fair_price=NULL WHERE id=?`, string(raw), id)
	queryExec(t, db, `INSERT INTO task_item_specs(task_id,item_id,kind,specs_json,config_key,source,updated_at) VALUES(?,'fields','sale','[{"name":"容量","value":"256G"},{"name":"颜色","value":null}]','known','user',1)`, taskID)
	var buffer bytes.Buffer
	info, err := s.ExportFindingsCSV(t.Context(), taskID, FindingQuery{View: "all"}, &buffer)
	if err != nil || info.Rows != 1 {
		t.Fatal(info, err)
	}
	rows := exportReadCSV(t, buffer.Bytes())
	row := rows[1]
	wantTitle := strings.ReplaceAll("'"+title, "\r\n", "\n")
	if row[0] != wantTitle || row[1] != "90071992547409.91" || row[2] != "容量: 256G、颜色: 规格不明" || row[3] != "" || row[4] != "" || row[5] != "不建议" || row[6] != "不建议" || row[9] != "' @卖家" || row[10] != "'+地区" || row[11] != "1970-01-01T09:00:00.000+09:00" || row[12] != "https://example.invalid/item/fields" {
		t.Fatal("已知值、中文、精确数字或文本防公式错误", row)
	}
	if row[8] != "'=理由,\"原文\"\r孤\n换行" || !bytes.Contains(buffer.Bytes(), []byte("\r孤回车\r\n第二\n行😀")) || !bytes.HasSuffix(buffer.Bytes(), []byte("\r\n")) {
		t.Fatal("字段内换行或记录结尾被破坏", buffer.String())
	}
	var stored string
	if err := db.QueryRowContext(t.Context(), `SELECT title FROM listings WHERE item_id='fields'`).Scan(&stored); err != nil || stored != title {
		t.Fatal("导出改了数据库原文", stored, err)
	}
	for _, decision := range []analysis.Decision{DecisionPending, DecisionFailed, analysis.DecisionScored} {
		queryExec(t, db, `UPDATE findings SET decision=?,grade=NULL,score=NULL,fair_price=999 WHERE id=?`, decision, id)
		buffer.Reset()
		if _, err := s.ExportFindingsCSV(t.Context(), taskID, FindingQuery{View: "all"}, &buffer); err != nil {
			t.Fatal(err)
		}
		if got := exportReadCSV(t, buffer.Bytes())[1][2:6]; !reflect.DeepEqual(got, []string{"", "", "", ""}) {
			t.Fatal("未评分发现泄漏旧值", decision, got)
		}
	}
	queryExec(t, db, `UPDATE findings SET decision='scored',grade='steal',score=0,fair_price=999 WHERE id=?`, id)
	queryExec(t, db, `UPDATE tasks SET judge_mode='keyword' WHERE id=?`, taskID)
	buffer.Reset()
	if _, err := s.ExportFindingsCSV(t.Context(), taskID, FindingQuery{View: "all"}, &buffer); err != nil {
		t.Fatal(err)
	}
	if got := exportReadCSV(t, buffer.Bytes())[1][2:6]; !reflect.DeepEqual(got, []string{"", "", "", ""}) {
		t.Fatal("关键词任务泄漏保留的评分字段", got)
	}
}

func TestExportFindingsCSVReusesEffectiveChecksAndSavedConclusion(t *testing.T) {
	s, database := exportFixture(t)
	db := database.DB()
	taskID := queryTask(t, db, nil)
	profile := analysis.Profile{Risks: []analysis.ProfileRisk{
		{ID: "carrier_lock", Attitude: analysis.Confirm, Rev: 2},
		{ID: "refurbished", Attitude: analysis.Mind, Rev: 1},
		{ID: "missing_parts", Attitude: analysis.Ignore, Rev: 1},
		{ID: "c1", Name: "自定义检查", Attitude: analysis.Confirm, Rev: 1},
	}, NextNo: 2}
	raw, _ := json.Marshal(profile)
	queryExec(t, db, `UPDATE tasks SET profile_json=? WHERE id=?`, string(raw), taskID)
	queryListing(t, db, "risks", "风险", queryPtr(int64(0)))
	id := queryFinding(t, db, taskID, "risks", analysis.DecisionScored, queryPtr(analysis.GradeGood), queryPtr(int64(0)), 0)
	facts := analysis.Facts{Checks: []analysis.CheckFact{
		{Key: "carrier_lock", Status: analysis.CheckOK, Rev: 1},
		{Key: "refurbished", Status: analysis.CheckSuspect, Rev: 1},
		{Key: "missing_parts", Status: analysis.CheckRisk, Rev: 1},
		{Key: "c1", Status: analysis.CheckRisk, Rev: 1},
	}, ExtraFindings: []analysis.ExtraFinding{{Name: "清单外"}, {Name: "清单外"}}}
	raw, _ = json.Marshal(facts)
	queryExec(t, db, `UPDATE findings SET analysis_json=?,user_overrides_json='{"risks":{"c1":{"status":"ok","rev":1}}}' WHERE id=?`, string(raw), id)
	var buffer bytes.Buffer
	if _, err := s.ExportFindingsCSV(t.Context(), taskID, FindingQuery{View: "all"}, &buffer); err != nil {
		t.Fatal(err)
	}
	row := exportReadCSV(t, buffer.Bytes())[1]
	if row[7] != "网络锁、翻新、缺配件、清单外" || row[4] != "0" || row[6] != "推荐" || row[1] != "0.00" {
		t.Fatal("有效风险、忽略态度或原保存结论被重算", row)
	}
	queryExec(t, db, `UPDATE listings SET is_dismissed=1 WHERE item_id='risks'`)
	buffer.Reset()
	if _, err := s.ExportFindingsCSV(t.Context(), taskID, FindingQuery{View: "hidden"}, &buffer); err != nil {
		t.Fatal(err)
	}
	if row := exportReadCSV(t, buffer.Bytes())[1]; row[6] == "推荐" {
		t.Fatal("已隐藏商品仍算当前推荐", row)
	}
}

func TestExportCSVTextAndMoneyBoundaries(t *testing.T) {
	for _, text := range []string{"=1", " +1", "\t-1", "\r\n@x", "\ufeff＝x", "\u200b＋x", "－x", "＠x"} {
		if got := exportCSVText(text); got != "'"+text {
			t.Fatal("危险文本没有只加前缀", text, got)
		}
	}
	for _, text := range []string{"", " 普通", "'@x", "1+2", "中文=公式", "\t文字"} {
		if got := exportCSVText(text); got != text {
			t.Fatal("普通文本被改动", text, got)
		}
	}
	for value, want := range map[int64]string{0: "0.00", 1: "0.01", -1: "-0.01", 123450: "1234.50", -9223372036854775808: "-92233720368547758.08"} {
		if got := exportMoney(&value); got != want {
			t.Fatal("整数金额丢精度", value, got, want)
		}
	}
}

func TestExportFindingsFiltersAndConclusionFollowCurrentList(t *testing.T) {
	s, database := exportFixture(t)
	db := database.DB()
	taskID := queryTask(t, db, queryPtr(int64(10000)))
	types := []struct {
		name     string
		decision analysis.Decision
		grade    *analysis.Grade
		price    *int64
	}{
		{"推荐", analysis.DecisionScored, queryPtr(analysis.GradeGood), queryPtr(int64(10000))},
		{"合理", analysis.DecisionScored, queryPtr(analysis.GradeFair), queryPtr(int64(10000))},
		{"超预算", analysis.DecisionMatched, nil, queryPtr(int64(10001))},
		{"价格未知", analysis.DecisionMatched, nil, nil},
		{"求购", analysis.DecisionRejected, nil, queryPtr(int64(1000))},
		{"不符合", analysis.DecisionRejected, nil, queryPtr(int64(1000))},
		{"隐藏", analysis.DecisionMatched, nil, queryPtr(int64(1000))},
	}
	for _, item := range types {
		queryListing(t, db, item.name, item.name, item.price)
		id := queryFinding(t, db, taskID, item.name, item.decision, item.grade, nil, 1)
		switch item.name {
		case "求购":
			queryExec(t, db, `UPDATE findings SET reject_kind='junk',reason='{"code":"analysis.junk.wanted","args":{}}' WHERE id=?`, id)
		case "不符合":
			queryExec(t, db, `UPDATE findings SET reject_kind='unmatched' WHERE id=?`, id)
		case "隐藏":
			queryExec(t, db, `UPDATE listings SET is_dismissed=1 WHERE item_id=?`, item.name)
		}
	}
	queryExec(t, db, `UPDATE listings SET last_drop_amount=1,last_drop_at=0 WHERE item_id='推荐'`)
	for _, query := range []FindingQuery{
		{}, {View: "other"}, {View: "hidden"}, {View: "all", Q: "求购"},
		{View: "all", Decision: queryPtr(analysis.DecisionRejected)},
		{View: "all", PriceDrop: queryPtr(true)}, {View: "all", PriceDrop: queryPtr(false)},
		{View: "all", OverBudget: queryPtr(true)}, {View: "all", OverBudget: queryPtr(false)},
		{View: "all", Q: "没有这件商品"},
	} {
		query.TaskID = &taskID
		page, err := s.ListFindings(t.Context(), query)
		if err != nil {
			t.Fatal(err)
		}
		var buffer bytes.Buffer
		info, err := s.ExportFindingsCSV(t.Context(), taskID, query, &buffer)
		if err != nil || info.Rows != page.Total {
			t.Fatal("导出与正式列表筛选不符", query, info, page.Total, err)
		}
		rows := exportReadCSV(t, buffer.Bytes())
		for i, view := range page.Items {
			if rows[i+1][0] != view.Listing.Title {
				t.Fatal("正式筛选后的顺序不符", query, rows, page.Items)
			}
			want := map[string]string{"推荐": "推荐", "合理": "价格合理", "超预算": "超出预算", "价格未知": "未推荐", "求购": "求购帖", "不符合": "不符合需求", "隐藏": "未推荐"}[view.Listing.Title]
			if rows[i+1][6] != want {
				t.Fatal("中文结论使用了错误资格或改写历史判断", rows[i+1], want)
			}
		}
	}
}

func TestExportLaterBatchFailureKeepsErrorAndReleasesRead(t *testing.T) {
	s, database := exportFixture(t)
	taskID := queryTask(t, s.db, nil)
	for i := range 202 {
		item := fmt.Sprintf("batch-%03d", i)
		queryListing(t, s.db, item, item, nil)
		id := queryFinding(t, s.db, taskID, item, analysis.DecisionMatched, nil, nil, int64(202-i))
		if i == 201 {
			queryExec(t, s.db, `UPDATE findings SET reason='bad-json' WHERE id=?`, id)
		}
	}
	var csvBuffer bytes.Buffer
	if info, err := s.ExportFindingsCSV(t.Context(), taskID, FindingQuery{Sort: "latest", View: "all"}, &csvBuffer); err == nil || info != (ExportInfo{}) {
		t.Fatal("第二批坏行没有使整个生成失败", info, err)
	}
	if rows := exportReadCSV(t, csvBuffer.Bytes()); len(rows) != 202 {
		t.Fatal("错误前已成功写出的行数异常", len(rows))
	}
	runID := exportRun(t, s, taskID, "后批失败", queryPtr(int64(1)), true)
	for seq := 1; seq <= 202; seq++ {
		detail := `{"code":"run.cancelled","args":{}}`
		if seq == 202 {
			detail = `bad-json`
		}
		queryExec(t, s.db, `INSERT INTO run_steps(run_id,seq,status,detail) VALUES(?,?,'fail',?)`, runID, seq, detail)
	}
	var logBuffer bytes.Buffer
	if info, err := s.ExportRunLogs(t.Context(), RunQuery{}, locales.ZhCN, &logBuffer); err == nil || info != (ExportInfo{}) {
		t.Fatal("第二批步骤坏行没有使整个生成失败", info, err)
	}
	if rows := exportReadTSV(t, logBuffer.Bytes()); len(rows) != 202 {
		t.Fatal("步骤错误前已写出的行数异常", len(rows))
	}
	if err := database.ReadSnapshot(t.Context(), func(tx *sql.Tx) error { return tx.QueryRowContext(t.Context(), "SELECT 1").Scan(new(int)) }); err != nil {
		t.Fatal("后批失败没有关闭只读快照", err)
	}
}

func exportRun(t *testing.T, s *Service, taskID int64, name string, start *int64, finished bool) int64 {
	t.Helper()
	result := queryExec(t, s.db, `INSERT INTO run_records(task_id,task_name,trigger,started_at,ended_at,outcome,failure_kind,page_count,warnings_json) VALUES(?,?,'manual',?,?,'failed','restart',0,'[]')`, taskID, name, start, func() *int64 {
		if finished {
			return start
		}
		return nil
	}())
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func exportReadTSV(t *testing.T, raw []byte) [][]string {
	t.Helper()
	if bytes.HasPrefix(raw, []byte{0xef, 0xbb, 0xbf}) || !bytes.HasSuffix(raw, []byte("\n")) {
		t.Fatal("文本日志BOM或结尾错误")
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	rows := make([][]string, len(lines))
	for i, line := range lines {
		rows[i] = strings.Split(line, "\t")
		if len(rows[i]) != 30 || strings.ContainsAny(line, "\r\u2028\u2029") {
			t.Fatal("文本日志不是三十列单物理行", i, rows[i])
		}
	}
	if !reflect.DeepEqual(rows[0], runExportHeader) {
		t.Fatal("文本日志表头错误", rows[0])
	}
	return rows
}

func TestExportRunLogsAllRowsStepsAndUnknownTimes(t *testing.T) {
	s, database := exportFixture(t)
	taskID := queryTask(t, database.DB(), nil)
	for i := range 405 {
		start := int64(i % 3)
		var at *int64
		if i%7 != 0 {
			at = &start
		}
		exportRun(t, s, taskID, fmt.Sprintf("旧任务-%03d", i), at, i%5 != 0)
	}
	query := RunQuery{TaskID: &taskID, ListQuery: ListQuery{Limit: 67}}
	want := []string{}
	for {
		page, err := s.ListRuns(t.Context(), query)
		if err != nil {
			t.Fatal(err)
		}
		for _, run := range page.Items {
			want = append(want, strconv.FormatInt(run.ID, 10))
		}
		if page.NextCursor == nil {
			break
		}
		query.Cursor = *page.NextCursor
	}
	runID, _ := strconv.ParseInt(want[0], 10, 64)
	message := locales.Message{Code: "analysis.title_unmatched", Args: map[string]any{"reason": "原文\\n\tTAB\rCR\nLF\u2028分隔\u2029😀"}}
	raw, _ := json.Marshal(message)
	for seq := range 211 {
		queryExec(t, s.db, `INSERT INTO run_steps(run_id,seq,at,status,message,detail) VALUES(?,?,NULL,'fail',?,?)`, runID, seq+1, string(raw), string(raw))
	}
	var writer exportHookWriter
	writer.hook = func() error {
		_, err := s.db.ExecContext(t.Context(), `DELETE FROM run_records WHERE id=?`, runID)
		return err
	}
	query.Cursor, query.Limit = "从全部开始", 1
	info, err := s.ExportRunLogs(t.Context(), query, locales.En, &writer)
	if err != nil || info.Rows != 616 || info.Filename != "kakatta-runs-2030-11-12.txt" {
		t.Fatal(info, err)
	}
	rows := exportReadTSV(t, writer.Bytes())
	got, steps := []string{}, 0
	for _, row := range rows[1:] {
		if row[0] == "run" {
			got = append(got, row[1])
			if row[9] == "" && (row[10] != "" || row[11] != "" || row[28] != "") {
				t.Fatal("在途记录泄漏失败占位", row)
			}
			if row[13] != "0" || row[14] != "" {
				t.Fatal("零与未知计数被混淆", row)
			}
		} else {
			steps++
			if row[25] != strconv.Itoa(steps) || row[26] != "" || row[1] != want[0] || row[28] != `原文\\n\tTAB\rCR\nLF\u2028分隔\u2029😀` || row[29] != row[28] {
				t.Fatal("步骤遗漏、换行语义或未知时间损坏", row)
			}
		}
	}
	if !reflect.DeepEqual(got, want) || steps != 211 {
		t.Fatal("运行跨批或全部步骤漏行", len(got), steps)
	}
}

func TestExportRunLogsFoldWindowAndLocalizedWarnings(t *testing.T) {
	s, _ := exportFixture(t)
	taskID := logTestTask(t, s, "旧名称\t原样")
	now := s.now()
	var run RunRecord
	for _, gap := range []time.Duration{25 * time.Hour, 3 * time.Hour, time.Hour / 2} {
		tx := logTestTx(t, s)
		var err error
		run, err = recordSkipTx(t.Context(), tx, RunRecord{TaskID: &taskID, TaskName: "旧名称\t原样", Trigger: TriggerAuto, Message: &locales.Message{Code: "run.no_account"}}, now.Add(-gap).UnixMilli())
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	warnings, _ := json.Marshal([]locales.Message{{Code: "run.network_failed"}, {Code: "run.cancelled"}})
	queryExec(t, s.db, `UPDATE run_records SET warnings_json=? WHERE id=?`, string(warnings), run.ID)
	queryExec(t, s.db, `UPDATE tasks SET name='新名称' WHERE id=?`, taskID)
	for _, language := range []locales.Language{locales.ZhCN, locales.En} {
		for _, window := range []bool{false, true} {
			query, count := RunQuery{TaskID: &taskID}, "3"
			if window {
				query.FromAt, query.ToAt, count = queryPtr(now.Add(-24*time.Hour).UnixMilli()), queryPtr(now.UnixMilli()), "2"
			}
			var buffer bytes.Buffer
			if _, err := s.ExportRunLogs(t.Context(), query, language, &buffer); err != nil {
				t.Fatal(err)
			}
			row := exportReadTSV(t, buffer.Bytes())[1]
			translated, _ := locales.Format(language, locales.Message{Code: "run.no_account"})
			if row[3] != `旧名称\t原样` || row[12] != count || row[28] != translated || !strings.Contains(row[24], "[") || row[24] == "[]" {
				t.Fatal("窗口次数、旧名称或本地化警告错误", row)
			}
		}
	}
}

func TestExportRetainsHistoricalMessagesAndAllRows(t *testing.T) {
	s, database := exportFixture(t)
	taskID := queryTask(t, database.DB(), nil)
	messages := []struct {
		stored string
		zh     string
		en     string
	}{
		{stored: `{"code":"run.no_account","args":{}}`, zh: "没有可用的闲鱼账号"},
		{stored: `{"code":"unknown.code","args":{"amount":9007199254740993,"ratio":1.2300e+005,"text":"=原文\t\r\n\\n"}}`},
		{stored: `{"code":"analysis.title_unmatched","args":{"old_reason":"旧参数"}}`},
	}
	var err error
	messages[0].en, err = locales.Format(locales.En, locales.Message{Code: "run.no_account"})
	if err != nil {
		t.Fatal(err)
	}
	for i := range messages {
		message := &messages[i]
		if message.zh == "" {
			message.zh, message.en = message.stored, message.stored
		}
		name := fmt.Sprintf("history-%d", i)
		queryListing(t, s.db, name, name, nil)
		id := queryFinding(t, s.db, taskID, name, analysis.DecisionMatched, nil, nil, int64(i))
		queryExec(t, s.db, `UPDATE findings SET reason=? WHERE id=?`, message.stored, id)
		runID := exportRun(t, s, taskID, name, queryPtr(int64(i)), true)
		queryExec(t, s.db, `UPDATE run_records SET message=?,warnings_json=? WHERE id=?`, message.stored, `[`+messages[0].stored+`,`+message.stored+`]`, runID)
		queryExec(t, s.db, `INSERT INTO run_steps(run_id,seq,status,message,detail) VALUES(?,1,'fail',?,?)`, runID, message.stored, message.stored)
	}
	var csvBuffer bytes.Buffer
	info, err := s.ExportFindingsCSV(t.Context(), taskID, FindingQuery{View: "all", Sort: "latest"}, &csvBuffer)
	if err != nil || info.Rows != 3 {
		t.Fatal("历史消息阻断了CSV导出", info, err)
	}
	csvRows := exportReadCSV(t, csvBuffer.Bytes())
	if len(csvRows) != 4 {
		t.Fatal("CSV遗漏行", csvRows)
	}
	for i, row := range csvRows[1:] {
		if row[8] != messages[2-i].zh {
			t.Fatal("CSV未保留历史消息及参数", row[8], messages[2-i].zh)
		}
	}
	unescape := func(field string) string {
		value, err := strconv.Unquote(`"` + strings.ReplaceAll(field, `"`, `\"`) + `"`)
		if err != nil {
			t.Fatal("日志字段无法还原", field, err)
		}
		return value
	}
	var logBuffer bytes.Buffer
	info, err = s.ExportRunLogs(t.Context(), RunQuery{}, locales.En, &logBuffer)
	if err != nil || info.Rows != 6 {
		t.Fatal("历史消息阻断了日志导出", info, err)
	}
	logRows := exportReadTSV(t, logBuffer.Bytes())
	if len(logRows) != 7 {
		t.Fatal("文本日志遗漏运行或步骤", logRows)
	}
	for i := range messages {
		run, step := logRows[1+i*2], logRows[2+i*2]
		want := messages[2-i].en
		if run[0] != "run" || step[0] != "step" || run[1] != step[1] || unescape(run[28]) != want || unescape(step[28]) != want || unescape(step[29]) != want {
			t.Fatal("运行和步骤未保留历史消息及参数", run, step, want)
		}
		var warnings []string
		if err := json.Unmarshal([]byte(unescape(run[24])), &warnings); err != nil || !reflect.DeepEqual(warnings, []string{messages[0].en, want}) {
			t.Fatal("运行警告未同时保留译文和历史内容", warnings, err)
		}
	}
}

type exportFaultWriter struct {
	remaining int
	failure   error
}

func (w *exportFaultWriter) Write(p []byte) (int, error) {
	if len(p) <= w.remaining {
		w.remaining -= len(p)
		return len(p), nil
	}
	n := w.remaining
	w.remaining = 0
	return n, w.failure
}

func TestExportFailuresNeverReportSuccessOrRetainSnapshot(t *testing.T) {
	s, database := exportFixture(t)
	taskID := queryTask(t, database.DB(), nil)
	queryListing(t, s.db, "one", "商品", nil)
	id := queryFinding(t, s.db, taskID, "one", analysis.DecisionMatched, nil, nil, 0)
	runID := exportRun(t, s, taskID, "任务", queryPtr(int64(1)), true)
	methods := []func(context.Context, io.Writer) (ExportInfo, error){
		func(ctx context.Context, writer io.Writer) (ExportInfo, error) {
			return s.ExportFindingsCSV(ctx, taskID, FindingQuery{View: "all"}, writer)
		},
		func(ctx context.Context, writer io.Writer) (ExportInfo, error) {
			return s.ExportRunLogs(ctx, RunQuery{}, locales.ZhCN, writer)
		},
	}
	sentinel := errors.New("写入失败")
	for _, method := range methods {
		for _, failure := range []error{nil, sentinel} {
			info, err := method(t.Context(), &exportFaultWriter{remaining: 10, failure: failure})
			want := failure
			if want == nil {
				want = io.ErrShortWrite
			}
			if !errors.Is(err, want) || info != (ExportInfo{}) {
				t.Fatal("写入失败被当作成功", info, err)
			}
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if info, err := method(ctx, io.Discard); !errors.Is(err, context.Canceled) || info != (ExportInfo{}) {
			t.Fatal("预取消仍导出成功", info, err)
		}
		ctx, cancel = context.WithCancel(t.Context())
		writer := exportHookWriter{hook: func() error { cancel(); return nil }}
		if _, err := method(ctx, &writer); !errors.Is(err, context.Canceled) {
			t.Fatal("写入中取消没被观察到", err)
		}
	}
	for _, failure := range []string{`{}`, `{"code":"broken","args":[]}`, `bad json`} {
		queryExec(t, s.db, `UPDATE findings SET reason=? WHERE id=?`, failure, id)
		queryExec(t, s.db, `UPDATE run_records SET message=? WHERE id=?`, failure, runID)
		for _, method := range methods {
			if info, err := method(t.Context(), io.Discard); err == nil || info != (ExportInfo{}) {
				t.Fatal("坏行被跳过或伪报成功", failure, info, err)
			}
		}
	}
	s.readSnapshot = nil
	for _, method := range methods {
		_, err := method(t.Context(), io.Discard)
		logTestErrorKind(t, err, ErrorUnavailable)
	}
	if _, err := database.DB().ExecContext(t.Context(), `INSERT INTO meta(key,value) VALUES('export.finished','1')`); err != nil {
		t.Fatal("失败后没有归还快照或连接", err)
	}
	s.mu.Lock()
	active := s.mutations
	s.mu.Unlock()
	if active != 0 {
		t.Fatal("失败后泄漏生命周期登记", active)
	}
}

func TestExportCloseCancelsAndJoinsActiveSnapshot(t *testing.T) {
	s, _ := exportFixture(t)
	entered, stopped, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	s.readSnapshot = func(ctx context.Context, _ func(*sql.Tx) error) error {
		close(entered)
		<-ctx.Done()
		close(stopped)
		<-release
		return ctx.Err()
	}
	exported := make(chan error, 1)
	go func() {
		_, err := s.ExportRunLogs(t.Context(), RunQuery{}, locales.ZhCN, io.Discard)
		exported <- err
	}()
	lifecycleWait(t, entered)
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	lifecycleWait(t, stopped)
	select {
	case <-closed:
		t.Fatal("关闭未等导出归还快照")
	default:
	}
	close(release)
	if err := lifecycleResult(t, exported); !errors.Is(err, context.Canceled) {
		t.Fatal("关闭没取消导出", err)
	}
	if err := lifecycleResult(t, closed); err != nil {
		t.Fatal(err)
	}
}
