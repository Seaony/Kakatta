package notification

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/analysis/client"
	"github.com/Seaony/Kakatta/internal/imagecache"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/notify"
	"github.com/Seaony/Kakatta/internal/service"
)

type modeChangeNoExternal struct{ calls atomic.Int64 }

func (f *modeChangeNoExternal) Open(context.Context, service.ResourceSpec) (service.SessionResource, error) {
	f.calls.Add(1)
	return nil, errors.New("本测试不应打开浏览器")
}

func (f *modeChangeNoExternal) Complete(context.Context, client.Request) (client.Result, error) {
	f.calls.Add(1)
	return client.Result{}, errors.New("本测试不应请求模型")
}

func modeChangePatch(t *testing.T, values map[string]any) service.TaskPatch {
	t.Helper()
	patch := service.TaskPatch{}
	for key, value := range values {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		patch[key] = raw
	}
	return patch
}

func modeChangeReadFinding(t *testing.T, s *Service, id int64) service.Finding {
	t.Helper()
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	value, err := service.NotificationFindingTx(t.Context(), tx, id)
	if err != nil {
		t.Fatal(err)
	}
	return value.Finding
}

func modeChangeJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// 结论写入是本地持久夹具；模式切换和通知走正式业务入口。
func modeChangeConclude(t *testing.T, s *Service, task service.Task, findingID int64) {
	t.Helper()
	var decision analysis.Decision
	var score *int
	var grade *analysis.Grade
	var factsJSON, sellerJSON any
	var reason *locales.Message
	matched := []string{}
	layer := int64(1)
	if task.JudgeMode == analysis.ModeKeyword {
		result := analysis.JudgeKeyword("camera 正常使用", "国行", task.MatchKeywords, task.ExcludeKeywords)
		if result.Rejected {
			t.Fatal("关键词夹具没有形成命中结论")
		}
		decision, reason, matched = analysis.DecisionMatched, result.Reason, result.MatchedKeywords
	} else {
		facts := analysis.Facts{Summary: "当前方式的新结论"}
		for _, requirement := range task.Profile.Musts {
			facts.Musts = append(facts.Musts, analysis.RequirementFact{ID: requirement.ID, Rev: requirement.Rev, Status: analysis.ReqMet})
		}
		seller := analysis.JudgeSeller(analysis.SellerInput{}, nil, task.Keyword)
		result := analysis.Score(analysis.ScoreInput{Profile: task.Profile, Facts: facts, Price: formatPtr(int64(10000)), Fair: &analysis.FairPrice{Cents: formatPtr(int64(13000)), Sample: 5}, Seller: seller})
		if result.Decision != analysis.DecisionScored || result.Score == nil || *result.Score < 80 {
			t.Fatal("算分夹具没有形成推荐结论", result)
		}
		decision, score, grade, reason, layer = result.Decision, result.Score, result.Grade, &result.Reason, 3
		factsJSON, sellerJSON = modeChangeJSON(t, facts), modeChangeJSON(t, seller)
	}
	planExec(t, s.db, `UPDATE findings SET decision=?,stage='done',decided_layer=?,score=?,grade=?,reason=?,
		analysis_json=?,seller_checks_json=?,matched_keywords_json=?,fair_price=13000,fair_sample=5,scored_price=10000
		WHERE id=? AND task_id=? AND search_generation=?`, decision, layer, score, grade, modeChangeJSON(t, reason),
		factsJSON, sellerJSON, modeChangeJSON(t, matched), findingID, task.ID, task.SearchGeneration)
}

func modeChangeSeedFinding(t *testing.T, s *Service, task service.Task, item string, pending bool) int64 {
	t.Helper()
	now := s.now().UnixMilli()
	planExec(t, s.db, `INSERT INTO listings(item_id,title,desktop_url,price,first_seen_at,last_seen_at)
		VALUES(?,'camera 正常使用','https://example.invalid/item',10000,?,?)`, item, now, now)
	result := planExec(t, s.db, `INSERT INTO findings(task_id,item_id,search_generation,found_at,is_baseline,decision,stage,is_read)
		VALUES(?,?,?,?,0,'pending','detail',0)`, task.ID, item, task.SearchGeneration, now)
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if !pending {
		modeChangeConclude(t, s, task, id)
	}
	return id
}

func TestModeChangeRealServiceKeepsNotificationHistoryAndAcceptsNewConclusions(t *testing.T) {
	for _, oldMode := range []analysis.Mode{analysis.ModeAI, analysis.ModeKeyword} {
		t.Run(string(oldMode), func(t *testing.T) {
			s := lifecycleService(t)
			var mu sync.Mutex
			var sentItems []string
			s.sender = notify.New(notify.Dependencies{HTTP: &http.Client{Transport: deliveryTransport(func(r *http.Request) (*http.Response, error) {
				var body struct {
					ItemID string `json:"item_id"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					return nil, err
				}
				mu.Lock()
				sentItems = append(sentItems, body.ItemID)
				mu.Unlock()
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
			})}})
			cache, err := imagecache.New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			blocked := &modeChangeNoExternal{}
			business, err := service.New(t.Context(), service.Dependencies{DB: s.db, Settings: s.settings, Cipher: s.cipher,
				DataDir: t.TempDir(), Resources: blocked, Models: blocked, Notifier: s, ImageCache: cache, Now: s.now,
				Publish: func(string, *locales.Message, json.RawMessage) error { return nil }})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := business.Close(); err != nil {
					t.Error(err)
				}
				if blocked.calls.Load() != 0 {
					t.Error("模式切换联调调用了浏览器或模型")
				}
			})
			channel, err := s.CreateChannel(t.Context(), channelPatch(`{"type":"webhook","name":"模式切换本地通知","secrets":{"url":"http://127.0.0.1/not-networked"}}`))
			if err != nil {
				t.Fatal(err)
			}
			profile := analysis.Profile{Musts: []analysis.Requirement{{Text: "国行", Source: analysis.SourceUser}}}
			created, err := business.CreateTask(t.Context(), modeChangePatch(t, map[string]any{
				"keyword": "camera", "enabled": false, "judge_mode": oldMode, "profile": profile, "channel_ids": []int64{channel.ID},
			}))
			if err != nil {
				t.Fatal(err)
			}
			task := created.Task
			planExec(t, s.db, `UPDATE tasks SET baseline_pending=0 WHERE id=?`, task.ID)
			task.BaselinePending = false
			ref := service.RunRef{TaskID: task.ID, SearchGeneration: task.SearchGeneration}
			sentID := modeChangeSeedFinding(t, s, task, "sent-history", false)
			if result, err := s.FlushRun(t.Context(), ref, []int64{sentID}); err != nil || result.NotifiedCount != 1 || len(result.Warnings) != 0 {
				t.Fatalf("旧方式真实成功记录建立失败：%+v %v", result, err)
			}
			sentBefore := modeChangeReadFinding(t, s, sentID)
			historyBefore, err := s.ChannelDeliveries(t.Context(), channel.ID)
			if err != nil || len(historyBefore) != 1 || historyBefore[0].OK == nil || !*historyBefore[0].OK {
				t.Fatalf("缺少旧方式的真实成功投递：%+v %v", historyBefore, err)
			}
			unjudgedID := modeChangeSeedFinding(t, s, task, "old-unjudged", false)
			waitingID := modeChangeSeedFinding(t, s, task, "old-waiting", false)
			futureID := modeChangeSeedFinding(t, s, task, "current-pending", true)
			planExec(t, s.db, `UPDATE findings SET notify_kind='recommended',recommended_price=10000 WHERE id=?`, waitingID)
			planExec(t, s.db, `INSERT INTO notification_intents(finding_id,task_id,kind,state,created_at,updated_at,price)
				VALUES(?,?,'recommended','pending',?,?,10000)`, waitingID, task.ID, s.now().UnixMilli(), s.now().UnixMilli())
			beforeUnjudged := modeChangeReadFinding(t, s, unjudgedID)
			beforeWaiting := modeChangeReadFinding(t, s, waitingID)
			patch := map[string]any{"judge_mode": analysis.ModeKeyword}
			if oldMode == analysis.ModeKeyword {
				patch = map[string]any{"judge_mode": analysis.ModeAI, "profile": profile}
			}
			updated, err := business.UpdateTask(t.Context(), task.ID, modeChangePatch(t, patch))
			if err != nil || updated.Task.SearchGeneration != ref.SearchGeneration || updated.Task.BaselinePending {
				t.Fatalf("正式模式切换失败或改了搜索基线：%+v %v", updated, err)
			}
			if result, err := s.FlushRun(t.Context(), ref, []int64{unjudgedID, waitingID}); err != nil || result.NotifiedCount != 0 || len(result.Warnings) != 0 {
				t.Fatalf("旧模式结论导致通知内部错误或被发送：%+v %v", result, err)
			}
			for id, expected := range map[int64]service.Finding{unjudgedID: beforeUnjudged, waitingID: beforeWaiting} {
				expected.NotifyKind = nil
				expected.NotifySkipReason = &locales.Message{Code: "notify.mode_changed", Args: map[string]any{}}
				if actual := modeChangeReadFinding(t, s, id); !reflect.DeepEqual(actual, expected) {
					t.Fatalf("旧结论没有只结算通知标记：%+v != %+v", actual, expected)
				}
			}
			if actual := modeChangeReadFinding(t, s, sentID); !reflect.DeepEqual(actual, sentBefore) {
				t.Fatal("已经送达的发现历史被改变")
			}
			historyAfter, err := s.ChannelDeliveries(t.Context(), channel.ID)
			if err != nil || !reflect.DeepEqual(historyBefore, historyAfter) || deliveryCount(t, s.db, `SELECT count(*) FROM notification_intents WHERE finding_id=? AND state='sent'`, sentID) != 1 ||
				deliveryCount(t, s.db, `SELECT count(*) FROM notification_intents WHERE finding_id=? AND state='cancelled' AND json_extract(reason,'$.code')='notify.mode_changed'`, waitingID) != 1 {
				t.Fatalf("切换改了成功投递或留下旧待发意图：%+v %v", historyAfter, err)
			}
			pending := modeChangeReadFinding(t, s, futureID)
			if pending.Decision != service.DecisionPending || pending.NotifyKind != nil || pending.NotifySkipReason != nil || pending.ProcessingRevision != 2 {
				t.Fatalf("模式切换提前禁止了当前待处理发现：%+v", pending)
			}
			modeChangeConclude(t, s, updated.Task, futureID)
			if result, err := s.FlushRun(t.Context(), ref, []int64{futureID}); err != nil || result.NotifiedCount != 1 || len(result.Warnings) != 0 {
				t.Fatalf("当前方式的新结论不能正常通知：%+v %v", result, err)
			}
			if result, err := s.FlushRun(t.Context(), ref, nil); err != nil || result.NotifiedCount != 0 {
				t.Fatalf("新结论成功后重复通知或闭锁：%+v %v", result, err)
			}
			if actual := modeChangeReadFinding(t, s, sentID); !reflect.DeepEqual(actual, sentBefore) {
				t.Fatal("新结论通知改写了旧成功发现")
			}
			finalHistory, err := s.ChannelDeliveries(t.Context(), channel.ID)
			if err != nil || len(finalHistory) != 2 || !reflect.DeepEqual(finalHistory[1], historyBefore[0]) {
				t.Fatalf("新结论通知改写了旧成功投递：%+v %v", finalHistory, err)
			}
			mu.Lock()
			actualItems := append([]string(nil), sentItems...)
			mu.Unlock()
			if !reflect.DeepEqual(actualItems, []string{"sent-history", "current-pending"}) ||
				deliveryCount(t, s.db, `SELECT count(*) FROM deliveries WHERE ok=1`) != 2 ||
				deliveryCount(t, s.db, `SELECT count(*) FROM delivery_items WHERE finding_id IN (?,?)`, unjudgedID, waitingID) != 0 ||
				modeChangeReadFinding(t, s, futureID).NotifiedAt == nil {
				t.Fatalf("旧推荐被发送或新结论没有保存真实成功：%v", actualItems)
			}
		})
	}
}

var _ service.ResourceFactory = (*modeChangeNoExternal)(nil)
var _ service.Models = (*modeChangeNoExternal)(nil)
