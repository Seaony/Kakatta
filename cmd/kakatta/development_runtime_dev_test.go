//go:build kakatta_dev

package main

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/development"
	"github.com/Seaony/Kakatta/internal/devfixture"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/xianyu/fake"
)

const developmentRuntimeURL = "http://127.0.0.1:18080"
const developmentRuntimeControl = "http://127.0.0.1:19090"

func requireDevelopmentRuntime(t *testing.T) {
	t.Helper()
	if os.Getenv("KAKATTA_DEV_RUNTIME") != "1" {
		t.Skip("由 deploy/test-dev-runtime.sh 在断网容器里运行")
	}
	interfaces, err := net.Interfaces()
	if err != nil || len(interfaces) != 1 || interfaces[0].Flags&net.FlagLoopback == 0 {
		t.Fatal("开发容器验收只允许回环网卡")
	}
	if os.Getenv("KAKATTA_DEV_RUNTIME_WORK") != "/work" {
		t.Fatal("开发容器验收必须使用独立工作挂载")
	}
}

func TestDevelopmentRuntimeFixtures(t *testing.T) {
	requireDevelopmentRuntime(t)
	ctx, stop := signal.NotifyContext(t.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fixture := devfixture.New(ctx, developmentRuntimeControl)
	defer fixture.Close()
	metadata := fixture.Metadata()
	upstream, _ := url.Parse(metadata.ModelBaseURL)
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	var mu sync.Mutex
	var gate chan struct{}
	var held, completed, cancelled int
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
		if err != nil {
			http.Error(w, "本地模型读取失败", http.StatusBadRequest)
			return
		}
		r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(body))
		if bytes.Contains(body, []byte(`"listing_facts"`)) {
			mu.Lock()
			waiting := gate
			if waiting != nil {
				held++
			}
			mu.Unlock()
			if waiting != nil {
				select {
				case <-waiting:
				case <-r.Context().Done():
					mu.Lock()
					cancelled++
					mu.Unlock()
					return
				case <-ctx.Done():
					return
				}
			}
			defer func() { mu.Lock(); completed++; mu.Unlock() }()
		}
		// 元数据已带 /v1，反向代理不能再追加一次。
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/v1")
		proxy.ServeHTTP(w, r)
	}))
	defer model.Close()
	metadata.ModelBaseURL = model.URL + "/v1"
	developmentRuntimeWrite(t, "/work/fixture.json", metadata)
	base := fixture.Handler()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/runtime/gate":
			mu.Lock()
			if gate != nil {
				close(gate)
				gate = nil
			}
			if r.URL.Query().Get("hold") == "1" {
				gate = make(chan struct{})
			}
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case "/runtime/state":
			mu.Lock()
			defer mu.Unlock()
			json.NewEncoder(w).Encode(map[string]int{"held": held, "completed": completed, "cancelled": cancelled})
		default:
			base.ServeHTTP(w, r)
		}
	})
	listener, err := net.Listen("tcp4", "127.0.0.1:19090")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	go server.Serve(listener)
	<-ctx.Done()
	mu.Lock()
	if gate != nil {
		close(gate)
		gate = nil
	}
	mu.Unlock()
	if err := server.Close(); err != nil {
		t.Error(err)
	}
}

type developmentRuntimeSaved struct {
	Cookies   []*http.Cookie `json:"cookies"`
	AccountID int64          `json:"account_id"`
	TaskID    int64          `json:"task_id"`
	Visits    int64          `json:"visits"`
	Logins    int64          `json:"logins"`
	KeySHA    string         `json:"key_sha"`
}

func TestDevelopmentRuntimeDriver(t *testing.T) {
	requireDevelopmentRuntime(t)
	stage := os.Getenv("KAKATTA_DEV_RUNTIME_STAGE")
	if stage != "first" && stage != "restart" {
		t.Fatal("开发容器验收阶段无效")
	}
	report := map[string]any{"stage": stage, "passed": false, "steps": []string{}}
	defer func() {
		report["passed"] = !t.Failed()
		developmentRuntimeWrite(t, "/work/"+stage+".json", report)
	}()
	record := func(step string) {
		report["steps"] = append(report["steps"].([]string), step)
		t.Log(step)
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	base, _ := url.Parse(developmentRuntimeURL)
	var status development.Status
	developmentRuntimeWait(t, "开发后端就绪", 150*time.Second, func() bool {
		developmentRuntimeHTTP(t, client, "GET", "http://127.0.0.1:18081/__dev/status", nil, 200, &status)
		return status.Phase == "running" && status.BackendReady
	})
	if status.Mode != "real" || !status.SchedulerPaused {
		t.Fatal("真实开发模式未默认暂停调度")
	}
	record("真实开发模式默认暂停调度，后端只监听回环")
	var saved developmentRuntimeSaved
	if stage == "first" {
		developmentRuntimeHTTP(t, client, "GET", developmentRuntimeURL+"/api/v1/me", nil, 401, nil)
		developmentRuntimeHTTP(t, client, "POST", developmentRuntimeURL+"/api/v1/setup", map[string]string{"username": "容器验收", "password": "development-runtime-password"}, 200, nil)
		developmentRuntimeHTTP(t, client, "POST", developmentRuntimeURL+"/api/v1/auth/login", map[string]string{"username": "容器验收", "password": "development-runtime-password"}, 200, nil)
		developmentRuntimeHTTP(t, client, "GET", developmentRuntimeURL+"/api/v1/me", nil, 200, nil)
		saved.Cookies = jar.Cookies(base)
		if len(saved.Cookies) != 1 || saved.Cookies[0].Name != "kakatta_session" {
			t.Fatal("真实登录没有建立网页会话")
		}
		record("未登录接口拒绝访问；直接设置账号密码及正常登录均通过")
		saved = developmentRuntimeSetup(t, client, saved)
		developmentRuntimeWait(t, "假扫码完成并关闭浏览器", 45*time.Second, func() bool {
			developmentRuntimeHTTP(t, client, "POST", developmentRuntimeControl+"/control", map[string]string{"action": "login_complete"}, 200, nil)
			return developmentRuntimeScalar(t, "SELECT count(*) FROM accounts WHERE id=? AND state='active' AND last_run_ended_at IS NOT NULL", saved.AccountID) == 1
		})
		var fixtureState struct {
			Logins int64 `json:"login_completions"`
		}
		developmentRuntimeHTTP(t, client, "GET", developmentRuntimeControl+"/state", nil, 200, &fixtureState)
		saved.Logins = fixtureState.Logins
		record("真实 Chrome、Xvfb 和远程画面完成本地假扫码并保存账号")
		time.Sleep(1500 * time.Millisecond)
		if developmentRuntimeScalar(t, "SELECT count(*) FROM run_records") != 0 {
			t.Fatal("默认暂停时已启用任务仍被自动调度")
		}
		developmentRuntimeSearch(t, client, "7101")
		developmentRuntimeRun(t, client, saved.TaskID)
		developmentRuntimeWait(t, "第一轮基线运行成功", 60*time.Second, func() bool {
			return developmentRuntimeScalar(t, "SELECT count(*) FROM run_records WHERE ended_at IS NOT NULL AND outcome='success'") == 1
		})
		record("手动假任务经过真实浏览器、规则和模型成功建立首轮基线")
		developmentRuntimeHoldRun(t, client, saved.TaskID, "7102")
		before := status.Generation
		developmentRuntimeWriteProbe(t, "运行中热更新")
		developmentRuntimeWait(t, "候选编译后等待当前运行", 60*time.Second, func() bool {
			developmentRuntimeHTTP(t, client, "GET", "http://127.0.0.1:18081/__dev/status", nil, 200, &status)
			return status.Phase == "reloading"
		})
		if status.Generation != before || !status.BackendReady || developmentRuntimeScalar(t, "SELECT count(*) FROM run_records WHERE ended_at IS NULL") != 1 {
			t.Fatal("热更新在活跃运行完成前切换了后端")
		}
		time.Sleep(500 * time.Millisecond)
		developmentRuntimeGateHealthy(t, client)
		developmentRuntimeHTTP(t, client, "POST", developmentRuntimeControl+"/runtime/gate?hold=0", nil, 204, nil)
		developmentRuntimeWait(t, "运行完成后自动换代", 60*time.Second, func() bool {
			developmentRuntimeHTTP(t, client, "GET", "http://127.0.0.1:18081/__dev/status", nil, 200, &status)
			return status.Generation == before+1 && status.Phase == "running" && status.BackendReady
		})
		developmentRuntimeCheckRuns(t, client, 2, 1)
		developmentRuntimeHTTP(t, client, "GET", developmentRuntimeURL+"/api/v1/me", nil, 200, nil)
		if !status.SchedulerPaused || developmentRuntimeScalar(t, "SELECT count(*) FROM accounts WHERE id=? AND state='active'", saved.AccountID) != 1 {
			t.Fatal("热更新丢失账号或恢复了默认暂停的调度")
		}
		saved.Visits = developmentRuntimeScalar(t, "SELECT count(*) FROM quota_events")
		saved.KeySHA = developmentRuntimeKey(t)
		developmentRuntimeWrite(t, "/work/session.json", saved)
		report["generation"] = status.Generation
		record("运行期间保存Go源码后等待模型完成，商品正常推送，随后自动换代且原会话继续有效")
	} else {
		developmentRuntimeRead(t, "/work/session.json", &saved)
		jar.SetCookies(base, saved.Cookies)
		developmentRuntimeHTTP(t, client, "GET", developmentRuntimeURL+"/api/v1/me", nil, 200, nil)
		if developmentRuntimeKey(t) != saved.KeySHA || developmentRuntimeScalar(t, "SELECT count(*) FROM quota_events") < saved.Visits || developmentRuntimeScalar(t, "SELECT count(*) FROM accounts WHERE id=? AND state='active'", saved.AccountID) != 1 {
			t.Fatal("完整容器重启丢失密钥、额度或账号")
		}
		developmentRuntimeCheckRuns(t, client, 2, 1)
		record("完整容器重启后原网页会话、密钥、数据库、额度和账号仍然保留")
		developmentRuntimeHoldRun(t, client, saved.TaskID, "7103")
		developmentRuntimeWrite(t, "/work/shutdown-ready.json", map[string]bool{"ready": true})
		developmentRuntimeWait(t, "单次SIGINT进入安全排空", 30*time.Second, func() bool {
			developmentRuntimeHTTP(t, client, "GET", "http://127.0.0.1:18081/__dev/status", nil, 200, &status)
			return status.Phase == "stopping"
		})
		time.Sleep(500 * time.Millisecond)
		developmentRuntimeGateHealthy(t, client)
		if developmentRuntimeScalar(t, "SELECT count(*) FROM run_records WHERE ended_at IS NULL") != 1 {
			t.Fatal("Ctrl+C 提前终止了活跃运行")
		}
		developmentRuntimeHTTP(t, client, "POST", developmentRuntimeControl+"/runtime/gate?hold=0", nil, 204, nil)
		developmentRuntimeWait(t, "退出前活跃运行自然完成", 45*time.Second, func() bool {
			return developmentRuntimeScalar(t, "SELECT count(*) FROM run_records WHERE ended_at IS NOT NULL AND outcome='success'") == 3
		})
		developmentRuntimeCheckRuns(t, client, 3, 2)
		var fixtureState struct {
			Logins int64 `json:"login_completions"`
		}
		developmentRuntimeHTTP(t, client, "GET", developmentRuntimeControl+"/state", nil, 200, &fixtureState)
		if fixtureState.Logins != saved.Logins {
			t.Fatal("重载或重启意外重新扫码")
		}
		record("原浏览器登录无需重扫，单次SIGINT等待当前运行成功和推送后才关闭")
	}
}

func developmentRuntimeSetup(t *testing.T, client *http.Client, saved developmentRuntimeSaved) developmentRuntimeSaved {
	values := map[string]any{"general.update_check": false, "general.onboarding_done": true, "crawl.quiet_hours": map[string]any{"enabled": false, "start": "01:00", "end": "07:30"}, "crawl.account_min_gap_seconds": 2, "crawl.account_min_gap_jitter_seconds": 0, "crawl.account_hourly_visits": 200, "crawl.interval_jitter_percent": 0, "crawl.typing_interval_ms": map[string]int{"min": 1, "max": 1}, "ai.image_count": 1}
	for _, name := range []string{"home_stay_seconds", "page_wait_seconds", "detail_pre_wait_seconds", "detail_stay_seconds", "action_wait_seconds"} {
		values["crawl."+name] = map[string]float64{"min": 0.01, "max": 0.01}
	}
	developmentRuntimeHTTP(t, client, "PATCH", developmentRuntimeURL+"/api/v1/settings", map[string]any{"values": values}, 200, nil)
	var account service.Account
	developmentRuntimeHTTP(t, client, "POST", developmentRuntimeURL+"/api/v1/accounts", map[string]string{"name": "容器假账号"}, 201, &account)
	saved.AccountID = account.ID
	var metadata devfixture.Metadata
	developmentRuntimeRead(t, "/work/fixture.json", &metadata)
	developmentRuntimeHTTP(t, client, "POST", developmentRuntimeURL+"/api/v1/ai/providers", map[string]any{"name": "容器本地模型", "preset": "custom", "base_url": metadata.ModelBaseURL, "api_key": metadata.ModelKey, "vision_model": "local-vision", "text_model": "local-text", "enabled": true}, 201, nil)
	var channel struct {
		ID int64 `json:"id"`
	}
	developmentRuntimeHTTP(t, client, "POST", developmentRuntimeURL+"/api/v1/channels", map[string]any{"name": "容器本地推送", "type": "webhook", "secrets": map[string]string{"url": metadata.PushURL}}, 201, &channel)
	profile := analysis.DefaultProfile()
	profile.Summary = "自用 MacBook Pro"
	profile.Specs = []analysis.ProfileSpec{{Name: "型号", Options: []string{"MacBook Pro"}, Values: []string{"MacBook Pro"}, Source: analysis.SourceUser, Edited: true}}
	var task service.TaskSaveResult
	developmentRuntimeHTTP(t, client, "POST", developmentRuntimeURL+"/api/v1/tasks", map[string]any{"name": "容器开发监控", "keyword": "MacBook", "enabled": true, "account_id": account.ID, "account_pinned": true, "max_pages": 1, "personal_only": true, "judge_mode": "ai", "requirement": "自用 MacBook Pro", "profile": profile, "notify_policy": "all_new", "channel_ids": []int64{channel.ID}, "analyze_on_first_run": true}, 201, &task)
	saved.TaskID = task.Task.ID
	developmentRuntimeHTTP(t, client, "POST", fmt.Sprintf("%s/api/v1/accounts/%d/login", developmentRuntimeURL, account.ID), nil, 202, nil)
	return saved
}

func developmentRuntimeRun(t *testing.T, client *http.Client, task int64) {
	t.Helper()
	// 运行记录先落盘，浏览器和远程画面随后才完成关闭。
	developmentRuntimeWait(t, "上一轮资源回收", 45*time.Second, func() bool {
		var runtime service.TaskRuntime
		developmentRuntimeHTTP(t, client, "GET", fmt.Sprintf("%s/api/v1/tasks/%d/runtime", developmentRuntimeURL, task), nil, 200, &runtime)
		return runtime.Phase == service.TaskIdle
	})
	developmentRuntimeWait(t, "账号正常运行间隔", 15*time.Second, func() bool {
		next := developmentRuntimeScalar(t, `SELECT COALESCE(a.last_run_ended_at,0)+COALESCE(a.next_gap_seconds,0)*1000
			FROM accounts a JOIN tasks t ON t.account_id=a.id WHERE t.id=?`, task)
		return time.Now().UnixMilli() > next+100
	})
	developmentRuntimeHTTP(t, client, "POST", fmt.Sprintf("%s/api/v1/tasks/%d/run", developmentRuntimeURL, task), nil, 202, nil)
}

func developmentRuntimeSearch(t *testing.T, client *http.Client, id string) {
	t.Helper()
	developmentRuntimeHTTP(t, client, "POST", developmentRuntimeControl+"/control", map[string]any{"action": "search", "pages": [][]fake.SearchItem{{{ID: id, Title: "MacBook Pro 自用", Price: "10000", SellerID: "seller" + id}}}}, 200, nil)
}

func developmentRuntimeHoldRun(t *testing.T, client *http.Client, task int64, id string) {
	t.Helper()
	developmentRuntimeSearch(t, client, id)
	var before map[string]int
	developmentRuntimeHTTP(t, client, "GET", developmentRuntimeControl+"/runtime/state", nil, 200, &before)
	developmentRuntimeHTTP(t, client, "POST", developmentRuntimeControl+"/runtime/gate?hold=1", nil, 204, nil)
	developmentRuntimeRun(t, client, task)
	developmentRuntimeWait(t, "真实运行到达可控模型请求", 60*time.Second, func() bool {
		var state map[string]int
		developmentRuntimeHTTP(t, client, "GET", developmentRuntimeControl+"/runtime/state", nil, 200, &state)
		return state["held"] > before["held"]
	})
}

func developmentRuntimeGateHealthy(t *testing.T, client *http.Client) {
	t.Helper()
	var state map[string]int
	developmentRuntimeHTTP(t, client, "GET", developmentRuntimeControl+"/runtime/state", nil, 200, &state)
	if state["cancelled"] != 0 {
		t.Fatal("管理器取消了仍在运行的模型请求")
	}
}

func developmentRuntimeCheckRuns(t *testing.T, client *http.Client, runs, pushes int64) {
	t.Helper()
	developmentRuntimeWait(t, "推送回执落库", 15*time.Second, func() bool {
		return developmentRuntimeScalar(t, "SELECT count(*) FROM notification_intents WHERE state='sent'") == pushes
	})
	if developmentRuntimeScalar(t, "SELECT count(*) FROM run_records WHERE ended_at IS NOT NULL AND outcome='success'") != runs || developmentRuntimeScalar(t, "SELECT count(*) FROM run_records WHERE outcome='failed'") != 0 {
		t.Fatal("运行未正常结束，或重启造成失败记录")
	}
	var state struct {
		Pushes []map[string]any `json:"pushes"`
	}
	developmentRuntimeHTTP(t, client, "GET", developmentRuntimeControl+"/state", nil, 200, &state)
	if int64(len(state.Pushes)) != pushes {
		t.Fatal("本地推送漏发或重复发送")
	}
}

func developmentRuntimeWriteProbe(t *testing.T, text string) {
	t.Helper()
	content := "//go:build kakatta_dev\n\npackage main\n\nconst developmentRuntimeProbe = " + fmt.Sprintf("%q\n", text) + "\n"
	if err := os.WriteFile("/workspace/cmd/kakatta/runtime_probe_dev.go", []byte(content), 0600); err != nil {
		t.Fatal("不能修改隔离源码以验证热更新", err)
	}
}

func developmentRuntimeKey(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("/data/secret.key")
	if err != nil {
		t.Fatal("不能读取验收数据目录内的密钥指纹", err)
	}
	value := sha256.Sum256(raw)
	return hex.EncodeToString(value[:])
}

func developmentRuntimeHTTP(t *testing.T, client *http.Client, method, address string, body any, want int, result any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, address, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Origin", req.URL.Scheme+"://"+req.URL.Host)
	response, err := client.Do(req)
	if err != nil {
		t.Fatal("本地容器请求失败", req.URL.Path, err)
	}
	defer response.Body.Close()
	if response.StatusCode != want {
		raw, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
		t.Fatalf("%s %s 返回HTTP%d，预期%d：%s", method, req.URL.Path, response.StatusCode, want, raw)
	}
	if result != nil {
		if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(result); err != nil {
			t.Fatal(err)
		}
	}
}

func developmentRuntimeScalar(t *testing.T, query string, args ...any) int64 {
	t.Helper()
	db, err := sql.Open("sqlite", "file:/data/kakatta.db?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var value int64
	if err := db.QueryRowContext(t.Context(), query, args...).Scan(&value); err != nil {
		t.Fatal("只读核对失败", err)
	}
	return value
}

func developmentRuntimeWait(t *testing.T, name string, limit time.Duration, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal(name + "超时")
}

func developmentRuntimeWrite(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err == nil {
		err = os.WriteFile(path, append(raw, '\n'), 0600)
	}
	if err != nil {
		t.Fatal("不能保存开发容器验收产物", filepath.Base(path), err)
	}
}

func developmentRuntimeRead(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err == nil {
		err = json.Unmarshal(raw, value)
	}
	if err != nil {
		t.Fatal("不能读取开发容器验收产物", filepath.Base(path), err)
	}
}
