package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/browser"
	"github.com/Seaony/Kakatta/internal/resources"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/xianyu/fake"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
	"github.com/coder/websocket"
)

type countedAccountResources struct {
	factory service.ResourceFactory
	opens   atomic.Int32
}

func (f *countedAccountResources) Open(ctx context.Context, spec service.ResourceSpec) (service.SessionResource, error) {
	f.opens.Add(1)
	return f.factory.Open(ctx, spec)
}

func TestBusinessRemoteBrowserAcceptanceAndRevocation(t *testing.T) {
	if os.Getenv("KAKATTA_BROWSER_TESTS") != "1" || runtime.GOOS != "linux" {
		t.Skip("需要在断网 Linux 容器设置 KAKATTA_BROWSER_TESTS=1，验证真实浏览器与远程连接")
	}
	if _, err := browser.FindChrome(); err != nil {
		t.Fatal(err)
	}
	site := fake.Start()
	defer site.Close()
	site.SetMaskOnSearch(true)
	factory, err := resources.New(web.Site{Home: site.Home(), APIHost: site.APIHost()})
	if err != nil {
		t.Fatal(err)
	}
	counted := &countedAccountResources{factory: factory}
	f, notifier := newVerificationBusinessFixture(t, counted)
	fast := settings.Values{
		"crawl.home_stay_seconds":   json.RawMessage(`{"min":0.01,"max":0.01}`),
		"crawl.action_wait_seconds": json.RawMessage(`{"min":0.01,"max":0.01}`),
		"crawl.typing_interval_ms":  json.RawMessage(`{"min":1,"max":1}`),
		"crawl.quiet_hours":         json.RawMessage(`{"enabled":false,"start":"01:00","end":"07:30"}`),
	}
	if _, err := f.settings.Apply(t.Context(), fast); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.DB().ExecContext(t.Context(), `INSERT INTO sqlite_sequence(name,seq) VALUES('accounts',10000)`); err != nil {
		t.Fatal(err)
	}
	account := createHTTPAccount(t, f, "本地远程验收", nil)
	notifier.accountID.Store(account.ID)
	if _, err := f.store.DB().ExecContext(t.Context(), "UPDATE accounts SET state='active' WHERE id=?", account.ID); err != nil {
		t.Fatal(err)
	}
	created := f.request(http.MethodPost, "/api/v1/tasks", fmt.Sprintf(`{"keyword":"相机","name":"远程验收","enabled":false,"judge_mode":"keyword","account_id":%d,"account_pinned":true}`, account.ID), true)
	requireStatus(t, created, http.StatusCreated)
	task := decodeBody[service.TaskSaveResult](t, created).Task
	notifier.taskID.Store(task.ID)
	run := f.request(http.MethodPost, fmt.Sprintf("/api/v1/tasks/%d/run", task.ID), "", true)
	requireStatus(t, run, http.StatusAccepted)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	var session service.SessionView
	poll := time.NewTicker(50 * time.Millisecond)
	defer poll.Stop()
	for session.RemotePath == nil || session.Phase != service.SessionVerifyHold || notifier.eventID.Load() == 0 || notifier.runID.Load() == 0 {
		value, err := f.service.GetAccount(ctx, account.ID)
		if err != nil {
			t.Fatal(err)
		}
		if value.Session != nil {
			session = *value.Session
		}
		select {
		case <-ctx.Done():
			t.Fatalf("假页面没有保留同一验证会话或完成事件和运行结算：phase=%s event=%d run=%d", session.Phase, notifier.eventID.Load(), notifier.runID.Load())
		case <-poll.C:
		}
	}
	if session.ExpiresAt == nil || *session.ExpiresAt != f.clock.Load()+30*time.Minute.Milliseconds() || counted.opens.Load() != 1 {
		t.Fatal("验证保留期或单一浏览器资源不正确")
	}
	path := *session.RemotePath
	f.advance(29 * time.Minute)
	for _, suffix := range []string{"", "viewport", "viewer.js", "novnc/core/rfb.js"} {
		requireStatus(t, f.request(http.MethodGet, path+suffix, "", false), http.StatusUnauthorized)
		requireStatus(t, f.request(http.MethodGet, path+suffix, "", true), http.StatusOK)
	}
	read, err := f.service.GetSession(ctx, account.ID, session.ID)
	if err != nil || !equalOptionalTime(read.ExpiresAt, session.ExpiresAt) || read.Phase != service.SessionVerifyHold {
		t.Fatal("只读页面和资源不能把保留期变成新十分钟")
	}
	accepted := f.request(http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/verification", account.ID), "", true)
	requireStatus(t, accepted, http.StatusAccepted)
	if value := decodeBody[service.SessionView](t, accepted); value.ID != session.ID || !equalOptionalTime(value.ExpiresAt, session.ExpiresAt) {
		t.Fatal("接手动作不能在连接前续期或另开浏览器")
	}
	if _, err := f.settings.Apply(ctx, settings.Values{"security.trusted_proxies": json.RawMessage(`["127.0.0.1"]`)}); err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(f.server)
	defer api.Close()
	address := "ws" + strings.TrimPrefix(api.URL, "http") + path + "vnc"
	headers := http.Header{
		"Cookie":            {secureCookieName + "=" + f.session.Token},
		"X-Forwarded-Proto": {"https"},
		"X-Forwarded-Host":  {"public.example"},
		"Origin":            {"https://attacker.invalid"},
	}
	bad, response, err := websocket.Dial(ctx, address, &websocket.DialOptions{HTTPHeader: headers})
	if bad != nil {
		bad.CloseNow()
	}
	if response != nil {
		response.Body.Close()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatal("不同源远程连接必须在接受前被拒绝")
	}
	read, err = f.service.GetSession(ctx, account.ID, session.ID)
	if err != nil || !equalOptionalTime(read.ExpiresAt, session.ExpiresAt) || read.Phase != service.SessionVerifyHold {
		t.Fatal("未接受的连接不能启动人工十分钟")
	}
	headers.Set("Origin", "https://public.example")
	connection, _, err := websocket.Dial(ctx, address, &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		t.Fatal("可信代理的同源连接没有使用已核验的外部Host")
	}
	defer connection.CloseNow()
	readVNCVersion(t, ctx, connection)
	firstDeadline := f.clock.Load() + 10*time.Minute.Milliseconds()
	read, err = f.service.GetSession(ctx, account.ID, session.ID)
	if err != nil || read.Phase != service.SessionVerifying || read.ExpiresAt == nil || *read.ExpiresAt != firstDeadline {
		t.Fatal("实际接受连接后必须从首次接手时起完整十分钟")
	}
	beforeRequests := len(site.SearchQueries())
	confirm, _ := json.Marshal(map[string]string{"session_id": session.ID})
	stillPending := f.request(http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/verification/confirm", account.ID), string(confirm), true)
	requireStatus(t, stillPending, http.StatusConflict)
	if len(site.SearchQueries()) != beforeRequests {
		t.Fatal("确认仍有验证时不能自行刷新、导航或重发搜索")
	}
	connection.CloseNow()
	f.advance(time.Minute)
	connection, _, err = websocket.Dial(ctx, address, &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.CloseNow()
	readVNCVersion(t, ctx, connection)
	read, err = f.service.GetSession(ctx, account.ID, session.ID)
	if err != nil || read.ExpiresAt == nil || *read.ExpiresAt != firstDeadline || counted.opens.Load() != 1 {
		t.Fatal("重复连接不能续期或新开浏览器")
	}
	if err := f.auth.RevokeToken(ctx, f.session.Identity, f.session.Identity.TokenID); err != nil {
		t.Fatal(err)
	}
	readCtx, stop := context.WithTimeout(ctx, 7*time.Second)
	defer stop()
	for {
		if _, _, err := connection.Read(readCtx); err != nil {
			if readCtx.Err() != nil {
				t.Fatal("鉴权吊销后原WebSocket没有及时断开")
			}
			break
		}
	}
	if _, err := f.service.GetSession(ctx, account.ID, session.ID); err != nil {
		t.Fatal("吊销viewer凭据不能误关账号的整个浏览器会话")
	}
	if err := f.service.CloseSession(ctx, account.ID, session.ID); err != nil {
		t.Fatal(err)
	}
}

func readVNCVersion(t *testing.T, ctx context.Context, connection *websocket.Conn) {
	t.Helper()
	read, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var version []byte
	for len(version) < 4 {
		kind, data, err := connection.Read(read)
		if err != nil || kind != websocket.MessageBinary {
			t.Fatal("已接受的远程连接没有真实VNC数据")
		}
		version = append(version, data...)
	}
	if !strings.HasPrefix(string(version), "RFB ") {
		t.Fatal("远程连接没有连接到本地VNC服务")
	}
}
