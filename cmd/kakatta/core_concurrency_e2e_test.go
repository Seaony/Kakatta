package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/xianyu/fake"
)

func TestCoreFlowConcurrentTasksRespectAccountPagesAndModelSlots(t *testing.T) {
	if os.Getenv("KAKATTA_CORE_FLOW_TESTS") != "1" {
		t.Skip("由 deploy/test-core-flow.sh 在断网容器里运行")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	firstStarted, releaseFirst := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(releaseFirst) })
	defer release()
	var titleCalls atomic.Int64
	h := newCoreFlowHarness(t, ctx, func(_ http.ResponseWriter, r *http.Request, kind string) bool {
		if kind != "title_screen" || titleCalls.Add(1) != 1 {
			return false
		}
		close(firstStarted)
		select {
		case <-releaseFirst:
		case <-r.Context().Done():
			t.Error("首个标题请求在测试放行前被取消")
		}
		return false
	}, nil)
	h.Request(http.MethodPatch, "/api/v1/settings", map[string]any{"values": map[string]any{"crawl.global_pages": 1}}, nil, http.StatusOK)
	h.xianyu.SetSearchItems([]fake.SearchItem{{ID: "301", Title: "MacBook Pro 自用设备", Price: "8000", SellerID: "seller301"}}, nil)
	var second service.AccountView
	h.Request(http.MethodPost, "/api/v1/accounts", map[string]any{"name": "第二个本地扫码账号"}, &second, http.StatusCreated)
	h.Request(http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/login", second.ID), nil, nil, http.StatusAccepted)
	coreWait(t, ctx, "第二个账号登录完成并满足实际运行间隔", func() bool {
		h.Request(http.MethodGet, fmt.Sprintf("/api/v1/accounts/%d", second.ID), nil, &second, http.StatusOK)
		return second.State == service.AccountActive && second.Session == nil && second.LastRunEndedAt != nil && second.NextGapSeconds != nil && time.Now().UnixMilli() >= *second.LastRunEndedAt+*second.NextGapSeconds*1000
	})
	first := h.CreateTask(map[string]any{"name": "先运行的任务"})
	sameAccount := h.CreateTask(map[string]any{"name": "同账号排队任务"})
	otherAccount := h.CreateTask(map[string]any{"name": "另一账号排队任务", "account_id": second.ID})

	var allowed, maxActive, observations atomic.Int64
	allowed.Store(1)
	singlePageQueued := make(chan struct{})
	monitorError, monitorDone := make(chan error, 1), make(chan struct{})
	monitorCtx, stopMonitor := context.WithCancel(ctx)
	go func() {
		defer close(monitorDone)
		tick := time.NewTicker(50 * time.Millisecond)
		defer tick.Stop()
		for {
			requestCtx, stop := context.WithTimeout(monitorCtx, 5*time.Second)
			request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, h.server.URL+"/api/v1/system/status", nil)
			if err == nil {
				request.AddCookie(&http.Cookie{Name: "kakatta_session", Value: h.token})
				var response *http.Response
				response, err = h.server.Client().Do(request)
				if err == nil {
					var status service.SystemStatus
					if response.StatusCode != http.StatusOK {
						err = fmt.Errorf("并发观察返回状态%d", response.StatusCode)
					} else {
						err = json.NewDecoder(response.Body).Decode(&status)
					}
					response.Body.Close()
					if err == nil {
						observations.Add(1)
						active := status.Browsers.Active
						for old := maxActive.Load(); active > old && !maxActive.CompareAndSwap(old, active); old = maxActive.Load() {
						}
						if active > allowed.Load() || active > status.Browsers.Limit {
							err = fmt.Errorf("实际占用%d个页面，当前只允许%d个，状态上限%d个", active, allowed.Load(), status.Browsers.Limit)
						}
						accounts := map[int64]bool{}
						for _, session := range status.Browsers.Sessions {
							if session.Phase == service.SessionQueued {
								continue
							}
							if accounts[session.AccountID] {
								err = fmt.Errorf("账号%d同时占用了多个页面", session.AccountID)
							}
							accounts[session.AccountID] = true
						}
						if active == 1 && status.Browsers.Limit == 1 && status.Scheduler.QueuedCount == 2 {
							select {
							case <-singlePageQueued:
							default:
								close(singlePageQueued)
							}
						}
					}
				}
			}
			stop()
			if err != nil && monitorCtx.Err() == nil {
				monitorError <- err
				return
			}
			select {
			case <-monitorCtx.Done():
				return
			case <-tick.C:
			}
		}
	}()
	defer func() { stopMonitor(); <-monitorDone }()
	checkMonitor := func() {
		t.Helper()
		select {
		case err := <-monitorError:
			t.Fatal(err)
		default:
		}
	}
	h.Run(first.ID)
	coreWait(t, ctx, "首个任务的真实标题请求进入本地模型", func() bool {
		checkMonitor()
		select {
		case <-firstStarted:
			return true
		default:
			return false
		}
	})
	for _, taskID := range []int64{sameAccount.ID, otherAccount.ID} {
		h.Request(http.MethodPost, fmt.Sprintf("/api/v1/tasks/%d/run", taskID), nil, nil, http.StatusAccepted)
	}
	coreWait(t, ctx, "单页上限下两个正式手动请求都在排队", func() bool {
		checkMonitor()
		var same, other service.TaskRuntime
		h.Request(http.MethodGet, fmt.Sprintf("/api/v1/tasks/%d/runtime", sameAccount.ID), nil, &same, http.StatusOK)
		h.Request(http.MethodGet, fmt.Sprintf("/api/v1/tasks/%d/runtime", otherAccount.ID), nil, &other, http.StatusOK)
		select {
		case <-singlePageQueued:
			return same.Phase == service.TaskQueued && other.Phase == service.TaskQueued
		default:
			return false
		}
	})
	if graphScalar(t, h.g, `SELECT count(*) FROM run_records`) != 1 || len(h.ModelKinds()) != 1 {
		t.Fatal("唯一页面已被首任务占用时仍启动了其他运行或模型请求")
	}
	allowed.Store(2)
	h.Request(http.MethodPatch, "/api/v1/settings", map[string]any{"values": map[string]any{"crawl.global_pages": 2}}, nil, http.StatusOK)
	coreWait(t, ctx, "另一账号的第二个浏览器完成搜索并等待模型名额", func() bool {
		checkMonitor()
		return graphScalar(t, h.g, `SELECT count(*) FROM findings WHERE task_id=? AND stage='title' AND decision='pending'`, otherAccount.ID) == 1
	})
	var status service.SystemStatus
	h.Request(http.MethodGet, "/api/v1/system/status", nil, &status, http.StatusOK)
	var queued service.TaskRuntime
	h.Request(http.MethodGet, fmt.Sprintf("/api/v1/tasks/%d/runtime", sameAccount.ID), nil, &queued, http.StatusOK)
	if status.Browsers.Active != 2 || status.Browsers.Limit != 2 || status.Scheduler.QueuedCount != 1 || queued.Phase != service.TaskQueued ||
		graphScalar(t, h.g, `SELECT count(*) FROM run_records WHERE task_id=?`, sameAccount.ID) != 0 {
		t.Fatalf("放开第二页后没有只启动另一账号，或同账号互斥失效：状态=%+v 排队=%+v", status, queued)
	}
	if active, maximum := h.ModelConcurrency(); active != 1 || maximum != 1 || len(h.ModelKinds()) != 1 {
		t.Fatalf("第二个浏览器没有等待全局唯一模型名额：active=%d max=%d calls=%v", active, maximum, h.ModelKinds())
	}
	release()
	coreWait(t, ctx, "三个真实任务完成并释放全部浏览器", func() bool {
		checkMonitor()
		return graphScalar(t, h.g, `SELECT count(*) FROM run_records WHERE ended_at IS NOT NULL`) == 3 &&
			graphScalar(t, h.g, `SELECT count(*) FROM meta WHERE key LIKE 'runtime.session.%'`) == 0
	})
	stopMonitor()
	<-monitorDone
	checkMonitor()
	if maxActive.Load() != 2 || observations.Load() == 0 {
		t.Fatalf("没有实际观察到双浏览器运行：max=%d observations=%d", maxActive.Load(), observations.Load())
	}
	if active, maximum := h.ModelConcurrency(); active != 0 || maximum != 1 || len(h.ModelKinds()) != 6 || titleCalls.Load() != 3 || len(h.Products()) != 3 {
		t.Fatalf("模型物理并发或逐任务请求数不符：active=%d max=%d kinds=%v products=%d", active, maximum, h.ModelKinds(), len(h.Products()))
	}
	if graphScalar(t, h.g, `SELECT count(*) FROM run_records WHERE trigger='manual' AND outcome='success' AND failure_kind IS NULL`) != 3 ||
		graphScalar(t, h.g, `SELECT count(DISTINCT task_id) FROM run_records`) != 3 ||
		graphScalar(t, h.g, `SELECT count(*) FROM findings WHERE item_id='301' AND is_baseline=1 AND decision='scored' AND stage='done' AND attempts=2`) != 3 ||
		graphScalar(t, h.g, `SELECT count(*) FROM findings`) != 3 || graphScalar(t, h.g, `SELECT count(*) FROM listings`) != 1 ||
		graphScalar(t, h.g, `SELECT count(*) FROM ai_calls WHERE ok=1`) != 6 || graphScalar(t, h.g, `SELECT count(*) FROM ai_calls WHERE ok=0`) != 0 {
		t.Fatal("并发运行漏掉了任务，或商品、发现、模型记账发生重复/失败")
	}
	for _, taskID := range []int64{first.ID, sameAccount.ID, otherAccount.ID} {
		if graphScalar(t, h.g, `SELECT count(*) FROM findings WHERE task_id=? AND item_id='301'`, taskID) != 1 ||
			graphScalar(t, h.g, `SELECT count(*) FROM ai_calls WHERE task_id=? AND purpose='title_screen' AND ok=1`, taskID) != 1 ||
			graphScalar(t, h.g, `SELECT count(*) FROM ai_calls WHERE task_id=? AND purpose='analysis' AND ok=1`, taskID) != 1 {
			t.Fatalf("任务%d没有恰好完成一次标题和商品判断", taskID)
		}
	}
	if graphScalar(t, h.g, `SELECT count(*) FROM notification_intents`) != 0 || graphScalar(t, h.g, `SELECT count(*) FROM deliveries`) != 0 || len(h.Pushes()) != 0 {
		t.Fatal("三个基线任务在并发运行后错误地产生了推送")
	}
	t.Logf("两个账号三个任务全部完成；连续观察%d次，页面最多%d个、同账号唯一会话，模型物理并发最多1且基线无推送", observations.Load(), maxActive.Load())
}
