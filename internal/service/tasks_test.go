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
	"sync/atomic"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/store"
)

type taskHarness struct {
	t     *testing.T
	ctx   context.Context
	s     *Service
	clock *atomic.Int64
}

func newTaskHarness(t *testing.T) taskHarness {
	t.Helper()
	database, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	clock := &atomic.Int64{}
	clock.Store(time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC).UnixMilli())
	now := func() time.Time { return time.UnixMilli(clock.Load()) }
	preferences, err := appsettings.New(ctx, database.DB(), settings.Environment{TZ: "UTC"}, now)
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{db: database.DB(), settings: preferences, now: now}
	s.initRuntime()
	t.Cleanup(func() { s.cancel(errServiceClosed) })
	return taskHarness{t: t, ctx: ctx, s: s, clock: clock}
}

func (h taskHarness) exec(query string, args ...any) sql.Result {
	h.t.Helper()
	result, err := h.s.db.ExecContext(h.ctx, query, args...)
	if err != nil {
		h.t.Fatal(err)
	}
	return result
}

func (h taskHarness) count(query string, args ...any) int64 {
	h.t.Helper()
	var count int64
	if err := h.s.db.QueryRowContext(h.ctx, query, args...).Scan(&count); err != nil {
		h.t.Fatal(err)
	}
	return count
}

func taskPatch(values map[string]any) TaskPatch {
	patch := TaskPatch{}
	for key, value := range values {
		patch[key], _ = json.Marshal(value)
	}
	return patch
}

func (h taskHarness) create(extra map[string]any) Task {
	h.t.Helper()
	values := map[string]any{"keyword": "camera"}
	for key, value := range extra {
		values[key] = value
	}
	result, err := h.s.CreateTask(h.ctx, taskPatch(values))
	if err != nil {
		h.t.Fatal(err)
	}
	return result.Task
}

func (h taskHarness) seedFinding(task Task, item string, decision analysis.Decision, stage FindingStage, layer *int64) int64 {
	h.t.Helper()
	h.exec(`INSERT INTO listings(item_id,title,desktop_url,first_seen_at,last_seen_at,price) VALUES(?,?,?,1,2,10000)`, item, "camera "+item, "https://example.invalid/item/"+item)
	result := h.exec(`INSERT INTO findings(task_id,item_id,search_generation,found_at,is_baseline,decision,stage,decided_layer,is_read)
		VALUES(?,?,?,3,0,?,?,?,0)`, task.ID, item, task.SearchGeneration, decision, stage, layer)
	id, err := result.LastInsertId()
	if err != nil {
		h.t.Fatal(err)
	}
	h.exec(`INSERT INTO seen_items(task_id,item_id,first_seen_at) VALUES(?,?,1)`, task.ID, item)
	return id
}

func TestTaskCreateUsesCurrentDefaultsAndExplicitEmptyChannels(t *testing.T) {
	h := newTaskHarness(t)
	h.exec(`INSERT INTO channels(id,uuid,type,name,enabled,config_json) VALUES(1,'one','bark','启用',1,'{}'),(2,'two','bark','停用',0,'{}')`)
	h.exec(`INSERT INTO accounts(id,name,state,created_at) VALUES(1,'较晚','active',2),(2,'较早','active',1),(3,'未登录','signed_out',0)`)
	if _, err := h.s.settings.Apply(h.ctx, settings.Values{"crawl.max_pages": json.RawMessage(`2`), "crawl.task_interval_minutes": json.RawMessage(`45`)}); err != nil {
		t.Fatal(err)
	}
	defaults, err := h.s.TaskDefaults(h.ctx)
	if err != nil || defaults.MaxPages != 2 || defaults.IntervalMinutes != 45 || defaults.JudgeMode != analysis.ModeKeyword || !reflect.DeepEqual(defaults.ChannelIDs, []int64{1}) {
		t.Fatalf("默认草稿不跟当前设置：%+v %v", defaults, err)
	}
	keyword := strings.Repeat("相", 31) + "机"
	task := h.create(map[string]any{"keyword": " " + keyword + " "})
	if task.Keyword != keyword || task.Name != strings.Repeat("相", 30) || task.SearchGeneration != 1 || !task.BaselinePending || task.AccountPinned ||
		task.AccountID == nil || *task.AccountID != 2 || task.CreatedAt == nil || *task.CreatedAt != h.clock.Load() ||
		task.NextRunAt == nil || *task.NextRunAt != h.clock.Load() || !reflect.DeepEqual(task.ChannelIDs, []int64{1}) {
		t.Fatalf("新任务默认值不正确：%+v", task)
	}
	explicit := h.create(map[string]any{"name": "不推送", "channel_ids": []int64{}, "enabled": false, "account_pinned": true, "account_id": 3})
	if explicit.Enabled || len(explicit.ChannelIDs) != 0 || explicit.AccountID == nil || *explicit.AccountID != 3 || !explicit.AccountPinned {
		t.Fatalf("显式零值、空列表或未登录固定账号被覆盖：%+v", explicit)
	}
	view, err := h.s.GetTask(h.ctx, task.ID)
	if err != nil || !reflect.DeepEqual(view.Task, task) || view.Runtime.Phase != TaskIdle || view.FindingCounts["total"] != 0 {
		t.Fatalf("读回任务与保存不一致：%+v %v", view, err)
	}
}

func TestTaskCreateDefaultsToAIOnlyWhenConfigured(t *testing.T) {
	h := newTaskHarness(t)
	h.exec(`INSERT INTO ai_providers(uuid,name,base_url,vision_model,enabled,sort_index) VALUES('provider','本地假模型','http://127.0.0.1:1','vision',1,0)`)
	draft, err := h.s.TaskDefaults(h.ctx)
	if err != nil || draft.JudgeMode != analysis.ModeAI || draft.NotifyPolicy != "good" || len(draft.Profile.Risks) == 0 {
		t.Fatalf("有服务商的默认草稿错误：%+v %v", draft, err)
	}
	if _, err := h.s.CreateTask(h.ctx, taskPatch(map[string]any{"keyword": "camera"})); err == nil {
		t.Fatal("没有规格或必须满足的AI草稿被保存")
	}
	task := h.create(map[string]any{"judge_mode": "keyword"})
	if task.JudgeMode != analysis.ModeKeyword || task.NotifyPolicy != "recommended" || !reflect.DeepEqual(task.Profile, analysis.Profile{}) {
		t.Fatalf("显式关键词模式没有清空AI草稿：%+v", task)
	}
}

func TestTaskSaveFailureRollsBackAndPublishRunsAfterCommit(t *testing.T) {
	h := newTaskHarness(t)
	h.exec(`INSERT INTO channels(id,uuid,type,name,enabled,config_json) VALUES(1,'one','bark','测试',1,'{}')`)
	called := 0
	h.s.publish = func(_ string, _ *locales.Message, _ json.RawMessage) error {
		called++
		if h.count(`SELECT COUNT(*) FROM tasks`) != 1 {
			t.Fatal("发布时事务还没提交")
		}
		return errors.New("事件流不可用")
	}
	h.exec(`CREATE TRIGGER fail_task_channel BEFORE INSERT ON task_channels BEGIN SELECT RAISE(ABORT,'模拟写入失败'); END`)
	if _, err := h.s.CreateTask(h.ctx, taskPatch(map[string]any{"keyword": "camera"})); err == nil || h.count(`SELECT COUNT(*) FROM tasks`) != 0 || called != 0 {
		t.Fatal("渠道失败留下了任务或发布了成功事件", err)
	}
	h.exec(`DROP TRIGGER fail_task_channel`)
	created, err := h.s.CreateTask(h.ctx, taskPatch(map[string]any{"keyword": "camera"}))
	if err != nil || created.Task.ID <= 0 || called != 1 {
		t.Fatalf("提交后的发布失败把保存伪装失败：%+v %v %d", created, err, called)
	}
}

func TestTaskUpdateOnlySearchFieldsResetGeneration(t *testing.T) {
	h := newTaskHarness(t)
	task := h.create(nil)
	h.exec(`UPDATE tasks SET baseline_pending=0 WHERE id=?`, task.ID)
	pending := h.seedFinding(task, "pending", DecisionPending, StageTitle, nil)
	failed := h.seedFinding(task, "failed", DecisionFailed, StageDetail, nil)
	h.exec(`UPDATE findings SET retryable=1 WHERE id=?`, failed)
	concluded := h.seedFinding(task, "concluded", analysis.DecisionMatched, StageDone, taskProfileTestPtr[int64](1))
	h.exec(`UPDATE findings SET notify_kind='recommended' WHERE id=?`, concluded)
	h.exec(`INSERT INTO notification_intents(finding_id,task_id,kind,state,created_at,updated_at,price) VALUES(?,?,'recommended','pending',1,1,10000)`, concluded, task.ID)
	sent := h.seedFinding(task, "sent", analysis.DecisionMatched, StageDone, taskProfileTestPtr[int64](1))
	h.exec(`UPDATE findings SET notify_kind='recommended',notified_at=5 WHERE id=?`, sent)
	unchanged, err := h.s.UpdateTask(h.ctx, task.ID, taskPatch(map[string]any{"keyword": " camera ", "name": "新名称", "max_pages": 4}))
	if err != nil || unchanged.Effects.SearchReset || unchanged.Task.SearchGeneration != 1 || unchanged.Task.BaselinePending || len(unchanged.Warnings) == 0 {
		t.Fatalf("非搜索项或相同规整搜索词切了代：%+v %v", unchanged, err)
	}
	h.clock.Add(5000)
	changed, err := h.s.UpdateTask(h.ctx, task.ID, taskPatch(map[string]any{"keyword": "lens", "interval_minutes": 60}))
	if err != nil || !changed.Effects.SearchReset || changed.Task.SearchGeneration != 2 || !changed.Task.BaselinePending ||
		changed.Task.NextRunAt == nil || *changed.Task.NextRunAt != h.clock.Load()+3600000 {
		t.Fatalf("搜索修改/间隔没有原子保存：%+v %v", changed, err)
	}
	for _, id := range []int64{pending, failed} {
		var decision, stage, reason string
		var retry bool
		if err := h.s.db.QueryRowContext(h.ctx, `SELECT decision,stage,reason,retryable FROM findings WHERE id=?`, id).Scan(&decision, &stage, &reason, &retry); err != nil {
			t.Fatal(err)
		}
		if decision != "skipped" || stage != "done" || retry || !strings.Contains(reason, "run.search_changed") {
			t.Fatalf("旧待处理仍可继续：%s %s %t %s", decision, stage, retry, reason)
		}
	}
	if h.count(`SELECT COUNT(*) FROM findings WHERE id=? AND notify_kind IS NULL AND notify_skip_reason IS NOT NULL`, concluded) != 1 ||
		h.count(`SELECT COUNT(*) FROM findings WHERE id=? AND notify_kind='recommended' AND notified_at=5 AND notify_skip_reason IS NULL`, sent) != 1 ||
		h.count(`SELECT COUNT(*) FROM notification_intents WHERE finding_id=? AND state='cancelled'`, concluded) != 1 ||
		h.count(`SELECT COUNT(*) FROM seen_items WHERE task_id=?`, task.ID) != 4 {
		t.Fatal("旧通知、已发送历史或见过记录处理错误")
	}
}

func TestTaskSearchSaveDoesNotCancelNewGeneration(t *testing.T) {
	h := newTaskHarness(t)
	task := h.create(nil)
	<-h.s.barrier
	barrierHeld := true
	defer func() {
		if barrierHeld {
			h.s.barrier <- struct{}{}
		}
	}()
	h.s.mu.Lock()
	committed := h.s.changed
	h.s.mu.Unlock()
	finished := make(chan error, 1)
	go func() {
		_, err := h.s.UpdateTask(h.ctx, task.ID, taskPatch(map[string]any{"keyword": "lens"}))
		finished <- err
	}()
	select {
	case <-committed:
	case <-h.ctx.Done():
		t.Fatal("保存未提交", h.ctx.Err())
	}
	if h.count(`SELECT search_generation FROM tasks WHERE id=?`, task.ID) != 2 {
		t.Fatal("等待信号不是提交后的代次")
	}
	var cancelled atomic.Bool
	done := make(chan struct{})
	h.s.mu.Lock()
	h.s.runs[task.ID] = &runExecution{Ref: RunRef{TaskID: task.ID, SearchGeneration: 2}, Done: done,
		Cancel: func(error) { cancelled.Store(true); close(done) }}
	h.s.mu.Unlock()
	h.s.barrier <- struct{}{}
	barrierHeld = false
	select {
	case err := <-finished:
		if err != nil || cancelled.Load() {
			t.Fatalf("提交后取消了新代：%v %t", err, cancelled.Load())
		}
	case <-h.ctx.Done():
		t.Fatal("保存没有结束", h.ctx.Err())
	}
	h.s.mu.Lock()
	delete(h.s.runs, task.ID)
	h.s.mu.Unlock()
}

func TestTaskModeSwitchMigratesRetryQueueWithoutResettingHistory(t *testing.T) {
	h := newTaskHarness(t)
	profile := analysis.Profile{Musts: []analysis.Requirement{{Text: "国行", Source: analysis.SourceUser}}}
	task := h.create(map[string]any{"judge_mode": "ai", "profile": profile})
	h.exec(`UPDATE tasks SET baseline_pending=0 WHERE id=?`, task.ID)
	pending := h.seedFinding(task, "pending", DecisionPending, StageTitle, nil)
	retry := h.seedFinding(task, "retry", DecisionFailed, StageTitle, nil)
	abandoned := h.seedFinding(task, "abandoned", DecisionFailed, StageTitle, nil)
	history := h.seedFinding(task, "history", analysis.DecisionScored, StageDone, taskProfileTestPtr[int64](3))
	h.exec(`UPDATE findings SET attempts=2,processing_revision=2 WHERE id IN (?,?)`, pending, retry)
	h.exec(`UPDATE findings SET retryable=1 WHERE id=?`, retry)
	h.exec(`UPDATE findings SET attempts=3,retryable=0,processing_revision=2 WHERE id=?`, abandoned)
	h.exec(`UPDATE findings SET score=88,grade='good' WHERE id=?`, history)
	result, err := h.s.UpdateTask(h.ctx, task.ID, taskPatch(map[string]any{"judge_mode": "keyword"}))
	if err != nil || result.Task.SearchGeneration != 1 || result.Task.BaselinePending || result.Task.NotifyPolicy != "recommended" || !reflect.DeepEqual(result.Task.Profile, analysis.Profile{}) {
		t.Fatalf("模式切换误动搜索基线：%+v %v", result, err)
	}
	if h.count(`SELECT COUNT(*) FROM findings WHERE id IN (?,?) AND stage='detail' AND attempts=2 AND processing_revision=3`, pending, retry) != 2 ||
		h.count(`SELECT COUNT(*) FROM findings WHERE id=? AND stage='title' AND attempts=3 AND processing_revision=2`, abandoned) != 1 ||
		h.count(`SELECT COUNT(*) FROM findings WHERE id=? AND decision='scored' AND score=88 AND grade='good'`, history) != 1 {
		t.Fatal("模式迁移重置了次数、复活放弃项或改了已完成结论")
	}
}

func taskModeChangeFixture(t *testing.T, mode analysis.Mode) (taskHarness, Task, TaskPatch, []int64, []int64) {
	t.Helper()
	h := newTaskHarness(t)
	profile := analysis.Profile{Musts: []analysis.Requirement{{Text: "国行", Source: analysis.SourceUser}}}
	task := h.create(map[string]any{"judge_mode": mode, "profile": profile})
	h.exec(`UPDATE tasks SET baseline_pending=0 WHERE id=?`, task.ID)
	decision, layer := analysis.DecisionMatched, int64(1)
	patch := taskPatch(map[string]any{"judge_mode": "ai", "profile": profile})
	if mode == analysis.ModeAI {
		decision, layer = analysis.DecisionScored, 3
		patch = taskPatch(map[string]any{"judge_mode": "keyword"})
	}
	ids := make([]int64, 6)
	for i, item := range []string{"unjudged", "waiting", "sent", "already_skipped", "sent_waiting_drop", "analyzing"} {
		ids[i] = h.seedFinding(task, item, decision, StageDone, &layer)
		if mode == analysis.ModeAI {
			h.exec(`UPDATE findings SET score=88,grade='good',analysis_json='{"musts":[{"id":"m1","status":"met","rev":1}]}',summary='原来的判断' WHERE id=?`, ids[i])
		} else {
			h.exec(`UPDATE findings SET matched_keywords_json='["camera"]' WHERE id=?`, ids[i])
		}
	}
	h.exec(`UPDATE findings SET recommended_price=12345,last_drop_notified_price=12000,drop_notified_at=70 WHERE task_id=?`, task.ID)
	h.exec(`UPDATE findings SET notify_kind='recommended' WHERE id=?`, ids[1])
	h.exec(`UPDATE findings SET notify_kind='recommended',notified_at=80 WHERE id IN (?,?)`, ids[2], ids[4])
	h.exec(`UPDATE findings SET notify_skip_reason='{"code":"notify.baseline","args":{}}' WHERE id=?`, ids[3])
	stage := StageTitle
	if mode == analysis.ModeKeyword {
		stage = StageDetail
	}
	h.exec(`UPDATE findings SET decision='pending',stage=?,decided_layer=NULL,score=NULL,grade=NULL,analysis_json=NULL WHERE id=?`, stage, ids[5])
	intentIDs := make([]int64, 3)
	for i, values := range []struct {
		finding int64
		kind    string
		state   string
	}{{ids[1], "recommended", "pending"}, {ids[2], "recommended", "sent"}, {ids[4], "price_drop", "pending"}} {
		var sentAt any
		if values.state == "sent" {
			sentAt = int64(80)
		}
		result := h.exec(`INSERT INTO notification_intents(finding_id,task_id,kind,state,created_at,updated_at,price,previous_price,failure_count,first_failed_at,sent_at)
			VALUES(?,?,?,?,10,20,10000,12345,1,30,?)`, values.finding, task.ID, values.kind, values.state, sentAt)
		var err error
		intentIDs[i], err = result.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
	}
	view, err := h.s.GetTask(h.ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	return h, view.Task, patch, ids, intentIDs
}

func taskModeLoadFinding(t *testing.T, h taskHarness, id int64) Finding {
	t.Helper()
	tx, err := h.s.db.BeginTx(h.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	finding, err := loadFindingTx(h.ctx, tx, id)
	if err != nil {
		t.Fatal(err)
	}
	return finding
}

func TestTaskModeChangeSettlesUndeliveredNotifications(t *testing.T) {
	for _, mode := range []analysis.Mode{analysis.ModeAI, analysis.ModeKeyword} {
		t.Run(string(mode), func(t *testing.T) {
			h, task, patch, ids, intentIDs := taskModeChangeFixture(t, mode)
			before := make([]Finding, len(ids))
			for i, id := range ids {
				before[i] = taskModeLoadFinding(t, h, id)
			}
			h.clock.Add(1000)
			result, err := h.s.UpdateTask(h.ctx, task.ID, patch)
			if err != nil || result.Task.SearchGeneration != task.SearchGeneration || result.Task.BaselinePending != task.BaselinePending {
				t.Fatalf("模式切换误动搜索代次或基线：%+v %v", result.Task, err)
			}
			for i, id := range ids[:5] {
				expected := before[i]
				if i == 0 || i == 1 {
					expected.NotifyKind = nil
					expected.NotifySkipReason = &locales.Message{Code: "notify.mode_changed", Args: map[string]any{}}
				}
				actual := taskModeLoadFinding(t, h, id)
				if !reflect.DeepEqual(actual, expected) {
					t.Errorf("第%d件通知没有结算或原结论/成功基准被改变：\n实际%+v\n预期%+v", i, actual, expected)
				}
			}
			pending := taskModeLoadFinding(t, h, ids[5])
			if pending.NotifyKind != nil || pending.NotifySkipReason != nil || pending.Decision != DecisionPending {
				t.Fatal("尚未得到结论的正常补处理被提前禁止通知", pending)
			}
			for i, id := range intentIDs {
				var state string
				var reason sql.NullString
				var price, previous, failures, failedAt, updatedAt int64
				var sentAt sql.NullInt64
				if err := h.s.db.QueryRowContext(h.ctx, `SELECT state,reason,price,previous_price,failure_count,first_failed_at,updated_at,sent_at
					FROM notification_intents WHERE id=?`, id).Scan(&state, &reason, &price, &previous, &failures, &failedAt, &updatedAt, &sentAt); err != nil {
					t.Fatal(err)
				}
				if price != 10000 || previous != 12345 || failures != 1 || failedAt != 30 {
					t.Fatal("取消意图改了原价格或失败记录")
				}
				if i == 1 {
					if state != "sent" || reason.Valid || !sentAt.Valid || sentAt.Int64 != 80 || updatedAt != 20 {
						t.Fatal("已经成功的意图被改写")
					}
					continue
				}
				var message locales.Message
				if state != "cancelled" || !reason.Valid || json.Unmarshal([]byte(reason.String), &message) != nil ||
					message.Code != "notify.mode_changed" || updatedAt != h.clock.Load() || sentAt.Valid {
					t.Errorf("旧模式待发意图没有取消：%s %s %d", state, reason.String, updatedAt)
				}
			}
		})
	}
}

func TestTaskModeChangeNotificationFailureRollsBack(t *testing.T) {
	for _, mode := range []analysis.Mode{analysis.ModeAI, analysis.ModeKeyword} {
		t.Run(string(mode), func(t *testing.T) {
			h, task, patch, ids, _ := taskModeChangeFixture(t, mode)
			before := make([]Finding, len(ids))
			for i, id := range ids {
				before[i] = taskModeLoadFinding(t, h, id)
			}
			published := false
			h.s.publish = func(string, *locales.Message, json.RawMessage) error { published = true; return nil }
			h.exec(`CREATE TRIGGER fail_mode_intent BEFORE UPDATE OF state ON notification_intents WHEN NEW.state='cancelled'
				BEGIN SELECT RAISE(ABORT,'模拟意图写入失败'); END`)
			h.clock.Add(1000)
			if _, err := h.s.UpdateTask(h.ctx, task.ID, patch); err == nil {
				t.Fatal("通知结算没有参与事务，模拟失败仍保存了新模式")
			}
			view, err := h.s.GetTask(h.ctx, task.ID)
			if err != nil || !reflect.DeepEqual(view.Task, task) || published || h.count(`SELECT COUNT(*) FROM notification_intents WHERE state='pending'`) != 2 {
				t.Fatalf("失败没有回滚模式或发布了成功：%+v %v %t", view.Task, err, published)
			}
			for i, id := range ids {
				if actual := taskModeLoadFinding(t, h, id); !reflect.DeepEqual(actual, before[i]) {
					t.Fatal("通知结算失败留下了部分发现修改", actual)
				}
			}
		})
	}
}

func TestTaskProfileIDsSurviveSwitchingThroughKeywordMode(t *testing.T) {
	h := newTaskHarness(t)
	profile := analysis.Profile{Musts: []analysis.Requirement{{Text: "国行", Source: analysis.SourceUser}}}
	task := h.create(map[string]any{"judge_mode": "ai", "profile": profile})
	if task.Profile.Musts[0].ID != "m1" {
		t.Fatal("初始编号错误", task.Profile)
	}
	findingID := h.seedFinding(task, "history", analysis.DecisionScored, StageDone, taskProfileTestPtr[int64](3))
	facts := analysis.Facts{Musts: []analysis.RequirementFact{{ID: "m1", Status: analysis.ReqMet, Rev: 1}}}
	raw, _ := json.Marshal(facts)
	h.exec(`UPDATE findings SET analysis_json=?,score=88,grade='good' WHERE id=?`, string(raw), findingID)
	h.exec(`INSERT INTO task_item_specs(task_id,item_id,kind,specs_json,config_key,excluded,source,updated_at) VALUES(?,'history','sale','[]','*',0,'detail',1)`, task.ID)
	keyword, err := h.s.UpdateTask(h.ctx, task.ID, taskPatch(map[string]any{"judge_mode": "keyword"}))
	if err != nil || !reflect.DeepEqual(keyword.Task.Profile, analysis.Profile{}) {
		t.Fatalf("关键词清单未清空：%+v %v", keyword.Task.Profile, err)
	}
	var storedProfile string
	if err := h.s.db.QueryRowContext(h.ctx, `SELECT profile_json FROM tasks WHERE id=?`, task.ID).Scan(&storedProfile); err != nil || storedProfile != "{}" {
		t.Fatalf("关键词模式暗存了旧清单：%s %v", storedProfile, err)
	}
	profile.Musts[0].Text = "日版"
	ai, err := h.s.UpdateTask(h.ctx, task.ID, taskPatch(map[string]any{"judge_mode": "ai", "profile": profile}))
	if err != nil || ai.Task.Profile.Musts[0].ID != "m2" || ai.Task.Profile.NextNo != 3 {
		t.Fatalf("清空模式让编号重新可用：%+v %v", ai.Task.Profile, err)
	}
	profile = ai.Task.Profile
	profile.Bonuses = []analysis.Requirement{{Text: "原盒", Source: analysis.SourceUser}}
	updated, err := h.s.UpdateTask(h.ctx, task.ID, taskPatch(map[string]any{"profile": profile}))
	if err != nil || updated.Effects.NeedsReanalysisCount != 1 || updated.Task.Profile.Bonuses[0].ID != "b3" {
		t.Fatalf("历史旧met被冒用或编号再次复用：%+v %v", updated, err)
	}
	var saved analysis.Facts
	if err := h.s.db.QueryRowContext(h.ctx, `SELECT analysis_json FROM findings WHERE id=?`, findingID).Scan(&storedProfile); err != nil || json.Unmarshal([]byte(storedProfile), &saved) != nil || saved.Musts[0].ID != "m1" {
		t.Fatal("旧事实被清理或改写", err)
	}
}

func TestTaskProfileWatermarkBackfillsLegacyRowsAndRejectsCorruption(t *testing.T) {
	h := newTaskHarness(t)
	task := h.create(map[string]any{"judge_mode": "ai", "profile": analysis.Profile{Musts: []analysis.Requirement{{Text: "国行", Source: analysis.SourceUser}}}})
	h.exec(`DELETE FROM meta WHERE key=?`, taskProfileWatermarkPrefix+fmt.Sprint(task.ID))
	legacy := task.Profile
	legacy.NextNo, legacy.Musts[0].ID = 0, "m8"
	raw, _ := json.Marshal(legacy)
	h.exec(`UPDATE tasks SET profile_json=? WHERE id=?`, string(raw), task.ID)
	if _, err := h.s.UpdateTask(h.ctx, task.ID, taskPatch(map[string]any{"judge_mode": "keyword"})); err != nil {
		t.Fatal("缺失的旧水位不能安全回填", err)
	}
	profile := analysis.Profile{Musts: []analysis.Requirement{{Text: "日版", Source: analysis.SourceUser}}, NextNo: 1}
	updated, err := h.s.UpdateTask(h.ctx, task.ID, taskPatch(map[string]any{"judge_mode": "ai", "profile": profile}))
	if err != nil || updated.Task.Profile.Musts[0].ID != "m9" {
		t.Fatalf("没有保留旧最大编号，或信了客户端水位：%+v %v", updated.Task.Profile, err)
	}
	for _, corrupt := range []string{"0", "-1", "01", "null", "92233720368547758080"} {
		h.exec(`UPDATE meta SET value=? WHERE key=?`, corrupt, taskProfileWatermarkPrefix+fmt.Sprint(task.ID))
		if _, err := h.s.UpdateTask(h.ctx, task.ID, taskPatch(map[string]any{"name": "不能保存"})); err == nil {
			t.Fatalf("坏水位被当成1：%q", corrupt)
		}
	}
}

func TestTaskUpdateRollbackKeepsGenerationAndProtectedData(t *testing.T) {
	h := newTaskHarness(t)
	task := h.create(nil)
	finding := h.seedFinding(task, "item", DecisionPending, StageDetail, nil)
	h.exec(`CREATE TRIGGER fail_task_update BEFORE UPDATE ON tasks WHEN NEW.keyword='lens' BEGIN SELECT RAISE(ABORT,'模拟更新失败'); END`)
	if _, err := h.s.UpdateTask(h.ctx, task.ID, taskPatch(map[string]any{"keyword": "lens"})); err == nil {
		t.Fatal("模拟更新失败没有返回错误")
	}
	view, err := h.s.GetTask(h.ctx, task.ID)
	if err != nil || view.Keyword != "camera" || view.SearchGeneration != 1 || h.count(`SELECT COUNT(*) FROM findings WHERE id=? AND decision='pending'`, finding) != 1 {
		t.Fatalf("任务失败但旧发现已失效：%+v %v", view, err)
	}
}

func TestTaskModeMigrationFailureRollsBackEntireSave(t *testing.T) {
	h := newTaskHarness(t)
	task := h.create(map[string]any{"judge_mode": "ai", "profile": analysis.Profile{Musts: []analysis.Requirement{{Text: "国行", Source: analysis.SourceUser}}}})
	id := h.seedFinding(task, "pending", DecisionPending, StageTitle, nil)
	h.exec(`CREATE TRIGGER fail_task_mode_migration BEFORE UPDATE OF processing_revision ON findings BEGIN SELECT RAISE(ABORT,'模拟处理队列保存失败'); END`)
	if _, err := h.s.UpdateTask(h.ctx, task.ID, taskPatch(map[string]any{"judge_mode": "keyword"})); err == nil {
		t.Fatal("处理队列保存失败仍然保存了新模式")
	}
	view, err := h.s.GetTask(h.ctx, task.ID)
	if err != nil || view.JudgeMode != analysis.ModeAI || !reflect.DeepEqual(view.Profile, task.Profile) ||
		h.count(`SELECT COUNT(*) FROM findings WHERE id=? AND stage='title' AND processing_revision=1`, id) != 1 {
		t.Fatalf("模式失败没有回滚全部修改：%+v %v", view.Task, err)
	}
}

func TestTaskInvalidReferencesAndRegionsDoNotWrite(t *testing.T) {
	h := newTaskHarness(t)
	for _, values := range []map[string]any{
		{"keyword": "camera", "account_pinned": true, "account_id": 999},
		{"keyword": "camera", "channel_ids": []int64{999}},
		{"keyword": "camera", "region_province": "不存在的地区"},
	} {
		_, err := h.s.CreateTask(h.ctx, taskPatch(values))
		var failure *ServiceError
		if !errors.As(err, &failure) || failure.Kind != ErrorInvalid || h.count(`SELECT COUNT(*) FROM tasks`) != 0 ||
			h.count(`SELECT COUNT(*) FROM meta WHERE key LIKE 'task.profile_next_no.%'`) != 0 {
			t.Fatalf("非法引用被保存或错误被伪装：%v", err)
		}
	}
	task := h.create(nil)
	if _, err := h.s.UpdateTask(h.ctx, task.ID, taskPatch(map[string]any{"channel_ids": []int64{999}})); err == nil {
		t.Fatal("更新接受不存在的渠道")
	}
	view, err := h.s.GetTask(h.ctx, task.ID)
	if err != nil || len(view.ChannelIDs) != 0 {
		t.Fatal("非法渠道更新留下了部分修改", err)
	}
}

func TestTaskExclusionsRequireExplicitConfirmationAndProtectFavorites(t *testing.T) {
	h := newTaskHarness(t)
	task := h.create(nil)
	normal := h.seedFinding(task, "normal", analysis.DecisionMatched, StageDone, taskProfileTestPtr[int64](1))
	favorite := h.seedFinding(task, "favorite", analysis.DecisionMatched, StageDone, taskProfileTestPtr[int64](1))
	rejected := h.seedFinding(task, "rejected", analysis.DecisionRejected, StageDone, taskProfileTestPtr[int64](1))
	h.exec(`UPDATE listings SET description='有拆修记录'`)
	h.exec(`UPDATE listings SET is_favorite=1 WHERE item_id='favorite'`)
	h.exec(`UPDATE findings SET reject_kind='filter' WHERE id=?`, rejected)
	h.exec(`INSERT INTO notification_intents(finding_id,task_id,kind,state,created_at,updated_at,price) VALUES(?,?,'recommended','pending',1,1,10000),(?,?,'recommended','pending',1,1,10000)`, normal, task.ID, favorite, task.ID)
	result, err := h.s.UpdateTask(h.ctx, task.ID, taskPatch(map[string]any{"exclude_keywords": []string{"拆修"}}))
	if err != nil || result.Effects.ExistingExcludeMatchCount != 1 || !reflect.DeepEqual(result.Effects.AddedExcludeWords, []string{"拆修"}) ||
		h.count(`SELECT COUNT(*) FROM findings WHERE id=? AND decision='matched'`, normal) != 1 ||
		h.count(`SELECT COUNT(*) FROM notification_intents WHERE task_id=? AND state='pending'`, task.ID) != 2 {
		t.Fatalf("保存排除词提前隐藏或数量错误：%+v %v", result.Effects, err)
	}
	count, err := h.s.HideAddedExcludes(h.ctx, task.ID, []string{"拆修"})
	if err != nil || count != 1 || h.count(`SELECT COUNT(*) FROM findings WHERE id=? AND decision='rejected' AND reject_kind='excluded' AND decided_layer=1`, normal) != 1 ||
		h.count(`SELECT COUNT(*) FROM findings WHERE id=? AND decision='matched'`, favorite) != 1 ||
		h.count(`SELECT COUNT(*) FROM notification_intents WHERE finding_id=? AND state='cancelled'`, normal) != 1 ||
		h.count(`SELECT COUNT(*) FROM notification_intents WHERE finding_id=? AND state='pending'`, favorite) != 1 ||
		h.count(`SELECT COUNT(*) FROM seen_items WHERE task_id=?`, task.ID) != 3 {
		t.Fatalf("明确隐藏破坏收藏、见过或已拒绝结论：%d %v", count, err)
	}
	if _, err := h.s.HideAddedExcludes(h.ctx, task.ID, []string{"已删词"}); err == nil {
		t.Fatal("陈旧的确认仍然生效")
	}
}

func TestTaskListCursorBindsFilterAndDoesNotTruncateTotal(t *testing.T) {
	h := newTaskHarness(t)
	for i := 0; i < 4; i++ {
		h.create(map[string]any{"name": fmt.Sprintf("相机%d", i), "enabled": i%2 == 0})
	}
	first, err := h.s.ListTasks(h.ctx, TaskQuery{ListQuery: ListQuery{Limit: 1}, Q: "相机"})
	if err != nil || first.Total != 4 || len(first.Items) != 1 || first.NextCursor == nil {
		t.Fatalf("第一页/总数错误：%+v %v", first, err)
	}
	second, err := h.s.ListTasks(h.ctx, TaskQuery{ListQuery: ListQuery{Limit: 1, Cursor: *first.NextCursor}, Q: "相机"})
	if err != nil || second.Total != 4 || len(second.Items) != 1 || second.Items[0].ID <= first.Items[0].ID {
		t.Fatalf("下一页没有稳定前进：%+v %v", second, err)
	}
	if _, err := h.s.ListTasks(h.ctx, TaskQuery{ListQuery: ListQuery{Cursor: *first.NextCursor}, Enabled: taskProfileTestPtr(true)}); err == nil {
		t.Fatal("更改筛选仍接受旧游标")
	}
	empty, err := h.s.ListTasks(h.ctx, TaskQuery{Q: "没有这个任务"})
	if err != nil || empty.Total != 0 || empty.Items == nil || len(empty.Items) != 0 {
		t.Fatalf("空列表不是空数组：%+v %v", empty, err)
	}
	if _, err := h.s.ListTasks(h.ctx, TaskQuery{Q: string([]byte{0xff})}); err == nil {
		t.Fatal("坏UTF8进入了查询和游标绑定")
	}
}

func TestTaskDeleteKeepsFavoriteListingAndHistoricalRun(t *testing.T) {
	h := newTaskHarness(t)
	task := h.create(nil)
	h.seedFinding(task, "favorite", analysis.DecisionMatched, StageDone, taskProfileTestPtr[int64](1))
	h.exec(`UPDATE listings SET is_favorite=1`)
	h.exec(`INSERT INTO run_records(task_id,task_name,trigger,started_at,ended_at,outcome) VALUES(?,'历史任务','auto',1,2,'success')`, task.ID)
	h.exec(`INSERT INTO system_events(kind,subject_id,message,created_at) VALUES('task_failure_pause',?,'{"code":"run.failure_paused","args":{}}',1)`, task.ID)
	h.exec(`INSERT INTO meta(key,value) VALUES(?,?)`, latestSearchPrefix+fmt.Sprint(task.ID), `{"run_id":1,"search_generation":1,"item_ids":["favorite"]}`)
	if err := h.s.DeleteTask(h.ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	if h.count(`SELECT COUNT(*) FROM tasks`) != 0 || h.count(`SELECT COUNT(*) FROM findings`) != 0 || h.count(`SELECT COUNT(*) FROM seen_items`) != 0 ||
		h.count(`SELECT COUNT(*) FROM listings WHERE item_id='favorite' AND is_favorite=1`) != 1 ||
		h.count(`SELECT COUNT(*) FROM run_records WHERE task_id IS NULL AND task_name='历史任务'`) != 1 ||
		h.count(`SELECT COUNT(*) FROM system_events WHERE kind='task_failure_pause' AND subject_id=? AND resolved_at IS NOT NULL`, task.ID) != 1 ||
		h.count(`SELECT COUNT(*) FROM meta WHERE key=?`, latestSearchPrefix+fmt.Sprint(task.ID)) != 0 {
		t.Fatal("删除任务丢收藏、历史或没清任务搜索快照")
	}
	if err := h.s.DeleteTask(h.ctx, task.ID); err == nil {
		t.Fatal("不存在的任务被假报删除成功")
	}
}

func TestTaskStorageCorruptionAndCancelledCallsFailClosed(t *testing.T) {
	h := newTaskHarness(t)
	task := h.create(nil)
	h.exec(`UPDATE tasks SET profile_json='null' WHERE id=?`, task.ID)
	if _, err := h.s.GetTask(h.ctx, task.ID); err == nil {
		t.Fatal("损坏的清单被当作空配置")
	}
	ctx, cancel := context.WithCancel(h.ctx)
	cancel()
	if _, err := h.s.CreateTask(ctx, taskPatch(map[string]any{"keyword": "lens"})); !errors.Is(err, context.Canceled) || h.count(`SELECT COUNT(*) FROM tasks`) != 1 {
		t.Fatal("取消被吞掉或仍写入任务", err)
	}
}
