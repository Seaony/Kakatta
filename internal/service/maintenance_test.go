package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/imagecache"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/settings"
)

func TestClearBusinessKeepsCredentialsQuotasAndRiskState(t *testing.T) {
	db, _, now := cleanupDB(t)
	old := now - 100*24*time.Hour.Milliseconds()
	cleanupExec(t, db, `INSERT INTO admin(id,username,password_hash,created_at,password_changed_at) VALUES(1,'owner','unchanged',?,?)`, old, old)
	cleanupExec(t, db, `INSERT INTO auth_tokens(id,kind,token_hash,name,created_at,expires_at) VALUES(1,'web_session',zeroblob(32),'current',?,?)`, now, now+1)
	cleanupExec(t, db, `INSERT INTO proxies(id,uuid,name,url_enc) VALUES(1,'proxy-id','fixed',X'010203')`)
	cleanupExec(t, db, `INSERT INTO accounts(id,name,proxy_id,state,cooldown_until,verify_level,verify_url,last_cooldown_end_at,cooldown_manual_used,last_run_ended_at,next_gap_seconds) VALUES(1,'kept',1,'cooling',?,3,'https://example.invalid/verify',?,1,?,210)`, now+1000, old, now)
	cleanupExec(t, db, `INSERT INTO quota_events(account_id,kind,at) VALUES(1,'search',?)`, old)
	cleanupExec(t, db, `INSERT INTO settings(key,value_json) VALUES('general.timezone','"UTC"')`)
	cleanupExec(t, db, `INSERT INTO prompts(key,content) VALUES('market','kept')`)
	cleanupExec(t, db, `INSERT INTO ai_providers(id,uuid,name,base_url,vision_model,sort_index) VALUES(1,'provider-id','kept','https://example.invalid','model',1)`)
	cleanupExec(t, db, `INSERT INTO ai_compat(provider_id,model) VALUES(1,'model')`)
	cleanupExec(t, db, `INSERT INTO channels(id,uuid,type,name,config_json) VALUES(1,'channel-id','webhook','kept','{}')`)
	cleanupTask(t, db, 1)
	cleanupExec(t, db, `UPDATE tasks SET account_id=1`)
	cleanupExec(t, db, `INSERT INTO task_channels(task_id,channel_id) VALUES(1,1)`)
	cleanupListing(t, db, "clear", old, true, true)
	cleanupFinding(t, db, 1, 1, "clear", old)
	cleanupExec(t, db, `INSERT INTO price_points(item_id,price,observed_at) VALUES('clear',100,?)`, old)
	cleanupExec(t, db, `INSERT INTO seen_items(task_id,item_id) VALUES(1,'clear')`)
	cleanupExec(t, db, `INSERT INTO task_sightings(task_id,item_id,day,last_seen_at) VALUES(1,'clear','2026-11-11',?)`, old)
	cleanupExec(t, db, `INSERT INTO task_item_specs(task_id,item_id,source,updated_at) VALUES(1,'clear','user',?)`, old)
	cleanupExec(t, db, `INSERT INTO seller_profiles(seller_id,fetched_at) VALUES('seller',?)`, old)
	cleanupExec(t, db, `INSERT INTO blocked_sellers(seller_id) VALUES('seller')`)
	cleanupExec(t, db, `INSERT INTO market_insights(task_id,generated_at) VALUES(1,?)`, old)
	cleanupExec(t, db, `INSERT INTO ai_calls(at,provider_id,task_id) VALUES(?,1,1)`, old)
	cleanupExec(t, db, `INSERT INTO notification_intents(id,finding_id,task_id,kind,state,created_at,updated_at) VALUES(1,1,1,'recommended','pending',?,?)`, old, old)
	cleanupExec(t, db, `INSERT INTO deliveries(id,at,channel_id,task_id) VALUES(1,?,1,1)`, old)
	cleanupExec(t, db, `INSERT INTO delivery_items(delivery_id,finding_id) VALUES(1,1)`)
	cleanupExec(t, db, `INSERT INTO delivery_intents(delivery_id,intent_id) VALUES(1,1)`)
	cleanupExec(t, db, `INSERT INTO run_records(id,task_id,task_name,trigger,started_at,ended_at,outcome) VALUES(1,1,'old','auto',?,?,'skipped')`, old, old)
	cleanupExec(t, db, `INSERT INTO run_steps(run_id,seq,at,status) VALUES(1,1,?,'ok')`, old)
	cleanupExec(t, db, `INSERT INTO run_skip_events(run_id,seq,at) VALUES(1,1,?)`, old)
	for i, kind := range []string{"account_verify", "account_login", "account_deny", "ai_error", "ai_budget", "login_failures", "task_failure_pause", "xianyu_layout"} {
		cleanupExec(t, db, `INSERT INTO system_events(id,kind,subject_id,created_at) VALUES(?,?,1,?)`, i+1, kind, old)
	}
	meta := map[string]string{
		"task.latest_search.1":                                  `{"item_ids":["clear"]}`,
		"task.profile_next_no.1":                                "20",
		"migration.legacy_notify_failures.1":                    "2",
		"task.latest_search.invalid":                            "unknown",
		"task.latest_search.1.extra":                            "unknown",
		"task.latest_search.migration.legacy_notify_failures.1": "unknown",
		"quota.clock":                                           "kept-clock",
		"quota.clock_initialized":                               "1",
		"runtime.session.1":                                     "kept-recovery",
		"account.cleanup.2":                                     "kept-cleanup",
		"account.logged_out.1":                                  "1",
		"auth.generation":                                       "kept-auth",
		"stats.started_at":                                      strconv.FormatInt(old, 10),
		"stats.coverage.ai_calls":                               strconv.FormatInt(old, 10),
		"stats.coverage.findings":                               strconv.FormatInt(old, 10),
		"stats.coverage.run_skip_events":                        strconv.FormatInt(old, 10),
	}
	for key, value := range meta {
		cleanupExec(t, db, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	result, err := clearBusinessSQLTx(t.Context(), tx, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"tasks", "task_channels", "listings", "findings", "price_points", "seen_items", "task_sightings", "task_item_specs", "seller_profiles", "blocked_sellers", "market_insights", "notification_intents", "deliveries", "delivery_items", "delivery_intents", "run_records", "run_steps", "run_skip_events"} {
		if cleanupHas(t, db, "SELECT EXISTS(SELECT 1 FROM "+table+")") || result.Deleted[table] != 1 {
			t.Errorf("业务表%s没有完整清除或计数错误：%d", table, result.Deleted[table])
		}
	}
	for _, table := range []string{"admin", "auth_tokens", "accounts", "proxies", "settings", "prompts", "channels", "ai_providers", "ai_compat", "quota_events", "ai_calls"} {
		if !cleanupHas(t, db, "SELECT EXISTS(SELECT 1 FROM "+table+")") || result.Deleted[table] != 0 {
			t.Errorf("保护表%s被修改：%+v", table, result.Deleted)
		}
	}
	if !cleanupHas(t, db, `SELECT EXISTS(SELECT 1 FROM accounts WHERE id=1 AND proxy_id=1 AND state='cooling' AND cooldown_until=? AND verify_level=3 AND last_cooldown_end_at=? AND cooldown_manual_used=1 AND last_run_ended_at=? AND next_gap_seconds=210)`, now+1000, old, now) {
		t.Fatal("账号风险或间隔被清除")
	}
	if result.Deleted["system_events"] != 2 || result.Deleted["meta"] != 3 {
		t.Fatalf("事件或内部标记清理范围错误：%+v", result.Deleted)
	}
	for key, want := range meta {
		if key == "task.latest_search.1" || key == "task.profile_next_no.1" || key == "migration.legacy_notify_failures.1" {
			if cleanupHas(t, db, `SELECT EXISTS(SELECT 1 FROM meta WHERE key=?)`, key) {
				t.Fatal("业务搜索快照未清除")
			}
			continue
		}
		if key == "stats.coverage.findings" || key == "stats.coverage.run_skip_events" {
			want = strconv.FormatInt(now, 10)
		}
		var got string
		if err := db.QueryRowContext(t.Context(), `SELECT value FROM meta WHERE key=?`, key).Scan(&got); err != nil || got != want {
			t.Errorf("保护标记%s改变：%s %v", key, got, err)
		}
	}
	insert, err := db.ExecContext(t.Context(), `INSERT INTO tasks(name,keyword,max_pages,interval_minutes,judge_mode,notify_policy,sort_index) VALUES('after','after',1,30,'keyword','recommended',1)`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := insert.LastInsertId()
	if id <= 1 {
		t.Fatal("清除业务重置了自动编号")
	}
}

func TestClearBusinessRollbackKeepsDataAndStats(t *testing.T) {
	db, _, now := cleanupDB(t)
	cleanupTask(t, db, 1)
	cleanupListing(t, db, "kept", now, false, false)
	cleanupFinding(t, db, 1, 1, "kept", now)
	cleanupExec(t, db, `CREATE TRIGGER stop_business_clear BEFORE DELETE ON listings BEGIN SELECT RAISE(ABORT,'local failure'); END`)
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := clearBusinessSQLTx(t.Context(), tx, now); err == nil {
		t.Fatal("清除失败被当作成功")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if !cleanupHas(t, db, `SELECT EXISTS(SELECT 1 FROM tasks WHERE id=1)`) || !cleanupHas(t, db, `SELECT EXISTS(SELECT 1 FROM findings WHERE id=1)`) || cleanupHas(t, db, `SELECT EXISTS(SELECT 1 FROM meta WHERE key='stats.coverage.findings')`) {
		t.Fatal("清除失败留下部分变化")
	}
}

type maintenanceCache struct {
	prune func(context.Context, int64) (int64, error)
	clear func(context.Context) (int64, error)
}

func (c *maintenanceCache) Prune(ctx context.Context, limit int64) (int64, error) {
	if c.prune != nil {
		return c.prune(ctx, limit)
	}
	return 0, nil
}

func (c *maintenanceCache) Clear(ctx context.Context) (int64, error) {
	if c.clear != nil {
		return c.clear(ctx)
	}
	return 0, nil
}

func maintenanceService(t *testing.T) (*Service, int64) {
	t.Helper()
	db, _, now := cleanupDB(t)
	prefs, err := appsettings.New(t.Context(), db, settings.Environment{TZ: "UTC"}, func() time.Time { return time.UnixMilli(now) })
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{db: db, settings: prefs, now: func() time.Time { return time.UnixMilli(now) }, imageCache: &maintenanceCache{}, notifier: &maintenanceNotifier{}}
	s.initRuntime()
	t.Cleanup(func() { s.cancel(context.Canceled) })
	return s, now
}

type maintenanceNotifier struct {
	entered chan struct{}
	settled chan struct{}
	failure error
	resumes atomic.Int64
}

func (*maintenanceNotifier) FlushRun(context.Context, RunRef, []int64) (NotifyResult, error) {
	return NotifyResult{}, errors.New("清理测试不应发送商品通知")
}

func (*maintenanceNotifier) System(context.Context, int64) error {
	return errors.New("清理测试不应推送系统事件")
}

func (n *maintenanceNotifier) Quiesce(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if n.entered != nil {
		n.entered <- struct{}{}
	}
	if n.failure != nil {
		return nil, n.failure
	}
	if n.settled != nil {
		select {
		case <-n.settled:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return func() { n.resumes.Add(1) }, nil
}

func TestMaintenanceCommitsBeforeFilesAndReportsPartialResults(t *testing.T) {
	for _, action := range []string{"cleanup", "clear"} {
		for _, cacheFails := range []bool{false, true} {
			t.Run(action+"/cache_failure="+strconv.FormatBool(cacheFails), func(t *testing.T) {
				s, now := maintenanceService(t)
				s.recordMaintenanceWarnings([]locales.Message{{Code: "maintenance.image_cleanup_failed", Args: map[string]any{}}})
				old := now - 40*24*time.Hour.Milliseconds()
				cleanupTask(t, s.db, 1)
				cleanupListing(t, s.db, "expired", old, false, false)
				cleanupFinding(t, s.db, 1, 1, "expired", old)
				calls := 0
				files := func(ctx context.Context) (int64, error) {
					calls++
					if cleanupHas(t, s.db, `SELECT EXISTS(SELECT 1 FROM findings WHERE id=1)`) {
						t.Fatal("数据库未提交就开始删图片")
					}
					s.mu.Lock()
					paused := s.maintenance
					s.mu.Unlock()
					if paused != 0 {
						t.Fatal("文件IO时仍占运行维护屏障")
					}
					if cacheFails {
						return 2, errors.New("本地图片删除失败")
					}
					return 2, nil
				}
				s.imageCache = &maintenanceCache{
					prune: func(ctx context.Context, maxBytes int64) (int64, error) {
						if maxBytes != 500*(1<<20) {
							t.Fatalf("没有使用当前缓存容量：%d", maxBytes)
						}
						return files(ctx)
					},
					clear: files,
				}
				var result CleanupResult
				var err error
				if action == "cleanup" {
					result, err = s.Cleanup(t.Context())
				} else {
					result, err = s.ClearBusinessData(t.Context(), "清除")
				}
				if err != nil || calls != 1 || result.ImagesDeleted != 2 || result.Deleted["findings"] != 1 {
					t.Fatalf("维护结果错误：%+v calls=%d err=%v", result, calls, err)
				}
				if cacheFails && (len(result.Warnings) != 1 || result.Warnings[0].Code != "maintenance.image_cleanup_failed") || !cacheFails && len(result.Warnings) != 0 {
					t.Fatalf("部分失败被伪装成完全成功：%+v", result.Warnings)
				}
				if got := s.maintenanceWarningsSnapshot(); len(got) != len(result.Warnings) {
					t.Fatalf("维护结果没有更新旧警告：%+v", got)
				}
			})
		}
	}
}

func TestMaintenanceFailureDoesNotDeleteFilesOrLeavePartialData(t *testing.T) {
	for _, action := range []string{"cleanup", "clear"} {
		for _, failure := range []string{"storage", "clock", "cancelled"} {
			t.Run(action+"/"+failure, func(t *testing.T) {
				s, now := maintenanceService(t)
				old := now - 40*24*time.Hour.Milliseconds()
				cleanupTask(t, s.db, 1)
				cleanupListing(t, s.db, "kept", old, false, false)
				cleanupFinding(t, s.db, 1, 1, "kept", old)
				calls := 0
				s.imageCache = &maintenanceCache{prune: func(context.Context, int64) (int64, error) { calls++; return 0, nil }, clear: func(context.Context) (int64, error) { calls++; return 0, nil }}
				ctx := t.Context()
				switch failure {
				case "storage":
					cleanupExec(t, s.db, `CREATE TRIGGER prevent_maintenance BEFORE DELETE ON findings BEGIN SELECT RAISE(ABORT,'local failure'); END`)
				case "clock":
					cleanupExec(t, s.db, `UPDATE meta SET value='broken' WHERE key='quota.clock'`)
				case "cancelled":
					cancelled, cancel := context.WithCancel(ctx)
					cancel()
					ctx = cancelled
				}
				var err error
				if action == "cleanup" {
					_, err = s.Cleanup(ctx)
				} else {
					_, err = s.ClearBusinessData(ctx, "清除")
				}
				if err == nil || calls != 0 || !cleanupHas(t, s.db, `SELECT EXISTS(SELECT 1 FROM findings WHERE id=1)`) {
					t.Fatalf("维护失败没有保住数据/文件：calls=%d err=%v", calls, err)
				}
				if failure == "cancelled" && !errors.Is(err, context.Canceled) {
					t.Fatalf("取消原因丢失：%v", err)
				}
				s.mu.Lock()
				paused := s.maintenance
				s.mu.Unlock()
				if paused != 0 {
					t.Fatal("失败后没有归还维护屏障")
				}
			})
		}
	}
}

func TestClearBusinessRequiresExactConfirmation(t *testing.T) {
	s, _ := maintenanceService(t)
	cleanupTask(t, s.db, 1)
	for _, input := range []string{"", "Clear", " 清除", "清除 "} {
		_, err := s.ClearBusinessData(t.Context(), input)
		var problem *ServiceError
		if !errors.As(err, &problem) || problem.Kind != ErrorInvalid || problem.Reason.Code != "maintenance.confirmation_required" {
			t.Fatalf("错误确认%s放行或原因错误：%v", input, err)
		}
	}
	if !cleanupHas(t, s.db, `SELECT EXISTS(SELECT 1 FROM tasks WHERE id=1)`) {
		t.Fatal("未确认就删除了任务")
	}
}

func TestClearImageCacheReportsActualResultWithoutChangingBusiness(t *testing.T) {
	s, _ := maintenanceService(t)
	cleanupTask(t, s.db, 1)
	failure := errors.New("本地缓存删除失败")
	for _, sendErr := range []error{nil, failure} {
		s.imageCache = &maintenanceCache{clear: func(context.Context) (int64, error) { return 2, sendErr }}
		result, err := s.ClearImageCache(t.Context())
		if result.ImagesDeleted != 2 || (sendErr == nil && err != nil) || sendErr != nil && !errors.Is(err, sendErr) {
			t.Fatalf("缓存清除结果或原错误丢失：%+v %v", result, err)
		}
		warnings := s.maintenanceWarningsSnapshot()
		if sendErr == nil && len(warnings) != 0 || sendErr != nil && (len(warnings) != 1 || warnings[0].Code != "maintenance.image_cleanup_failed") {
			t.Fatalf("缓存结果没有同步维护警告：%+v", warnings)
		}
	}
	if !cleanupHas(t, s.db, `SELECT EXISTS(SELECT 1 FROM tasks WHERE id=1)`) {
		t.Fatal("清空图片缓存改变了业务数据")
	}
}

func TestMaintenanceRejectsMissingCacheBeforeDatabaseChanges(t *testing.T) {
	s, now := maintenanceService(t)
	s.imageCache = nil
	cleanupTask(t, s.db, 1)
	old := now - 40*24*time.Hour.Milliseconds()
	cleanupListing(t, s.db, "kept", old, false, false)
	cleanupFinding(t, s.db, 1, 1, "kept", old)
	for _, action := range []func() (CleanupResult, error){
		func() (CleanupResult, error) { return s.Cleanup(t.Context()) },
		func() (CleanupResult, error) { return s.ClearBusinessData(t.Context(), "清除") },
		func() (CleanupResult, error) { return s.ClearImageCache(t.Context()) },
	} {
		if _, err := action(); err == nil {
			t.Fatal("缓存依赖缺失时伪装成完整成功")
		}
	}
	if !cleanupHas(t, s.db, `SELECT EXISTS(SELECT 1 FROM findings WHERE id=1)`) {
		t.Fatal("缺少缓存依赖时仍删除了数据库内容")
	}
}

func TestMaintenanceUsesRealDiskImageCache(t *testing.T) {
	s, _ := maintenanceService(t)
	cache, err := imagecache.New(filepath.Join(t.TempDir(), "cache", "images"))
	if err != nil {
		t.Fatal(err)
	}
	s.imageCache = cache
	if _, err := s.settings.Apply(t.Context(), settings.Values{"cache.image_max_mb": []byte(`1`)}); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 700*1024)
	for _, key := range []string{"first", "second"} {
		if err := cache.Put(t.Context(), key, data, 2*(1<<20)); err != nil {
			t.Fatal(err)
		}
	}
	result, err := s.Cleanup(t.Context())
	if err != nil || result.ImagesDeleted != 1 || len(result.Warnings) != 0 {
		t.Fatalf("真实磁盘缓存未按当前1MiB清理：%+v %v", result, err)
	}
	result, err = s.ClearImageCache(t.Context())
	if err != nil || result.ImagesDeleted != 1 {
		t.Fatalf("真实缓存清空计数错误：%+v %v", result, err)
	}
	if err := cache.Put(t.Context(), "third", []byte("thumbnail"), 1<<20); err != nil {
		t.Fatal(err)
	}
	cleanupTask(t, s.db, 1)
	result, err = s.ClearBusinessData(t.Context(), "清除")
	if err != nil || result.ImagesDeleted != 1 || result.Deleted["tasks"] != 1 {
		t.Fatalf("真实缓存和业务未一起清除：%+v %v", result, err)
	}
	if _, err := cache.Get(t.Context(), "third"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("清除后图片仍在：%v", err)
	}
}

func TestClearBusinessWaitsForNotificationReceipts(t *testing.T) {
	for _, outcome := range []string{"settled", "cancelled", "failed"} {
		t.Run(outcome, func(t *testing.T) {
			s, _ := maintenanceService(t)
			cleanupTask(t, s.db, 1)
			notifier := &maintenanceNotifier{entered: make(chan struct{}, 1), settled: make(chan struct{})}
			failure := errors.New("本地通知回执结算失败")
			if outcome == "failed" {
				notifier.failure = failure
			}
			s.notifier = notifier
			var cacheCalls atomic.Int64
			s.imageCache = &maintenanceCache{clear: func(context.Context) (int64, error) { cacheCalls.Add(1); return 1, nil }}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			finished := make(chan error, 1)
			go func() { _, err := s.ClearBusinessData(ctx, "清除"); finished <- err }()
			select {
			case <-notifier.entered:
			case <-ctx.Done():
				t.Fatal("清除没有等待通知维护入口")
			}
			if outcome != "failed" {
				if !cleanupHas(t, s.db, `SELECT EXISTS(SELECT 1 FROM tasks WHERE id=1)`) || cacheCalls.Load() != 0 {
					t.Fatal("通知回执尚未结算就清除了数据或文件")
				}
				select {
				case err := <-finished:
					t.Fatalf("通知未结算却提前完成：%v", err)
				default:
				}
				if outcome == "settled" {
					close(notifier.settled)
				} else {
					cancel()
				}
			}
			var err error
			select {
			case err = <-finished:
			case <-time.After(3 * time.Second):
				t.Fatal("通知结算或取消后，清除未结束")
			}
			if outcome == "settled" {
				if err != nil || cacheCalls.Load() != 1 || notifier.resumes.Load() != 1 || cleanupHas(t, s.db, `SELECT EXISTS(SELECT 1 FROM tasks WHERE id=1)`) {
					t.Fatalf("通知结算后的清除/恢复错误：calls=%d resumes=%d err=%v", cacheCalls.Load(), notifier.resumes.Load(), err)
				}
			} else {
				want := failure
				if outcome == "cancelled" {
					want = context.Canceled
				}
				if !errors.Is(err, want) || cacheCalls.Load() != 0 || notifier.resumes.Load() != 0 || !cleanupHas(t, s.db, `SELECT EXISTS(SELECT 1 FROM tasks WHERE id=1)`) {
					t.Fatalf("通知维护失败却清除了业务或丢失原错：%v", err)
				}
			}
		})
	}
}
