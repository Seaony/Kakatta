package notification

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/notify"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/store"
)

func channelsService(t *testing.T) *Service {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "channels.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	preferences, err := appsettings.New(t.Context(), db.DB(), settings.Environment{TZ: "UTC"}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Dependencies{DB: db.DB(), Settings: preferences, Cipher: channelCipher(t), Sender: notify.New(notify.Dependencies{}), Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := s.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return s
}

func channelsCreate(t *testing.T, s *Service, kind, name, address string) Channel {
	t.Helper()
	patch := ChannelPatch{}
	for key, value := range map[string]any{"type": kind, "name": name, "secrets": map[string]string{"url": address}} {
		patch[key], _ = json.Marshal(value)
	}
	channel, err := s.CreateChannel(t.Context(), patch)
	if err != nil {
		t.Fatal(err)
	}
	return channel
}

func channelsCount(t *testing.T, db *sql.DB, query string, args ...any) int64 {
	t.Helper()
	var count int64
	if err := db.QueryRowContext(t.Context(), query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func channelsWait[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("等待渠道操作超时")
		var zero T
		return zero
	}
}

func TestChannelsCRUDDoesNotSendAndDuplicateRollsBack(t *testing.T) {
	s := channelsService(t)
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer server.Close()
	first := channelsCreate(t, s, "ntfy", "家人", server.URL+"/private-first")
	second := channelsCreate(t, s, "ntfy", "朋友", server.URL+"/private-second")
	_, err := s.CreateChannel(t.Context(), channelPatch(`{"type":"ntfy","name":"家人","secrets":{"url":"https://example.invalid/private-third"}}`))
	channelProblem(t, err, service.ErrorInvalid, "channels.name_in_use")
	previous, err := s.loadSavedChannel(t.Context(), second.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.UpdateChannel(t.Context(), second.ID, channelPatch(`{"name":"家人","enabled":false,"secrets":{"url":"https://example.invalid/should-not-save"}}`))
	channelProblem(t, err, service.ErrorInvalid, "channels.name_in_use")
	current, err := s.loadSavedChannel(t.Context(), second.ID)
	if err != nil || !sameChannelConfiguration(previous, current) {
		t.Fatalf("重名更新没有回滚: %v", err)
	}
	view, err := s.UpdateChannel(t.Context(), first.ID, channelPatch(`{"enabled":false,"secrets":{"token":""}}`))
	if err != nil || view.Enabled || !view.SecretSet["url"] || view.SecretSet["token"] {
		t.Fatalf("更新结果不符: %+v, %v", view, err)
	}
	views, err := s.ListChannels(t.Context())
	if err != nil || len(views) != 2 || requests.Load() != 0 {
		t.Fatalf("渠道保存意外外发或列表不符: %d, %d, %v", len(views), requests.Load(), err)
	}
	encoded, err := json.Marshal(views)
	if err != nil || bytes.Contains(encoded, []byte("private-")) || bytes.Contains(encoded, []byte("secret_enc")) || bytes.Contains(encoded, []byte("uuid")) {
		t.Fatalf("列表泄露敏感内容: %s, %v", encoded, err)
	}
	_, err = s.GetChannel(t.Context(), 999)
	channelProblem(t, err, service.ErrorNotFound, "channels.not_found")
}

func TestChannelDeliveriesShowsLatestTwentyAndPreservesCount(t *testing.T) {
	s := channelsService(t)
	channel := channelsCreate(t, s, "ntfy", "家人", "https://example.invalid/private")
	for i := 1; i <= 25; i++ {
		if _, err := s.db.ExecContext(t.Context(), `INSERT INTO deliveries(at,channel_id,task_id,kind,item_count,ok,error) VALUES(100,?,7,'summary',?,1,NULL)`, channel.ID, i); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.ExecContext(t.Context(), `INSERT INTO deliveries(at,channel_id,kind,item_count,ok) VALUES(NULL,?,'test',0,1)`, channel.ID); err != nil {
		t.Fatal(err)
	}
	items, err := s.ChannelDeliveries(t.Context(), channel.ID)
	if err != nil || len(items) != 20 {
		t.Fatalf("历史条数不符: %d, %v", len(items), err)
	}
	for index, item := range items {
		want := int64(25 - index)
		if item.ID != want || item.ItemCount == nil || *item.ItemCount != want || item.ChannelName == nil || *item.ChannelName != "家人" || item.Kind == nil || *item.Kind != "summary" {
			t.Fatalf("历史顺序或合并数不符: %+v", item)
		}
	}
	_, err = s.ChannelDeliveries(t.Context(), 999)
	channelProblem(t, err, service.ErrorNotFound, "channels.not_found")
}

func TestChannelTestUsesSavedSampleAndDoesNotOverwriteNewConfiguration(t *testing.T) {
	s := channelsService(t)
	type sample struct{ title, body, image, link string }
	started := make(chan sample, 1)
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body bytes.Buffer
		_, _ = body.ReadFrom(r.Body)
		title, _ := new(mime.WordDecoder).DecodeHeader(r.Header.Get("Title"))
		started <- sample{title: title, body: body.String(), image: r.Header.Get("Attach"), link: r.Header.Get("Click")}
		<-release
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("private-provider-response https://secret.invalid/token"))
	}))
	defer server.Close()
	defer unblock()
	channel := channelsCreate(t, s, "ntfy", "原渠道", server.URL+"/private-old")
	type outcome struct {
		value ChannelTestResult
		err   error
	}
	finished := make(chan outcome, 1)
	go func() { value, err := s.TestChannel(t.Context(), channel.ID); finished <- outcome{value, err} }()
	message := channelsWait(t, started)
	if !strings.Contains(message.title, "Kakatta") || message.body == "" || message.image == "" || message.link != "https://www.goofish.com/" || strings.Contains(message.body, "private-old") {
		t.Fatalf("测试消息不符: %+v", message)
	}
	_, err := s.UpdateChannel(t.Context(), channel.ID, channelPatch(`{"name":"新渠道","secrets":{"url":"https://example.invalid/private-new"}}`))
	if err != nil {
		t.Fatal(err)
	}
	unblock()
	got := channelsWait(t, finished)
	if got.err != nil || got.value.OK || got.value.DeliveryID == 0 || got.value.Error == nil {
		t.Fatalf("测试结果不符: %+v, %v", got.value, got.err)
	}
	raw, _ := json.Marshal(got.value)
	if bytes.Contains(raw, []byte("private-")) || bytes.Contains(raw, []byte("secret.invalid")) {
		t.Fatalf("错误泄露响应或地址: %s", raw)
	}
	view, err := s.GetChannel(t.Context(), channel.ID)
	if err != nil || view.LastTestAt != nil || view.LastTestOK != nil || view.LastError != nil {
		t.Fatalf("旧配置测试覆盖了新配置状态: %+v, %v", view, err)
	}
	if channelsCount(t, s.db, `SELECT COUNT(*) FROM deliveries WHERE channel_id=? AND kind='test' AND item_count=0 AND ok=0`, channel.ID) != 1 {
		t.Fatal("测试投递没有保存")
	}
}

func TestChannelTestRereadsConfigurationAfterQueue(t *testing.T) {
	s := channelsService(t)
	var oldCalls, newCalls atomic.Int64
	oldServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { oldCalls.Add(1) }))
	defer oldServer.Close()
	newServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { newCalls.Add(1) }))
	defer newServer.Close()
	channel := channelsCreate(t, s, "ntfy", "家人", oldServer.URL+"/old")
	record, err := s.loadSavedChannel(t.Context(), channel.ID)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := s.queues.acquire(t.Context(), channelIdentity{ID: channel.ID, UUID: record.UUID})
	if err != nil {
		t.Fatal(err)
	}
	releaseTurn := sync.OnceFunc(func() { turn.finish(notify.Ntfy, false, time.Time{}) })
	defer releaseTurn()
	finished := make(chan error, 1)
	go func() { _, err := s.TestChannel(t.Context(), channel.ID); finished <- err }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		s.queues.mu.Lock()
		users := turn.lane.users
		s.queues.mu.Unlock()
		if users == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("测试未进入队列")
		}
		time.Sleep(time.Millisecond)
	}
	patch := ChannelPatch{"secrets": json.RawMessage(`{"url":"` + newServer.URL + `/new"}`)}
	if _, err := s.UpdateChannel(t.Context(), channel.ID, patch); err != nil {
		t.Fatal(err)
	}
	releaseTurn()
	if err := channelsWait(t, finished); err != nil {
		t.Fatal(err)
	}
	if oldCalls.Load() != 0 || newCalls.Load() != 1 {
		t.Fatalf("排队时缓存了旧配置: %d, %d", oldCalls.Load(), newCalls.Load())
	}
	view, err := s.GetChannel(t.Context(), channel.ID)
	if err != nil || view.LastTestOK == nil || !*view.LastTestOK || view.LastTestAt == nil {
		t.Fatalf("当前配置测试结果未保存: %+v, %v", view, err)
	}
}

func TestChannelTestRereadsEnabledAfterInterval(t *testing.T) {
	s := channelsService(t)
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer server.Close()
	channel := channelsCreate(t, s, "wecom", "家人", server.URL+"/hook")
	waiting := make(chan struct{}, 1)
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	s.queues.mu.Lock()
	s.queues.waitFor = func(ctx context.Context, delay time.Duration) error {
		waiting <- struct{}{}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-release:
			s.queues.mu.Lock()
			s.queues.startedAt = time.Now().Add(-time.Minute)
			s.queues.mu.Unlock()
			return nil
		}
	}
	s.queues.mu.Unlock()
	finished := make(chan error, 1)
	go func() { _, err := s.TestChannel(t.Context(), channel.ID); finished <- err }()
	channelsWait(t, waiting)
	if _, err := s.UpdateChannel(t.Context(), channel.ID, channelPatch(`{"enabled":false}`)); err != nil {
		t.Fatal(err)
	}
	unblock()
	channelProblem(t, channelsWait(t, finished), service.ErrorConflict, "channels.disabled")
	if requests.Load() != 0 || channelsCount(t, s.db, `SELECT COUNT(*) FROM deliveries`) != 0 {
		t.Fatal("停用后仍然发送或写了投递")
	}
}

func TestDeleteChannelCancelsSendAndCleansReferencesAtomically(t *testing.T) {
	s := channelsService(t)
	started := make(chan struct{}, 1)
	releaseHandler := make(chan struct{})
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		started <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-releaseHandler:
		}
	}))
	defer server.Close()
	defer close(releaseHandler)
	defer s.cancel()
	channel := channelsCreate(t, s, "ntfy", "家人", server.URL+"/private")
	other := channelsCreate(t, s, "ntfy", "朋友", server.URL+"/other")
	for _, id := range []int64{2, 1} {
		if _, err := s.db.ExecContext(t.Context(), `INSERT INTO tasks(id,name,keyword,max_pages,interval_minutes,judge_mode,notify_policy,sort_index) VALUES(?,'任务','相机',1,30,'keyword','recommended',0)`, id); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.ExecContext(t.Context(), `INSERT INTO task_channels(task_id,channel_id) VALUES(?,?)`, id, channel.ID); err != nil {
			t.Fatal(err)
		}
	}
	ids, _ := json.Marshal([]int64{channel.ID, other.ID})
	if _, err := s.settings.Apply(t.Context(), settings.Values{"notify.system_channel_ids": ids}); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 2)
	go func() { _, err := s.TestChannel(t.Context(), channel.ID); finished <- err }()
	channelsWait(t, started)
	go func() { _, err := s.TestChannel(t.Context(), channel.ID); finished <- err }()
	record, err := s.loadSavedChannel(t.Context(), channel.ID)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		s.queues.mu.Lock()
		lane := s.queues.lanes[channelIdentity{ID: channel.ID, UUID: record.UUID}]
		users := lane.users
		s.queues.mu.Unlock()
		if users == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("第二次测试未进入队列")
		}
		time.Sleep(time.Millisecond)
	}
	result, err := s.DeleteChannel(t.Context(), channel.ID)
	if err != nil || !reflect.DeepEqual(result.AffectedTaskIDs, []int64{1, 2}) {
		t.Fatalf("删除结果不符: %+v, %v", result, err)
	}
	if err := channelsWait(t, finished); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := channelsWait(t, finished); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if requests.Load() != 1 {
		t.Fatalf("删除后队列又发了请求: %d", requests.Load())
	}
	if channelsCount(t, s.db, `SELECT COUNT(*) FROM channels WHERE id=?`, channel.ID) != 0 || channelsCount(t, s.db, `SELECT COUNT(*) FROM task_channels WHERE channel_id=?`, channel.ID) != 0 {
		t.Fatal("删除引用没有清理")
	}
	snapshot, err := s.settings.Get(t.Context())
	if err != nil || string(snapshot.Values["notify.system_channel_ids"]) != string(mustChannelJSON([]int64{other.ID})) {
		t.Fatalf("系统渠道引用没有清理: %s, %v", snapshot.Values["notify.system_channel_ids"], err)
	}
	if channelsCount(t, s.db, `SELECT COUNT(*) FROM deliveries WHERE channel_id=? AND kind='test'`, channel.ID) != 1 {
		t.Fatal("删除前取消的发送结果没有结算")
	}
}

func mustChannelJSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
}

func TestDeleteChannelRollsBackReferencesAndResumesAdmission(t *testing.T) {
	s := channelsService(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	channel := channelsCreate(t, s, "ntfy", "家人", server.URL)
	other := channelsCreate(t, s, "ntfy", "朋友", server.URL)
	if _, err := s.db.ExecContext(t.Context(), `INSERT INTO tasks(id,name,keyword,max_pages,interval_minutes,judge_mode,notify_policy,sort_index) VALUES(1,'任务','相机',1,30,'keyword','recommended',0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), `INSERT INTO task_channels(task_id,channel_id) VALUES(1,?)`, channel.ID); err != nil {
		t.Fatal(err)
	}
	ids := mustChannelJSON([]int64{channel.ID, other.ID})
	if _, err := s.settings.Apply(t.Context(), settings.Values{"notify.system_channel_ids": ids}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), `CREATE TRIGGER reject_channel_delete BEFORE DELETE ON channels BEGIN SELECT RAISE(ABORT,'拒绝删除'); END`); err != nil {
		t.Fatal(err)
	}
	_, err := s.DeleteChannel(t.Context(), channel.ID)
	channelProblem(t, err, service.ErrorUnavailable, "notification.storage_failure")
	if channelsCount(t, s.db, `SELECT COUNT(*) FROM task_channels WHERE channel_id=?`, channel.ID) != 1 {
		t.Fatal("删除失败丢失任务引用")
	}
	snapshot, err := s.settings.Get(t.Context())
	if err != nil || !bytes.Equal(snapshot.Values["notify.system_channel_ids"], ids) {
		t.Fatalf("删除失败没有回滚系统引用: %s, %v", snapshot.Values["notify.system_channel_ids"], err)
	}
	value, err := s.TestChannel(t.Context(), channel.ID)
	if err != nil || !value.OK {
		t.Fatalf("删除失败未恢复接纳: %+v, %v", value, err)
	}
}

func TestChannelTestReceiptRecoveryDoesNotDuplicateCommittedDelivery(t *testing.T) {
	s := channelsService(t)
	channel := channelsCreate(t, s, "ntfy", "家人", "https://example.invalid/not-sent")
	record, err := s.loadSavedChannel(t.Context(), channel.ID)
	if err != nil {
		t.Fatal(err)
	}
	at := time.UnixMilli(1000)
	var deliveryID int64
	var calls int
	var firstID int64
	err = s.saveReceipt(t.Context(), func(ctx context.Context) error {
		calls++
		view, err := s.recordChannelTest(ctx, record, at, notify.Result{OK: true}, &deliveryID)
		if err != nil {
			return err
		}
		if calls == 1 {
			firstID = view.DeliveryID
			return errors.New("模拟提交结果丢失")
		}
		if view.DeliveryID != firstID {
			return errors.New("补记换了投递编号")
		}
		return nil
	})
	channelProblem(t, err, service.ErrorUnavailable, "notification.storage_failure")
	if firstID == 0 || deliveryID != firstID || channelsCount(t, s.db, `SELECT COUNT(*) FROM deliveries WHERE channel_id=?`, channel.ID) != 1 {
		t.Fatal("首次收据没有保存稳定投递编号")
	}
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || channelsCount(t, s.db, `SELECT COUNT(*) FROM deliveries WHERE channel_id=?`, channel.ID) != 1 {
		t.Fatalf("收据补记重复写了历史: %d", calls)
	}
}

func TestDeleteChannelKeepsLastExplicitSystemTargetAndResumesAdmission(t *testing.T) {
	s := channelsService(t)
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer server.Close()
	channel := channelsCreate(t, s, "ntfy", "家人", server.URL)
	channelsCreate(t, s, "ntfy", "朋友", server.URL)
	if _, err := s.db.ExecContext(t.Context(), `INSERT INTO tasks(id,name,keyword,max_pages,interval_minutes,judge_mode,notify_policy,sort_index) VALUES(1,'任务','相机',1,30,'keyword','recommended',0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), `INSERT INTO task_channels(task_id,channel_id) VALUES(1,?)`, channel.ID); err != nil {
		t.Fatal(err)
	}
	ids := mustChannelJSON([]int64{channel.ID})
	if _, err := s.settings.Apply(t.Context(), settings.Values{"notify.system_channel_ids": ids}); err != nil {
		t.Fatal(err)
	}
	_, err := s.DeleteChannel(t.Context(), channel.ID)
	channelProblem(t, err, service.ErrorConflict, "channels.system_target_required")
	var problem *service.ServiceError
	if !errors.As(err, &problem) || len(problem.Reason.Args) != 0 {
		t.Fatalf("删除冲突原因参数不符: %v", err)
	}
	for _, translation := range []struct {
		language locales.Language
		text     string
	}{
		{locales.ZhCN, "请先修改系统推送渠道，再删除这个渠道"},
		{locales.En, "Change the system notification channels before deleting this channel"},
	} {
		text, err := locales.Format(translation.language, problem.Reason)
		if err != nil || text != translation.text {
			t.Fatalf("删除冲突文案不符: %s, %q, %v", translation.language, text, err)
		}
	}
	if requests.Load() != 0 || channelsCount(t, s.db, `SELECT COUNT(*) FROM channels WHERE id=?`, channel.ID) != 1 ||
		channelsCount(t, s.db, `SELECT COUNT(*) FROM task_channels WHERE channel_id=?`, channel.ID) != 1 {
		t.Fatal("删除被拒绝仍然改了渠道或任务引用")
	}
	snapshot, err := s.settings.Get(t.Context())
	if err != nil || !bytes.Equal(snapshot.Values["notify.system_channel_ids"], ids) {
		t.Fatalf("删除被拒绝改了系统推送范围: %s, %v", snapshot.Values["notify.system_channel_ids"], err)
	}
	value, err := s.TestChannel(t.Context(), channel.ID)
	if err != nil || !value.OK || requests.Load() != 1 {
		t.Fatalf("删除被拒绝没有恢复渠道接纳: %+v, %v", value, err)
	}
}

func TestDeliveryItemsReturnsAllMembersAndKeepsDeletedFinding(t *testing.T) {
	s := channelsService(t)
	if _, err := s.db.ExecContext(t.Context(), `INSERT INTO tasks(id,name,keyword,max_pages,interval_minutes,judge_mode,notify_policy,sort_index) VALUES(1,'任务','相机',1,30,'keyword','recommended',0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), `INSERT INTO deliveries(id,at,task_id,kind,item_count,ok) VALUES(1,100,77,'summary',13,0)`); err != nil {
		t.Fatal(err)
	}
	for id := int64(13); id >= 1; id-- {
		itemID := strconv.FormatInt(id, 10)
		if _, err := s.db.ExecContext(t.Context(), `INSERT INTO listings(item_id,title,desktop_url,first_seen_at,last_seen_at) VALUES(?,?,?,1,1)`, itemID, "商品"+itemID, "https://example.invalid/item"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.ExecContext(t.Context(), `INSERT INTO findings(id,task_id,item_id,search_generation,found_at,is_baseline,decision,stage,is_read) VALUES(?,1,?,1,1,0,'matched','done',0)`, id, itemID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.ExecContext(t.Context(), `INSERT INTO delivery_items(delivery_id,finding_id) VALUES(1,?)`, id); err != nil {
			t.Fatal(err)
		}
	}
	items, err := s.DeliveryItems(t.Context(), 1)
	if err != nil || len(items) != 13 {
		t.Fatalf("合并投递成员被截断: %d, %v", len(items), err)
	}
	for index, item := range items {
		id := int64(index + 1)
		if item.FindingID != id || item.TaskID == nil || *item.TaskID != 77 || item.Title == nil || *item.Title != "商品"+strconv.FormatInt(id, 10) {
			t.Fatalf("成员顺序或历史任务编号不符: %+v", item)
		}
	}
	if _, err := s.db.ExecContext(t.Context(), `DELETE FROM findings WHERE id=5`); err != nil {
		t.Fatal(err)
	}
	items, err = s.DeliveryItems(t.Context(), 1)
	if err != nil || len(items) != 13 || items[4].FindingID != 5 || items[4].Title != nil || items[4].TaskID == nil || *items[4].TaskID != 77 {
		t.Fatalf("清理发现后丢失投递成员: %+v, %v", items, err)
	}
	if channelsCount(t, s.db, `SELECT item_count FROM deliveries WHERE id=1`) != 13 || channelsCount(t, s.db, `SELECT COUNT(*) FROM delivery_items WHERE delivery_id=1`) != 13 ||
		channelsCount(t, s.db, `SELECT COUNT(*) FROM findings`) != 12 {
		t.Fatal("读取成员改了历史数量或复活了发现")
	}
}

func TestDeliveryItemsFallsBackToCurrentTaskAndHandlesEmptyOrMissingDelivery(t *testing.T) {
	s := channelsService(t)
	if _, err := s.db.ExecContext(t.Context(), `INSERT INTO tasks(id,name,keyword,max_pages,interval_minutes,judge_mode,notify_policy,sort_index) VALUES(1,'任务','相机',1,30,'keyword','recommended',0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), `INSERT INTO listings(item_id,title,desktop_url,first_seen_at,last_seen_at) VALUES('1','商品','https://example.invalid/item',1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), `INSERT INTO findings(id,task_id,item_id,search_generation,found_at,is_baseline,decision,stage,is_read) VALUES(1,1,'1',1,1,0,'matched','done',0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), `INSERT INTO deliveries(id,at,kind,item_count,ok) VALUES(1,100,'summary',1,0),(2,101,'system',0,0),(3,102,'test',0,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), `INSERT INTO delivery_items(delivery_id,finding_id) VALUES(1,1)`); err != nil {
		t.Fatal(err)
	}
	items, err := s.DeliveryItems(t.Context(), 1)
	if err != nil || len(items) != 1 || items[0].TaskID == nil || *items[0].TaskID != 1 {
		t.Fatalf("未从当前发现补任务编号: %+v, %v", items, err)
	}
	if _, err := s.db.ExecContext(t.Context(), `DELETE FROM tasks WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	items, err = s.DeliveryItems(t.Context(), 1)
	if err != nil || len(items) != 1 || items[0].FindingID != 1 || items[0].TaskID != nil || items[0].Title != nil {
		t.Fatalf("实体已清理时成员空值不符: %+v, %v", items, err)
	}
	for _, id := range []int64{2, 3} {
		items, err := s.DeliveryItems(t.Context(), id)
		if err != nil || items == nil || len(items) != 0 {
			t.Fatalf("系统或测试投递没有返回空列表: %+v, %v", items, err)
		}
	}
	_, err = s.DeliveryItems(t.Context(), 999)
	channelProblem(t, err, service.ErrorNotFound, "deliveries.not_found")
}
