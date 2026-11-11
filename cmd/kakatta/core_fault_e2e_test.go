package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/config"
	"github.com/Seaony/Kakatta/internal/quota"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/xianyu/fake"
	"golang.org/x/sys/unix"
	"modernc.org/sqlite"
)

func coreFaultEnabled(t *testing.T) {
	t.Helper()
	if os.Getenv("KAKATTA_CORE_FAULT_TESTS") != "1" {
		t.Skip("由 deploy/test-core-fault.sh 在隔离容器里运行")
	}
}

func TestCoreFaultStorageHealth(t *testing.T) {
	coreFaultEnabled(t)
	g := graphFixture(t)
	graphAdmin(t, g)
	if _, err := g.database.DB().ExecContext(t.Context(), `CREATE TRIGGER core_fault_clock BEFORE UPDATE ON meta WHEN OLD.key='quota.clock' BEGIN SELECT RAISE(ABORT,'local storage write failure'); END`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = g.database.DB().Exec(`DROP TRIGGER IF EXISTS core_fault_clock`) })
	if err := g.core.Prepare(t.Context()); err == nil {
		t.Fatal("没有真正触发存储准备失败")
	}
	if err := g.core.Start(); err == nil {
		t.Fatal("存储准备失败后仍启动调度")
	}
	host := httptest.NewServer(g.handler)
	t.Cleanup(host.Close)
	response, err := host.Client().Get(host.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("存储保护停止后仍报告健康：HTTP=%d，body=%s，err=%v", response.StatusCode, body, err)
	}
}

func coreFaultChromeEnabled(t *testing.T) {
	t.Helper()
	coreFaultEnabled(t)
	if runtime.GOOS != "linux" || os.Getenv("KAKATTA_CORE_FLOW_TESTS") != "1" {
		t.Skip("故障整链只在专用 Linux 断网容器里运行")
	}
}

func coreFaultAccount(h *coreFlowHarness, name string) service.AccountView {
	h.t.Helper()
	var account service.AccountView
	h.Request(http.MethodPost, "/api/v1/accounts", map[string]any{"name": name}, &account, http.StatusCreated)
	h.Request(http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/login", account.ID), nil, nil, http.StatusAccepted)
	coreFaultAccountIdle(h, account.ID)
	h.Request(http.MethodGet, fmt.Sprintf("/api/v1/accounts/%d", account.ID), nil, &account, http.StatusOK)
	return account
}

func coreFaultAccountIdle(h *coreFlowHarness, accountID int64) {
	h.t.Helper()
	coreWait(h.t, h.ctx, "账号关闭资源并自然等完运行间隔", func() bool {
		var account service.AccountView
		h.Request(http.MethodGet, fmt.Sprintf("/api/v1/accounts/%d", accountID), nil, &account, http.StatusOK)
		return account.State == service.AccountActive && account.Session == nil && account.LastRunEndedAt != nil && account.NextGapSeconds != nil && time.Now().UnixMilli() >= *account.LastRunEndedAt+*account.NextGapSeconds*1000
	})
}

func coreFaultOneItem(id string) []fake.SearchItem {
	return []fake.SearchItem{{ID: id, Title: "MacBook Pro 自用设备", Price: "8000", SellerID: "seller" + id}}
}

func coreFaultRun(h *coreFlowHarness, taskID int64) {
	h.t.Helper()
	h.Request(http.MethodPost, fmt.Sprintf("/api/v1/tasks/%d/run", taskID), nil, nil, http.StatusAccepted)
}

func coreFaultOrdinaryFailure(h *coreFlowHarness, taskID, runs int64) {
	h.t.Helper()
	h.WaitRuns(taskID, runs)
	var outcome, failure string
	if err := h.g.database.DB().QueryRowContext(h.ctx, `SELECT outcome,COALESCE(failure_kind,'') FROM run_records WHERE task_id=? ORDER BY id DESC LIMIT 1`, taskID).Scan(&outcome, &failure); err != nil {
		h.t.Fatal(err)
	}
	if outcome != "failed" || failure != "page" && failure != "network" {
		h.t.Fatalf("普通故障被当作成功、取消或风控：outcome=%s failure=%s", outcome, failure)
	}
	if graphScalar(h.t, h.g, `SELECT consecutive_failures FROM tasks WHERE id=?`, taskID) != 1 {
		h.t.Fatal("普通故障没有计入任务失败保护")
	}
	coreFaultAccountIdle(h, h.accountID)
	if graphScalar(h.t, h.g, `SELECT count(*) FROM meta WHERE key=?`, fmt.Sprintf("runtime.session.%d", h.accountID)) != 0 {
		h.t.Fatal("普通故障没有结算账号保护标记")
	}
}

func TestCoreFaultChromeKilled(t *testing.T) {
	coreFaultChromeEnabled(t)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var armed atomic.Bool
	releaseOnce := sync.OnceFunc(func() { close(release) })
	h := newCoreFlowHarness(t, ctx, func(_ http.ResponseWriter, r *http.Request, kind string) bool {
		if kind != "title_screen" || !armed.CompareAndSwap(true, false) {
			return false
		}
		close(entered)
		select {
		case <-release:
		case <-r.Context().Done():
		}
		return false
	}, nil)
	t.Cleanup(releaseOnce)
	h.Request(http.MethodPatch, "/api/v1/settings", map[string]any{"values": map[string]any{"crawl.global_pages": 2, "ai.max_concurrent_requests": 2}}, nil, http.StatusOK)
	other := coreFaultAccount(h, "不受浏览器故障影响的账号")
	h.xianyu.SetSearchItems(coreFaultOneItem("401"), nil)
	task := h.CreateTask(map[string]any{"keyword": "故障主任务"})
	queued := h.CreateTask(map[string]any{"keyword": "同账号恢复任务"})
	otherTask := h.CreateTask(map[string]any{"keyword": "独立账号任务", "account_id": other.ID})
	h.Run(task.ID)
	h.WaitRuns(task.ID, 1)
	coreFaultAccountIdle(h, h.accountID)
	h.xianyu.SetSearchItems(coreFaultOneItem("402"), nil)
	armed.Store(true)
	coreFaultRun(h, task.ID)
	coreWait(t, ctx, "故障轮物理模型请求到达", func() bool {
		select {
		case <-entered:
			return true
		default:
			return false
		}
	})
	coreFaultRun(h, queued.ID)
	coreFaultRun(h, otherTask.ID)
	h.WaitRuns(otherTask.ID, 1)
	if graphScalar(t, h.g, `SELECT count(*) FROM run_records WHERE task_id=? AND outcome='success'`, otherTask.ID) != 1 {
		t.Fatal("故障账号等待期间阻塞了另一个账号")
	}
	profile := filepath.Join(h.cfg.DataDir, "browser", strconv.FormatInt(h.accountID, 10))
	process, err := coreFaultFindChrome(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := coreFaultKillChrome(profile, process); err != nil {
		t.Fatal(err)
	}
	t.Logf("只向本测试 Chrome 主进程发送 SIGKILL：pid=%d start=%s profile=%s", process.pid, process.start, profile)
	releaseOnce()
	coreFaultOrdinaryFailure(h, task.ID, 2)
	h.WaitRuns(queued.ID, 1)
	if graphScalar(t, h.g, `SELECT count(*) FROM run_records WHERE task_id=? AND outcome='success'`, queued.ID) != 1 {
		t.Fatal("同账号队列没有在浏览器故障后恢复")
	}
	coreFaultAccountIdle(h, h.accountID)
	h.xianyu.SetSearchItems(coreFaultOneItem("403"), nil)
	coreFaultRun(h, task.ID)
	h.WaitRuns(task.ID, 3)
	if graphScalar(t, h.g, `SELECT count(*) FROM run_records WHERE task_id=? AND outcome='success'`, task.ID) != 2 {
		t.Fatal("杀掉浏览器后的新轮没有成功")
	}
	if graphScalar(t, h.g, `SELECT count(*) FROM findings WHERE task_id=? AND item_id='402'`, task.ID) != 1 {
		t.Fatal("恢复补处理重复创建了原发现")
	}
}

func TestCoreFaultTruncatedJSONAndHungResponse(t *testing.T) {
	coreFaultChromeEnabled(t)
	for _, mode := range []string{"truncated", "hung"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
			defer cancel()
			h := newCoreFlowHarness(t, ctx, nil, nil)
			h.Request(http.MethodPatch, "/api/v1/settings", map[string]any{"values": map[string]any{"crawl.global_pages": 2}}, nil, http.StatusOK)
			other := coreFaultAccount(h, "接口故障之外的账号")
			h.xianyu.SetSearchItems(coreFaultOneItem("411"), nil)
			task := h.CreateTask(map[string]any{"keyword": "目标接口故障", "personal_only": false, "free_shipping_only": mode == "hung"})
			otherTask := h.CreateTask(map[string]any{"keyword": "正常接口任务", "account_id": other.ID})
			h.Run(task.ID)
			h.WaitRuns(task.ID, 1)
			coreFaultAccountIdle(h, h.accountID)
			h.xianyu.SetSearchItems(coreFaultOneItem("412"), nil)
			callsBefore := graphScalar(t, h.g, `SELECT count(*) FROM ai_calls WHERE task_id=?`, task.ID)
			physicalBefore := len(h.ModelKinds())
			intentsBefore := graphScalar(t, h.g, `SELECT count(*) FROM notification_intents WHERE task_id=?`, task.ID)
			deliveriesBefore := graphScalar(t, h.g, `SELECT count(*) FROM deliveries WHERE task_id=? AND kind<>'system'`, task.ID)
			options := fake.SearchFault{Final: true, TruncatedJSON: true}
			if mode == "hung" {
				options = fake.SearchFault{Step: "shipping", Hold: true}
			}
			gate := h.xianyu.SetSearchFault(task.Keyword, options)
			t.Cleanup(gate.Release)
			started := time.Now()
			coreFaultRun(h, task.ID)
			coreWait(t, ctx, "只让目标关键词进入接口故障", func() bool {
				select {
				case <-gate.Entered:
					return true
				default:
					return false
				}
			})
			coreFaultRun(h, otherTask.ID)
			h.WaitRuns(otherTask.ID, 1)
			if graphScalar(t, h.g, `SELECT count(*) FROM run_records WHERE task_id=? AND outcome='success'`, otherTask.ID) != 1 {
				t.Fatal("接口故障期间另一个账号没有正常成功")
			}
			if mode == "hung" && graphScalar(t, h.g, `SELECT count(*) FROM run_records WHERE task_id=? AND ended_at IS NULL`, task.ID) != 1 {
				t.Fatal("另一账号没有在挂起请求仍在等待时完成")
			}
			coreFaultOrdinaryFailure(h, task.ID, 2)
			otherCalls := graphScalar(t, h.g, `SELECT count(*) FROM ai_calls WHERE task_id=?`, otherTask.ID)
			if graphScalar(t, h.g, `SELECT count(*) FROM ai_calls WHERE task_id=?`, task.ID) != callsBefore || int64(len(h.ModelKinds())-physicalBefore) != otherCalls {
				t.Fatal("未完成筛选的故障任务仍调用了模型，或把其它账号调用误计到目标任务")
			}
			if graphScalar(t, h.g, `SELECT count(*) FROM notification_intents WHERE task_id=?`, task.ID) != intentsBefore || graphScalar(t, h.g, `SELECT count(*) FROM deliveries WHERE task_id=? AND kind<>'system'`, task.ID) != deliveriesBefore {
				t.Fatal("未完成筛选的故障任务产生了商品通知")
			}
			if mode == "hung" && time.Since(started) < 29*time.Second {
				t.Fatal("没有沿正式的20秒加10秒等待规则结束")
			}
			if graphScalar(t, h.g, `SELECT count(*) FROM findings WHERE task_id=? AND item_id='412'`, task.ID) != 0 || graphScalar(t, h.g, `SELECT count(*) FROM seen_items WHERE task_id=? AND item_id='412'`, task.ID) != 0 {
				t.Fatal("未完成筛选的故障响应进入了发现或已见集合")
			}
			if mode == "hung" {
				select {
				case <-gate.Done:
				case <-ctx.Done():
					t.Fatal("关闭浏览器后挂起请求没有取消")
				}
			}
			h.xianyu.ClearSearchFault(task.Keyword)
			gate.Release()
			coreFaultRun(h, task.ID)
			h.WaitRuns(task.ID, 3)
			if graphScalar(t, h.g, `SELECT count(*) FROM run_records WHERE task_id=? AND outcome='success'`, task.ID) != 2 || graphScalar(t, h.g, `SELECT count(*) FROM findings WHERE task_id=? AND item_id='412'`, task.ID) != 1 {
				t.Fatal("接口恢复后没有恰好补入一次商品")
			}
		})
	}
}

type coreFaultProcess struct {
	pid, parent int
	start       string
}

func coreFaultReadProcess(pid int) (coreFaultProcess, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return coreFaultProcess{}, err
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return coreFaultProcess{}, errors.New("测试进程信息缺少名称边界")
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) < 20 {
		return coreFaultProcess{}, errors.New("测试进程信息不完整")
	}
	parent, err := strconv.Atoi(fields[1])
	return coreFaultProcess{pid: pid, parent: parent, start: fields[19]}, err
}

func coreFaultUsesProfile(args []string, profile string) bool {
	matched := false
	for index, arg := range args {
		if strings.HasPrefix(arg, "--type=") {
			return false
		}
		if arg == "--user-data-dir="+profile || arg == "--user-data-dir" && index+1 < len(args) && args[index+1] == profile {
			matched = true
		}
	}
	return matched
}

func coreFaultOwnedChromes(profile string) ([]coreFaultProcess, error) {
	entries, err := filepath.Glob("/proc/[0-9]*/cmdline")
	if err != nil {
		return nil, err
	}
	var found []coreFaultProcess
	for _, name := range entries {
		data, err := os.ReadFile(name)
		if err != nil || !coreFaultUsesProfile(strings.Split(string(data), "\x00"), profile) {
			continue
		}
		pid, err := strconv.Atoi(filepath.Base(filepath.Dir(name)))
		if err != nil {
			return nil, err
		}
		process, err := coreFaultReadProcess(pid)
		if err != nil {
			return nil, err
		}
		ancestor := process
		for depth := 0; depth < 128 && ancestor.pid > 1; depth++ {
			if ancestor.parent == os.Getpid() {
				found = append(found, process)
				break
			}
			ancestor, err = coreFaultReadProcess(ancestor.parent)
			if err != nil {
				break
			}
		}
	}
	return found, nil
}

func coreFaultFindChrome(profile string) (coreFaultProcess, error) {
	found, err := coreFaultOwnedChromes(profile)
	if err != nil {
		return coreFaultProcess{}, err
	}
	if len(found) != 1 {
		return coreFaultProcess{}, fmt.Errorf("本测试 profile 必须匹配唯一主进程，实际%d个", len(found))
	}
	return found[0], nil
}

func coreFaultKillChrome(profile string, expected coreFaultProcess) error {
	current, err := coreFaultFindChrome(profile)
	if err != nil {
		return err
	}
	if current != expected || current.pid <= 1 || current.pid == os.Getpid() {
		return errors.New("测试浏览器身份发生变化，拒绝发送信号")
	}
	return syscall.Kill(current.pid, syscall.SIGKILL)
}

// 回收检查只读完整进程表，被tini收养的同一profile也不能漏掉。
func coreFaultCountChromeProfiles(procRoot, profile string) (int, error) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 || !entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(procRoot, entry.Name(), "cmdline"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if coreFaultUsesProfile(strings.Split(string(data), "\x00"), profile) {
			count++
		}
	}
	return count, nil
}

func TestCoreFaultProfileMatcher(t *testing.T) {
	for _, test := range []struct {
		args []string
		want bool
	}{
		{[]string{"chrome", "--user-data-dir=/tmp/test/browser/1"}, true},
		{[]string{"chrome", "--user-data-dir", "/tmp/test/browser/1"}, true},
		{[]string{"chrome", "--user-data-dir=/tmp/test/browser/10"}, false},
		{[]string{"chrome", "--user-data-dir=/tmp/other/browser/1"}, false},
		{[]string{"chrome", "--type=renderer", "--user-data-dir=/tmp/test/browser/1"}, false},
	} {
		if got := coreFaultUsesProfile(test.args, "/tmp/test/browser/1"); got != test.want {
			t.Fatalf("profile 匹配错误：%v got=%v", test.args, got)
		}
	}
}

func TestCoreFaultChromeAbsenceCountsAdoptedAndRejectsReadFailure(t *testing.T) {
	procRoot := t.TempDir()
	profile := "/tmp/test/browser/1"
	for pid, args := range map[string][]string{
		"41": {"chrome", "--user-data-dir=/tmp/test/browser/10"},
		"42": {"chrome", "--user-data-dir=" + profile},
		"43": {"chrome", "--type=renderer", "--user-data-dir=" + profile},
		"44": {"chrome", "--user-data-dir", profile},
	} {
		dir := filepath.Join(procRoot, pid)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(strings.Join(args, "\x00")), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(pid+" (chrome) S 1"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if count, err := coreFaultCountChromeProfiles(procRoot, profile); err != nil || count != 2 {
		t.Fatal("被收养或多个Chrome不能当作不存在", count, err)
	}
	for _, pid := range []string{"42", "44"} {
		if err := os.RemoveAll(filepath.Join(procRoot, pid)); err != nil {
			t.Fatal(err)
		}
	}
	if count, err := coreFaultCountChromeProfiles(procRoot, profile); err != nil || count != 0 {
		t.Fatal("没有目标主进程时计数错误", count, err)
	}
	if err := os.MkdirAll(filepath.Join(procRoot, "45", "cmdline"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := coreFaultCountChromeProfiles(procRoot, profile); err == nil {
		t.Fatal("读取进程表失败被当成零Chrome")
	}
}

func coreFaultRequireMarkers(markers []int64, accountID, heldID int64) error {
	if len(markers) == 0 {
		return errors.New("存储故障没有留下任何恢复保护标记")
	}
	for _, wanted := range []int64{accountID, heldID} {
		found := false
		for _, accountID := range markers {
			found = found || accountID == wanted
		}
		if !found {
			return fmt.Errorf("账号%d缺少恢复保护标记，须核实是否已合法结算：实际%v", wanted, markers)
		}
	}
	return nil
}

func TestCoreFaultMarkersCannotPassWithEmptyOrPartialProof(t *testing.T) {
	for _, markers := range [][]int64{nil, {}, {1}, {2}, {3}} {
		if coreFaultRequireMarkers(markers, 1, 2) == nil {
			t.Fatal("空或缺失的恢复保护证据被当作通过", markers)
		}
	}
	if err := coreFaultRequireMarkers([]int64{1, 2}, 1, 2); err != nil {
		t.Fatal(err)
	}
}

type coreFaultProof struct {
	AccountID, HeldID, CoolingID, TaskID, QueuedID, ProviderID, ChannelID int64
	QuotaCount, AIUsage, CooldownUntil, StoppedAt                         int64
	Database, Reason, Clock                                               string
	Markers                                                               []int64
}

func TestCoreFaultStorage(t *testing.T) {
	coreFaultChromeEnabled(t)
	if os.Getenv("KAKATTA_CORE_FAULT_TMPFS") != "/fault-db" {
		t.Fatal("必须使用独立的 /fault-db 有限 tmpfs")
	}
	for _, mode := range []string{"disk_full", "sqlite_locked"} {
		t.Run(mode, func(t *testing.T) {
			data := t.TempDir()
			databaseDir, err := os.MkdirTemp("/fault-db", mode+"-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(databaseDir) })
			for _, phase := range []string{"inject", "recover"} {
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
				binary, err := os.Executable()
				if err != nil {
					cancel()
					t.Fatal(err)
				}
				command := exec.CommandContext(ctx, binary, "-test.run=^TestCoreFaultStorageChild$", "-test.v", "-test.timeout=4m30s")
				command.Env = append(os.Environ(), "KAKATTA_CORE_FAULT_PHASE="+phase, "KAKATTA_CORE_FAULT_MODE="+mode, "KAKATTA_CORE_FAULT_DATA="+data, "KAKATTA_CORE_FAULT_DATABASE="+filepath.Join(databaseDir, "kakatta.db"))
				output, err := command.CombinedOutput()
				cancel()
				t.Logf("%s/%s 子进程：\n%s", mode, phase, output)
				if err != nil {
					t.Fatalf("%s/%s 故障子进程未通过：%v", mode, phase, err)
				}
			}
			if report := os.Getenv("KAKATTA_CORE_FAULT_REPORT"); report != "" {
				proof, err := os.ReadFile(filepath.Join(data, "fault-proof.json"))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(report, mode+"-proof.json"), proof, 0600); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestCoreFaultStorageChild(t *testing.T) {
	phase := os.Getenv("KAKATTA_CORE_FAULT_PHASE")
	if phase == "" {
		t.Skip("只由存储故障父测试启动")
	}
	coreFaultChromeEnabled(t)
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	cfg := config.Config{DataDir: os.Getenv("KAKATTA_CORE_FAULT_DATA")}
	paths, err := config.CreateDataDirectories(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	paths.Database = os.Getenv("KAKATTA_CORE_FAULT_DATABASE")
	if !strings.HasPrefix(paths.Database, "/fault-db/") || paths.DataDir == "" {
		t.Fatal("拒绝使用专用测试挂载以外的故障数据库")
	}
	var key [32]byte
	for index := range key {
		key[index] = byte(index + 1)
	}
	if phase == "recover" {
		coreFaultRecover(t, ctx, cfg, paths, key)
		return
	}
	if phase != "inject" {
		t.Fatal("未知故障子进程阶段")
	}
	mode := os.Getenv("KAKATTA_CORE_FAULT_MODE")
	entered, release := make(chan struct{}), make(chan struct{})
	var armed atomic.Bool
	releaseOnce := sync.OnceFunc(func() { close(release) })
	h := newCoreFlowHarnessWithPaths(t, ctx, cfg, paths, key, func(_ http.ResponseWriter, r *http.Request, kind string) bool {
		if kind != "title_screen" || !armed.CompareAndSwap(true, false) {
			return false
		}
		close(entered)
		select {
		case <-release:
		case <-r.Context().Done():
		}
		return false
	}, nil)
	t.Cleanup(releaseOnce)
	h.Request(http.MethodPatch, "/api/v1/settings", map[string]any{"values": map[string]any{"crawl.global_pages": 2}}, nil, http.StatusOK)
	for _, zone := range []string{"Pacific/Kiritimati", "UTC"} {
		h.Request(http.MethodPatch, "/api/v1/settings", map[string]any{"values": map[string]any{"general.timezone": zone}}, nil, http.StatusOK)
	}
	held := coreFaultAccount(h, "存储故障时验证保留的账号")
	cooling := coreFaultAccount(h, "恢复时仍须冷却的账号")
	cooldown := time.Now().Add(time.Hour).UnixMilli()
	if _, err := h.g.database.DB().ExecContext(ctx, `UPDATE accounts SET state='cooling',cooldown_until=?,verify_level=1,cooldown_manual_used=1 WHERE id=?`, cooldown, cooling.ID); err != nil {
		t.Fatal(err)
	}
	h.xianyu.SetSearchItems(coreFaultOneItem("421"), nil)
	task := h.CreateTask(map[string]any{"keyword": "存储故障主任务"})
	queued := h.CreateTask(map[string]any{"keyword": "存储故障时已接纳的手动任务"})
	verify := h.CreateTask(map[string]any{"keyword": "保留验证任务", "account_id": held.ID})
	h.Run(task.ID)
	h.WaitRuns(task.ID, 1)
	coreFaultAccountIdle(h, h.accountID)
	h.xianyu.SetMaskOnSearch(true)
	coreFaultRun(h, verify.ID)
	coreWait(t, ctx, "验证浏览器实际保留", func() bool {
		var view service.AccountView
		h.Request(http.MethodGet, fmt.Sprintf("/api/v1/accounts/%d", held.ID), nil, &view, http.StatusOK)
		return view.Session != nil && view.Session.Phase == service.SessionVerifyHold
	})
	h.xianyu.SetMaskOnSearch(false)
	h.xianyu.SetSearchItems(coreFaultOneItem("422"), nil)
	armed.Store(true)
	coreFaultRun(h, task.ID)
	coreWait(t, ctx, "存储故障前物理模型请求已发出", func() bool {
		select {
		case <-entered:
			return true
		default:
			return false
		}
	})
	coreFaultRun(h, queued.ID)
	var queuedRuntime service.TaskRuntime
	h.Request(http.MethodGet, fmt.Sprintf("/api/v1/tasks/%d/runtime", queued.ID), nil, &queuedRuntime, http.StatusOK)
	if queuedRuntime.Phase != service.TaskQueued {
		t.Fatal("故障前没有真正接纳排队手动运行")
	}
	independent, err := sql.Open("sqlite", "file:"+filepath.ToSlash(paths.Database)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = independent.Close() })
	connection, err := independent.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = connection.ExecContext(context.Background(), "ROLLBACK"); _ = connection.Close() })
	reason := "runtime.storage_locked"
	clearFault := func() {}
	if mode == "disk_full" {
		reason = "runtime.storage_full"
		var busy, logFrames, checkpointed int
		if err := connection.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointed); err != nil || busy != 0 {
			t.Fatal("无法在无在途写事务时清空测试WAL", busy, err)
		}
		filler := filepath.Join(filepath.Dir(paths.Database), "space.filler")
		coreFaultFillFilesystem(t, filler)
		clearFault = sync.OnceFunc(func() {
			if err := os.Remove(filler); err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Error(err)
			}
		})
		t.Cleanup(clearFault)
		_, fullErr := connection.ExecContext(ctx, "BEGIN IMMEDIATE")
		if fullErr == nil {
			_, fullErr = connection.ExecContext(ctx, "CREATE TABLE core_fault_space_probe(payload BLOB)")
		}
		if fullErr == nil {
			_, fullErr = connection.ExecContext(ctx, "COMMIT")
		}
		_, _ = connection.ExecContext(ctx, "ROLLBACK")
		var sqliteError *sqlite.Error
		if !errors.As(fullErr, &sqliteError) || sqliteError.Code()&0xff != 13 {
			t.Fatalf("没有得到真实SQLITE_FULL，不能把本例算通过：%v", fullErr)
		}
		t.Logf("有限tmpfs实际耗尽，独立写探针得到SQLite错误码%d：%v", sqliteError.Code(), fullErr)
	} else if mode == "sqlite_locked" {
		if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
			t.Fatal("独立连接没有拿到真实写锁", err)
		}
		clearFault = sync.OnceFunc(func() {
			if _, err := connection.ExecContext(context.Background(), "ROLLBACK"); err != nil {
				t.Error(err)
			}
		})
		t.Cleanup(clearFault)
	} else {
		t.Fatal("未知存储故障")
	}
	releaseOnce()
	coreWait(t, ctx, "正式HTTP报告明确存储保护原因", func() bool {
		status, body, err := coreFaultRead(h, "/api/v1/system/status")
		return err == nil && status == http.StatusServiceUnavailable && coreFaultReason(body) == reason
	})
	stoppedAt := time.Now().UnixMilli()
	visits, calls := h.xianyu.RequestCount(), len(h.ModelKinds())
	coreWait(t, ctx, "全局存储停止自行关闭运行与验证保留Chrome", func() bool {
		for _, accountID := range []int64{h.accountID, held.ID} {
			count, err := coreFaultCountChromeProfiles("/proc", filepath.Join(cfg.DataDir, "browser", strconv.FormatInt(accountID, 10)))
			if err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				return false
			}
		}
		return true
	})
	for _, endpoint := range []struct {
		path   string
		status int
	}{
		{"/healthz", http.StatusServiceUnavailable}, {"/api/v1/system/status", http.StatusServiceUnavailable},
		{"/api/v1/settings", http.StatusOK}, {"/api/v1/me", http.StatusOK},
	} {
		status, body, err := coreFaultRead(h, endpoint.path)
		if err != nil || status != endpoint.status || endpoint.path == "/api/v1/system/status" && coreFaultReason(body) != reason {
			t.Errorf("停止期间诊断合同不符：%s status=%d reason=%s err=%v", endpoint.path, status, coreFaultReason(body), err)
		}
	}
	coreFaultAssertNoVisits(h, visits, calls)
	clearFault()
	var lastOutcome, lastFailure string
	var endedAt sql.NullInt64
	if err := h.g.database.DB().QueryRowContext(ctx, `SELECT outcome,COALESCE(failure_kind,''),ended_at FROM run_records WHERE task_id=? ORDER BY id DESC LIMIT 1`, task.ID).Scan(&lastOutcome, &lastFailure, &endedAt); err != nil {
		t.Fatal(err)
	}
	if lastOutcome != "failed" || endedAt.Valid && lastFailure != "config" || !endedAt.Valid && lastFailure != "restart" {
		t.Fatalf("存储失败未留下失败记录或保守占位：outcome=%s failure=%s ended=%v", lastOutcome, lastFailure, endedAt.Valid)
	}
	if graphScalar(t, h.g, `SELECT count(*) FROM run_records WHERE task_id=?`, queued.ID) != 0 {
		t.Fatal("全局停止后已接纳的手动队列仍开始运行")
	}
	// 清除物理故障不重置保护锁；本进程仍不接纳新运行。
	status, body, err := coreFaultRead(h, "/api/v1/system/status")
	if err != nil || status != http.StatusServiceUnavailable || coreFaultReason(body) != reason {
		t.Errorf("清障后自动绕过了本进程保护：HTTP=%d reason=%s err=%v", status, coreFaultReason(body), err)
	}
	coreFaultAssertNoVisits(h, visits, calls)
	proof := coreFaultProof{AccountID: h.accountID, HeldID: held.ID, CoolingID: cooling.ID, TaskID: task.ID, QueuedID: queued.ID, ProviderID: h.providerID, ChannelID: h.channelID,
		QuotaCount: graphScalar(t, h.g, "SELECT count(*) FROM quota_events"), AIUsage: graphScalar(t, h.g, "SELECT COALESCE(sum(COALESCE(input_tokens,0)+COALESCE(output_tokens,0)),0) FROM ai_calls"),
		CooldownUntil: cooldown, StoppedAt: stoppedAt, Database: paths.Database, Reason: reason}
	if err := h.g.database.DB().QueryRowContext(ctx, "SELECT value FROM meta WHERE key='quota.clock'").Scan(&proof.Clock); err != nil {
		t.Fatal(err)
	}
	rows, err := h.g.database.DB().QueryContext(ctx, "SELECT CAST(substr(key,17) AS INTEGER) FROM meta WHERE key LIKE 'runtime.session.%' ORDER BY key")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var accountID int64
		if err := rows.Scan(&accountID); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		proof.Markers = append(proof.Markers, accountID)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		t.Fatal(err)
	}
	if err := coreFaultRequireMarkers(proof.Markers, proof.AccountID, proof.HeldID); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.DataDir, "fault-proof.json"), data, 0600); err != nil {
		t.Fatal("不能写出恢复验收证据", err)
	}
	// 关闭结果允许保留已发生的保护错误；只放弃测试包装的引用，待子进程退出后才重开同库。
	if err := h.closeGraph(); err != nil {
		t.Log("旧graph按保护规则拒绝伪成功关闭，恢复将在新子进程执行")
		h.g = nil
	}
	for _, accountID := range []int64{h.accountID, held.ID} {
		count, err := coreFaultCountChromeProfiles("/proc", filepath.Join(cfg.DataDir, "browser", strconv.FormatInt(accountID, 10)))
		if err != nil || count != 0 {
			t.Fatal("故障子进程退出前仍有目标Chrome或无法确定进程状态", accountID, count, err)
		}
	}
}

func coreFaultRead(h *coreFlowHarness, path string) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(h.ctx, 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, h.server.URL+path, nil)
	if err != nil {
		return 0, nil, err
	}
	request.AddCookie(&http.Cookie{Name: "kakatta_session", Value: h.token})
	response, err := h.server.Client().Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	return response.StatusCode, body, err
}

func coreFaultReason(body []byte) string {
	var result struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &result)
	return result.Error.Code
}

func coreFaultAssertNoVisits(h *coreFlowHarness, visits int64, calls int) {
	h.t.Helper()
	deadline := time.NewTimer(1500 * time.Millisecond)
	defer deadline.Stop()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		if h.xianyu.RequestCount() != visits || len(h.ModelKinds()) != calls {
			h.t.Fatalf("存储全局停止期间仍新增访问：假闲鱼%d→%d 模型%d→%d", visits, h.xianyu.RequestCount(), calls, len(h.ModelKinds()))
		}
		select {
		case <-deadline.C:
			return
		case <-tick.C:
		case <-h.ctx.Done():
			h.t.Fatal(h.ctx.Err())
		}
	}
}

func coreFaultFillFilesystem(t *testing.T, name string) {
	t.Helper()
	var stat unix.Statfs_t
	if runtime.GOOS != "linux" || !strings.HasPrefix(name, "/fault-db/") || unix.Statfs(filepath.Dir(name), &stat) != nil || int64(stat.Type) != 0x01021994 || uint64(stat.Blocks)*uint64(stat.Bsize) > 128<<20 {
		t.Fatal("只能填满独立且不超过128MiB的测试tmpfs")
	}
	file, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	block := bytes.Repeat([]byte{0x5a}, 1<<20)
	var written int64
	for written <= 128<<20 {
		n, err := file.Write(block)
		written += int64(n)
		if errors.Is(err, syscall.ENOSPC) {
			t.Logf("独立tmpfs实际写入%d字节后收到ENOSPC", written)
			return
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("测试挂载没有按容量上限报ENOSPC，停止写入")
}

func coreFaultRecover(t *testing.T, ctx context.Context, cfg config.Config, paths config.Paths, key [32]byte) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(cfg.DataDir, "fault-proof.json"))
	if err != nil {
		t.Fatal(err)
	}
	var proof coreFaultProof
	if err := json.Unmarshal(data, &proof); err != nil || proof.Database != paths.Database || proof.QuotaCount < 1 {
		t.Fatal("恢复证据无效", err)
	}
	if err := coreFaultRequireMarkers(proof.Markers, proof.AccountID, proof.HeldID); err != nil {
		t.Fatal(err)
	}
	h := &coreFlowHarness{t: t, ctx: ctx, cfg: cfg, paths: paths, key: key, xianyu: fake.Start(), accountID: proof.AccountID, providerID: proof.ProviderID, channelID: proof.ChannelID}
	t.Cleanup(h.Close)
	h.xianyu.EnableAutomaticLogin()
	h.xianyu.EnableSellerProfile()
	h.xianyu.SetSearchItems(append(coreFaultOneItem("422"), coreFaultOneItem("423")...), nil)
	h.model = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
			ResponseFormat struct {
				Schema struct {
					Name string `json:"name"`
				} `json:"json_schema"`
			} `json:"response_format"`
		}
		if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Error("恢复轮没有经过正式模型兼容请求")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		kind := request.ResponseFormat.Schema.Name
		content := ""
		if kind == "listing_facts" {
			content = `{"specs":[{"name":"型号","value":"MacBook Pro"}],"brand_new":false,"musts":[],"bonuses":[],"checks":[],"seller_signals":[],"price_note":null,"extra_findings":[],"adjust":{"value":0,"reason":""},"summary":"自用设备"}`
		} else if kind == "title_screen" {
			var text string
			if len(request.Messages) != 2 || json.Unmarshal(request.Messages[1].Content, &text) != nil {
				t.Error("恢复标题输入格式错误")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			blocks := strings.Split(text, "```json\n")
			var batch []struct {
				No int `json:"no"`
			}
			if len(blocks) != 3 || json.Unmarshal([]byte(strings.SplitN(blocks[2], "\n```", 2)[0]), &batch) != nil || len(batch) == 0 {
				t.Error("恢复标题批次缺少商品")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			items := make([]map[string]any, 0, len(batch))
			for _, item := range batch {
				items = append(items, map[string]any{"no": item.No, "kind": "sale", "specs": []any{map[string]any{"name": "型号", "value": "MacBook Pro"}}, "brand_new": false, "verdict": "pass", "reason": ""})
			}
			body, _ := json.Marshal(map[string]any{"items": items})
			content = string(body)
		} else {
			t.Error("恢复轮出现未知模型用途", kind)
		}
		h.mu.Lock()
		h.modelKinds = append(h.modelKinds, kind)
		h.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": content}, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": 20, "completion_tokens": 10}})
	}))
	h.push = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("恢复推送请求无效")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		h.mu.Lock()
		h.pushed = append(h.pushed, body)
		h.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	h.openGraph()
	recoveredAt := time.Now().UnixMilli()
	if err := h.g.core.Prepare(ctx); err != nil {
		t.Fatal("清障后新进程准备失败", err)
	}
	if graphScalar(t, h.g, "SELECT count(*) FROM quota_events") != proof.QuotaCount || graphScalar(t, h.g, "SELECT COALESCE(sum(COALESCE(input_tokens,0)+COALESCE(output_tokens,0)),0) FROM ai_calls") != proof.AIUsage {
		t.Fatal("重启恢复减少或重建了原访问/模型额度")
	}
	if graphScalar(t, h.g, "SELECT count(*) FROM accounts WHERE id=? AND state='cooling' AND cooldown_until=? AND cooldown_manual_used=1", proof.CoolingID, proof.CooldownUntil) != 1 {
		t.Fatal("重启解除或缩短了冷却/手动机会保护")
	}
	if graphScalar(t, h.g, "SELECT count(*) FROM accounts WHERE id=? AND state='needs_verification'", proof.HeldID) != 1 {
		t.Fatal("恢复错误解除需要验证的账号状态")
	}
	var previousClock, recoveredClock quota.ClockState
	var clock string
	if err := h.g.database.DB().QueryRowContext(ctx, "SELECT value FROM meta WHERE key='quota.clock'").Scan(&clock); err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal([]byte(proof.Clock), &previousClock) != nil || json.Unmarshal([]byte(clock), &recoveredClock) != nil || previousClock.Validate() != nil || recoveredClock.Validate() != nil || recoveredClock.HighestUTC < previousClock.HighestUTC {
		t.Fatal("恢复使保护时钟倒退")
	}
	for _, window := range previousClock.Windows {
		if window.End <= recoveredClock.HighestUTC {
			continue
		}
		found := false
		for _, restored := range recoveredClock.Windows {
			found = found || restored == window
		}
		if !found {
			t.Fatal("重启丢失仍有效的旧时区每日保护窗口", window)
		}
	}
	for _, accountID := range proof.Markers {
		if graphScalar(t, h.g, "SELECT count(*) FROM accounts WHERE id=? AND last_run_ended_at>=? AND next_gap_seconds>=2", accountID, recoveredAt) != 1 {
			t.Fatal("未结束会话的间隔没有从恢复时刻重新等待", accountID)
		}
	}
	if graphScalar(t, h.g, "SELECT count(*) FROM run_records WHERE ended_at IS NULL") != 0 || graphScalar(t, h.g, "SELECT count(*) FROM meta WHERE key LIKE 'runtime.session.%'") != 0 {
		t.Fatal("新进程没有完成未结束运行和会话标记恢复")
	}
	login, err := h.g.authentication.PasswordLogin(ctx, "本地用户", "graph-test-password", "故障恢复测试")
	if err != nil || login.Session == nil {
		t.Fatal("恢复后不能使用原管理员登录", err)
	}
	h.token = login.Session.Token
	h.Request(http.MethodPut, fmt.Sprintf("/api/v1/ai/providers/%d", proof.ProviderID), map[string]any{"name": "恢复后的本地模型", "preset": nil, "base_url": h.model.URL + "/v1", "api_key": "local-core-flow-key", "vision_model": "local-vision", "text_model": "local-text", "enabled": true}, nil, http.StatusOK)
	h.Request(http.MethodPatch, fmt.Sprintf("/api/v1/channels/%d", proof.ChannelID), map[string]any{"name": "恢复后的本地推送", "secrets": map[string]any{"url": h.push.URL}}, nil, http.StatusOK)
	if err := h.g.start(); err != nil {
		t.Fatal(err)
	}
	if status, _, err := coreFaultRead(h, "/healthz"); err != nil || status != http.StatusOK {
		t.Fatal("新进程恢复后仍不健康", status, err)
	}
	if graphScalar(t, h.g, "SELECT count(*) FROM run_records WHERE task_id=?", proof.QueuedID) != 0 {
		t.Fatal("重启恢复了旧的手动排队请求")
	}
	coreFaultAccountIdle(h, proof.AccountID)
	count := graphScalar(t, h.g, "SELECT count(*) FROM run_records WHERE task_id=?", proof.TaskID)
	coreFaultRun(h, proof.TaskID)
	h.WaitRuns(proof.TaskID, count+1)
	if graphScalar(t, h.g, "SELECT count(*) FROM run_records WHERE task_id=? AND outcome='success'", proof.TaskID) != 2 || graphScalar(t, h.g, "SELECT count(*) FROM findings WHERE task_id=? AND item_id='423' AND decision='scored'", proof.TaskID) != 1 {
		t.Fatal("清障后同库新进程不能完成下一轮")
	}
	t.Logf("同库新进程恢复成功：原额度%d条、冷却截止%d保持，旧手动队列未重放，恢复轮完成", proof.QuotaCount, proof.CooldownUntil)
}

func TestCoreFaultShortSQLiteLock(t *testing.T) {
	coreFaultEnabled(t)
	g := graphFixture(t)
	graphAdmin(t, g)
	if _, err := g.database.DB().ExecContext(t.Context(), `INSERT INTO meta(key,value) VALUES('core.fault.short.lock','0')`); err != nil {
		t.Fatal(err)
	}
	var number int
	var alias, path string
	if err := g.database.DB().QueryRowContext(t.Context(), "PRAGMA database_list").Scan(&number, &alias, &path); err != nil {
		t.Fatal(err)
	}
	independent, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer independent.Close()
	connection, err := independent.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	defer connection.ExecContext(context.Background(), "ROLLBACK")
	if _, err := connection.ExecContext(t.Context(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := g.database.DB().ExecContext(t.Context(), `UPDATE meta SET value='1' WHERE key='core.fault.short.lock'`)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatal("独立写锁没有真正阻止另一个连接写入", err)
	case <-time.After(350 * time.Millisecond):
	}
	if _, err := connection.ExecContext(t.Context(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("短暂写锁释放后未恢复", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("短暂写锁释放后仍无法写入")
	}
}
