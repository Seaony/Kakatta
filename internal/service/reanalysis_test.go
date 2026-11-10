package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/analysis/client"
	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/store"
)

func TestReanalysisDetailCacheBoundary(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC).UnixMilli()
	for _, test := range []struct {
		name    string
		fetched *int64
		fresh   bool
	}{
		{name: "unknown"},
		{name: "just_fetched", fetched: reanalysisTestPtr(now), fresh: true},
		{name: "inside_day", fetched: reanalysisTestPtr(now - int64(24*time.Hour/time.Millisecond) + 1), fresh: true},
		{name: "one_day_old", fetched: reanalysisTestPtr(now - int64(24*time.Hour/time.Millisecond))},
		{name: "older", fetched: reanalysisTestPtr(now - int64(25*time.Hour/time.Millisecond))},
		{name: "future", fetched: reanalysisTestPtr(now + 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := detailCached(test.fetched, now); got != test.fresh {
				t.Fatalf("详情缓存可用=%v，预期%v", got, test.fresh)
			}
		})
	}
}

func reanalysisTestPtr[T any](value T) *T { return &value }

func reanalysisTestDB(t *testing.T) (context.Context, *sql.DB, Task, Finding, Listing) {
	t.Helper()
	storage, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "reanalysis.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { storage.Close() })
	db := storage.DB()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	result, err := db.ExecContext(ctx, `INSERT INTO tasks(name,keyword,max_pages,interval_minutes,judge_mode,sort_index,notify_policy,search_generation)
		VALUES('任务','测试',1,60,'ai',0,'good',3)`)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	task := Task{ID: id, SearchGeneration: 3, TaskFields: TaskFields{JudgeMode: analysis.ModeAI}}
	item := Listing{ItemID: "item", Title: "测试商品", DesktopURL: "https://example.invalid/item", Price: reanalysisTestPtr(int64(9000)), DetailFetchedAt: reanalysisTestPtr(int64(990))}
	if _, err := db.ExecContext(ctx, `INSERT INTO listings(item_id,title,desktop_url,price,detail_fetched_at,first_seen_at,last_seen_at)
		VALUES(?,?,?,?,?,1,2)`, item.ItemID, item.Title, item.DesktopURL, item.Price, item.DetailFetchedAt); err != nil {
		t.Fatal(err)
	}
	result, err = db.ExecContext(ctx, `INSERT INTO findings(task_id,item_id,search_generation,processing_revision,found_at,is_baseline,decision,stage,
		attempts,retryable,reason,analysis_json,user_overrides_json,score,grade,scored_price,scored_at,notify_kind,notify_skip_reason,
		notified_at,recommended_price,last_drop_notified_price,drop_notified_at,is_read,reanalysis_origin)
		VALUES(?,?,2,7,3,1,'failed','detail',3,1,'{"code":"old_failure","args":{}}','{"old_fact":true}','{"old_override":true}',88,'good',10000,4,
		'recommended','{"code":"baseline","args":{}}',5,10000,9500,6,1,0)`, task.ID, item.ItemID)
	if err != nil {
		t.Fatal(err)
	}
	findingID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	finding := Finding{ID: findingID, TaskID: task.ID, ItemID: item.ItemID, SearchGeneration: 2, ProcessingRevision: 7}
	return ctx, db, task, finding, item
}

func reanalysisTestTx(t *testing.T, ctx context.Context, db *sql.DB) *sql.Tx {
	t.Helper()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tx.Rollback() })
	return tx
}

func TestPrepareReanalysisResetsWorkAndKeepsHistory(t *testing.T) {
	for _, mode := range []analysis.Mode{analysis.ModeAI, analysis.ModeKeyword} {
		t.Run(string(mode), func(t *testing.T) {
			ctx, db, task, finding, item := reanalysisTestDB(t)
			task.JudgeMode = mode
			tx := reanalysisTestTx(t, ctx, db)
			response, work, err := prepareReanalysisTx(ctx, tx, task, finding, item, analysis.LayerResult{}, 1000)
			if err != nil {
				t.Fatal(err)
			}
			if response.FindingID != finding.ID || response.Status != ReanalysisQueued || response.NeedsDetail || response.Reason != nil || work.FindingID != finding.ID || work.ProcessingRevision != 8 || work.TaskID != task.ID || work.SearchGeneration != task.SearchGeneration {
				t.Fatalf("新处理标识不正确：%+v %+v", response, work)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			var decision, stage string
			var generation, revision, attempts, retryable, requestedAt, origin int64
			var rejectKind, layer, reason any
			if err := db.QueryRowContext(ctx, `SELECT decision,stage,search_generation,processing_revision,attempts,retryable,reanalyze_requested_at,reanalysis_origin,reject_kind,decided_layer,reason FROM findings WHERE id=?`, finding.ID).
				Scan(&decision, &stage, &generation, &revision, &attempts, &retryable, &requestedAt, &origin, &rejectKind, &layer, &reason); err != nil {
				t.Fatal(err)
			}
			wantStage := string(StageTitle)
			if mode == analysis.ModeKeyword {
				wantStage = string(StageDetail)
			}
			if decision != string(DecisionPending) || stage != wantStage || generation != 3 || revision != 8 || attempts != 0 || retryable != 0 || requestedAt != 1000 || origin != 1 || rejectKind != nil || layer != nil || reason != nil {
				t.Fatalf("重分析状态未全部重置：%s %s %d %d %d %d %d %d %v %v %v", decision, stage, generation, revision, attempts, retryable, requestedAt, origin, rejectKind, layer, reason)
			}
			var fact, override, notifyKind, notifyReason, grade string
			var score, scoredPrice, scoredAt, notifiedAt, recommended, lastDropPrice, dropAt, baseline, read int64
			if err := db.QueryRowContext(ctx, `SELECT analysis_json,user_overrides_json,score,grade,scored_price,scored_at,notify_kind,notify_skip_reason,notified_at,recommended_price,last_drop_notified_price,drop_notified_at,is_baseline,is_read FROM findings WHERE id=?`, finding.ID).
				Scan(&fact, &override, &score, &grade, &scoredPrice, &scoredAt, &notifyKind, &notifyReason, &notifiedAt, &recommended, &lastDropPrice, &dropAt, &baseline, &read); err != nil {
				t.Fatal(err)
			}
			if fact != `{"old_fact":true}` || override != `{"old_override":true}` || score != 88 || grade != "good" || scoredPrice != 10000 || scoredAt != 4 || notifyKind != "recommended" || notifyReason != `{"code":"baseline","args":{}}` || notifiedAt != 5 || recommended != 10000 || lastDropPrice != 9500 || dropAt != 6 || baseline != 1 || read != 1 {
				t.Fatal("重分析清掉了事实、用户覆盖、首次记录或通知基准")
			}
		})
	}
}

func TestPrepareReanalysisFirstLayerRejectAndRollback(t *testing.T) {
	ctx, db, task, finding, item := reanalysisTestDB(t)
	item.DetailFetchedAt = nil
	layer := analysis.LayerResult{Rejected: true, RejectKind: analysis.RejectExcluded, Reason: &locales.Message{Code: "analysis.keyword_excluded", Args: map[string]any{"word": "故障"}}}
	tx := reanalysisTestTx(t, ctx, db)
	response, _, err := prepareReanalysisTx(ctx, tx, task, finding, item, layer, 1000)
	if err != nil || response.Status != ReanalysisRejected || response.NeedsDetail || response.Reason != layer.Reason {
		t.Fatalf("第一层拒绝仍入队或要求详情：%+v %v", response, err)
	}
	var decision, stage, kind string
	var decided, origin int64
	var requested any
	if err := tx.QueryRowContext(ctx, `SELECT decision,stage,reject_kind,decided_layer,reanalyze_requested_at,reanalysis_origin FROM findings WHERE id=?`, finding.ID).
		Scan(&decision, &stage, &kind, &decided, &requested, &origin); err != nil {
		t.Fatal(err)
	}
	if decision != "rejected" || stage != "done" || kind != "excluded" || decided != 1 || requested != nil || origin != 1 {
		t.Fatal("第一层拒绝没有立即完成")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var revision int64
	if err := db.QueryRowContext(ctx, `SELECT processing_revision FROM findings WHERE id=?`, finding.ID).Scan(&revision); err != nil || revision != 7 {
		t.Fatalf("调用方回滚没有还原处理版本：%d %v", revision, err)
	}
}

func TestPrepareReanalysisRejectsStaleOrExhaustedWork(t *testing.T) {
	ctx, db, task, finding, item := reanalysisTestDB(t)
	tx := reanalysisTestTx(t, ctx, db)
	if _, _, err := prepareReanalysisTx(ctx, tx, task, finding, item, analysis.LayerResult{}, 1000); err != nil {
		t.Fatal(err)
	}
	if _, _, err := prepareReanalysisTx(ctx, tx, task, finding, item, analysis.LayerResult{}, 1000); err == nil {
		t.Fatal("同毫秒的旧处理版本仍然覆盖了新请求")
	} else {
		var failure *ServiceError
		if !errors.As(err, &failure) || failure.Kind != ErrorConflict || failure.Reason.Code != "findings.changed" {
			t.Fatalf("旧处理版本未返回冲突：%v", err)
		}
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE findings SET processing_revision=? WHERE id=?`, int64(math.MaxInt64), finding.ID); err != nil {
		t.Fatal(err)
	}
	finding.ProcessingRevision = math.MaxInt64
	tx = reanalysisTestTx(t, ctx, db)
	if _, _, err := prepareReanalysisTx(ctx, tx, task, finding, item, analysis.LayerResult{}, 1000); err == nil {
		t.Fatal("耗尽的处理版本发生回绕")
	}
}

type reanalysisModel struct{ calls int }

func (model *reanalysisModel) Complete(context.Context, client.Request) (client.Result, error) {
	model.calls++
	return client.Result{}, errors.New("不应调用模型")
}

func reanalysisTestService(t *testing.T) (context.Context, *Service, int64) {
	t.Helper()
	ctx, db, task, finding, _ := reanalysisTestDB(t)
	profile := analysis.Profile{Musts: []analysis.Requirement{{ID: "m1", Text: "没有故障", Source: analysis.SourceUser, Rev: 1}}, NextNo: 2}
	raw, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE tasks SET profile_json=?,next_run_at=12345 WHERE id=?`, string(raw), task.ID); err != nil {
		t.Fatal(err)
	}
	now := func() time.Time { return time.UnixMilli(1000) }
	preferences, err := appsettings.New(ctx, db, settings.Environment{TZ: "UTC"}, now)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, &Service{db: db, settings: preferences, now: now}, finding.ID
}

func reanalysisAddFinding(t *testing.T, ctx context.Context, db *sql.DB, taskID int64, itemID, title string, fetchedAt *int64) int64 {
	t.Helper()
	if _, err := db.ExecContext(ctx, `INSERT INTO listings(item_id,title,desktop_url,first_seen_at,last_seen_at,detail_fetched_at)
		VALUES(?,?,'https://example.invalid/item',1,2,?)`, itemID, title, fetchedAt); err != nil {
		t.Fatal(err)
	}
	result, err := db.ExecContext(ctx, `INSERT INTO findings(task_id,item_id,search_generation,processing_revision,found_at,is_baseline,decision,stage,is_read)
		VALUES(?,?,2,7,3,0,'pending','title',0)`, taskID, itemID)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func reanalysisRevision(t *testing.T, ctx context.Context, db *sql.DB, findingID int64) int64 {
	t.Helper()
	var revision int64
	if err := db.QueryRowContext(ctx, `SELECT processing_revision FROM findings WHERE id=?`, findingID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	return revision
}

func reanalysisClock(t *testing.T, ctx context.Context, db *sql.DB) string {
	t.Helper()
	var clock string
	if err := db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, appsettings.ClockKey).Scan(&clock); err != nil {
		t.Fatal(err)
	}
	return clock
}

func TestPreviewReanalysisUsesCurrentFiltersWithoutWriting(t *testing.T) {
	ctx, service, first := reanalysisTestService(t)
	var taskID int64
	if err := service.db.QueryRowContext(ctx, `SELECT task_id FROM findings WHERE id=?`, first).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	stale := reanalysisAddFinding(t, ctx, service.db, taskID, "stale", "测试商品", nil)
	rejected := reanalysisAddFinding(t, ctx, service.db, taskID, "rejected", "故障商品", nil)
	if _, err := service.db.ExecContext(ctx, `UPDATE tasks SET exclude_keywords_json='["故障"]' WHERE id=?`, taskID); err != nil {
		t.Fatal(err)
	}
	clock := reanalysisClock(t, ctx, service.db)
	service.now = func() time.Time { return time.UnixMilli(2000) }
	preview, err := service.PreviewReanalysis(ctx, []int64{first, stale, rejected, first})
	if err != nil {
		t.Fatal(err)
	}
	if preview.AsOf != 2000 || preview.RequestedCount != 3 || preview.NeedsDetailCount != 1 || preview.CachedDetailCount != 1 {
		t.Fatalf("预览计数不符合当前第一层和详情缓存：%+v", preview)
	}
	if reanalysisClock(t, ctx, service.db) != clock {
		t.Fatal("只读预览写入了额度时钟")
	}
	for _, id := range []int64{first, stale, rejected} {
		if reanalysisRevision(t, ctx, service.db, id) != 7 {
			t.Fatal("只读预览改变了处理状态")
		}
	}
}

func TestBatchReanalysisWithOneCachedItemOnlyQueues(t *testing.T) {
	ctx, service, id := reanalysisTestService(t)
	model := &reanalysisModel{}
	service.models = model
	var published int
	service.publish = func(kind string, reason *locales.Message, data json.RawMessage) error {
		published++
		if kind != "finding.changed" || reason != nil || reanalysisRevision(t, ctx, service.db, id) != 8 {
			t.Fatal("变化事件没有等到事务提交")
		}
		return errors.New("事件接收方不可用")
	}
	result, err := service.Reanalyze(ctx, []int64{id, id})
	if err != nil || len(result.Items) != 1 || result.Items[0].Status != ReanalysisQueued || result.Items[0].NeedsDetail || result.NeedsDetailCount != 0 {
		t.Fatalf("单件批量没有只入队：%+v %v", result, err)
	}
	if model.calls != 0 || published != 1 {
		t.Fatal("批量调用了模型，或事件数量错误")
	}
	var nextRun int64
	if err := service.db.QueryRowContext(ctx, `SELECT next_run_at FROM tasks`).Scan(&nextRun); err != nil || nextRun != 12345 {
		t.Fatalf("重新分析改变了自动运行计划：%d %v", nextRun, err)
	}
}

func TestReanalysisBatchRollsBackMissingAndWriteFailure(t *testing.T) {
	for _, failure := range []string{"missing", "write"} {
		t.Run(failure, func(t *testing.T) {
			ctx, service, first := reanalysisTestService(t)
			var taskID int64
			if err := service.db.QueryRowContext(ctx, `SELECT task_id FROM findings WHERE id=?`, first).Scan(&taskID); err != nil {
				t.Fatal(err)
			}
			second := first + 999
			if failure == "write" {
				second = reanalysisAddFinding(t, ctx, service.db, taskID, "second", "另一个商品", nil)
				if _, err := service.db.ExecContext(ctx, `CREATE TRIGGER reject_second_reanalysis BEFORE UPDATE ON findings WHEN NEW.item_id='second'
					BEGIN SELECT RAISE(ABORT,'拒绝第二件'); END`); err != nil {
					t.Fatal(err)
				}
			}
			beforeClock := reanalysisClock(t, ctx, service.db)
			service.now = func() time.Time { return time.UnixMilli(2000) }
			service.publish = func(string, *locales.Message, json.RawMessage) error { t.Fatal("回滚前发布了事件"); return nil }
			if result, err := service.Reanalyze(ctx, []int64{first, second}); err == nil || result.Items != nil {
				t.Fatalf("失败批次返回了部分成功：%+v %v", result, err)
			}
			if reanalysisRevision(t, ctx, service.db, first) != 7 || reanalysisClock(t, ctx, service.db) != beforeClock {
				t.Fatal("批次失败仍提交了首件或额度时钟")
			}
		})
	}
}

func TestReanalysisRejectsBadSelectionAndBrokenClock(t *testing.T) {
	for _, ids := range [][]int64{nil, {}, {0}, {-1}} {
		service := &Service{}
		if _, err := service.PreviewReanalysis(t.Context(), ids); err == nil {
			t.Fatal("预览接受了非法选择")
		}
		if _, err := service.Reanalyze(t.Context(), ids); err == nil {
			t.Fatal("重新分析接受了非法选择")
		}
	}
	ctx, service, id := reanalysisTestService(t)
	if _, err := service.db.ExecContext(ctx, `DELETE FROM meta WHERE key=?`, appsettings.ClockKey); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Reanalyze(ctx, []int64{id}); err == nil {
		t.Fatal("额度保护损坏时仍提交重新分析")
	}
	if reanalysisRevision(t, ctx, service.db, id) != 7 {
		t.Fatal("保护损坏仍改变了工作版本")
	}
}

func TestReanalysisMutationGateAndClockRollback(t *testing.T) {
	for _, state := range []struct {
		closing     bool
		maintenance int
	}{{closing: true}, {maintenance: 1}} {
		service := &Service{closing: state.closing, maintenance: state.maintenance}
		if _, err := service.Reanalyze(t.Context(), []int64{1}); err == nil {
			t.Fatal("服务关闭或维护时仍接收重分析写入")
		} else {
			var failure *ServiceError
			if !errors.As(err, &failure) || failure.Kind != ErrorUnavailable || failure.Reason.Code != "runtime.closed" {
				t.Fatalf("写入门禁没有返回明确原因：%v", err)
			}
		}
	}
	ctx, service, id := reanalysisTestService(t)
	fetched := int64(1000) - int64(24*time.Hour/time.Millisecond) - 1
	if _, err := service.db.ExecContext(ctx, `UPDATE listings SET detail_fetched_at=?`, fetched); err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return time.UnixMilli(0) }
	preview, err := service.PreviewReanalysis(ctx, []int64{id})
	if err != nil || preview.AsOf != 1000 || preview.NeedsDetailCount != 1 || preview.CachedDetailCount != 0 {
		t.Fatalf("系统时钟倒退让过期详情重新可用：%+v %v", preview, err)
	}
}

func TestSingleReanalysisQueuesStaleDetailsAndRejectsCurrentFilter(t *testing.T) {
	for _, reject := range []bool{false, true} {
		t.Run(map[bool]string{false: "stale", true: "filtered"}[reject], func(t *testing.T) {
			ctx, service, id := reanalysisTestService(t)
			if _, err := service.db.ExecContext(ctx, `UPDATE listings SET detail_fetched_at=NULL`); err != nil {
				t.Fatal(err)
			}
			if reject {
				if _, err := service.db.ExecContext(ctx, `UPDATE tasks SET exclude_keywords_json='["测试"]'`); err != nil {
					t.Fatal(err)
				}
			}
			model := &reanalysisModel{}
			service.models = model
			result, err := service.ReanalyzeOne(ctx, id)
			if err != nil || len(result.Items) != 1 {
				t.Fatalf("单件重分析没有返回本件结果：%+v %v", result, err)
			}
			if reject {
				if result.Items[0].Status != ReanalysisRejected || result.NeedsDetailCount != 0 {
					t.Fatalf("当前条件拒绝后仍入队：%+v", result)
				}
			} else if result.Items[0].Status != ReanalysisQueued || result.NeedsDetailCount != 1 {
				t.Fatalf("过期详情未进入正常队列：%+v", result)
			}
			if model.calls != 0 {
				t.Fatal("无需即时处理时调用了模型")
			}
		})
	}
}

func TestSingleKeywordReanalysisUsesCacheWithoutRunOrQuota(t *testing.T) {
	ctx, service, id := reanalysisTestService(t)
	if _, err := service.db.ExecContext(ctx, `UPDATE tasks SET judge_mode='keyword',profile_json='{}',notify_policy='recommended',match_keywords_json='["测试"]'`); err != nil {
		t.Fatal(err)
	}
	model := &reanalysisModel{}
	service.models = model
	result, err := service.ReanalyzeOne(ctx, id)
	if err != nil || len(result.Items) != 1 || result.Items[0].Status != ReanalysisCached || result.NeedsDetailCount != 0 {
		t.Fatalf("关键词缓存没有即时重判：%+v %v", result, err)
	}
	var decision, stage string
	var origin, revision int64
	if err := service.db.QueryRowContext(ctx, `SELECT decision,stage,reanalysis_origin,processing_revision FROM findings WHERE id=?`, id).Scan(&decision, &stage, &origin, &revision); err != nil {
		t.Fatal(err)
	}
	if decision != "matched" || stage != "done" || origin != 1 || revision != 8 || model.calls != 0 {
		t.Fatalf("关键词缓存状态或模型次数错误：%s %s %d %d %d", decision, stage, origin, revision, model.calls)
	}
	for _, table := range []string{"run_records", "quota_events", "deliveries"} {
		var count int
		if err := service.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("缓存重判建立了运行、占用额度或发送了推荐：%s=%d %v", table, count, err)
		}
	}
}

func TestSingleAIReanalysisDoesNotPretendUnavailableModelSucceeded(t *testing.T) {
	ctx, service, id := reanalysisTestService(t)
	service.models = nil
	result, err := service.ReanalyzeOne(ctx, id)
	if err != nil {
		var failure *ServiceError
		if !errors.Is(err, client.ErrNoProvider) && (!errors.As(err, &failure) || failure.Kind != ErrorUnavailable) {
			t.Fatalf("缺少模型返回了无关错误：%v", err)
		}
	} else if len(result.Items) != 1 || result.Items[0].Status != ReanalysisQueued || result.Items[0].Reason == nil || result.Items[0].Reason.Code != "await_ai" {
		t.Fatalf("缺少模型仍报告缓存分析完成：%+v", result)
	}
	var decision string
	var origin int64
	if err := service.db.QueryRowContext(ctx, `SELECT decision,reanalysis_origin FROM findings WHERE id=?`, id).Scan(&decision, &origin); err != nil || decision != "pending" || origin != 1 {
		t.Fatalf("不可用模型丢失待分析及来源：%s %d %v", decision, origin, err)
	}
}

type reanalysisModelFunc func(context.Context, client.Request) (client.Result, error)

func (model reanalysisModelFunc) Complete(ctx context.Context, request client.Request) (client.Result, error) {
	return model(ctx, request)
}

func TestSingleAICacheNoProviderStaysAwaitingWithoutAttempt(t *testing.T) {
	ctx, service, id := reanalysisTestService(t)
	calls := 0
	service.models = reanalysisModelFunc(func(_ context.Context, request client.Request) (client.Result, error) {
		calls++
		if request.Kind != client.TitleScreen {
			t.Fatalf("尚未通过标题就执行了第三层：%s", request.Kind)
		}
		return client.Result{}, client.ErrNoProvider
	})
	result, err := service.ReanalyzeOne(ctx, id)
	if err != nil || len(result.Items) != 1 || result.Items[0].Status != ReanalysisQueued || result.Items[0].Reason == nil || result.Items[0].Reason.Code != "await_ai" || calls != 1 {
		t.Fatalf("没有服务商未留待正常队列：%+v calls=%d %v", result, calls, err)
	}
	var decision, stage, rawReason string
	var requested sql.NullInt64
	var attempts, origin int64
	if err := service.db.QueryRowContext(ctx, `SELECT decision,stage,attempts,reanalysis_origin,reanalyze_requested_at,reason FROM findings WHERE id=?`, id).
		Scan(&decision, &stage, &attempts, &origin, &requested, &rawReason); err != nil {
		t.Fatal(err)
	}
	var reason locales.Message
	if err := json.Unmarshal([]byte(rawReason), &reason); err != nil || reason.Code != "await_ai" || reason.Args["cause"] != "no_provider" || decision != "pending" || stage != "title" || attempts != 0 || origin != 1 || requested.Valid {
		t.Fatalf("等待分析消耗次数、丢失来源或未清处理时间：%s %s %d %d %+v %s %v", decision, stage, attempts, origin, requested, rawReason, err)
	}
}

func TestSingleAICacheUsesTitleAndFactsWithoutBrowser(t *testing.T) {
	ctx, service, id := reanalysisTestService(t)
	kinds := []client.Kind{}
	service.models = reanalysisModelFunc(func(_ context.Context, request client.Request) (client.Result, error) {
		kinds = append(kinds, request.Kind)
		if request.Validate == nil || request.Schema == nil || len(request.Images) != 0 {
			t.Fatalf("缓存重判没有使用正式请求构造：%+v", request)
		}
		var text string
		switch request.Kind {
		case client.TitleScreen:
			text = `{"items":[{"no":1,"kind":"sale","specs":[],"brand_new":null,"verdict":"pass","reason":"正常出售"}]}`
		case client.Analysis:
			text = `{"specs":[],"brand_new":null,"musts":[],"bonuses":[],"checks":[],"seller_signals":[],"price_note":null,"extra_findings":[],"adjust":{"value":0,"reason":""},"summary":"缓存重判结果"}`
		default:
			t.Fatalf("缓存单件执行了其他模型用途：%s", request.Kind)
		}
		if err := request.Validate(text); err != nil {
			t.Fatal(err)
		}
		return client.Result{Text: text, ProviderID: 17, Model: "local-fixture"}, nil
	})
	result, err := service.ReanalyzeOne(ctx, id)
	if err != nil || len(result.Items) != 1 || result.Items[0].Status != ReanalysisCached || len(kinds) != 2 || kinds[0] != client.TitleScreen || kinds[1] != client.Analysis {
		t.Fatalf("缓存没有完整重走标题和事实层：%+v %v %v", result, kinds, err)
	}
	var decision, stage, model, summary string
	var attempts, origin int64
	var requested sql.NullInt64
	if err := service.db.QueryRowContext(ctx, `SELECT decision,stage,analysis_model,summary,attempts,reanalysis_origin,reanalyze_requested_at FROM findings WHERE id=?`, id).
		Scan(&decision, &stage, &model, &summary, &attempts, &origin, &requested); err != nil {
		t.Fatal(err)
	}
	if decision != "scored" || stage != "done" || model != "local-fixture" || summary != "缓存重判结果" || attempts != 2 || origin != 1 || requested.Valid {
		t.Fatalf("缓存事实没有保存完整：%s %s %s %s %d %d %+v", decision, stage, model, summary, attempts, origin, requested)
	}
	for _, table := range []string{"run_records", "quota_events", "deliveries"} {
		var count int
		if err := service.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("缓存模型重判建立了运行、占用额度或发送了推荐：%s=%d %v", table, count, err)
		}
	}
}
