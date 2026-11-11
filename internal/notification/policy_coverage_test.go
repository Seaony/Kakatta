package notification

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/notify"
	"github.com/Seaony/Kakatta/internal/service"
)

func TestFlushRunSendsOnlyCurrentTaskEnabledChannels(t *testing.T) {
	db := deliveryDB(t)
	deliverySeed(t, db, 1, intentRecommended)
	requests := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	s := deliveryService(t, db, server.Client())
	selected := deliveryChannel(t, s, "当前任务启用", server.URL+"/selected")
	unbound := deliveryChannel(t, s, "未勾选启用", server.URL+"/unbound")
	other := deliveryChannel(t, s, "其他任务启用", server.URL+"/other")
	disabled := deliveryChannel(t, s, "当前任务停用", server.URL+"/disabled")
	planExec(t, db, `INSERT INTO tasks(id,name,keyword,max_pages,interval_minutes,judge_mode,notify_policy,sort_index)
		VALUES(2,'其他任务','相机',1,60,'keyword','recommended',1)`)
	planExec(t, db, `DELETE FROM task_channels WHERE task_id=1 AND channel_id=?`, unbound.ID)
	planExec(t, db, `UPDATE task_channels SET task_id=2 WHERE task_id=1 AND channel_id=?`, other.ID)
	planExec(t, db, `UPDATE channels SET enabled=0 WHERE id=?`, disabled.ID)

	result, err := s.FlushRun(t.Context(), service.RunRef{TaskID: 1, SearchGeneration: 1}, nil)
	if err != nil || result.NotifiedCount != 1 || len(result.Warnings) != 0 {
		t.Fatalf("任务渠道发送结果错误：%+v %v", result, err)
	}
	if len(requests) != 1 {
		t.Fatalf("当前任务之外的渠道收到请求：%d", len(requests))
	}
	if path := <-requests; path != "/selected" {
		t.Fatalf("发送到了错误渠道：%s", path)
	}
	if deliveryCount(t, db, `SELECT count(*) FROM deliveries`) != 1 ||
		deliveryCount(t, db, `SELECT count(*) FROM deliveries WHERE channel_id=? AND task_id=1 AND ok=1`, selected.ID) != 1 ||
		deliveryCount(t, db, `SELECT count(*) FROM notification_intents WHERE state='sent' AND failure_count=0`) != 1 {
		t.Fatal("投递记录不属于当前勾选启用的渠道")
	}
}

func TestFlushRunQueuedChannelChangesDoNotAttemptOrCountFailure(t *testing.T) {
	for _, change := range []string{"停用", "解绑"} {
		t.Run(change, func(t *testing.T) {
			db := deliveryDB(t)
			deliverySeed(t, db, 1, intentRecommended)
			firstFailedAt := time.Now().Add(-time.Hour).UnixMilli()
			planExec(t, db, `UPDATE notification_intents SET failure_count=1,first_failed_at=?`, firstFailedAt)
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()
			s := deliveryService(t, db, server.Client())
			channel := deliveryChannel(t, s, "排队后变化", server.URL)
			turn := mustQueueTurn(t, &s.queues, channel)
			release := sync.OnceFunc(func() { turn.finish(notify.Webhook, false, time.Now()) })
			defer release()
			type completed struct {
				result service.NotifyResult
				err    error
			}
			done := make(chan completed, 1)
			go func() {
				result, err := s.FlushRun(t.Context(), service.RunRef{TaskID: 1, SearchGeneration: 1}, nil)
				done <- completed{result, err}
			}()
			deliveryAwait(t, func() bool {
				s.queues.mu.Lock()
				defer s.queues.mu.Unlock()
				return s.queues.lanes[channel].users == 2
			})
			if change == "停用" {
				planExec(t, db, `UPDATE channels SET enabled=0 WHERE id=?`, channel.ID)
			} else {
				planExec(t, db, `DELETE FROM task_channels WHERE task_id=1 AND channel_id=?`, channel.ID)
			}
			release()
			select {
			case got := <-done:
				if got.err != nil || got.result.NotifiedCount != 0 || len(got.result.Warnings) != 0 {
					t.Fatalf("未发送渠道被当成投递失败：%+v %v", got.result, got.err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("渠道变化后任务发送没有结束")
			}
			if calls.Load() != 0 || deliveryCount(t, db, `SELECT count(*) FROM deliveries`) != 0 ||
				deliveryCount(t, db, `SELECT count(*) FROM delivery_items`) != 0 ||
				deliveryCount(t, db, `SELECT count(*) FROM delivery_intents`) != 0 {
				t.Fatal("排队后失效的渠道仍实际发送或记录投递")
			}
			if deliveryCount(t, db, `SELECT count(*) FROM notification_intents
				WHERE state='pending' AND failure_count=1 AND first_failed_at=? AND reason IS NULL`, firstFailedAt) != 1 ||
				deliveryCount(t, db, `SELECT count(*) FROM findings WHERE notified_at IS NULL AND notify_skip_reason IS NULL`) != 1 {
				t.Fatal("未出站却累计全败或改变待发状态")
			}
		})
	}
}

func TestDecideFirstCompatibleSkipReasonsUseDocumentedPriority(t *testing.T) {
	rejected := notificationReason("analysis.spec_mismatch", map[string]any{"name": "型号", "value": "mini 5"})
	risk := notificationReason("analysis.risk_confirmed", map[string]any{"name": "ID锁", "id": "id_lock", "attitude": "confirm"})
	overBudget := notificationReason("notify.over_budget", map[string]any{"price": "¥100", "budget": "¥99.99"})
	for _, test := range []struct {
		name    string
		kind    analysis.RejectKind
		budget  *int64
		avoid   bool
		blocked bool
		unknown bool
		keyword bool
		want    *locales.Message
	}{
		{name: "屏蔽先于硬条件和预算", kind: analysis.RejectFilter, budget: decisionPtr(int64(9999)), blocked: true, want: notificationReason("notify.blocked", nil)},
		{name: "硬条件先于预算", kind: analysis.RejectFilter, budget: decisionPtr(int64(9999)), want: rejected},
		{name: "排除词先于预算", kind: analysis.RejectExcluded, budget: decisionPtr(int64(9999)), want: rejected},
		{name: "垃圾先于预算", kind: analysis.RejectJunk, budget: decisionPtr(int64(9999)), want: rejected},
		{name: "不建议先于预算", budget: decisionPtr(int64(9999)), avoid: true, want: risk},
		{name: "预算先于不符合需求", kind: analysis.RejectUnmatched, budget: decisionPtr(int64(9999)), want: overBudget},
		{name: "未知预算价先于不符合需求", kind: analysis.RejectUnmatched, budget: decisionPtr(int64(10000)), unknown: true, want: notificationReason("notify.price_unknown_budget", nil)},
		{name: "不符合需求先于渠道和重新分析", kind: analysis.RejectUnmatched, want: rejected},
		{name: "关键词未命中先于渠道和重新分析", kind: analysis.RejectKeywordMiss, keyword: true, want: notificationReason("notify.keyword_miss", nil)},
	} {
		t.Run(test.name, func(t *testing.T) {
			task, value := firstFixture(decisionPtr(int64(85)))
			task.BudgetMax = test.budget
			value.SellerBlocked, value.ReanalysisOrigin = test.blocked, true
			if test.keyword {
				task.JudgeMode, task.NotifyPolicy = analysis.ModeKeyword, "recommended"
			}
			if test.kind != "" {
				value.Finding.Decision, value.Finding.RejectKind = analysis.DecisionRejected, &test.kind
				value.Finding.Score, value.Finding.Grade, value.Finding.Reason = nil, nil, rejected
			}
			if test.avoid {
				value.Finding.Grade, value.Finding.Reason = decisionPtr(analysis.GradeAvoid), risk
			}
			if test.unknown {
				value.Listing.Price = nil
			}
			got, err := decideFirst(task, value, false)
			if err != nil || !got.Apply || got.Kind != nil || got.SetRecommendedPrice || !reflect.DeepEqual(got.SkipReason, test.want) {
				t.Fatalf("原因冲突时没有采用首个匹配：%+v，期望 %+v，错误 %v", got, test.want, err)
			}
		})
	}
}
