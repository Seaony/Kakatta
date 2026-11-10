package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/analysis/client"
	"github.com/Seaony/Kakatta/internal/analysis/prompts"
	"github.com/Seaony/Kakatta/internal/browser"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/remote"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/store"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

type titleModelFunc func(context.Context, client.Request) (client.Result, error)

func (f titleModelFunc) Complete(ctx context.Context, request client.Request) (client.Result, error) {
	return f(ctx, request)
}

func titleTestExec(t *testing.T, s *Service, query string, args ...any) {
	t.Helper()
	if _, err := s.db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal(query, err)
	}
}

func titleTestJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func titleTestRun(t *testing.T) (*Service, *runExecution) {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "titles.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	snapshot, err := settings.Resolve(nil, settings.Environment{TZ: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Values["ai.max_items_per_run"] = json.RawMessage(`100`)
	templates, err := prompts.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{db: db.DB(), now: func() time.Time { return time.UnixMilli(1_800_000_000_000) }}
	profile := analysis.Profile{
		Specs: []analysis.ProfileSpec{{Name: "型号", Options: []string{"A", "B"}, Values: []string{"A"}}},
		Musts: []analysis.Requirement{{ID: "m1", Text: "国行", Source: analysis.SourceUser, Rev: 1}}, NextNo: 2,
	}
	titleTestExec(t, s, `INSERT INTO tasks(id,name,keyword,max_pages,interval_minutes,judge_mode,profile_json,notify_policy,sort_index,baseline_pending)
		VALUES(1,'测试任务','商品',1,30,'ai',?,'good',0,0)`, titleTestJSON(t, profile))
	run := &runExecution{Ref: RunRef{RunID: 1, TaskID: 1, SearchGeneration: 1}, Snapshot: snapshot, Prompts: templates}
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	run.Task, err = loadTaskTx(t.Context(), tx, 1)
	tx.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	s.models = titleModelFunc(func(_ context.Context, request client.Request) (client.Result, error) {
		return titleTestResponse(t, request, nil), nil
	})
	return s, run
}

func titleTestFinding(t *testing.T, s *Service, id int64, stage FindingStage, decision analysis.Decision) Finding {
	t.Helper()
	itemID := fmt.Sprintf("item-%d", id)
	titleTestExec(t, s, `INSERT INTO listings(item_id,title,desktop_url,price,tags_json,published_at,first_seen_at,last_seen_at)
		VALUES(?,?,?,12345,'["包邮"]',?,1,1)`, itemID, fmt.Sprintf("商品%d A", id), "https://example.invalid/item", id)
	titleTestExec(t, s, `INSERT INTO findings(id,task_id,item_id,search_generation,found_at,is_baseline,decision,stage,is_read)
		VALUES(?,1,?,1,?,0,?,?,0)`, id, itemID, id, decision, stage)
	return titleTestLoad(t, s, id)
}

func titleTestLoad(t *testing.T, s *Service, id int64) Finding {
	t.Helper()
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	finding, err := loadFindingTx(t.Context(), tx, id)
	if err != nil {
		t.Fatal(err)
	}
	return finding
}

func titleTestCount(t *testing.T, s *Service, query string, args ...any) int64 {
	t.Helper()
	var result int64
	if err := s.db.QueryRowContext(t.Context(), query, args...).Scan(&result); err != nil {
		t.Fatal(query, err)
	}
	return result
}

func titleTestResponse(t *testing.T, request client.Request, change func(int, *analysis.TitleItem)) client.Result {
	t.Helper()
	if request.Kind != client.TitleScreen || request.TaskID == nil || *request.TaskID != 1 || request.Schema == nil || request.Validate == nil || len(request.Images) != 0 {
		t.Fatalf("不是正式标题请求：%+v", request)
	}
	_, tail, ok := strings.Cut(request.User, "## 商品（搜索列表）\n```json\n")
	if !ok {
		t.Fatal("没有商品输入")
	}
	body, ok := strings.CutSuffix(tail, "\n```")
	if !ok {
		t.Fatal("商品输入没有完整结束")
	}
	var rows []struct {
		No    int             `json:"no"`
		Title string          `json:"title"`
		Price json.RawMessage `json:"price"`
		Tags  []string        `json:"tags"`
	}
	if err := json.Unmarshal([]byte(body), &rows); err != nil || len(rows) < 1 || len(rows) > 20 {
		t.Fatalf("标题批次无效：%s %v", body, err)
	}
	screen := analysis.TitleScreen{Items: []analysis.TitleItem{}}
	for index, row := range rows {
		if row.No != index+1 || string(row.Price) != "123.45" || !reflect.DeepEqual(row.Tags, []string{"包邮"}) {
			t.Fatalf("序号、分转元或标签失真：%+v", row)
		}
		value := "A"
		item := analysis.TitleItem{No: row.No, Kind: analysis.KindSale, Specs: []analysis.SpecValue{{Name: "型号", Value: &value}}, Verdict: "pass", Reason: "正常出售"}
		if change != nil {
			change(index, &item)
		}
		screen.Items = append(screen.Items, item)
	}
	text := titleTestJSON(t, screen)
	if err := request.Validate(text); err != nil {
		t.Fatal("正式校验拒绝假回复", err)
	}
	return client.Result{Text: text, ProviderID: 1, Model: "model"}
}

func TestRunTitlesBatchesAndLeavesExcessDetailWorkPending(t *testing.T) {
	s, run := titleTestRun(t)
	for id := int64(1); id <= 45; id++ {
		titleTestFinding(t, s, id, StageTitle, DecisionPending)
		run.SearchSaved.NewFindingIDs = append(run.SearchSaved.NewFindingIDs, id)
	}
	run.Snapshot.Values["ai.max_items_per_run"] = json.RawMessage(`3`)
	var sizes []int
	s.models = titleModelFunc(func(_ context.Context, request client.Request) (client.Result, error) {
		result := titleTestResponse(t, request, nil)
		sizes = append(sizes, strings.Count(request.User, `"no":`))
		return result, nil
	})
	if err := s.runTitles(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sizes, []int{20, 20, 5}) || run.Layer2Count != 45 || run.Layer2Rejected != 0 {
		t.Fatalf("标题没有按二十件分批：sizes=%v count=%d reject=%d", sizes, run.Layer2Count, run.Layer2Rejected)
	}
	if len(run.P) != 3 || run.P[0].FindingID != 45 || run.P[1].FindingID != 44 || run.P[2].FindingID != 43 {
		t.Fatalf("P没有按最新发布时间截取，或丢失超额队列：%+v", run.P)
	}
	if got := titleTestCount(t, s, `SELECT count(*) FROM findings WHERE decision='pending' AND stage='detail' AND attempts=1`); got != 45 {
		t.Fatalf("超过本次详情上限的发现不应丢掉：%d", got)
	}
}

func TestRunTitlesKeepsReanalysisAndDetailQueuePriority(t *testing.T) {
	s, run := titleTestRun(t)
	for id := int64(1); id <= 7; id++ {
		stage := StageTitle
		if id == 2 || id == 7 {
			stage = StageDetail
		}
		titleTestFinding(t, s, id, stage, DecisionPending)
	}
	titleTestExec(t, s, `UPDATE findings SET is_baseline=1 WHERE id IN (1,6,7)`)
	titleTestExec(t, s, `UPDATE findings SET reanalyze_requested_at=80 WHERE id=1`)
	titleTestExec(t, s, `UPDATE findings SET reanalyze_requested_at=50 WHERE id=2`)
	run.SearchSaved.NewFindingIDs = []int64{3, 4, 6}
	for _, id := range []int64{1, 2, 5, 7} {
		run.Backlog = append(run.Backlog, titleTestLoad(t, s, id))
	}
	if err := s.runTitles(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	ids := []int64{}
	for _, work := range run.P {
		ids = append(ids, work.FindingID)
	}
	if !reflect.DeepEqual(ids, []int64{2, 1, 4, 3, 5, 7, 6}) {
		t.Fatalf("第二层成功后丢失了重分析、近期新商品或基线的优先级：%v", ids)
	}
}

func TestRunTitlesKeywordDoesNotUseModels(t *testing.T) {
	s, run := titleTestRun(t)
	titleTestExec(t, s, `UPDATE tasks SET judge_mode='keyword',profile_json='{}',notify_policy='recommended' WHERE id=1`)
	titleTestFinding(t, s, 1, StageDetail, DecisionPending)
	run.SearchSaved.NewFindingIDs = []int64{1}
	s.models = titleModelFunc(func(context.Context, client.Request) (client.Result, error) {
		t.Fatal("关键词任务不应调用标题模型")
		return client.Result{}, nil
	})
	if err := s.runTitles(t.Context(), run); err != nil || len(run.P) != 1 || run.Layer2Count != 0 {
		t.Fatalf("关键词处理队列错误：%v P=%v count=%d", err, run.P, run.Layer2Count)
	}
}

func TestRunTitlesIncludesBaselineAndFilterSpecsWithoutChangingTheirConclusions(t *testing.T) {
	s, run := titleTestRun(t)
	titleTestFinding(t, s, 1, StageTitle, DecisionPending)
	old := titleTestFinding(t, s, 2, StageTitle, DecisionPending)
	titleTestFinding(t, s, 3, StageDone, DecisionSkipped)
	titleTestFinding(t, s, 4, StageDone, analysis.DecisionRejected)
	titleTestExec(t, s, `UPDATE findings SET is_baseline=1,reason='{"code":"findings.baseline","args":{}}' WHERE id=3`)
	titleTestExec(t, s, `UPDATE findings SET reject_kind='filter',decided_layer=1 WHERE id=4`)
	for id := int64(5); id <= 8; id++ {
		titleTestFinding(t, s, id, StageTitle, DecisionPending)
	}
	titleTestExec(t, s, `UPDATE tasks SET exclude_keywords_json='["排除"]' WHERE id=1`)
	titleTestExec(t, s, `UPDATE listings SET title='排除商品' WHERE item_id IN ('item-5','item-6')`)
	titleTestExec(t, s, `UPDATE listings SET is_dismissed=1 WHERE item_id='item-7'`)
	titleTestExec(t, s, `UPDATE listings SET seller_id='blocked' WHERE item_id='item-8'`)
	titleTestExec(t, s, `INSERT INTO blocked_sellers(seller_id) VALUES('blocked')`)
	titleTestExec(t, s, `INSERT INTO task_item_specs(task_id,item_id,source,specs_json,config_key,kind,updated_at)
		VALUES(1,'item-3','user','[{"name":"型号","value":"B"}]','B','sale',1)`)
	run.SearchSaved.NewFindingIDs = []int64{1, 3, 4, 5, 6, 7, 8}
	run.SearchSaved.TitleOnlyFindingIDs = []int64{3, 4}
	run.Backlog = []Finding{old, old}
	calls := 0
	s.models = titleModelFunc(func(_ context.Context, request client.Request) (client.Result, error) {
		calls++
		if strings.Count(request.User, `"no":`) != 4 || strings.Contains(request.User, "排除商品") || strings.Contains(request.User, "商品7") || strings.Contains(request.User, "商品8") {
			t.Fatal("标题处理范围错误", request.User)
		}
		return titleTestResponse(t, request, func(index int, item *analysis.TitleItem) {
			if index == 0 || index == 1 {
				item.Kind = analysis.KindWanted
			}
		}), nil
	})
	if err := s.runTitles(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !reflect.DeepEqual(run.FirstConclusionIDs, []int64{1}) || run.Layer2Count != 4 || run.Layer2Rejected != 1 {
		t.Fatalf("标题去重或终态计数错误：calls=%d ids=%v count=%d rejected=%d", calls, run.FirstConclusionIDs, run.Layer2Count, run.Layer2Rejected)
	}
	baseline, filtered := titleTestLoad(t, s, 3), titleTestLoad(t, s, 4)
	if baseline.Decision != DecisionSkipped || baseline.Stage != StageDone || baseline.Attempts != 0 || filtered.RejectKind == nil || *filtered.RejectKind != analysis.RejectFilter || filtered.Attempts != 0 {
		t.Fatalf("行情专用标题改变了结论或次数：baseline=%+v filter=%+v", baseline, filtered)
	}
	if got := titleTestCount(t, s, `SELECT count(*) FROM task_item_specs WHERE item_id='item-3' AND source='user' AND config_key='B'`); got != 1 {
		t.Fatal("标题覆盖了用户规格")
	}
	if len(run.P) != 1 || run.P[0].FindingID != 2 || titleTestLoad(t, s, 2).Attempts != 1 {
		t.Fatalf("补处理标题没有推进或重复处理：%+v", run.P)
	}
}

func TestRunTitlesMissingItemsPassWithWarning(t *testing.T) {
	s, run := titleTestRun(t)
	titleTestFinding(t, s, 1, StageTitle, DecisionPending)
	run.SearchSaved.NewFindingIDs = []int64{1}
	s.models = titleModelFunc(func(_ context.Context, request client.Request) (client.Result, error) {
		text := `{"items":[{"no":1,"kind":"invalid"}]}`
		if err := request.Validate(text); err != nil {
			t.Fatal("单件坏格式应宽松放行", err)
		}
		return client.Result{Text: text}, nil
	})
	if err := s.runTitles(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	if finding := titleTestLoad(t, s, 1); finding.Stage != StageDetail || finding.Decision != DecisionPending || finding.Attempts != 1 || len(run.Warnings) == 0 {
		t.Fatalf("缺项没有作为未知放行：%+v warnings=%v", finding, run.Warnings)
	}
}

func TestRunTitlesUnavailableModelsPreserveAttemptsAndAllPendingStages(t *testing.T) {
	for _, modelErr := range []error{client.ErrNoProvider, client.ErrDailyLimit} {
		t.Run(modelErr.Error(), func(t *testing.T) {
			s, run := titleTestRun(t)
			for id := int64(1); id <= 23; id++ {
				titleTestFinding(t, s, id, StageTitle, DecisionPending)
				run.SearchSaved.NewFindingIDs = append(run.SearchSaved.NewFindingIDs, id)
			}
			detail := titleTestFinding(t, s, 100, StageDetail, DecisionPending)
			only := titleTestFinding(t, s, 200, StageDone, DecisionSkipped)
			hidden := titleTestFinding(t, s, 300, StageTitle, DecisionPending)
			titleTestExec(t, s, `UPDATE listings SET is_dismissed=1 WHERE item_id='item-300'`)
			run.Backlog = []Finding{detail}
			run.SearchSaved.NewFindingIDs = append(run.SearchSaved.NewFindingIDs, only.ID, hidden.ID)
			run.SearchSaved.TitleOnlyFindingIDs = []int64{only.ID}
			titleTestExec(t, s, `UPDATE findings SET attempts=2 WHERE stage IN ('title','detail')`)
			calls := 0
			s.models = titleModelFunc(func(context.Context, client.Request) (client.Result, error) {
				calls++
				return client.Result{}, modelErr
			})
			if err := s.runTitles(t.Context(), run); err != nil {
				t.Fatal(err)
			}
			if calls != 1 || len(run.P) != 0 || titleTestCount(t, s, `SELECT count(*) FROM findings WHERE decision='pending' AND attempts=2 AND json_extract(reason,'$.code')='await_ai'`) != 24 {
				t.Fatalf("无可用模型仍继续或消耗次数：calls=%d P=%v", calls, run.P)
			}
			if got := titleTestLoad(t, s, 200); got.Decision != DecisionSkipped || got.Reason != nil || got.Attempts != 0 {
				t.Fatalf("行情专用失败不该进入补队列：%+v", got)
			}
			if got := titleTestLoad(t, s, 300); got.Reason != nil || got.Attempts != 2 {
				t.Fatalf("不感兴趣的标题不该被标为等待模型：%+v", got)
			}
		})
	}
}

func TestRunTitlesOrdinaryFailureUsesPerFindingRetryLimit(t *testing.T) {
	s, run := titleTestRun(t)
	for id := int64(1); id <= 2; id++ {
		titleTestFinding(t, s, id, StageTitle, DecisionPending)
	}
	titleTestFinding(t, s, 3, StageDone, DecisionSkipped)
	titleTestExec(t, s, `UPDATE findings SET attempts=2 WHERE id=1`)
	run.SearchSaved.NewFindingIDs = []int64{1, 2, 3}
	run.SearchSaved.TitleOnlyFindingIDs = []int64{3}
	s.models = titleModelFunc(func(context.Context, client.Request) (client.Result, error) {
		return client.Result{}, &client.Failure{Last: findingMessage("analysis.client.network", map[string]any{"status": 0, "detail": "本地故障"}), Retryable: true}
	})
	if err := s.runTitles(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	first, second, only := titleTestLoad(t, s, 1), titleTestLoad(t, s, 2), titleTestLoad(t, s, 3)
	if first.Attempts != 3 || first.Retryable || second.Attempts != 1 || !second.Retryable || first.Stage != StageTitle || second.Stage != StageTitle || only.Attempts != 0 || only.Decision != DecisionSkipped {
		t.Fatalf("失败次数或行情专用范围错误：%+v %+v %+v", first, second, only)
	}
}

func TestRunTitlesInfrastructureAndCancellationStopWithoutFindingFailure(t *testing.T) {
	failure := &client.Failure{Last: findingMessage("analysis.client.network", nil), Retryable: true}
	for _, modelErr := range []error{client.ErrInfrastructure, client.ErrConfigChanged, context.Canceled,
		errors.Join(client.ErrInfrastructure, failure), errors.Join(client.ErrConfigChanged, failure), errors.Join(context.Canceled, failure)} {
		t.Run(modelErr.Error(), func(t *testing.T) {
			s, run := titleTestRun(t)
			titleTestFinding(t, s, 1, StageTitle, DecisionPending)
			run.SearchSaved.NewFindingIDs = []int64{1}
			s.models = titleModelFunc(func(context.Context, client.Request) (client.Result, error) { return client.Result{}, modelErr })
			if err := s.runTitles(t.Context(), run); !errors.Is(err, modelErr) {
				t.Fatalf("停止错误被吞掉：%v", err)
			}
			if got := titleTestLoad(t, s, 1); got.Attempts != 0 || got.Stage != StageTitle || got.Decision != DecisionPending {
				t.Fatalf("依赖故障被算成商品失败：%+v", got)
			}
		})
	}
}

func TestRunTitlesEmptySuccessCannotBypassValidation(t *testing.T) {
	s, run := titleTestRun(t)
	titleTestFinding(t, s, 1, StageTitle, DecisionPending)
	run.SearchSaved.NewFindingIDs = []int64{1}
	s.models = titleModelFunc(func(context.Context, client.Request) (client.Result, error) {
		return client.Result{}, nil
	})
	if err := s.runTitles(t.Context(), run); !errors.Is(err, client.ErrInfrastructure) {
		t.Fatalf("空成功依赖绕过了标题校验：%v", err)
	}
	if got := titleTestLoad(t, s, 1); got.Stage != StageTitle || got.Attempts != 0 || got.Decision != DecisionPending {
		t.Fatalf("无效输出被当成了通过：%+v", got)
	}
}

func TestRunTitlesMissingModelKeepsReanalysisAwaiting(t *testing.T) {
	s, run := titleTestRun(t)
	titleTestFinding(t, s, 1, StageTitle, DecisionPending)
	titleTestExec(t, s, `UPDATE findings SET reanalyze_requested_at=9,reanalysis_origin=1 WHERE id=1`)
	run.Backlog = []Finding{titleTestLoad(t, s, 1)}
	s.models = nil
	if err := s.runTitles(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	got := titleTestLoad(t, s, 1)
	if got.Reason == nil || got.Reason.Code != "await_ai" || got.Attempts != 0 || got.Decision != DecisionPending || got.ReanalyzeRequestedAt != nil || titleTestCount(t, s, `SELECT reanalysis_origin FROM findings WHERE id=1`) != 1 {
		t.Fatalf("缺模型时伪造成功或丢掉重分析来源：%+v", got)
	}
}

func TestRunTitlesLateWorkCannotOverwriteNewRequestOrSearch(t *testing.T) {
	for _, change := range []string{"revision", "generation", "delete"} {
		t.Run(change, func(t *testing.T) {
			s, run := titleTestRun(t)
			titleTestFinding(t, s, 1, StageTitle, DecisionPending)
			run.SearchSaved.NewFindingIDs = []int64{1}
			s.models = titleModelFunc(func(_ context.Context, request client.Request) (client.Result, error) {
				switch change {
				case "revision":
					titleTestExec(t, s, `UPDATE findings SET processing_revision=2 WHERE id=1`)
				case "generation":
					titleTestExec(t, s, `UPDATE tasks SET search_generation=2 WHERE id=1`)
				case "delete":
					titleTestExec(t, s, `DELETE FROM findings WHERE id=1`)
				}
				return titleTestResponse(t, request, nil), nil
			})
			err := s.runTitles(t.Context(), run)
			if change == "generation" && !errors.Is(err, errSearchChanged) || change != "generation" && err != nil {
				t.Fatalf("代次与单件工作版本没有区分：%v", err)
			}
			if titleTestCount(t, s, `SELECT count(*) FROM task_item_specs`) != 0 || titleTestCount(t, s, `SELECT count(*) FROM findings WHERE attempts<>0`) != 0 || len(run.FirstConclusionIDs) != 0 {
				t.Fatal("旧异步结果写回了规格或结论")
			}
		})
	}
}

func TestRunTitlesUsesCurrentProfileAndCurrentExclusionsOnCommit(t *testing.T) {
	for _, change := range []string{"profile", "exclude"} {
		t.Run(change, func(t *testing.T) {
			s, run := titleTestRun(t)
			titleTestFinding(t, s, 1, StageTitle, DecisionPending)
			run.SearchSaved.NewFindingIDs = []int64{1}
			s.models = titleModelFunc(func(_ context.Context, request client.Request) (client.Result, error) {
				if change == "profile" {
					profile := run.Task.Profile
					profile.Musts[0].Text, profile.Musts[0].Rev = "日版", 2
					titleTestExec(t, s, `UPDATE tasks SET profile_json=? WHERE id=1`, titleTestJSON(t, profile))
				} else {
					titleTestExec(t, s, `UPDATE tasks SET exclude_keywords_json='["商品"]' WHERE id=1`)
				}
				return titleTestResponse(t, request, func(_ int, item *analysis.TitleItem) { item.Verdict, item.Reason = "unmatched", "不是国行" }), nil
			})
			if err := s.runTitles(t.Context(), run); err != nil {
				t.Fatal(err)
			}
			got := titleTestLoad(t, s, 1)
			if change == "profile" && (got.Stage != StageDetail || got.Decision != DecisionPending || got.Attempts != 1) {
				t.Fatalf("旧清单拒绝了新条件：%+v", got)
			}
			if change == "exclude" && (got.Stage != StageTitle || got.Attempts != 0 || len(run.P) != 0) {
				t.Fatalf("新排除词生效后仍推进详情：%+v P=%v", got, run.P)
			}
		})
	}
}

func TestRunTitlesUnavailableAfterModeChangeDoesNotBlockKeywordWork(t *testing.T) {
	s, run := titleTestRun(t)
	titleTestFinding(t, s, 1, StageTitle, DecisionPending)
	run.SearchSaved.NewFindingIDs = []int64{1}
	s.models = titleModelFunc(func(context.Context, client.Request) (client.Result, error) {
		titleTestExec(t, s, `UPDATE tasks SET judge_mode='keyword',profile_json='{}',notify_policy='recommended' WHERE id=1`)
		titleTestExec(t, s, `UPDATE findings SET stage='detail',processing_revision=2 WHERE id=1`)
		return client.Result{}, client.ErrNoProvider
	})
	if err := s.runTitles(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	if got := titleTestLoad(t, s, 1); got.Reason != nil || got.Attempts != 0 || len(run.P) != 1 || run.P[0].ProcessingRevision != 2 {
		t.Fatalf("关键词工作被旧模型错误暂停：%+v P=%v", got, run.P)
	}
}

func TestRunTitlesUnavailableDoesNotTouchLaterDetailRequest(t *testing.T) {
	s, run := titleTestRun(t)
	titleTestFinding(t, s, 1, StageTitle, DecisionPending)
	run.Backlog = []Finding{titleTestFinding(t, s, 2, StageDetail, DecisionPending)}
	run.SearchSaved.NewFindingIDs = []int64{1}
	s.models = titleModelFunc(func(context.Context, client.Request) (client.Result, error) {
		titleTestExec(t, s, `UPDATE findings SET processing_revision=2,reanalyze_requested_at=99 WHERE id=2`)
		return client.Result{}, client.ErrNoProvider
	})
	if err := s.runTitles(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	if got := titleTestLoad(t, s, 2); got.Reason != nil || got.ReanalyzeRequestedAt == nil || *got.ReanalyzeRequestedAt != 99 || got.ProcessingRevision != 2 {
		t.Fatalf("旧运行改写了后来重新分析的请求：%+v", got)
	}
}

func TestRunTitlesBatchRollbackAndPublishAfterCommit(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			s, run := titleTestRun(t)
			for id := int64(1); id <= 2; id++ {
				titleTestFinding(t, s, id, StageTitle, DecisionPending)
			}
			run.SearchSaved.NewFindingIDs = []int64{1, 2}
			if fail {
				titleTestExec(t, s, `CREATE TRIGGER fail_title BEFORE UPDATE ON findings WHEN NEW.id=2 BEGIN SELECT RAISE(FAIL,'blocked'); END`)
			}
			published := 0
			s.publish = func(kind string, _ *locales.Message, data json.RawMessage) error {
				published++
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				var count int
				err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM findings WHERE attempts=1 AND stage='detail'`).Scan(&count)
				if kind != "finding.changed" || !json.Valid(data) || err != nil || count != 2 {
					t.Fatal("变化事件早于提交或内容不合法")
				}
				return errors.New("本地事件接收器不可用")
			}
			err := s.runTitles(t.Context(), run)
			if fail {
				var failure *ServiceError
				if !errors.As(err, &failure) || failure.Kind != ErrorUnavailable || published != 0 || titleTestCount(t, s, `SELECT count(*) FROM task_item_specs`) != 0 || titleTestCount(t, s, `SELECT count(*) FROM findings WHERE attempts<>0`) != 0 {
					t.Fatalf("标题批次没有一起回滚：err=%v published=%d", err, published)
				}
			} else if err != nil || published != 1 {
				t.Fatalf("提交后发布失败不能倒说持久失败：%v published=%d", err, published)
			}
		})
	}
}

func TestPromptSnapshotTxUsesOneRunSnapshot(t *testing.T) {
	s, run := titleTestRun(t)
	old := run.Prompts.Contents[client.TitleScreen] + "\n本轮旧模板"
	titleTestExec(t, s, `INSERT INTO prompts(key,content) VALUES('title_screen',?)`, old)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	run.Prompts, err = loadPromptSnapshotTx(t.Context(), tx)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	titleTestExec(t, s, `UPDATE prompts SET content=? WHERE key='title_screen'`, strings.Replace(old, "本轮旧模板", "下一轮模板", 1))
	titleTestFinding(t, s, 1, StageTitle, DecisionPending)
	run.SearchSaved.NewFindingIDs = []int64{1}
	s.models = titleModelFunc(func(_ context.Context, request client.Request) (client.Result, error) {
		if request.System != old {
			t.Fatal("模板保存影响了已经开始的运行")
		}
		return titleTestResponse(t, request, nil), nil
	})
	if err := s.runTitles(t.Context(), run); err != nil {
		t.Fatal(err)
	}
}

type titleUnusableResource struct{}

func (titleUnusableResource) Runner() *web.Runner      { return nil }
func (titleUnusableResource) Chrome() *browser.Session { return nil }
func (titleUnusableResource) OpenRemote(context.Context) (*remote.Session, error) {
	return nil, errors.New("本地测试没有远程画面")
}
func (titleUnusableResource) Close() error { return nil }

func TestRunTitlesGuardPreventsFirstAndLaterBatchWithoutReadyPage(t *testing.T) {
	for _, later := range []bool{false, true} {
		t.Run(fmt.Sprint(later), func(t *testing.T) {
			s, run := titleTestRun(t)
			count := int64(1)
			if later {
				count = 21
			} else {
				run.Resource = titleUnusableResource{}
			}
			for id := int64(1); id <= count; id++ {
				titleTestFinding(t, s, id, StageTitle, DecisionPending)
				run.SearchSaved.NewFindingIDs = append(run.SearchSaved.NewFindingIDs, id)
			}
			calls := 0
			s.models = titleModelFunc(func(_ context.Context, request client.Request) (client.Result, error) {
				calls++
				result := titleTestResponse(t, request, nil)
				run.Resource = titleUnusableResource{}
				return result, nil
			})
			err := s.runTitles(t.Context(), run)
			var unavailable *ServiceError
			wantCalls, wantSaved := 0, int64(0)
			if later {
				wantCalls, wantSaved = 1, 20
			}
			if !errors.As(err, &unavailable) || unavailable.Kind != ErrorUnavailable || calls != wantCalls || titleTestCount(t, s, `SELECT count(*) FROM findings WHERE attempts=1`) != wantSaved {
				t.Fatalf("标题批次绕过了页面守卫：later=%v calls=%d err=%v", later, calls, err)
			}
		})
	}
}

func TestRunTitlesGuardRiskWinsOverModelFailureAndCancellation(t *testing.T) {
	for _, risk := range []error{web.ErrNeedVerify, web.ErrDenied, web.ErrEnvBlocked, web.ErrLoginExpired} {
		t.Run(risk.Error(), func(t *testing.T) {
			s, run := titleTestRun(t)
			titleTestFinding(t, s, 1, StageTitle, DecisionPending)
			run.SearchSaved.NewFindingIDs = []int64{1}
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			cause := errors.New("本地运行取消")
			s.models = titleModelFunc(func(workCtx context.Context, _ client.Request) (client.Result, error) {
				if workCtx != ctx {
					t.Fatal("缓存处理没有使用守卫传下来的上下文")
				}
				cancel(cause)
				return client.Result{}, errors.Join(&web.RiskError{Kind: risk, Where: "标题处理", Signal: "本地信号"}, context.Canceled,
					&client.Failure{Last: findingMessage("analysis.client.network", nil), Retryable: true})
			})
			err := s.runTitles(ctx, run)
			if !errors.Is(err, risk) || !errors.Is(err, context.Canceled) || !errors.Is(err, cause) {
				t.Fatalf("守卫风险或运行取消原因丢失：%v", err)
			}
			if got := titleTestLoad(t, s, 1); got.Attempts != 0 || got.Decision != DecisionPending || got.Stage != StageTitle {
				t.Fatalf("风控被算成了标题失败：%+v", got)
			}
		})
	}
}

func TestRunTitlesGuardQuotaDoesNotBecomeFindingFailure(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelled), func(t *testing.T) {
			s, run := titleTestRun(t)
			titleTestFinding(t, s, 1, StageTitle, DecisionPending)
			run.SearchSaved.NewFindingIDs = []int64{1}
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			cause := errors.New("本地取消原因")
			s.models = titleModelFunc(func(context.Context, client.Request) (client.Result, error) {
				if cancelled {
					cancel(cause)
				}
				return client.Result{}, errors.Join(web.ErrQuota, &client.Failure{Last: findingMessage("analysis.client.network", nil), Retryable: true})
			})
			err := s.runTitles(ctx, run)
			if !errors.Is(err, web.ErrQuota) || cancelled && (!errors.Is(err, context.Canceled) || !errors.Is(err, cause)) {
				t.Fatalf("额度或取消被吞掉：%v", err)
			}
			if got := titleTestLoad(t, s, 1); got.Attempts != 0 || got.Decision != DecisionPending {
				t.Fatalf("额度错误变成了商品失败：%+v", got)
			}
		})
	}
}
