package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/config"
	"github.com/Seaony/Kakatta/internal/limits"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/security"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/store"
)

func accountTestService(t *testing.T) *Service {
	t.Helper()
	data := t.TempDir()
	if _, err := config.CreateDataDirectories(data); err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(t.Context(), filepath.Join(data, "accounts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	now := func() time.Time { return time.Date(2026, 11, 11, 5, 0, 0, 0, time.UTC) }
	preferences, err := appsettings.New(t.Context(), database.DB(), settings.Environment{TZ: "UTC"}, now)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := security.NewCipher(bytes.Repeat([]byte{6}, 32))
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{db: database.DB(), settings: preferences, cipher: cipher, dataDir: data, now: now}
	s.initRuntime()
	t.Cleanup(func() { s.cancel(context.Canceled) })
	return s
}

func accountTestPatch(values map[string]any) map[string]json.RawMessage {
	patch := map[string]json.RawMessage{}
	for key, value := range values {
		patch[key], _ = json.Marshal(value)
	}
	return patch
}

func accountTestInsert(t *testing.T, s *Service, name string, state AccountState, created int64) int64 {
	t.Helper()
	result, err := s.db.ExecContext(t.Context(), `INSERT INTO accounts(name,state,created_at) VALUES(?,?,?)`, name, state, created)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func accountTestTask(t *testing.T, s *Service, name string, account *int64, pinned, enabled bool) int64 {
	t.Helper()
	result, err := s.db.ExecContext(t.Context(), `INSERT INTO tasks(name,keyword,account_id,account_pinned,enabled,max_pages,interval_minutes,judge_mode,notify_policy,sort_index,created_at)
		VALUES(?, ?, ?, ?, ?, 1,30,'keyword','recommended',0,1)`, name, name, account, pinned, enabled)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func accountTestProblem(t *testing.T, err error, kind ErrorKind, code string) {
	t.Helper()
	var problem *ServiceError
	if !errors.As(err, &problem) || problem.Kind != kind || problem.Reason.Code != code {
		t.Fatalf("错误类型不符：%v，期望 %s/%s", err, kind, code)
	}
}

func accountTestTx(t *testing.T, s *Service) *sql.Tx {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	t.Cleanup(cancel)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tx.Rollback() })
	return tx
}

func TestAccountCreatePatchViewsAndQuota(t *testing.T) {
	s := accountTestService(t)
	proxy, err := s.CreateProxy(t.Context(), accountTestPatch(map[string]any{"name": "代理", "url": "http://user:private-password@127.0.0.1:9000"}))
	if err != nil {
		t.Fatal(err)
	}
	created, err := s.CreateAccount(t.Context(), accountTestPatch(map[string]any{"name": "  测试账号  ", "proxy_id": proxy.ID}))
	if err != nil {
		t.Fatal(err)
	}
	if created.Name != "测试账号" || created.State != AccountSignedOut || created.ProxyID == nil || *created.ProxyID != proxy.ID || created.Session != nil || created.CreatedAt == nil || *created.CreatedAt != s.now().UnixMilli() {
		t.Fatalf("创建账号没有采用领域默认：%+v", created)
	}
	if _, err := s.db.ExecContext(t.Context(), `UPDATE accounts SET state='needs_verification',verify_url='https://private.invalid/verification' WHERE id=?`, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), `INSERT INTO quota_events(account_id,kind,at) VALUES(?,'search',?),(?,'detail',?)`, created.ID, s.now().Add(-time.Minute).UnixMilli(), created.ID, s.now().Add(-2*time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	updated, err := s.UpdateAccount(t.Context(), created.ID, accountTestPatch(map[string]any{"name": "新名字"}))
	if err != nil || updated.ProxyID == nil || *updated.ProxyID != proxy.ID || updated.State != AccountNeedsVerification {
		t.Fatalf("更新名字改掉了绑定或状态：%+v %v", updated, err)
	}
	if updated.Quota.HourlyUsed != 1 || updated.Quota.DailySearchUsed != 1 || updated.Quota.DailyDetailUsed != 1 || updated.Quota.AsOf != s.now().UnixMilli() {
		t.Fatalf("额度读取不正确：%+v", updated.Quota)
	}
	encoded, _ := json.Marshal(updated)
	for _, secret := range []string{"verify_url", "https://private.invalid/verification", "private-password", "url_enc"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("公开账号泄露了内部字段", secret, string(encoded))
		}
	}
	cleared, err := s.UpdateAccount(t.Context(), created.ID, accountTestPatch(map[string]any{"proxy_id": nil}))
	if err != nil || cleared.ProxyID != nil {
		t.Fatal("显式null没有清代理", err)
	}
}

func TestAccountValidationAndIDCapacity(t *testing.T) {
	s := accountTestService(t)
	for _, patch := range []map[string]json.RawMessage{
		{}, {"name": json.RawMessage(`null`)}, {"name": json.RawMessage(` "" `)},
		{"name": json.RawMessage(`"账号"`), "state": json.RawMessage(`"active"`)},
		{"name": json.RawMessage(`"账号"`), "proxy_id": json.RawMessage(`0`)},
		{"name": json.RawMessage(`"账号"`), "proxy_id": json.RawMessage(`999`)},
		{"name": json.RawMessage([]byte{'"', 0xff, '"'})},
	} {
		_, err := s.CreateAccount(t.Context(), patch)
		accountTestProblem(t, err, ErrorInvalid, "accounts.invalid")
	}
	created, err := s.CreateAccount(t.Context(), accountTestPatch(map[string]any{"name": "正常"}))
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []json.RawMessage{json.RawMessage(`null`), json.RawMessage(" \n null \t "), json.RawMessage(`42`)} {
		_, err := s.UpdateAccount(t.Context(), created.ID, map[string]json.RawMessage{"name": raw})
		accountTestProblem(t, err, ErrorInvalid, "accounts.invalid")
	}
	if _, err := s.db.ExecContext(t.Context(), `UPDATE sqlite_sequence SET seq=? WHERE name='accounts'`, maxAccountID); err != nil {
		t.Fatal(err)
	}
	_, err = s.CreateAccount(t.Context(), accountTestPatch(map[string]any{"name": "不能绕回旧编号"}))
	accountTestProblem(t, err, ErrorInvalid, "accounts.id_capacity")
	var count int
	if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM accounts`).Scan(&count); err != nil || count != 1 {
		t.Fatal("拒绝容量后仍插入账号", count, err)
	}
}

func TestAccountPatchProxyIDEnforcesSafeIntegers(t *testing.T) {
	_, id, err := parseAccountPatch(accountTestPatch(map[string]any{"name": "账号", "proxy_id": limits.MaxSafeInteger}), nil)
	if err != nil || id == nil || *id != limits.MaxSafeInteger {
		t.Fatalf("安全上限未原样解析：%v %v", id, err)
	}
	for _, value := range []int64{limits.MaxSafeInteger + 1, limits.MinSafeInteger - 1} {
		_, _, err := parseAccountPatch(accountTestPatch(map[string]any{"name": "账号", "proxy_id": value}), nil)
		accountTestProblem(t, err, ErrorInvalid, "accounts.invalid")
		var problem *ServiceError
		if !errors.As(err, &problem) || len(problem.Fields) != 1 || problem.Fields[0].Key != "proxy_id" {
			t.Fatalf("代理编号错误未定位字段：%v", err)
		}
	}
}

func TestAutomaticAccountAssignmentCountsAllBoundTasksAndBalances(t *testing.T) {
	s := accountTestService(t)
	first := accountTestInsert(t, s, "早", AccountActive, 1)
	second := accountTestInsert(t, s, "晚", AccountActive, 2)
	accountTestInsert(t, s, "失效", AccountExpired, 0)
	accountTestTask(t, s, "停用也算绑定", &first, true, false)
	unbound1 := accountTestTask(t, s, "待分配1", nil, false, true)
	unbound2 := accountTestTask(t, s, "待分配2", nil, false, true)
	tx := accountTestTx(t, s)
	chosen, err := pickAccountTx(t.Context(), tx)
	if err != nil || chosen == nil || *chosen != second {
		t.Fatal("忽略了停用任务占用", chosen, err)
	}
	assigned, err := assignUnboundTasksTx(t.Context(), tx, 444)
	if err != nil || !reflect.DeepEqual(assigned, []int64{unbound1, unbound2}) {
		t.Fatal("补分配数量错误", assigned, err)
	}
	for task, want := range map[int64]int64{unbound1: second, unbound2: first} {
		var got, next int64
		if err := tx.QueryRowContext(t.Context(), `SELECT account_id,next_run_at FROM tasks WHERE id=?`, task).Scan(&got, &next); err != nil || got != want || next != 444 {
			t.Fatal("没有逐件重新平衡或没有立即排运行", task, got, next, err)
		}
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var stillUnbound bool
	if err := s.db.QueryRowContext(t.Context(), `SELECT account_id IS NULL FROM tasks WHERE id=?`, unbound1).Scan(&stillUnbound); err != nil || !stillUnbound {
		t.Fatal("分配操作擅自提交", err)
	}
}

func TestAutomaticReassignmentKeepsPinnedAndRetainsUnassignedChanges(t *testing.T) {
	s := accountTestService(t)
	account := accountTestInsert(t, s, "失效账号", AccountExpired, 1)
	automatic := accountTestTask(t, s, "自动", &account, false, true)
	fixed := accountTestTask(t, s, "固定", &account, true, true)
	tx := accountTestTx(t, s)
	changed, err := reassignAutomaticTasksTx(t.Context(), tx, account, 555)
	if err != nil || !reflect.DeepEqual(changed, []int64{automatic}) {
		t.Fatal("无替代账号时丢了任务变化", changed, err)
	}
	var automaticID *int64
	var next int64
	if err := tx.QueryRowContext(t.Context(), `SELECT account_id,next_run_at FROM tasks WHERE id=?`, automatic).Scan(&automaticID, &next); err != nil || automaticID != nil || next != 555 {
		t.Fatal("自动任务没有解除旧绑定", automaticID, next, err)
	}
	var fixedID int64
	if err := tx.QueryRowContext(t.Context(), `SELECT account_id FROM tasks WHERE id=?`, fixed).Scan(&fixedID); err != nil || fixedID != account {
		t.Fatal("固定任务被自动换号", fixedID, err)
	}
}

func TestAccountLogoutPreservesRiskGapQuotaAndMovesOnlyAutomaticTasks(t *testing.T) {
	s := accountTestService(t)
	first := accountTestInsert(t, s, "退出", AccountCooling, 1)
	second := accountTestInsert(t, s, "备用", AccountActive, 2)
	automatic := accountTestTask(t, s, "自动", &first, false, true)
	fixed := accountTestTask(t, s, "固定", &first, true, true)
	if _, err := s.db.ExecContext(t.Context(), `UPDATE accounts SET cooldown_until=9000000000000,verify_level=5,last_cooldown_end_at=123,cooldown_manual_used=1,last_run_ended_at=456,next_gap_seconds=240,verify_url='old' WHERE id=?`, first); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), `INSERT INTO quota_events(account_id,kind,at) VALUES(?,'search',?)`, first, s.now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(s.dataDir, "browser", strconv.FormatInt(first, 10))
	if err := os.MkdirAll(profile, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile, "local-fake-cookie"), []byte("fake"), 0600); err != nil {
		t.Fatal(err)
	}
	view, err := s.LogoutAccount(t.Context(), first)
	if err != nil {
		t.Fatal(err)
	}
	if view.State != AccountCooling || view.CooldownUntil == nil || *view.CooldownUntil != 9000000000000 || view.VerifyLevel != 5 || view.LastCooldownEndAt == nil || *view.LastCooldownEndAt != 123 || !view.CooldownManualUsed || view.LastRunEndedAt == nil || *view.LastRunEndedAt != 456 || view.NextGapSeconds == nil || *view.NextGapSeconds != 240 || view.Quota.DailySearchUsed != 1 {
		t.Fatalf("退出凭空清了保护：%+v", view)
	}
	if _, err := os.Stat(profile); !os.IsNotExist(err) {
		t.Fatal("退出没有清本地profile", err)
	}
	for task, want := range map[int64]int64{automatic: second, fixed: first} {
		var got int64
		if err := s.db.QueryRowContext(t.Context(), `SELECT account_id FROM tasks WHERE id=?`, task).Scan(&got); err != nil || got != want {
			t.Fatal("退出任务重分配不正确", task, got, err)
		}
	}
}

func TestAccountDeleteReleasesPinnedAndNeverReusesID(t *testing.T) {
	s := accountTestService(t)
	deleted := accountTestInsert(t, s, "删除", AccountActive, 1)
	remaining := accountTestInsert(t, s, "保留", AccountActive, 2)
	fixed := accountTestTask(t, s, "固定", &deleted, true, true)
	profile := filepath.Join(s.dataDir, "browser", strconv.FormatInt(deleted, 10))
	if err := os.Mkdir(profile, 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAccount(t.Context(), deleted); err != nil {
		t.Fatal(err)
	}
	var got int64
	var pinned bool
	if err := s.db.QueryRowContext(t.Context(), `SELECT account_id,account_pinned FROM tasks WHERE id=?`, fixed).Scan(&got, &pinned); err != nil || got != remaining || pinned {
		t.Fatal("删除没有解除固定并重新分配", got, pinned, err)
	}
	if _, err := os.Stat(profile); !os.IsNotExist(err) {
		t.Fatal("删除没有清目录", err)
	}
	created, err := s.CreateAccount(t.Context(), accountTestPatch(map[string]any{"name": "新账号"}))
	if err != nil || created.ID <= remaining {
		t.Fatal("删除后复用了账号编号", created.ID, err)
	}
}

func TestAccountCleanupMarkerSurvivesFailureAndDeleteCanRetry(t *testing.T) {
	s := accountTestService(t)
	id := accountTestInsert(t, s, "待清理", AccountActive, 1)
	profile := filepath.Join(s.dataDir, "browser", strconv.FormatInt(id, 10))
	if err := os.Mkdir(profile, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), `CREATE TRIGGER keep_account_cleanup BEFORE DELETE ON meta WHEN OLD.key LIKE 'account.cleanup.%' BEGIN SELECT RAISE(ABORT,'fake storage failure'); END`); err != nil {
		t.Fatal(err)
	}
	err := s.DeleteAccount(t.Context(), id)
	accountTestProblem(t, err, ErrorUnavailable, "service.storage_failure")
	var count int
	if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM accounts WHERE id=?`, id).Scan(&count); err != nil || count != 0 {
		t.Fatal("清理失败把已提交删除假装回滚", count, err)
	}
	var pending bool
	tx := accountTestTx(t, s)
	pending, err = accountCleanupPendingTx(t.Context(), tx, id)
	if err != nil || !pending {
		t.Fatal("清理失败丢了耐久marker", err)
	}
	tx.Rollback()
	if _, err := s.db.ExecContext(t.Context(), `DROP TRIGGER keep_account_cleanup`); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAccount(t.Context(), id); err != nil {
		t.Fatal("重复DELETE不能完成清理", err)
	}
	tx = accountTestTx(t, s)
	pending, err = accountCleanupPendingTx(t.Context(), tx, id)
	if err != nil || pending {
		t.Fatal("清理完成仍残留marker", pending, err)
	}
}

func TestAccountCleanupRejectsDamagedMarker(t *testing.T) {
	s := accountTestService(t)
	for _, key := range []string{"account.cleanup.01", "account.cleanup.../..", "account.cleanup.12768"} {
		if _, err := s.db.ExecContext(t.Context(), `INSERT INTO meta(key,value) VALUES(?,'1')`, key); err != nil {
			t.Fatal(err)
		}
		accountTestProblem(t, s.retryAccountCleanup(t.Context()), ErrorUnavailable, "accounts.cleanup_failed")
		if _, err := s.db.ExecContext(t.Context(), `DELETE FROM meta WHERE key=?`, key); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAccountListUsesStableCursorAndOmitsSecrets(t *testing.T) {
	s := accountTestService(t)
	for _, name := range []string{"一", "二", "三"} {
		accountTestInsert(t, s, name, AccountSignedOut, 1)
	}
	first, err := s.ListAccounts(t.Context(), ListQuery{Limit: 2})
	if err != nil || first.Total != 3 || len(first.Items) != 2 || first.NextCursor == nil {
		t.Fatal("第一页不完整", first, err)
	}
	second, err := s.ListAccounts(t.Context(), ListQuery{Limit: 1, Cursor: *first.NextCursor})
	if err != nil || second.Total != 3 || len(second.Items) != 1 || second.Items[0].ID <= first.Items[1].ID || second.NextCursor != nil {
		t.Fatal("第二页重复或total被截", second, err)
	}
	_, err = s.ListProxies(t.Context(), ListQuery{Cursor: *first.NextCursor})
	accountTestProblem(t, err, ErrorBadRequest, "service.invalid_query")
	_, err = s.ListAccounts(t.Context(), ListQuery{Limit: 201})
	accountTestProblem(t, err, ErrorBadRequest, "service.invalid_query")
}

func TestAccountTransactionFailureAndCancellationDoNotPublish(t *testing.T) {
	s := accountTestService(t)
	published := 0
	s.publish = func(string, *locales.Message, json.RawMessage) error { published++; return nil }
	if _, err := s.db.ExecContext(t.Context(), `CREATE TRIGGER reject_new_account BEFORE INSERT ON accounts BEGIN SELECT RAISE(ABORT,'fake failure'); END`); err != nil {
		t.Fatal(err)
	}
	_, err := s.CreateAccount(t.Context(), accountTestPatch(map[string]any{"name": "不应保存"}))
	accountTestProblem(t, err, ErrorUnavailable, "service.storage_failure")
	if published != 0 {
		t.Fatal("回滚前发布了事件")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = s.GetAccount(ctx, 1)
	if !errors.Is(err, context.Canceled) {
		t.Fatal("吞掉调用方取消", err)
	}
}

func TestAccountProfileRemovalFailureKeepsDurableRetry(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root不受目录写权限限制，运行在非root容器验收此项")
	}
	s := accountTestService(t)
	id := accountTestInsert(t, s, "目录清理失败", AccountActive, 1)
	browserDir := filepath.Join(s.dataDir, "browser")
	profile := filepath.Join(browserDir, strconv.FormatInt(id, 10))
	if err := os.Mkdir(profile, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(browserDir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(browserDir, 0700) })
	accountTestProblem(t, s.DeleteAccount(t.Context(), id), ErrorUnavailable, "accounts.cleanup_failed")
	tx := accountTestTx(t, s)
	pending, err := accountCleanupPendingTx(t.Context(), tx, id)
	if err != nil || !pending {
		t.Fatal("目录失败丢失清理marker", pending, err)
	}
	tx.Rollback()
	if err := os.Chmod(browserDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAccount(t.Context(), id); err != nil {
		t.Fatal("目录权限恢复后不能重试", err)
	}
}

func TestAccountCleanupRecoveryOnlyRemovesAuthorizedID(t *testing.T) {
	s := accountTestService(t)
	first := filepath.Join(s.dataDir, "browser", "1")
	second := filepath.Join(s.dataDir, "browser", "2")
	for _, profile := range []string{first, second} {
		if err := os.Mkdir(profile, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.ExecContext(t.Context(), `INSERT INTO meta(key,value) VALUES('account.cleanup.1','1')`); err != nil {
		t.Fatal(err)
	}
	if err := s.retryAccountCleanup(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Fatal("恢复没有清理标记目录", err)
	}
	if _, err := os.Stat(second); err != nil {
		t.Fatal("恢复清理了未授权账号目录", err)
	}
}

func TestAccountClockFailureRollsBackCreationAndMarkerDeletion(t *testing.T) {
	s := accountTestService(t)
	if _, err := s.db.ExecContext(t.Context(), `UPDATE meta SET value='broken' WHERE key='quota.clock'`); err != nil {
		t.Fatal(err)
	}
	_, err := s.CreateAccount(t.Context(), accountTestPatch(map[string]any{"name": "坏时钟不能创建"}))
	accountTestProblem(t, err, ErrorUnavailable, "service.storage_failure")
	var count int
	if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM accounts`).Scan(&count); err != nil || count != 0 {
		t.Fatal("时钟失败仍保存账号", count, err)
	}
}

func TestAccountAndProxyWritesRespectMaintenance(t *testing.T) {
	s := accountTestService(t)
	s.mu.Lock()
	s.maintenance++
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.maintenance--; s.mu.Unlock() }()
	_, err := s.CreateAccount(t.Context(), accountTestPatch(map[string]any{"name": "不能新建"}))
	accountTestProblem(t, err, ErrorUnavailable, "runtime.closed")
	_, err = s.CreateProxy(t.Context(), accountTestPatch(map[string]any{"name": "不能新建", "url": "http://localhost:8000"}))
	accountTestProblem(t, err, ErrorUnavailable, "runtime.closed")
}

func TestAccountDeniedLogoutCannotBypassPauseBySigningIn(t *testing.T) {
	s := accountTestService(t)
	id := accountTestInsert(t, s, "仍在拒绝期", AccountDenied, 1)
	until := s.now().Add(time.Hour).UnixMilli()
	reason := locales.Message{Code: "run.account_denied", Args: map[string]any{}}
	raw, _ := json.Marshal(reason)
	if _, err := s.db.ExecContext(t.Context(), `UPDATE accounts SET cooldown_until=?,state_reason=?,state_changed_at=111,verify_level=4 WHERE id=?`, until, string(raw), id); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(s.dataDir, "browser", strconv.FormatInt(id, 10))
	if err := os.Mkdir(profile, 0700); err != nil {
		t.Fatal(err)
	}
	view, err := s.LogoutAccount(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if view.State != AccountDenied || view.CooldownUntil == nil || *view.CooldownUntil != until || view.StateReason == nil || view.StateReason.Code != reason.Code || view.StateChangedAt == nil || *view.StateChangedAt != 111 || view.VerifyLevel != 4 {
		t.Fatalf("退出把拒绝暂停改掉：%+v", view)
	}
	if _, err := os.Stat(profile); !os.IsNotExist(err) {
		t.Fatal("保留拒绝期时没有清登录资料", err)
	}
	tx := accountTestTx(t, s)
	loggedOut, err := accountLoggedOutTx(t.Context(), tx, id)
	if err != nil || !loggedOut {
		t.Fatal("退出后的身份状态没有持久记录", loggedOut, err)
	}
	account, err := loadAccountTx(t.Context(), tx, id)
	if err != nil || !accountIsDenied(account, s.now().UnixMilli()) {
		t.Fatal("退出后没有继续命中拒绝门禁", account, err)
	}
	snapshot, err := s.settings.SnapshotTx(t.Context(), tx)
	if err != nil {
		t.Fatal(err)
	}
	blocked, err := manualSessionBlock(account, SessionLogin, snapshot, s.now())
	if err != nil || blocked == nil || blocked.Reason.Code != "run.account_denied" {
		t.Fatal("退出后扫码可绕过拒绝期", blocked, err)
	}
	tx.Rollback()
	if err := s.DeleteAccount(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	tx = accountTestTx(t, s)
	loggedOut, err = accountLoggedOutTx(t.Context(), tx, id)
	if err != nil || loggedOut {
		t.Fatal("删除账号没有一起清退出标记", loggedOut, err)
	}
}

func TestAccountLoggedOutMarkerIsStrictAndTransactional(t *testing.T) {
	s := accountTestService(t)
	tx := accountTestTx(t, s)
	if marked, err := accountLoggedOutTx(t.Context(), tx, 1); err != nil || marked {
		t.Fatal("缺marker被当成退出记录", marked, err)
	}
	if err := setAccountLoggedOutTx(t.Context(), tx, 1); err != nil {
		t.Fatal(err)
	}
	if marked, err := accountLoggedOutTx(t.Context(), tx, 1); err != nil || !marked {
		t.Fatal("设置marker没保存", marked, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	tx = accountTestTx(t, s)
	if marked, err := accountLoggedOutTx(t.Context(), tx, 1); err != nil || marked {
		t.Fatal("marker入口擅自提交", marked, err)
	}
	if _, err := tx.ExecContext(t.Context(), `INSERT INTO meta(key,value) VALUES('account.logged_out.1','broken')`); err != nil {
		t.Fatal(err)
	}
	if marked, err := accountLoggedOutTx(t.Context(), tx, 1); err == nil || marked {
		t.Fatal("坏marker未闭锁", marked, err)
	}
}

func TestAccountInvalidIDDoesNotQuiesceAllQueuedWork(t *testing.T) {
	s := accountTestService(t)
	s.queue = []*startRequest{{TaskID: 1, AccountID: 1, Kind: SessionRun}}
	for _, id := range []int64{0, -1, maxAccountID + 1} {
		accountTestProblem(t, s.DeleteAccount(t.Context(), id), ErrorNotFound, "accounts.not_found")
		_, err := s.LogoutAccount(t.Context(), id)
		accountTestProblem(t, err, ErrorNotFound, "accounts.not_found")
		if len(s.queue) != 1 {
			t.Fatal("无效账号编号触发了全局静止并移除队列", id)
		}
	}
}

func accountTestMeta(t *testing.T, s *Service) map[string]string {
	t.Helper()
	rows, err := s.db.QueryContext(t.Context(), `SELECT key,value FROM meta ORDER BY key`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	values := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			t.Fatal(err)
		}
		values[key] = value
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return values
}

func TestAccountGetAndListNeverPersistObservedClockOrExpiredWindows(t *testing.T) {
	s := accountTestService(t)
	created, err := s.CreateAccount(t.Context(), accountTestPatch(map[string]any{"name": "只读"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.settings.Apply(t.Context(), settings.Values{"general.timezone": json.RawMessage(`"Asia/Tokyo"`)}); err != nil {
		t.Fatal(err)
	}
	before := accountTestMeta(t, s)
	base := s.now()
	future := base.Add(48 * time.Hour)
	s.now = func() time.Time { return future }
	view, err := s.GetAccount(t.Context(), created.ID)
	if err != nil || view.Quota.AsOf != future.UnixMilli() {
		t.Fatal("读取没有在内存推进当前时间", view.Quota, err)
	}
	page, err := s.ListAccounts(t.Context(), ListQuery{})
	if err != nil || len(page.Items) != 1 || page.Items[0].Quota.AsOf != future.UnixMilli() {
		t.Fatal("列表没有使用受保护的当前时间", page, err)
	}
	if after := accountTestMeta(t, s); !reflect.DeepEqual(before, after) {
		t.Fatalf("GET或列表写回了最高观察时间/清掉旧日窗口：\n之前%v\n之后%v", before, after)
	}
	s.now = func() time.Time { return base.Add(-time.Hour) }
	view, err = s.GetAccount(t.Context(), created.ID)
	if err != nil || view.Quota.AsOf != base.UnixMilli() {
		t.Fatal("回拨读取没有只按已持久最高时间保底", view.Quota, err)
	}
	if after := accountTestMeta(t, s); !reflect.DeepEqual(before, after) {
		t.Fatal("回拨后的只读查询写了meta")
	}
}

func accountTestEvent(t *testing.T, s *Service, accountID int64, kind string) int64 {
	t.Helper()
	result, err := s.db.ExecContext(t.Context(), `INSERT INTO system_events(kind,subject_id,message,created_at) VALUES(?,?,?,?)`, kind, accountID, `{"code":"accounts.signed_out","args":{}}`, s.now().UnixMilli()-1)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func accountTestEventResolvedAt(t *testing.T, s *Service, eventID int64) *int64 {
	t.Helper()
	var resolved *int64
	if err := s.db.QueryRowContext(t.Context(), `SELECT resolved_at FROM system_events WHERE id=?`, eventID).Scan(&resolved); err != nil {
		t.Fatal(err)
	}
	return resolved
}

func TestAccountLogoutResolvesVerificationInTheSameTransaction(t *testing.T) {
	s := accountTestService(t)
	id := accountTestInsert(t, s, "待验证", AccountNeedsVerification, 1)
	eventID := accountTestEvent(t, s, id, "account_verify")
	view, err := s.LogoutAccount(t.Context(), id)
	if err != nil || view.State != AccountSignedOut {
		t.Fatal("退出失败", view, err)
	}
	if resolved := accountTestEventResolvedAt(t, s, eventID); resolved == nil || *resolved != s.now().UnixMilli() {
		t.Fatal("退出后仍有旧验证告警", resolved)
	}
}

func TestAccountDeleteResolvesOnlyItsThreeEventsAndKeepsHistory(t *testing.T) {
	s := accountTestService(t)
	id := accountTestInsert(t, s, "删除", AccountNeedsVerification, 1)
	other := accountTestInsert(t, s, "其他账号", AccountActive, 2)
	ids := []int64{}
	for _, kind := range []string{"account_verify", "account_login", "account_deny"} {
		ids = append(ids, accountTestEvent(t, s, id, kind))
	}
	otherEvent := accountTestEvent(t, s, other, "account_verify")
	if _, err := s.db.ExecContext(t.Context(), `INSERT INTO deliveries(at,kind,ok) VALUES(1,'system',1)`); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAccount(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	for _, eventID := range ids {
		if resolved := accountTestEventResolvedAt(t, s, eventID); resolved == nil || *resolved != s.now().UnixMilli() {
			t.Fatal("删除后留下不存在账号的未解决事件", eventID, resolved)
		}
	}
	if accountTestEventResolvedAt(t, s, otherEvent) != nil {
		t.Fatal("删除账号解决了其他账号的事件")
	}
	var deliveries int
	if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM deliveries`).Scan(&deliveries); err != nil || deliveries != 1 {
		t.Fatal("删除账号丢了历史送达", deliveries, err)
	}
}

func TestAccountLogoutKeepsDenyEventUntilDeadline(t *testing.T) {
	s := accountTestService(t)
	id := accountTestInsert(t, s, "拒绝期", AccountDenied, 1)
	until := s.now().Add(time.Hour)
	if _, err := s.db.ExecContext(t.Context(), `UPDATE accounts SET cooldown_until=? WHERE id=?`, until.UnixMilli(), id); err != nil {
		t.Fatal(err)
	}
	eventID := accountTestEvent(t, s, id, "account_deny")
	view, err := s.LogoutAccount(t.Context(), id)
	if err != nil || view.State != AccountDenied || accountTestEventResolvedAt(t, s, eventID) != nil {
		t.Fatal("拒绝期退出提前解决了拒绝事件", view, err)
	}
	s.now = func() time.Time { return until }
	view, err = s.LogoutAccount(t.Context(), id)
	if err != nil || view.State != AccountSignedOut || accountTestEventResolvedAt(t, s, eventID) == nil {
		t.Fatal("期限到了退出仍留下旧拒绝告警", view, err)
	}
}

func TestAccountEventFailureRollsBackLogoutAndDelete(t *testing.T) {
	for _, action := range []string{"logout", "delete"} {
		t.Run(action, func(t *testing.T) {
			s := accountTestService(t)
			id := accountTestInsert(t, s, "待验证", AccountNeedsVerification, 1)
			eventID := accountTestEvent(t, s, id, "account_verify")
			profile := filepath.Join(s.dataDir, "browser", strconv.FormatInt(id, 10))
			if err := os.Mkdir(profile, 0700); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.ExecContext(t.Context(), `CREATE TRIGGER reject_account_event_resolution BEFORE UPDATE ON system_events WHEN OLD.kind='account_verify' BEGIN SELECT RAISE(ABORT,'fake event failure'); END`); err != nil {
				t.Fatal(err)
			}
			var err error
			if action == "logout" {
				_, err = s.LogoutAccount(t.Context(), id)
			} else {
				err = s.DeleteAccount(t.Context(), id)
			}
			accountTestProblem(t, err, ErrorUnavailable, "service.storage_failure")
			tx := accountTestTx(t, s)
			account, err := loadAccountTx(t.Context(), tx, id)
			if err != nil || account.State != AccountNeedsVerification {
				t.Fatal("事件失败没有回滚账号修改", account, err)
			}
			pending, err := accountCleanupPendingTx(t.Context(), tx, id)
			if err != nil || pending {
				t.Fatal("事件失败留了半截清理标记", pending, err)
			}
			tx.Rollback()
			if accountTestEventResolvedAt(t, s, eventID) != nil {
				t.Fatal("失败却解决了事件")
			}
			if _, err := os.Stat(profile); err != nil {
				t.Fatal("数据库失败后仍删了登录资料", err)
			}
		})
	}
}

func TestAccountCreateAndLogoutStillPersistTheirProtectedClock(t *testing.T) {
	s := accountTestService(t)
	base := s.now()
	createdAt := base.Add(time.Hour)
	s.now = func() time.Time { return createdAt }
	created, err := s.CreateAccount(t.Context(), accountTestPatch(map[string]any{"name": "写动作仍保护时钟"}))
	if err != nil {
		t.Fatal(err)
	}
	var clock struct {
		HighestUTC int64 `json:"highest_utc"`
	}
	if err := json.Unmarshal([]byte(accountTestMeta(t, s)["quota.clock"]), &clock); err != nil || clock.HighestUTC != createdAt.UnixMilli() {
		t.Fatal("创建没有持久保存保护后的时钟", clock, err)
	}
	loggedOutAt := base.Add(2 * time.Hour)
	s.now = func() time.Time { return loggedOutAt }
	if _, err := s.LogoutAccount(t.Context(), created.ID); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(accountTestMeta(t, s)["quota.clock"]), &clock); err != nil || clock.HighestUTC != loggedOutAt.UnixMilli() {
		t.Fatal("退出没有持久保存保护后的时钟", clock, err)
	}
}
