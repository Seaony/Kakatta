package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis/client"
	"github.com/Seaony/Kakatta/internal/analysis/prompts"
	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/eventstore"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/pacer"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/store"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

type finishNotifier struct {
	flush  func(context.Context, RunRef, []int64) (NotifyResult, error)
	calls  int
	ids    []int64
	events []int64
}

func (n *finishNotifier) FlushRun(ctx context.Context, ref RunRef, ids []int64) (NotifyResult, error) {
	n.calls++
	n.ids = append([]int64(nil), ids...)
	if n.flush != nil {
		return n.flush(ctx, ref, ids)
	}
	return NotifyResult{}, nil
}
func (n *finishNotifier) System(_ context.Context, id int64) error {
	n.events = append(n.events, id)
	return nil
}

type finishPacerClock struct{}

func (finishPacerClock) Sleep(ctx context.Context, _ time.Duration) error { return ctx.Err() }
func (finishPacerClock) Float64() float64                                 { return 0 }

type finishModels func(context.Context, client.Request) (client.Result, error)

func (f finishModels) Complete(ctx context.Context, request client.Request) (client.Result, error) {
	return f(ctx, request)
}

func finishExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal(query, err)
	}
}

func finishScalar[T any](t *testing.T, db *sql.DB, query string, args ...any) T {
	t.Helper()
	var value T
	if err := db.QueryRowContext(t.Context(), query, args...).Scan(&value); err != nil {
		t.Fatal(query, err)
	}
	return value
}

func finishFixture(t *testing.T) (*Service, *runExecution, *finishNotifier, time.Time) {
	t.Helper()
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "finish.db"))
	if err != nil {
		t.Fatal(err)
	}
	config, err := appsettings.New(t.Context(), db.DB(), settings.Environment{TZ: "UTC"}, func() time.Time { return now })
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := config.Apply(t.Context(), settings.Values{
		"crawl.quiet_hours":             json.RawMessage(`{"enabled":false,"start":"01:00","end":"07:30"}`),
		"crawl.interval_jitter_percent": json.RawMessage(`0`),
	}); err != nil {
		db.Close()
		t.Fatal(err)
	}
	notifier := &finishNotifier{}
	s := &Service{db: db.DB(), settings: config, now: func() time.Time { return now }, notifier: notifier,
		pacer: pacer.New(finishPacerClock{}), events: eventstore.Store{DB: db.DB(), Now: func() time.Time { return now }}}
	s.initRuntime()
	t.Cleanup(func() {
		s.cancel(errServiceClosed)
		s.jobs.Wait()
		db.Close()
	})
	finishExec(t, s.db, `INSERT INTO accounts(id,name,state) VALUES(1,'账号','active')`)
	finishExec(t, s.db, `INSERT INTO tasks(id,name,keyword,account_id,max_pages,interval_minutes,judge_mode,notify_policy,sort_index,
		consecutive_failures,failure_paused_until,next_run_at) VALUES(1,'任务','手机',1,1,5,'keyword','recommended',0,2,?,?)`,
		now.Add(time.Hour).UnixMilli(), now.Add(2*time.Hour).UnixMilli())
	started := now.Add(-time.Minute).UnixMilli()
	finishExec(t, s.db, `INSERT INTO run_records(id,task_id,task_name,trigger,started_at,outcome,failure_kind)
		VALUES(1,1,'任务','manual',?,'failed','restart')`, started)
	snapshot, err := config.Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	promptSnapshot, err := prompts.Resolve(map[client.Kind]string{client.Market: "旧行情模板"})
	if err != nil {
		t.Fatal(err)
	}
	run := &runExecution{Ref: RunRef{RunID: 1, TaskID: 1, SearchGeneration: 1}, Snapshot: snapshot,
		Prompts: promptSnapshot, Account: Account{ID: 1}, PageCount: 1, FirstPageCount: 1, AnalyzedCount: 1,
		SearchSaved: SearchSaved{ScannedCount: 1, NewCount: 1}, FirstConclusionIDs: []int64{41},
		Record: RunRecord{ID: 1, TaskID: finishInt64(1), TaskName: "任务", Trigger: TriggerManual, StartedAt: &started},
	}
	return s, run, notifier, now
}

func finishInt64(value int64) *int64 { return &value }

func TestFinishRecordStopKinds(t *testing.T) {
	cases := []struct {
		name    string
		cause   error
		outcome RunOutcome
		kind    FailureKind
	}{
		{"成功", nil, RunSuccess, ""},
		{"页面", errors.New("含敏感地址的原始错误"), RunFailed, FailurePage},
		{"网络", &finishNetworkError{}, RunFailed, FailureNetwork},
		{"超时", errRunTimeout, RunFailed, FailureTimeout},
		{"页面内部截止", context.DeadlineExceeded, RunFailed, FailurePage},
		{"整体截止", errors.Join(context.DeadlineExceeded, errRunTimeout), RunFailed, FailureTimeout},
		{"验证码", web.ErrNeedVerify, RunFailed, FailureRiskVerify},
		{"拒绝", web.ErrDenied, RunFailed, FailureRiskDeny},
		{"环境", web.ErrEnvBlocked, RunFailed, FailureRiskEnv},
		{"掉登录", web.ErrLoginExpired, RunSkipped, FailureLogin},
		{"额度", web.ErrQuota, RunSkipped, FailureQuota},
		{"取消", errUserCancelled, RunFailed, FailureCancelled},
		{"关闭", errors.Join(context.Canceled, errServiceClosed), RunFailed, FailureRestart},
		{"风险不被取消盖住", errors.Join(context.Canceled, web.ErrDenied), RunFailed, FailureRiskDeny},
		{"改条件优先", errors.Join(errSearchChanged, web.ErrDenied), RunFailed, FailureCancelled},
		{"存储", storageFailure(errors.New("内部错误")), RunFailed, FailureConfig},
		{"存储优先于取消", errors.Join(context.Canceled, storageFailure(errors.New("日志写入失败"))), RunFailed, FailureConfig},
		{"模型记账优先于取消", errors.Join(context.Canceled, client.ErrInfrastructure), RunFailed, FailureConfig},
		{"模型配置变化", client.ErrConfigChanged, RunFailed, FailureConfig},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			record := finishRecord(&runExecution{}, test.cause)
			if record.Outcome == nil || *record.Outcome != test.outcome {
				t.Fatalf("结果不符：%+v", record)
			}
			if test.kind == "" && record.FailureKind != nil || test.kind != "" && (record.FailureKind == nil || *record.FailureKind != test.kind) {
				t.Fatalf("失败类型不符：%+v", record)
			}
			if len(record.Message.Args) != 0 {
				t.Fatalf("原始错误不能进入原因参数：%+v", record.Message)
			}
		})
	}
}

type finishNetworkError struct{}

func (*finishNetworkError) Error() string   { return "网络错误" }
func (*finishNetworkError) Timeout() bool   { return false }
func (*finishNetworkError) Temporary() bool { return true }

func TestFinishRunUsesCurrentPlanAndDoesNotHoldTransactionDuringNotify(t *testing.T) {
	s, run, notifier, now := finishFixture(t)
	var published bool
	s.publish = func(_ string, _ *locales.Message, _ json.RawMessage) error {
		published = finishScalar[int64](t, s.db, `SELECT ended_at FROM run_records WHERE id=1`) == now.UnixMilli()
		return nil
	}
	notifier.flush = func(ctx context.Context, ref RunRef, ids []int64) (NotifyResult, error) {
		bounded, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		_, err := s.db.ExecContext(bounded, `UPDATE tasks SET interval_minutes=7 WHERE id=1`)
		return NotifyResult{NotifiedCount: 2}, err
	}
	if err := s.finishRun(t.Context(), run, nil); err != nil {
		t.Fatal(err)
	}
	if !published || notifier.calls != 1 || !reflect.DeepEqual(notifier.ids, []int64{41}) {
		t.Fatalf("提交/通知边界不符：published=%t calls=%d ids=%v", published, notifier.calls, notifier.ids)
	}
	if got := finishScalar[int64](t, s.db, `SELECT next_run_at FROM tasks WHERE id=1`); got != now.Add(7*time.Minute).UnixMilli() {
		t.Fatalf("旧运行配置覆盖当前间隔：%d", got)
	}
	if got := finishScalar[int64](t, s.db, `SELECT last_run_at FROM tasks WHERE id=1`); got != *run.Record.StartedAt {
		t.Fatal("最近运行时间不是本次开始时间", got)
	}
	if got := finishScalar[int64](t, s.db, `SELECT last_success_at FROM tasks WHERE id=1`); got != now.UnixMilli() {
		t.Fatal("成功时间不符", got)
	}
	if got := finishScalar[int](t, s.db, `SELECT consecutive_failures+baseline_pending FROM tasks WHERE id=1`); got != 0 {
		t.Fatal("成功没有清失败计数和基线", got)
	}
	if err := s.finishRun(t.Context(), run, errors.New("晚到的重复收尾")); err != nil || notifier.calls != 1 {
		t.Fatalf("重复收尾再次通知：%d %v", notifier.calls, err)
	}
}

func TestFinishRunFailureCountersAndCancellation(t *testing.T) {
	for _, test := range []struct {
		name   string
		cause  error
		count  int64
		events int
	}{
		{"页面", errors.New("页面失败"), 3, 1},
		{"超时", errRunTimeout, 3, 1},
		{"验证", web.ErrNeedVerify, 2, 0},
		{"登录", web.ErrLoginExpired, 2, 0},
		{"额度", web.ErrQuota, 2, 0},
		{"用户取消", errUserCancelled, 2, 0},
		{"服务关闭", errServiceClosed, 2, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, run, notifier, now := finishFixture(t)
			cancelled, cancel := context.WithCancel(context.Background())
			cancel()
			run.Context = cancelled
			if err := s.finishRun(t.Context(), run, test.cause); err != nil {
				t.Fatal(err)
			}
			if notifier.calls != 1 || len(notifier.events) != test.events {
				t.Fatalf("中断未收尾或事件不符：%+v", notifier)
			}
			if got := finishScalar[int64](t, s.db, `SELECT consecutive_failures FROM tasks WHERE id=1`); got != test.count {
				t.Fatal("失败计数不符", got)
			}
			if got := finishScalar[int](t, s.db, `SELECT baseline_pending FROM tasks WHERE id=1`); got != 1 {
				t.Fatal("非成功错误地清基线", got)
			}
			if test.events != 0 {
				if got := finishScalar[int64](t, s.db, `SELECT failure_paused_until FROM tasks WHERE id=1`); got != now.Add(30*time.Minute).UnixMilli() {
					t.Fatal("失败暂停时间不符", got)
				}
			}
		})
	}
}

func TestFinishRunChangedGenerationDoesNotTouchNewPlan(t *testing.T) {
	for _, beforeNotify := range []bool{false, true} {
		t.Run(fmt.Sprint(beforeNotify), func(t *testing.T) {
			s, run, notifier, now := finishFixture(t)
			change := func() {
				finishExec(t, s.db, `UPDATE tasks SET search_generation=2,next_run_at=?,baseline_pending=1 WHERE id=1`, now.Add(12*time.Hour).UnixMilli())
			}
			if beforeNotify {
				change()
			} else {
				notifier.flush = func(context.Context, RunRef, []int64) (NotifyResult, error) { change(); return NotifyResult{}, nil }
			}
			if err := s.finishRun(t.Context(), run, nil); err != nil {
				t.Fatal(err)
			}
			if beforeNotify && notifier.calls != 0 {
				t.Fatal("旧代仍然通知")
			}
			if got := finishScalar[int64](t, s.db, `SELECT next_run_at FROM tasks WHERE id=1`); got != now.Add(12*time.Hour).UnixMilli() {
				t.Fatal("旧代覆盖新计划", got)
			}
			if got := finishScalar[int](t, s.db, `SELECT baseline_pending FROM tasks WHERE id=1`); got != 1 {
				t.Fatal("旧代清掉新基线", got)
			}
			if got := finishScalar[string](t, s.db, `SELECT failure_kind FROM run_records WHERE id=1`); got != "cancelled" {
				t.Fatal("旧代没有记取消", got)
			}
		})
	}
}

func TestFinishRunRollsBackFailedFinalTransaction(t *testing.T) {
	s, run, notifier, _ := finishFixture(t)
	finishExec(t, s.db, `CREATE TRIGGER reject_finish BEFORE UPDATE ON run_records BEGIN SELECT RAISE(FAIL,'blocked'); END`)
	if err := s.finishRun(t.Context(), run, errors.New("页面失败")); err == nil {
		t.Fatal("收尾写失败仍返回成功")
	}
	if got := finishScalar[int64](t, s.db, `SELECT consecutive_failures FROM tasks WHERE id=1`); got != 2 {
		t.Fatal("收尾失败仍推进失败计数", got)
	}
	if got := finishScalar[int](t, s.db, `SELECT count(*) FROM system_events`); got != 0 || len(notifier.events) != 0 {
		t.Fatal("回滚事件仍被保存或推送", got, notifier.events)
	}
	if got := finishScalar[bool](t, s.db, `SELECT ended_at IS NULL FROM run_records WHERE id=1`); !got || run.Record.EndedAt != nil {
		t.Fatal("失败仍把运行当作已结束")
	}
}

func TestFinishRunClockFailureStopsNotification(t *testing.T) {
	s, run, notifier, _ := finishFixture(t)
	finishExec(t, s.db, `CREATE TRIGGER reject_clock BEFORE UPDATE ON meta WHEN NEW.key='quota.clock' BEGIN SELECT RAISE(FAIL,'blocked'); END`)
	if err := s.finishRun(t.Context(), run, nil); err == nil || notifier.calls != 0 {
		t.Fatalf("时钟失败仍然发送：%d %v", notifier.calls, err)
	}
}

func TestFinishRunEarlierStorageFailureDoesNotSend(t *testing.T) {
	s, run, notifier, _ := finishFixture(t)
	cause := errors.Join(context.Canceled, storageFailure(errors.New("日志写入失败")))
	if err := s.finishRun(t.Context(), run, cause); err == nil || notifier.calls != 0 || len(notifier.events) != 0 {
		t.Fatalf("已知持久化失败后仍调用外部通知：calls=%d events=%v err=%v", notifier.calls, notifier.events, err)
	}
	if got := finishScalar[string](t, s.db, `SELECT failure_kind FROM run_records WHERE id=1`); got != "config" {
		t.Fatal("持久化失败被泛取消覆盖", got)
	}
	if got := finishScalar[int64](t, s.db, `SELECT consecutive_failures FROM tasks WHERE id=1`); got != 2 {
		t.Fatal("持久化失败进了普通失败保护", got)
	}
}

func TestFinishRunNotificationWarningsAndInfrastructureFailureDiffer(t *testing.T) {
	for _, infrastructure := range []bool{false, true} {
		t.Run(fmt.Sprint(infrastructure), func(t *testing.T) {
			s, run, notifier, _ := finishFixture(t)
			notifier.flush = func(context.Context, RunRef, []int64) (NotifyResult, error) {
				if infrastructure {
					return NotifyResult{}, errors.New("通知持久化失败")
				}
				warning := locales.Message{Code: "fixture.notification_warning", Args: map[string]any{}}
				return NotifyResult{Warnings: []locales.Message{warning, warning}}, nil
			}
			err := s.finishRun(t.Context(), run, nil)
			if (err != nil) != infrastructure {
				t.Fatalf("通知错误边界不符：%v", err)
			}
			if infrastructure {
				if got := finishScalar[string](t, s.db, `SELECT failure_kind FROM run_records WHERE id=1`); got != "config" {
					t.Fatal("基础设施错误被当作普通页面错误", got)
				}
				if got := finishScalar[int64](t, s.db, `SELECT consecutive_failures FROM tasks WHERE id=1`); got != 2 {
					t.Fatal("基础设施错误计入普通失败保护", got)
				}
			} else {
				if got := finishScalar[string](t, s.db, `SELECT outcome FROM run_records WHERE id=1`); got != "success" {
					t.Fatal("普通发送警告改变运行结果", got)
				}
				if got := finishScalar[int](t, s.db, `SELECT json_array_length(warnings_json) FROM run_records WHERE id=1`); got != 1 {
					t.Fatal("同轮警告没有去重", got)
				}
			}
		})
	}
}

func TestFinishRunLaterPageLayoutErrorWinsOverEarlierItems(t *testing.T) {
	s, run, notifier, _ := finishFixture(t)
	run.LayoutError = true
	if err := s.finishRun(t.Context(), run, nil); err != nil {
		t.Fatal(err)
	}
	if got := finishScalar[int](t, s.db, `SELECT count(*) FROM system_events WHERE kind='xianyu_layout' AND resolved_at IS NULL`); got != 1 || len(notifier.events) != 1 {
		t.Fatalf("后页结构错误被第一页成功盖住：%d %v", got, notifier.events)
	}
}

func TestFinishSystemEventsPersistSafeReasons(t *testing.T) {
	for _, layout := range []bool{false, true} {
		t.Run(fmt.Sprint(layout), func(t *testing.T) {
			s, run, _, _ := finishFixture(t)
			run.LayoutError = layout
			var cause error
			kind, want := "xianyu_layout", "run.layout_response_invalid"
			if !layout {
				cause = errors.New("不可公开的代理认证信息")
				kind, want = "task_failure_pause", "run.page_failed"
			}
			if err := s.finishRun(t.Context(), run, cause); err != nil {
				t.Fatal(err)
			}
			raw := finishScalar[string](t, s.db, `SELECT message FROM system_events WHERE kind=?`, kind)
			var event struct {
				Args struct {
					Reason locales.Message `json:"reason"`
				} `json:"args"`
			}
			if err := json.Unmarshal([]byte(raw), &event); err != nil || event.Args.Reason.Code != want || strings.Contains(raw, "认证信息") {
				t.Fatalf("事件缺安全的持久原因：%s %v", raw, err)
			}
			t.Log(raw)
		})
	}
}

func TestFinishLayoutEmptyRunsRequirePriorItems(t *testing.T) {
	for _, hadItems := range []bool{false, true} {
		t.Run(fmt.Sprint(hadItems), func(t *testing.T) {
			s, run, _, now := finishFixture(t)
			run.FirstPageCount, run.SearchSaved.ScannedCount, run.SearchSaved.NewCount = 0, 0, 0
			for index := 0; index < 2; index++ {
				at := now.Add(-time.Duration(index+2) * time.Minute).UnixMilli()
				finishExec(t, s.db, `INSERT INTO run_records(task_id,task_name,trigger,started_at,ended_at,outcome,page_count,scanned_count)
					VALUES(1,'任务','auto',?,?,'success',1,0)`, at, at)
			}
			if hadItems {
				at := now.Add(-10 * time.Minute).UnixMilli()
				finishExec(t, s.db, `INSERT INTO run_records(task_id,task_name,trigger,started_at,ended_at,outcome,page_count,scanned_count)
					VALUES(1,'任务','auto',?,?,'success',1,2)`, at, at)
			}
			if err := s.finishRun(t.Context(), run, nil); err != nil {
				t.Fatal(err)
			}
			got := finishScalar[int](t, s.db, `SELECT count(*) FROM system_events WHERE kind='xianyu_layout' AND resolved_at IS NULL`)
			if hadItems && got != 1 || !hadItems && got != 0 {
				t.Fatalf("冷门关键词误报或三次空结果漏报：hadItems=%t events=%d", hadItems, got)
			}
			if hadItems {
				raw := finishScalar[string](t, s.db, `SELECT message FROM system_events WHERE kind='xianyu_layout'`)
				var event struct {
					Args struct {
						Reason locales.Message `json:"reason"`
					} `json:"args"`
				}
				if err := json.Unmarshal([]byte(raw), &event); err != nil || event.Args.Reason.Code != "run.layout_empty_repeated" || event.Args.Reason.Args["count"] != float64(3) {
					t.Fatalf("没有持久化三次空结果事实：%s %v", raw, err)
				}
			}
		})
	}
}

func TestFinishLayoutIgnoresLegacyInterruptedSearches(t *testing.T) {
	s, run, _, now := finishFixture(t)
	run.FirstPageCount, run.SearchSaved.ScannedCount, run.SearchSaved.NewCount = 0, 0, 0
	for index := 0; index < 2; index++ {
		at := now.Add(-time.Duration(index+2) * time.Minute).UnixMilli()
		finishExec(t, s.db, `INSERT INTO run_records(task_id,task_name,trigger,started_at,ended_at,outcome,failure_kind,page_count,scanned_count)
			VALUES(1,'任务','auto',?,?,'failed','risk_deny',1,0)`, at, at)
	}
	at := now.Add(-10 * time.Minute).UnixMilli()
	finishExec(t, s.db, `INSERT INTO run_records(task_id,task_name,trigger,started_at,ended_at,outcome,page_count,scanned_count)
		VALUES(1,'任务','auto',?,?,'success',1,2)`, at, at)
	if err := s.finishRun(t.Context(), run, nil); err != nil {
		t.Fatal(err)
	}
	if got := finishScalar[int](t, s.db, `SELECT count(*) FROM system_events WHERE kind='xianyu_layout'`); got != 0 {
		t.Fatalf("两条不能确认首屏为空的旧记录触发了告警：%d", got)
	}
}

func TestFinishRecordRetainsParsedItemsWithoutSavedSearch(t *testing.T) {
	s, run, _, _ := finishFixture(t)
	run.PageCount, run.FirstPageCount = 2, 2
	run.Record.ScannedCount = finishInt64(3)
	run.SearchSaved = SearchSaved{}
	if err := s.finishRun(t.Context(), run, web.ErrDenied); err != nil {
		t.Fatal(err)
	}
	if got := finishScalar[int](t, s.db, `SELECT scanned_count FROM run_records WHERE id=1`); got != 3 {
		t.Fatalf("后页中断抹掉了已解析的商品数：%d", got)
	}
	if got := finishScalar[int](t, s.db, `SELECT new_count FROM run_records WHERE id=1`); got != 0 {
		t.Fatalf("后页中断时把未入库的商品算作新发现：%d", got)
	}
}

func seedFinishMarket(t *testing.T, s *Service, now time.Time, samples int, provider bool) {
	t.Helper()
	if provider {
		finishExec(t, s.db, `INSERT INTO ai_providers(id,uuid,name,base_url,vision_model,sort_index)
			VALUES(1,'fixture-provider','本地服务商','http://127.0.0.1.invalid','fixture',0)`)
	}
	for index := 0; index < samples; index++ {
		id := fmt.Sprintf("item-%d", index)
		finishExec(t, s.db, `INSERT INTO listings(item_id,title,price,desktop_url,first_seen_at,last_seen_at)
			VALUES(?,'手机',100,'本地商品链接',?,?)`, id, now.UnixMilli(), now.UnixMilli())
		finishExec(t, s.db, `INSERT INTO task_sightings(task_id,item_id,day,price,last_seen_at) VALUES(1,?,?,100,?)`,
			id, now.Format(time.DateOnly), now.UnixMilli())
		finishExec(t, s.db, `INSERT INTO task_item_specs(task_id,item_id,kind,specs_json,config_key,source,updated_at)
			VALUES(1,?,'sale','[]','*','title',?)`, id, now.UnixMilli())
	}
}

func TestRunInsightEligibilityAndPromptSnapshot(t *testing.T) {
	for _, test := range []struct {
		name     string
		samples  int
		provider bool
		recent   bool
		calls    int
	}{
		{"没有服务商", 10, false, false, 0},
		{"样本不足", 9, true, false, 0},
		{"六小时未到", 10, true, true, 0},
		{"符合资格", 10, true, false, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, run, _, now := finishFixture(t)
			seedFinishMarket(t, s, now, test.samples, test.provider)
			if test.recent {
				finishExec(t, s.db, `INSERT INTO market_insights(task_id,generated_at,text) VALUES(1,?,'旧解读')`, now.Add(-6*time.Hour+time.Millisecond).UnixMilli())
			}
			finishExec(t, s.db, `INSERT INTO prompts(key,content) VALUES('market','新行情模板')`)
			calls := 0
			s.models = finishModels(func(ctx context.Context, request client.Request) (client.Result, error) {
				calls++
				if request.Kind != client.Market || request.System != "旧行情模板" || len(request.Images) != 0 {
					t.Errorf("没有复用本次运行的行情模板/纯文字请求：%+v", request)
				}
				bounded, cancel := context.WithTimeout(ctx, time.Second)
				defer cancel()
				if _, err := s.db.ExecContext(bounded, `UPDATE tasks SET name='调用模型时无事务' WHERE id=1`); err != nil {
					t.Errorf("持有事务调用模型：%v", err)
				}
				return client.Result{Text: "本地行情解读", ProviderID: 1, Model: "fixture"}, nil
			})
			if err := s.generateRunInsight(t.Context(), run); err != nil {
				t.Fatal(err)
			}
			if calls != test.calls {
				t.Fatalf("资格判断错误：calls=%d want=%d", calls, test.calls)
			}
			if test.calls == 1 {
				if got := finishScalar[string](t, s.db, `SELECT text FROM market_insights WHERE task_id=1`); got != "本地行情解读" {
					t.Fatal("解读未保存", got)
				}
				if got := finishScalar[bool](t, s.db, `SELECT json_valid(stats_json) FROM market_insights WHERE task_id=1`); !got {
					t.Fatal("没有保存生成时统计快照")
				}
			}
		})
	}
}

func TestRunInsightCountsOnlySelectedMarketSamples(t *testing.T) {
	for _, test := range []struct {
		name       string
		selected   int
		unselected int
		calls      int
	}{
		{"配置全未勾选", 0, 10, 0},
		{"勾选配置不足十件", 9, 1, 0},
		{"勾选配置恰好十件", 10, 2, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, run, _, now := finishFixture(t)
			seedFinishMarket(t, s, now, test.selected+test.unselected, true)
			finishExec(t, s.db, `UPDATE tasks SET judge_mode='ai',profile_json='{"specs":[{"name":"容量","options":["128G","256G"],"values":["128G"],"source":"user"}],"next_no":1}' WHERE id=1`)
			finishExec(t, s.db, `UPDATE task_item_specs SET specs_json=CASE WHEN CAST(substr(item_id,6) AS INTEGER)<?
				THEN '[{"name":"容量","value":"128G"}]' ELSE '[{"name":"容量","value":"256G"}]' END WHERE task_id=1`, test.selected)
			calls := 0
			s.models = finishModels(func(_ context.Context, request client.Request) (client.Result, error) {
				calls++
				var payload struct {
					Groups []struct {
						Specs []struct{ Value string }
						Daily []struct {
							Day    string
							Sample int
						}
					}
				}
				if err := json.Unmarshal([]byte(request.User), &payload); err != nil {
					t.Fatal(err)
				}
				if len(payload.Groups) != 1 || len(payload.Groups[0].Specs) != 1 || payload.Groups[0].Specs[0].Value != "128G" {
					t.Errorf("解读请求应只带勾选的配置：%s", request.User)
				} else {
					var todaySamples int
					for _, point := range payload.Groups[0].Daily {
						if point.Day == now.Format(time.DateOnly) {
							todaySamples = point.Sample
						}
					}
					if todaySamples != test.selected {
						t.Errorf("请求的今日样本数为 %d，预期 %d", todaySamples, test.selected)
					}
				}
				return client.Result{Text: "本地行情解读", ProviderID: 1, Model: "fixture"}, nil
			})
			if err := s.generateRunInsight(t.Context(), run); err != nil {
				t.Fatal(err)
			}
			if calls != test.calls {
				t.Errorf("模型调用 %d 次，预期 %d 次", calls, test.calls)
			}
			if saved := finishScalar[int](t, s.db, `SELECT count(*) FROM market_insights`); saved != test.calls {
				t.Errorf("保存 %d 条解读，预期 %d 条", saved, test.calls)
			}
		})
	}
}

func TestRunInsightDiscardedAfterGenerationChange(t *testing.T) {
	s, run, _, now := finishFixture(t)
	seedFinishMarket(t, s, now, 10, true)
	s.models = finishModels(func(context.Context, client.Request) (client.Result, error) {
		finishExec(t, s.db, `UPDATE tasks SET search_generation=2 WHERE id=1`)
		return client.Result{Text: "旧代解读", ProviderID: 1, Model: "fixture"}, nil
	})
	if err := s.generateRunInsight(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	if got := finishScalar[int](t, s.db, `SELECT count(*) FROM market_insights`); got != 0 {
		t.Fatal("旧代模型结果被保存", got)
	}
}

func TestRunInsightFailureOnlyAddsWarningAndKeepsPriorInsight(t *testing.T) {
	s, run, _, now := finishFixture(t)
	seedFinishMarket(t, s, now, 10, true)
	finishExec(t, s.db, `INSERT INTO market_insights(task_id,generated_at,text) VALUES(1,?,'旧解读保留')`, now.Add(-7*time.Hour).UnixMilli())
	finishExec(t, s.db, `WITH RECURSIVE steps(seq) AS (VALUES(1) UNION ALL SELECT seq+1 FROM steps WHERE seq<100)
		INSERT INTO run_steps(run_id,seq,at,status,message) SELECT 1,seq,1,'ok','{"code":"fixture.step","args":{}}' FROM steps`)
	s.models = finishModels(func(context.Context, client.Request) (client.Result, error) {
		return client.Result{}, errors.New("不可公开的原始错误与密钥")
	})
	if err := s.finishRun(t.Context(), run, nil); err != nil {
		t.Fatal(err)
	}
	s.jobs.Wait()
	if got := finishScalar[string](t, s.db, `SELECT outcome FROM run_records WHERE id=1`); got != "success" {
		t.Fatal("后台行情错误改变运行结果", got)
	}
	if got := finishScalar[string](t, s.db, `SELECT text FROM market_insights WHERE task_id=1`); got != "旧解读保留" {
		t.Fatal("失败抹掉旧解读", got)
	}
	if got := finishScalar[int](t, s.db, `SELECT count(*) FROM run_steps WHERE run_id=1`); got != 101 {
		t.Fatal("一百步后漏掉了失败的解读步骤", got)
	}
	message := finishScalar[string](t, s.db, `SELECT message FROM run_steps WHERE run_id=1 ORDER BY seq DESC LIMIT 1`)
	var reason locales.Message
	if err := json.Unmarshal([]byte(message), &reason); err != nil || reason.Code != "run.market_failed" || len(reason.Args) != 0 {
		t.Fatalf("行情错误提示含原始错误或不合法：%s %v", message, err)
	}
}

func finishNextExecution(t *testing.T, s *Service, previous *runExecution, now time.Time, id int64) *runExecution {
	t.Helper()
	next := &runExecution{Ref: RunRef{RunID: id, TaskID: previous.Ref.TaskID, SearchGeneration: previous.Ref.SearchGeneration},
		Account: previous.Account, Prompts: previous.Prompts, SearchSaved: previous.SearchSaved,
		PageCount: previous.PageCount, FirstPageCount: previous.FirstPageCount, AnalyzedCount: previous.AnalyzedCount,
		FirstConclusionIDs: append([]int64(nil), previous.FirstConclusionIDs...)}
	started := now.Add(-time.Second).UnixMilli()
	next.Record = RunRecord{ID: id, TaskID: finishInt64(next.Ref.TaskID), TaskName: "任务", Trigger: TriggerManual, StartedAt: &started}
	finishExec(t, s.db, `INSERT INTO run_records(id,task_id,task_name,trigger,started_at,outcome,failure_kind)
		VALUES(?,1,'任务','manual',?,'failed','restart')`, id, started)
	return next
}

func finishAwait(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal("等待本地行情处理信号超时")
	}
}

func TestRunInsightConcurrentSuccessfulRunsOnlyStartOneRequest(t *testing.T) {
	s, first, _, now := finishFixture(t)
	seedFinishMarket(t, s, now, 10, true)
	entered, gate := make(chan struct{}, 2), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(gate) })
	t.Cleanup(unblock)
	var calls atomic.Int64
	s.models = finishModels(func(context.Context, client.Request) (client.Result, error) {
		calls.Add(1)
		entered <- struct{}{}
		<-gate
		return client.Result{Text: "只生成一次", ProviderID: 1, Model: "fixture"}, nil
	})
	if err := s.finishRun(t.Context(), first, nil); err != nil {
		t.Fatal(err)
	}
	finishAwait(t, entered)
	second := finishNextExecution(t, s, first, now, 2)
	if err := s.finishRun(t.Context(), second, nil); err != nil {
		t.Fatal(err)
	}
	// 请求尚未返回，不能靠已经保存的六小时间隔掩盖未注册的并发工作。
	_, release, available := s.claimInsight(first.Ref.TaskID, first.Account.ID)
	if available {
		release()
		t.Error("正在请求的行情没有占住该任务")
	}
	unblock()
	s.jobs.Wait()
	if got := calls.Load(); got != 1 {
		t.Fatalf("同任务并发成功运行发了%d次行情请求", got)
	}
	if got := finishScalar[int](t, s.db, `SELECT count(*) FROM market_insights WHERE task_id=1`); got != 1 {
		t.Fatal("行情结果重复保存", got)
	}
}

func TestRunInsightFailedRequestReleasesTaskForNextRun(t *testing.T) {
	s, first, _, now := finishFixture(t)
	seedFinishMarket(t, s, now, 10, true)
	entered, gate := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(gate) })
	t.Cleanup(unblock)
	var calls atomic.Int64
	s.models = finishModels(func(context.Context, client.Request) (client.Result, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-gate
			return client.Result{}, errors.New("本地首次请求失败")
		}
		return client.Result{Text: "失败后重新生成", ProviderID: 1, Model: "fixture"}, nil
	})
	if err := s.finishRun(t.Context(), first, nil); err != nil {
		t.Fatal(err)
	}
	finishAwait(t, entered)
	unblock()
	s.jobs.Wait()
	second := finishNextExecution(t, s, first, now, 2)
	if err := s.finishRun(t.Context(), second, nil); err != nil {
		t.Fatal(err)
	}
	s.jobs.Wait()
	if got := calls.Load(); got != 2 {
		t.Fatal("失败的行情占用没有释放", got)
	}
	if got := finishScalar[string](t, s.db, `SELECT text FROM market_insights WHERE task_id=1`); got != "失败后重新生成" {
		t.Fatal("失败后未能生成行情", got)
	}
}

func TestRunInsightCloseAndTaskDeletionCancelAndJoin(t *testing.T) {
	for _, deleting := range []bool{false, true} {
		t.Run(fmt.Sprint(deleting), func(t *testing.T) {
			s, run, _, now := finishFixture(t)
			seedFinishMarket(t, s, now, 10, true)
			entered, cancelled, gate := make(chan struct{}), make(chan struct{}), make(chan struct{})
			unblock := sync.OnceFunc(func() { close(gate) })
			t.Cleanup(unblock)
			s.models = finishModels(func(ctx context.Context, _ client.Request) (client.Result, error) {
				close(entered)
				<-ctx.Done()
				close(cancelled)
				<-gate
				return client.Result{Text: "取消后晚到的结果", ProviderID: 1, Model: "fixture"}, nil
			})
			if err := s.finishRun(t.Context(), run, nil); err != nil {
				t.Fatal(err)
			}
			finishAwait(t, entered)
			done := make(chan error, 1)
			go func() {
				if deleting {
					done <- s.DeleteTask(t.Context(), run.Ref.TaskID)
				} else {
					done <- s.Close()
				}
			}()
			finishAwait(t, cancelled)
			select {
			case err := <-done:
				t.Fatalf("行情请求没结束就完成关闭/删除：%v", err)
			default:
			}
			unblock()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("关闭/删除没有等待行情退出")
			}
			if got := finishScalar[int](t, s.db, `SELECT count(*) FROM market_insights`); got != 0 {
				t.Fatal("取消后仍保存晚到行情", got)
			}
			if got := finishScalar[int](t, s.db, `SELECT count(*) FROM run_steps`); got != 0 {
				t.Fatal("生命周期取消仍被当作模型失败写日志", got)
			}
			if deleting && finishScalar[int](t, s.db, `SELECT count(*) FROM tasks WHERE id=1`) != 0 {
				t.Fatal("任务没有在行情退出后删除")
			}
		})
	}
}
