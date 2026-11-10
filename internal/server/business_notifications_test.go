package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/notification"
	"github.com/Seaony/Kakatta/internal/notify"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/settings"
)

type blockedNotificationTransport struct{ calls atomic.Int64 }

func (transport *blockedNotificationTransport) RoundTrip(*http.Request) (*http.Response, error) {
	transport.calls.Add(1)
	return nil, errors.New("本用例不允许发出通知")
}

func registerNotificationHTTPFixture(t *testing.T, f *businessFixture, sender *notify.Sender) *notification.Service {
	t.Helper()
	if sender == nil {
		transport := new(blockedNotificationTransport)
		sender = notify.New(notify.Dependencies{
			HTTP: &http.Client{Transport: transport},
			DialContext: func(context.Context, string, string) (net.Conn, error) {
				return nil, errors.New("本用例不允许连接邮件服务器")
			},
		})
	}
	notifications, err := notification.New(notification.Dependencies{
		DB: f.store.DB(), Settings: f.settings.base, Cipher: f.cipher, Sender: sender,
		Now: func() time.Time { return time.UnixMilli(f.clock.Load()) },
		Publish: func(kind string, reason *locales.Message, data json.RawMessage) error {
			return f.server.Publish(Event{Type: kind, Reason: reason, Data: data})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := notifications.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	if err := f.server.RegisterNotifications(notifications); err != nil {
		t.Fatal(err)
	}
	return notifications
}

func newNotificationHTTPFixture(t *testing.T, sender *notify.Sender) (*businessFixture, *notification.Service) {
	t.Helper()
	f := prepareBusinessFixture(t)
	return f, registerNotificationHTTPFixture(t, f, sender)
}

func createHTTPChannel(t *testing.T, f *businessFixture, kind, config, secrets string) notification.Channel {
	t.Helper()
	body := fmt.Sprintf(`{"type":%q,"name":%q,"config":%s,"secrets":%s}`, kind, "渠道 "+kind, config, secrets)
	response := f.request(http.MethodPost, "/api/v1/channels", body, true)
	requireStatus(t, response, http.StatusCreated)
	assertChannelResponseSafe(t, response)
	return decodeBody[notification.Channel](t, response)
}

func assertChannelResponseSafe(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	for _, secret := range []string{"private-", "secret_enc", "uuid", `"secrets"`} {
		if bytes.Contains(response.Body.Bytes(), []byte(secret)) {
			t.Fatalf("渠道响应泄露敏感字段或内容：%s", secret)
		}
	}
}

func TestBusinessNotificationsCRUDAllKindsPreserveSecretsAndNumbers(t *testing.T) {
	transport := new(blockedNotificationTransport)
	f, _ := newNotificationHTTPFixture(t, notify.New(notify.Dependencies{HTTP: &http.Client{Transport: transport}}))
	initial := f.request(http.MethodGet, "/api/v1/channels", "", true)
	requireStatus(t, initial, http.StatusOK)
	if strings.TrimSpace(initial.Body.String()) != "[]" {
		t.Fatal("空渠道列表必须是数组")
	}
	var channels []notification.Channel
	for _, sample := range []struct{ kind, config, secrets string }{
		{"bark", `{}`, `{"url":"http://127.0.0.1:1/private-bark"}`},
		{"telegram", `{"chat_id":"42","api_url":"http://127.0.0.1:1"}`, `{"token":"private-telegram"}`},
		{"ntfy", `{}`, `{"url":"http://127.0.0.1:1/private-topic","token":"private-ntfy"}`},
		{"gotify", `{"url":"http://127.0.0.1:1"}`, `{"token":"private-gotify"}`},
		{"wecom", `{}`, `{"url":"http://127.0.0.1:1/private-wecom"}`},
		{"dingtalk", `{}`, `{"url":"http://127.0.0.1:1/private-dingtalk","secret":"private-sign"}`},
		{"feishu", `{}`, `{"url":"http://127.0.0.1:1/private-feishu","secret":"private-sign"}`},
		{"pushplus", `{"topic":"family"}`, `{"token":"private-pushplus"}`},
		{"serverchan", `{}`, `{"send_key":"private-serverchan"}`},
		{"webhook", `{"method":"POST","content_type":"JSON","body_template":{"number":9007199254740993}}`, `{"url":"http://127.0.0.1:1/private-hook","headers":{"Authorization":"private-header"}}`},
		{"email", `{"host":"127.0.0.1","port":587,"encryption":"starttls","username":"sender","from":"from@example.invalid","to":"to@example.invalid"}`, `{"password":"private-password"}`},
	} {
		t.Run(sample.kind, func(t *testing.T) {
			channel := createHTTPChannel(t, f, sample.kind, sample.config, sample.secrets)
			if channel.ID < 1 || !channel.Enabled || string(channel.Type) != sample.kind {
				t.Fatal("真实保存的渠道或默认启用状态不正确")
			}
			path := fmt.Sprintf("/api/v1/channels/%d", channel.ID)
			for _, response := range []*httptest.ResponseRecorder{
				f.request(http.MethodGet, path, "", true),
				f.request(http.MethodPatch, path, `{"name":"已改名","enabled":false}`, true),
				f.request(http.MethodPatch, path, `{"secrets":{}}`, true),
			} {
				requireStatus(t, response, http.StatusOK)
				assertChannelResponseSafe(t, response)
				value := decodeBody[notification.Channel](t, response)
				for key, present := range channel.SecretSet {
					if !present || !value.SecretSet[key] {
						t.Fatalf("缺失秘密字段必须保持：%s", key)
					}
				}
			}
			var uuid, public string
			var encrypted []byte
			if err := f.store.DB().QueryRowContext(t.Context(), `SELECT uuid,config_json,secret_enc FROM channels WHERE id=?`, channel.ID).Scan(&uuid, &public, &encrypted); err != nil {
				t.Fatal(err)
			}
			if len(encrypted) == 0 || bytes.Contains(encrypted, []byte("private-")) || strings.Contains(public, "private-") {
				t.Fatal("秘密必须加密保存，不能混进公开配置")
			}
			plain, err := f.cipher.Open(encrypted, "channels", "secret_enc", uuid)
			if err != nil || !bytes.Contains(plain, []byte("private-")) {
				t.Fatal("省略秘密后未保留真实加密值")
			}
			clear(plain)
			if sample.kind == "webhook" && !strings.Contains(public, "9007199254740993") {
				t.Fatal("Webhook 原始配置中的大整数丢失了精度")
			}
			if sample.kind == "ntfy" || sample.kind == "webhook" {
				body, key := `{"secrets":{"token":""}}`, "token"
				if sample.kind == "webhook" {
					body, key = `{"secrets":{"headers":{}}}`, "headers"
				}
				cleared := f.request(http.MethodPatch, path, body, true)
				requireStatus(t, cleared, http.StatusOK)
				value := decodeBody[notification.Channel](t, cleared)
				if value.SecretSet[key] || !value.SecretSet["url"] || value.Enabled {
					t.Fatal("可选秘密显式空值必须清除，其余值和false必须保留")
				}
			}
			channels = append(channels, channel)
		})
	}
	listed := f.request(http.MethodGet, "/api/v1/channels", "", true)
	requireStatus(t, listed, http.StatusOK)
	assertChannelResponseSafe(t, listed)
	if len(decodeBody[[]notification.Channel](t, listed)) != len(channels) {
		t.Fatal("渠道列表未反映全部真实保存行")
	}
	for _, channel := range channels {
		deleted := f.request(http.MethodDelete, fmt.Sprintf("/api/v1/channels/%d", channel.ID), "", true)
		requireStatus(t, deleted, http.StatusOK)
		if result := decodeBody[notification.ChannelDeleteResult](t, deleted); result.ID != channel.ID || result.AffectedTaskIDs == nil || len(result.AffectedTaskIDs) != 0 {
			t.Fatal("删除必须返回真实编号和受影响任务数组")
		}
	}
	if transport.calls.Load() != 0 {
		t.Fatal("渠道增删改查不能调用发送器")
	}
}

func TestBusinessNotificationsRoutesAuthenticationAndInputs(t *testing.T) {
	f, notifications := newNotificationHTTPFixture(t, nil)
	channel := createHTTPChannel(t, f, "ntfy", `{}`, `{"url":"http://127.0.0.1:1/private-topic","token":"private-token"}`)
	paths := map[string]bool{}
	for _, route := range notificationRoutes(f.server, notifications) {
		method, path, _ := strings.Cut(route.pattern, " ")
		path = sampleRoutePath(path)
		probe := &http.Request{Method: method, URL: &url.URL{Path: path}}
		if _, registered := f.server.mux.Handler(probe); registered != route.pattern {
			t.Fatalf("实际注册与通知路由不符：%s", route.pattern)
		}
		paths[method+" "+strings.TrimPrefix(route.pattern, method+" ")] = false
		requireStatus(t, f.request(method, path, "", false), http.StatusUnauthorized)
		requireStatus(t, f.request(method, path+"?unrecognized=private-query", "", true), http.StatusBadRequest)
		if method != http.MethodGet {
			requireStatus(t, f.request(method, path, "", true, func(r *http.Request) { r.Header.Set("Origin", "https://attacker.invalid") }), http.StatusForbidden)
		}
		if method != http.MethodPatch && !(method == http.MethodPost && path == "/api/v1/channels") {
			requireStatus(t, f.request(method, path, `{}`, true), http.StatusBadRequest)
		}
	}
	for _, operation := range testAPIOperations(t) {
		key := operation.Method + " " + operation.Path
		if _, exists := paths[key]; exists {
			if len(operation.Security) == 0 || operation.ImplementationStatus == "contract" {
				t.Fatalf("通知接口必须明确鉴权且已经接线：%s", key)
			}
			paths[key] = true
		} else if strings.HasPrefix(operation.Path, "/api/v1/channels") || strings.HasPrefix(operation.Path, "/api/v1/deliveries/") {
			t.Fatalf("通知契约缺少实际路由：%s", key)
		}
	}
	for path, described := range paths {
		if !described {
			t.Fatalf("实际通知接口缺少契约：%s", path)
		}
	}
	if err := f.server.RegisterNotifications(nil); err == nil {
		t.Fatal("缺少通知服务不能装配")
	}
	if err := f.server.RegisterNotifications(notifications); err == nil {
		t.Fatal("重复装配通知路由必须拒绝")
	}
	path := fmt.Sprintf("/api/v1/channels/%d", channel.ID)
	for _, invalid := range []struct {
		body   string
		status int
	}{
		{`null`, 400}, {`{"name":"a","name":"b"}`, 400}, {`{"secrets":{"token":"a","token":"b"}}`, 400},
		{`{"name":"a"}{}`, 400}, {"{\"name\":\"\xff\"}", 400},
		{`{"name":null}`, 422}, {`{"enabled":null}`, 422}, {`{"config":null}`, 422}, {`{"secrets":null}`, 422},
		{`{"secrets":{"token":null}}`, 422}, {`{"secrets":{"url":""}}`, 422}, {`{"uuid":"private-forged"}`, 422},
		{`{"name":"` + strings.Repeat("x", maxBody) + `"}`, 413},
	} {
		response := f.request(http.MethodPatch, path, invalid.body, true)
		requireStatus(t, response, invalid.status)
		if bytes.Contains(response.Body.Bytes(), []byte("private-")) {
			t.Fatal("非法补丁错误不能回显秘密内容")
		}
		if error := decodeBody[ErrorEnvelope](t, response); error.RequestID == "" || error.Error.Code == "" {
			t.Fatal("输入错误必须返回统一且可追踪的原因")
		}
	}
	requireStatus(t, f.request(http.MethodPatch, path, `{}`, true, func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }), http.StatusUnsupportedMediaType)
	for _, path := range []string{"0", "-1", "90071992547409930x", "9223372036854775808"} {
		requireStatus(t, f.request(http.MethodGet, "/api/v1/channels/"+path, "", true), http.StatusBadRequest)
	}
	for _, path := range []string{"?a=one&a=two", "?%ff=secret", "?a=%zz"} {
		requireStatus(t, f.request(http.MethodGet, "/api/v1/channels"+path, "", true), http.StatusBadRequest)
	}
	for _, route := range []struct{ method, path string }{
		{"GET", "/api/v1/channels/999"}, {"PATCH", "/api/v1/channels/999"}, {"DELETE", "/api/v1/channels/999"},
		{"POST", "/api/v1/channels/999/test"}, {"GET", "/api/v1/channels/999/deliveries"}, {"GET", "/api/v1/deliveries/999/items"},
	} {
		body := ""
		if route.method == http.MethodPatch {
			body = `{}`
		}
		requireStatus(t, f.request(route.method, route.path, body, true), http.StatusNotFound)
	}
	var saved int
	if err := f.store.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM channels`).Scan(&saved); err != nil || saved != 1 {
		t.Fatal("被拒绝的请求不能改写渠道数量")
	}
	if _, err := f.store.DB().ExecContext(t.Context(), `CREATE TRIGGER reject_channel_insert BEFORE INSERT ON channels BEGIN SELECT RAISE(ABORT,'private-storage-marker'); END`); err != nil {
		t.Fatal(err)
	}
	failed := f.request(http.MethodPost, "/api/v1/channels", `{"type":"bark","name":"失败存储","secrets":{"url":"http://127.0.0.1:1/private-url"}}`, true)
	requireStatus(t, failed, http.StatusServiceUnavailable)
	assertChannelResponseSafe(t, failed)
}

func TestBusinessNotificationsTestUsesSavedSampleAndLocalSender(t *testing.T) {
	type sample struct{ title, body, image, link, authorization string }
	messages := make(chan sample, 2)
	var status atomic.Int32
	status.Store(http.StatusOK)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		title, err := new(mime.WordDecoder).DecodeHeader(r.Header.Get("Title"))
		if err != nil {
			t.Error(err)
		}
		messages <- sample{title, string(body), r.Header.Get("Attach"), r.Header.Get("Click"), r.Header.Get("Authorization")}
		w.WriteHeader(int(status.Load()))
		_, _ = w.Write([]byte("private-provider-response"))
	}))
	defer backend.Close()
	f, _ := newNotificationHTTPFixture(t, notify.New(notify.Dependencies{HTTP: backend.Client()}))
	secrets, _ := json.Marshal(map[string]string{"url": backend.URL + "/private-topic", "token": "private-token"})
	channel := createHTTPChannel(t, f, "ntfy", `{}`, string(secrets))
	path := fmt.Sprintf("/api/v1/channels/%d", channel.ID)
	for index, expected := range []bool{true, false} {
		if !expected {
			status.Store(http.StatusUnauthorized)
		}
		response := f.request(http.MethodPost, path+"/test", "", true)
		requireStatus(t, response, http.StatusOK)
		assertChannelResponseSafe(t, response)
		result := decodeBody[notification.ChannelTestResult](t, response)
		if result.OK != expected || result.ChannelID != channel.ID || result.DeliveryID != int64(index+1) || result.At != f.clock.Load() || (result.Error == nil) != expected {
			t.Fatalf("测试结果没有对应真实发送与持久回执：%+v", result)
		}
		select {
		case sent := <-messages:
			if sent.title != "Kakatta 测试通知" || sent.body != "这是一条固定示例，用来确认推送渠道能否收到通知。" || sent.image == "" || sent.link != "https://www.goofish.com/" || sent.authorization != "Bearer private-token" {
				t.Fatal("实际发送必须使用保存的配置和固定示例，不替换成请求参数")
			}
		default:
			t.Fatal("接口未经过真实本地 HTTP 发送")
		}
		viewResponse := f.request(http.MethodGet, path, "", true)
		requireStatus(t, viewResponse, http.StatusOK)
		view := decodeBody[notification.Channel](t, viewResponse)
		if view.LastTestAt == nil || *view.LastTestAt != result.At || view.LastTestOK == nil || *view.LastTestOK != expected {
			t.Fatal("渠道测试状态没有保存")
		}
		items := f.request(http.MethodGet, fmt.Sprintf("/api/v1/deliveries/%d/items", result.DeliveryID), "", true)
		requireStatus(t, items, http.StatusOK)
		if strings.TrimSpace(items.Body.String()) != "[]" {
			t.Fatal("测试通知没有商品关联")
		}
	}
	requireStatus(t, f.request(http.MethodPost, path+"/test", `{"body":"private-custom-message"}`, true), http.StatusBadRequest)
	requireStatus(t, f.request(http.MethodPost, path+"/test?title=private-custom-message", "", true), http.StatusBadRequest)
	if len(messages) != 0 {
		t.Fatal("不支持的自定义测试输入不能触发发送")
	}
	history := f.request(http.MethodGet, path+"/deliveries", "", true)
	requireStatus(t, history, http.StatusOK)
	values := decodeBody[[]service.DeliverySummary](t, history)
	if len(values) != 2 || values[0].ID != 2 || values[0].Error == nil || values[1].ID != 1 || values[1].Error != nil {
		t.Fatal("发送历史必须反映真实成功和失败，不能暴露服务端响应正文")
	}
	assertChannelResponseSafe(t, history)
}

func TestBusinessNotificationsDeleteKeepsHistoryAndCompleteAssociations(t *testing.T) {
	f, _ := newNotificationHTTPFixture(t, nil)
	first := createHTTPChannel(t, f, "ntfy", `{}`, `{"url":"http://127.0.0.1:1/private-first"}`)
	second := createHTTPChannel(t, f, "bark", `{}`, `{"url":"http://127.0.0.1:1/private-second"}`)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := f.store.DB().ExecContext(t.Context(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []int{2, 1} {
		exec(`INSERT INTO tasks(id,name,keyword,enabled,max_pages,interval_minutes,judge_mode,notify_policy,sort_index) VALUES(?,'任务','相机',0,1,30,'keyword','recommended',0)`, id)
		exec(`INSERT INTO task_channels(task_id,channel_id) VALUES(?,?)`, id, first.ID)
	}
	exec(`INSERT INTO task_channels(task_id,channel_id) VALUES(1,?)`, second.ID)
	for id := 1; id <= 25; id++ {
		exec(`INSERT INTO deliveries(id,at,channel_id,task_id,kind,item_count,ok) VALUES(?,100,?,1,'summary',205,1)`, id, first.ID)
	}
	exec(`INSERT INTO deliveries(at,channel_id,kind,item_count,ok) VALUES(NULL,?,'system',0,1)`, first.ID)
	exec(`INSERT INTO listings(item_id,title,desktop_url,first_seen_at,last_seen_at) VALUES('本地商品','保留标题','http://localhost/item',1,1)`)
	exec(`INSERT INTO findings(id,task_id,item_id,search_generation,found_at,is_baseline,decision,stage,is_read) VALUES(1,1,'本地商品',1,1,0,'matched','done',0)`)
	tx, err := f.store.DB().BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for id := 1; id <= 205; id++ {
		if _, err := tx.ExecContext(t.Context(), `INSERT INTO delivery_items(delivery_id,finding_id) VALUES(25,?)`, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/channels/%d", first.ID)
	history := f.request(http.MethodGet, path+"/deliveries", "", true)
	requireStatus(t, history, http.StatusOK)
	values := decodeBody[[]service.DeliverySummary](t, history)
	if len(values) != 20 {
		t.Fatal("渠道历史只返回最近二十条")
	}
	for index, item := range values {
		if item.ID != int64(25-index) || item.ItemCount == nil || *item.ItemCount != 205 {
			t.Fatal("历史排序或真实合并条数被改写")
		}
	}
	checkItems := func() {
		t.Helper()
		response := f.request(http.MethodGet, "/api/v1/deliveries/25/items", "", true)
		requireStatus(t, response, http.StatusOK)
		items := decodeBody[[]notification.DeliveryItem](t, response)
		if len(items) != 205 || items[0].Title == nil || *items[0].Title != "保留标题" || items[1].Title != nil {
			t.Fatal("合并历史必须保留全部成员和已清理成员的空标题")
		}
		for index, item := range items {
			if item.FindingID != int64(index+1) || item.TaskID == nil || *item.TaskID != 1 {
				t.Fatal("完整关联的发现编号和任务不能丢失")
			}
		}
	}
	checkItems()
	setTargets := func(ids ...int64) {
		t.Helper()
		raw, _ := json.Marshal(ids)
		if _, err := f.settings.Apply(t.Context(), settings.Values{"notify.system_channel_ids": raw}); err != nil {
			t.Fatal(err)
		}
	}
	setTargets(first.ID)
	blocked := f.request(http.MethodDelete, path, "", true)
	requireStatus(t, blocked, http.StatusConflict)
	if errorCode(blocked) != "channels.system_target_required" {
		t.Fatal("不能删除最后一个明确指定的系统渠道")
	}
	requireStatus(t, f.request(http.MethodGet, path, "", true), http.StatusOK)
	setTargets(first.ID, second.ID)
	deleted := f.request(http.MethodDelete, path, "", true)
	requireStatus(t, deleted, http.StatusOK)
	result := decodeBody[notification.ChannelDeleteResult](t, deleted)
	if result.ID != first.ID || !slices.Equal(result.AffectedTaskIDs, []int64{1, 2}) {
		t.Fatal("删除必须回传全部受影响任务的真实顺序")
	}
	requireStatus(t, f.request(http.MethodGet, path, "", true), http.StatusNotFound)
	var references int
	if err := f.store.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM task_channels WHERE channel_id=?`, first.ID).Scan(&references); err != nil || references != 0 {
		t.Fatal("删除渠道必须清除任务引用")
	}
	snapshot, err := f.settings.Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var targets []int64
	if err := json.Unmarshal(snapshot.Values["notify.system_channel_ids"], &targets); err != nil || !slices.Equal(targets, []int64{second.ID}) {
		t.Fatal("删除渠道必须同步清除设置引用，保留另一个明确目标")
	}
	checkItems()
	requireStatus(t, f.request(http.MethodDelete, fmt.Sprintf("/api/v1/channels/%d", second.ID), "", true), http.StatusConflict)
}
