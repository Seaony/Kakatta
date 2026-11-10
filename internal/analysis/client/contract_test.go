package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func TestContractInfrastructureErrorsStopWithoutRotation(t *testing.T) {
	for _, phase := range []string{"providers", "load", "before", "after", "save", "change", "log", "config"} {
		t.Run(phase, func(t *testing.T) {
			f, repo, a, _, deps := newContractFixture(t, 2, func(w http.ResponseWriter, _ *http.Request, _ int, _ map[string]any) {
				if phase == "save" || phase == "log" || phase == "config" {
					w.WriteHeader(400)
					fmt.Fprint(w, `{"error":"response_format unsupported"}`)
					return
				}
				fmt.Fprint(w, contractSuccess(`{"ok":true}`, "stop", nil))
			})
			broken := errors.New("local store unavailable")
			switch phase {
			case "providers":
				repo.providersErr = broken
			case "load":
				repo.loadErr = broken
			case "before":
				a.beforeErr = broken
			case "after":
				a.afterErr = broken
			case "save":
				repo.saveErr = broken
			case "change":
				repo.changeErr = broken
			case "log":
				deps.CompatLog = func(context.Context, CompatKey, Compat, Compat) error { return broken }
			case "config":
				repo.saveErr = ErrConfigChanged
			}
			_, err := newContractClient(t, deps).Complete(context.Background(), contractJob(Analysis))
			if phase == "config" {
				if !errors.Is(err, ErrConfigChanged) {
					t.Fatalf("配置变更类型未保留：%v", err)
				}
			} else if !errors.Is(err, ErrInfrastructure) {
				t.Fatalf("存储/必要日志失败必须原样停止：%v", err)
			}
			want := 1
			if phase == "providers" || phase == "load" || phase == "before" {
				want = 0
			}
			if len(f.snapshot()) != want {
				t.Fatalf("基础设施失败后继续重试或轮换：得到%d次，应为%d", len(f.snapshot()), want)
			}
		})
	}
}

func TestContractAfterFailureLatchesEveryPublicEntry(t *testing.T) {
	f, _, a, _, deps := newContractFixture(t, 1, nil)
	a.afterErr = errors.New("write failed")
	c := newContractClient(t, deps)
	if _, err := c.Complete(context.Background(), contractJob(Analysis)); !errors.Is(err, ErrInfrastructure) {
		t.Fatalf("首次写失败：%v", err)
	}
	a.mu.Lock()
	a.afterErr = nil
	a.mu.Unlock()
	if _, err := c.Complete(context.Background(), contractJob(TitleScreen)); !errors.Is(err, ErrInfrastructure) {
		t.Fatalf("Complete绕过失效锁：%v", err)
	}
	if _, err := c.Test(context.Background(), 1, contractJob(TitleScreen), contractJob(Analysis)); !errors.Is(err, ErrInfrastructure) {
		t.Fatalf("Test绕过失效锁：%v", err)
	}
	if _, err := c.Probe(context.Background(), 1); !errors.Is(err, ErrInfrastructure) {
		t.Fatalf("Probe绕过失效锁：%v", err)
	}
	if len(f.snapshot()) != 1 {
		t.Fatalf("记账写失败后仍发出%d次请求", len(f.snapshot()))
	}
	starts, ends, _ := a.snapshot()
	if len(starts) != 1 || len(ends) != 1 {
		t.Fatalf("被锁住的调用不能再过Before：%d/%d", len(starts), len(ends))
	}
}

func TestContractDailyLimitChecksEveryPhysicalAttempt(t *testing.T) {
	for _, first := range []string{"compatibility", "rotation"} {
		t.Run(first, func(t *testing.T) {
			f, _, a, _, deps := newContractFixture(t, 2, func(w http.ResponseWriter, _ *http.Request, _ int, _ map[string]any) {
				if first == "compatibility" {
					w.WriteHeader(400)
					fmt.Fprint(w, `{"error":"json_schema unsupported","usage":{"prompt_tokens":2,"completion_tokens":3}}`)
					return
				}
				w.WriteHeader(503)
				fmt.Fprint(w, `{"error":"busy","usage":{"prompt_tokens":2,"completion_tokens":3}}`)
			})
			a.limit = 5
			_, err := newContractClient(t, deps).Complete(context.Background(), contractJob(Analysis))
			if !errors.Is(err, ErrDailyLimit) {
				t.Fatalf("兼容或轮换前必须重新查每日上限：%v", err)
			}
			_, ends, total := a.snapshot()
			if len(f.snapshot()) != 1 || len(ends) != 1 || total != 5 {
				t.Fatalf("失败请求的用量没有挡住下一请求：HTTP%d，After%d，总量%d", len(f.snapshot()), len(ends), total)
			}
		})
	}
}

func TestContractTestBypassesOnlyDailyLimitAndClearsAfterBothSteps(t *testing.T) {
	for _, textFails := range []bool{false, true} {
		t.Run(fmt.Sprint(textFails), func(t *testing.T) {
			f, repo, a, slots, deps := newContractFixture(t, 2, func(w http.ResponseWriter, _ *http.Request, _ int, body map[string]any) {
				if textFails && body["model"] == "text-2" {
					w.WriteHeader(401)
					fmt.Fprint(w, `{"error":"bad key"}`)
					return
				}
				fmt.Fprint(w, contractSuccess(`{"ok":true}`, "stop", contractUsage()))
			})
			repo.providers[1].ErrorState = &Reason{Code: "analysis.client.balance"}
			a.total, a.limit = 1, 1
			result, err := newContractClient(t, deps).Test(context.Background(), 2, contractJob(TitleScreen), contractJob(Analysis))
			if textFails {
				if err == nil || result.Text.Err == nil || result.Vision.Err != nil {
					t.Fatalf("试分析应保留两步不同结果：%+v %v", result, err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			requests := f.snapshot()
			if len(requests) != 2 {
				t.Fatalf("两步应分别发送：%d", len(requests))
			}
			for _, req := range requests {
				if !strings.Contains(req.path, "/p2/") {
					t.Fatalf("试分析换了目标服务商：%s", req.path)
				}
			}
			starts, ends, _ := a.snapshot()
			if len(starts) != 2 || len(ends) != 2 {
				t.Fatalf("两步未记账：%d/%d", len(starts), len(ends))
			}
			for _, start := range starts {
				if start.Purpose != "test" {
					t.Fatalf("试分析用途错误：%s", start.Purpose)
				}
			}
			kinds := repo.changeKinds()
			if slices.Contains(kinds, "test_success") == textFails {
				t.Fatalf("只允许两步均成功时清状态：%v", kinds)
			}
			if !textFails && kinds[len(kinds)-1] != "test_success" {
				t.Fatalf("清状态必须放在两步之后：%v", kinds)
			}
			if acquired, released, _ := slots.counts(); acquired != 1 || released != 1 {
				t.Fatalf("整个试分析只拿一个共享名额：%d/%d", acquired, released)
			}
		})
	}
}

func TestContractProbeIsFiveEvenWithFloorAndIgnoresBodyShape(t *testing.T) {
	for _, body := range []string{"", "not JSON", contractSuccess("cut", "length", contractUsage())} {
		t.Run(fmt.Sprint(len(body)), func(t *testing.T) {
			f, repo, a, _, deps := newContractFixture(t, 1, func(w http.ResponseWriter, _ *http.Request, _ int, _ map[string]any) { fmt.Fprint(w, body) })
			repo.providers[0].ErrorState = &Reason{Code: "analysis.client.balance"}
			floor := int64(8000)
			compat := DefaultCompat()
			compat.MaxTokensFloor = &floor
			key := CompatKey{Provider: repo.providers[0].Ref, BaseURL: repo.providers[0].BaseURL, Model: repo.providers[0].TextModel}
			repo.compat = map[string]Compat{contractCompatKey(key): compat}
			result, err := newContractClient(t, deps).Probe(context.Background(), 1)
			if err != nil {
				t.Fatalf("probe只认2xx：%v", err)
			}
			requests := f.snapshot()
			if len(requests) != 1 {
				t.Fatalf("probe不能因截断重发：%d", len(requests))
			}
			p := requests[0].body
			if p["max_tokens"] != float64(5) || p["temperature"] != nil || p["response_format"] != nil {
				t.Fatalf("probe必须固定5且无温度/结构化：%v", p)
			}
			starts, ends, _ := a.snapshot()
			if len(starts) != 1 || starts[0].Purpose != "probe" || len(ends) != 1 || !ends[0].OK || len(result.Calls) != 1 {
				t.Fatalf("probe计量或可用结果错误：%+v %+v", starts, ends)
			}
			repo.mu.Lock()
			saves := repo.saves
			repo.mu.Unlock()
			if saves != 0 || !slices.Contains(repo.changeKinds(), "probe_success") {
				t.Fatalf("probe不能改floor，成功才清状态：saves=%d changes=%v", saves, repo.changeKinds())
			}
		})
	}
}

func TestContractProbeDoesNotBypassDailyLimit(t *testing.T) {
	f, repo, a, _, deps := newContractFixture(t, 1, nil)
	repo.providers[0].ErrorState = &Reason{Code: "analysis.client.daily_quota"}
	a.total, a.limit = 10, 10
	if _, err := newContractClient(t, deps).Probe(context.Background(), 1); !errors.Is(err, ErrDailyLimit) {
		t.Fatalf("probe不得豁免每日上限：%v", err)
	}
	if len(f.snapshot()) != 0 {
		t.Fatal("超限probe发了请求")
	}
}

func TestContractCancellationStillAccountsAndDoesNotRotate(t *testing.T) {
	entered := make(chan struct{})
	f, _, a, slots, deps := newContractFixture(t, 2, func(w http.ResponseWriter, r *http.Request, _ int, _ map[string]any) {
		close(entered)
		<-r.Context().Done()
	})
	c := newContractClient(t, deps)
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { _, err := c.Complete(ctx, contractJob(Analysis)); finished <- err }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("请求没有到达假服务器")
	}
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("取消类型丢失：%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("取消未结束")
	}
	starts, ends, _ := a.snapshot()
	a.mu.Lock()
	canceledAfter := a.afterCanceled
	a.mu.Unlock()
	if len(starts) != 1 || len(ends) != 1 || ends[0].OK || canceledAfter {
		t.Fatalf("取消仍须用live context恰好一次收尾：%+v %+v %v", starts, ends, canceledAfter)
	}
	if len(f.snapshot()) != 1 {
		t.Fatal("取消后轮换了供应商")
	}
	if acquired, released, _ := slots.counts(); acquired != 1 || released != 1 {
		t.Fatalf("取消后名额泄漏：%d/%d", acquired, released)
	}
}

func TestContractAllEntryPointsShareSlots(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var activeMu sync.Mutex
	active, maximum := 0, 0
	_, _, _, slots, deps := newContractFixture(t, 1, func(w http.ResponseWriter, r *http.Request, _ int, _ map[string]any) {
		activeMu.Lock()
		active++
		maximum = max(maximum, active)
		activeMu.Unlock()
		defer func() { activeMu.Lock(); active--; activeMu.Unlock() }()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, contractSuccess(`{"ok":true}`, "stop", nil))
	})
	slots.attempted = make(chan struct{}, 12)
	c := newContractClient(t, deps)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	results := make(chan error, 6)
	for _, kind := range []Kind{TitleScreen, Analysis, Profile, Market} {
		go func() { _, err := c.Complete(ctx, contractJob(kind)); results <- err }()
	}
	go func() { _, err := c.Test(ctx, 1, contractJob(TitleScreen), contractJob(Analysis)); results <- err }()
	go func() { _, err := c.Probe(ctx, 1); results <- err }()
	for range 6 {
		select {
		case <-slots.attempted:
		case <-ctx.Done():
			t.Fatal("有入口没有申请共享名额")
		}
	}
	once.Do(func() { close(release) })
	for range 6 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	acquired, released, maxSlots := slots.counts()
	activeMu.Lock()
	maxHTTP := maximum
	activeMu.Unlock()
	if acquired != 6 || released != 6 || maxSlots > 2 || maxHTTP > 2 {
		t.Fatalf("共享并发闸门错误：%d/%d 名额峰值%d HTTP峰值%d", acquired, released, maxSlots, maxHTTP)
	}
}

func TestContractRedirectsDoNotProduceUnaccountedRequests(t *testing.T) {
	f, _, a, _, deps := newContractFixture(t, 1, func(w http.ResponseWriter, r *http.Request, _ int, _ map[string]any) {
		http.Redirect(w, r, "/followed", http.StatusTemporaryRedirect)
	})
	_, err := newContractClient(t, deps).Complete(context.Background(), contractJob(Analysis))
	if err == nil {
		t.Fatal("3xx不能当成功或自动跳转")
	}
	_, ends, _ := a.snapshot()
	if len(f.snapshot()) != 1 || len(ends) != 1 {
		t.Fatalf("自动跳转绕过了Before/After：HTTP%d After%d", len(f.snapshot()), len(ends))
	}
}

func TestContractDetailsRedactBeforeUnicodeTruncation(t *testing.T) {
	secret := `sëcret-quote"<>&`
	message := strings.Repeat("鱼", 190) + secret + strings.Repeat("尾", 20)
	f, repo, _, _, deps := newContractFixture(t, 1, func(w http.ResponseWriter, _ *http.Request, _ int, _ map[string]any) {
		w.WriteHeader(400)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": message})
	})
	repo.providers[0].APIKey = secret
	result, err := newContractClient(t, deps).Complete(context.Background(), contractJob(Analysis))
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("应返回可展开的失败详情：%v", err)
	}
	detail, _ := failure.Last.Args["detail"].(string)
	if detail == "" || utf8.RuneCountInString(detail) > 200 {
		t.Fatalf("错误详情应先遮密再按Unicode截到200字：%q", detail)
	}
	serialized, _ := json.Marshal(struct {
		Result Result
		Reason Reason
	}{result, failure.Last})
	for _, text := range []string{err.Error(), fmt.Sprint(failure.Last.Args), string(serialized)} {
		if strings.Contains(text, secret) || strings.Contains(text, "sëcret-quote") || strings.Contains(text, `\u003c\u003e\u0026`) {
			t.Fatalf("错误暴露了原始或JSON转义Key：%s", text)
		}
	}
	if len(f.snapshot()) != 1 {
		t.Fatal("普通400不能盲重试")
	}
}

func TestContractConfigurationChangesKeepTheirErrorType(t *testing.T) {
	f, repo, _, _, deps := newContractFixture(t, 1, func(w http.ResponseWriter, _ *http.Request, _ int, _ map[string]any) {
		fmt.Fprint(w, contractSuccess(`{"ok":true}`, "stop", nil))
	})
	repo.changeErr = ErrConfigChanged
	if _, err := newContractClient(t, deps).Complete(context.Background(), contractJob(Analysis)); !errors.Is(err, ErrConfigChanged) {
		t.Fatalf("旧请求不能覆盖新配置：%v", err)
	}
	if len(f.snapshot()) != 1 {
		t.Fatal("配置变更不应轮换")
	}
}

func TestContractNewRejectsMissingAndTypedNilDependencies(t *testing.T) {
	_, _, _, _, valid := newContractFixture(t, 1, nil)
	for _, tc := range []struct {
		name   string
		remove func(*Dependencies)
	}{
		{"repository", func(d *Dependencies) { d.Repository = nil }},
		{"typed_repository", func(d *Dependencies) { d.Repository = (*contractRepo)(nil) }},
		{"accounting", func(d *Dependencies) { d.Accounting = nil }},
		{"typed_accounting", func(d *Dependencies) { d.Accounting = (*contractAccounting)(nil) }},
		{"slots", func(d *Dependencies) { d.Slots = nil }},
		{"typed_slots", func(d *Dependencies) { d.Slots = (*contractSlots)(nil) }},
		{"http", func(d *Dependencies) { d.HTTP = nil }},
		{"typed_http", func(d *Dependencies) { d.HTTP = (*http.Client)(nil) }},
		{"compat_log", func(d *Dependencies) { d.CompatLog = nil }},
		{"zero_timeout", func(d *Dependencies) { d.Timeout = 0 }},
		{"negative_timeout", func(d *Dependencies) { d.Timeout = -time.Second }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := valid
			tc.remove(&deps)
			if client, err := New(deps); err == nil || client != nil {
				t.Fatalf("缺失必需依赖不得创建可用客户端：%v %v", client, err)
			}
		})
	}
}

func TestContractAfterFailureStopsRequestAlreadyWaitingForSlot(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	f, _, a, slots, deps := newContractFixture(t, 1, func(w http.ResponseWriter, r *http.Request, _ int, _ map[string]any) {
		close(entered)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, contractSuccess(`{"ok":true}`, "stop", contractUsage()))
	})
	a.afterErr = errors.New("first request accounting failed")
	slots.sem = make(chan struct{}, 1)
	slots.attempted = make(chan struct{}, 2)
	c := newContractClient(t, deps)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	results := make(chan error, 2)
	go func() { _, err := c.Complete(ctx, contractJob(Analysis)); results <- err }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("首个请求未发出")
	}
	select {
	case <-slots.attempted:
	case <-ctx.Done():
		t.Fatal("首个请求未申请名额")
	}
	go func() { _, err := c.Complete(ctx, contractJob(TitleScreen)); results <- err }()
	select {
	case <-slots.attempted:
	case <-ctx.Done():
		t.Fatal("第二请求未进入名额等待")
	}
	releaseOnce.Do(func() { close(release) })
	for range 2 {
		select {
		case err := <-results:
			if !errors.Is(err, ErrInfrastructure) {
				t.Fatalf("首个记账失败后两次调用都必须失败：%v", err)
			}
		case <-ctx.Done():
			t.Fatal("等待中的请求没有被结束")
		}
	}
	a.mu.Lock()
	a.afterErr = nil
	beforeCount := a.beforeCount
	a.mu.Unlock()
	if _, err := c.Complete(ctx, contractJob(Analysis)); !errors.Is(err, ErrInfrastructure) {
		t.Fatalf("后续Complete未锁定：%v", err)
	}
	if _, err := c.Test(ctx, 1, contractJob(TitleScreen), contractJob(Analysis)); !errors.Is(err, ErrInfrastructure) {
		t.Fatalf("后续Test未锁定：%v", err)
	}
	if _, err := c.Probe(ctx, 1); !errors.Is(err, ErrInfrastructure) {
		t.Fatalf("后续Probe未锁定：%v", err)
	}
	starts, ends, _ := a.snapshot()
	if len(f.snapshot()) != 1 || beforeCount != 1 || len(starts) != 1 || len(ends) != 1 {
		t.Fatalf("等候者或后续调用绕过After失败锁：HTTP%d Before%d Starts%d Ends%d", len(f.snapshot()), beforeCount, len(starts), len(ends))
	}
	if acquired, released, _ := slots.counts(); acquired != 2 || released != 2 {
		t.Fatalf("等候请求的名额没有释放：%d/%d", acquired, released)
	}
}
