package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/auth"
	"github.com/Seaony/Kakatta/internal/locales"
)

func TestEventHistoryReplayAndReset(t *testing.T) {
	b := newEventBroker()
	defer b.close()
	for number := 1; number <= 3; number++ {
		if err := b.publish(Event{Type: "test", Data: json.RawMessage(strconv.Itoa(number))}); err != nil {
			t.Fatal(err)
		}
	}
	initial, fresh, err := b.subscribe("")
	if err != nil || len(initial) != 0 {
		t.Fatal("首次连接不应重放过期业务事件")
	}
	b.unsubscribe(fresh)
	initial, resumed, err := b.subscribe(b.epoch + ".1")
	if err != nil || len(initial) != 2 || initial[0].id != b.epoch+".2" || initial[1].id != b.epoch+".3" {
		t.Fatal("合法游标重放不完整")
	}
	if err := b.publish(Event{Type: "test", Data: json.RawMessage(`4`)}); err != nil {
		t.Fatal(err)
	}
	if next := <-resumed.queue; next.id != b.epoch+".4" {
		t.Fatal("重放之后的新事件不能丢失")
	}
	b.unsubscribe(resumed)
	for _, cursor := range []string{"另一进程.1", b.epoch + ".999", b.epoch + ".01", "非法游标", b.epoch + ".-1"} {
		replay, subscriber, err := b.subscribe(cursor)
		if err != nil || len(replay) != 1 || replay[0].kind != "reset" || replay[0].id != b.id() {
			t.Fatal("无效或未来游标应通知重新取快照")
		}
		b.unsubscribe(subscriber)
	}
	for number := 0; number < eventHistory; number++ {
		if err := b.publish(Event{Type: "test"}); err != nil {
			t.Fatal(err)
		}
	}
	if len(b.history) != eventHistory {
		t.Fatal("重放历史未受上限控制")
	}
	old, subscriber, err := b.subscribe(b.epoch + ".1")
	if err != nil || len(old) != 1 || old[0].kind != "reset" {
		t.Fatal("过旧游标不应悄悄丢掉事件")
	}
	b.unsubscribe(subscriber)
	before := b.sequence - eventHistory
	boundary, subscriber, err := b.subscribe(b.epoch + "." + strconv.FormatUint(before, 10))
	if err != nil || len(boundary) != eventHistory {
		t.Fatal("最后一个可恢复游标应完整重放")
	}
	b.unsubscribe(subscriber)
}

func TestEventQueueBoundsAndClose(t *testing.T) {
	b := newEventBroker()
	_, subscriber, err := b.subscribe("")
	if err != nil {
		t.Fatal(err)
	}
	for number := 0; number <= eventQueue; number++ {
		if err := b.publish(Event{Type: "changed"}); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-subscriber.done:
	default:
		t.Fatal("慢客户端必须断开，不能静默丢消息")
	}
	if len(subscriber.queue) != eventQueue || len(b.subscribers) != 0 {
		t.Fatal("慢客户端队列必须有界并回收")
	}
	b.unsubscribe(subscriber)
	_, next, err := b.subscribe("")
	if err != nil {
		t.Fatal(err)
	}
	b.close()
	b.close()
	select {
	case <-next.done:
	default:
		t.Fatal("关闭事件流必须唤醒全部连接")
	}
	if err := b.publish(Event{Type: "changed"}); !errors.Is(err, errEventsClosed) {
		t.Fatal("关闭后不能发布")
	}
	if _, _, err := b.subscribe(""); !errors.Is(err, errEventsClosed) {
		t.Fatal("关闭后不能订阅")
	}
}

func TestEventValidationAndCopies(t *testing.T) {
	b := newEventBroker()
	defer b.close()
	for _, event := range []Event{
		{}, {Type: "bad\nevent"}, {Type: strings.Repeat("a", 65)},
		{Type: "test", Data: json.RawMessage(`{`)},
		{Type: "test", Data: json.RawMessage("\"\xff\"")},
		{Type: "test", Data: json.RawMessage(`"` + strings.Repeat("x", maxEventSize) + `"`)},
		{Type: "test", Reason: &locales.Message{Code: "test", Args: map[string]any{"bad": make(chan bool)}}},
	} {
		if err := b.publish(event); err == nil {
			t.Fatal("无效事件不应进入队列")
		}
	}
	data := json.RawMessage(`{"value":1}`)
	if err := b.publish(Event{Type: "test.changed", Data: data}); err != nil {
		t.Fatal(err)
	}
	data[len(data)-2] = '9'
	if strings.Contains(string(b.history[0].data), `"value":9`) {
		t.Fatal("调用方不能修改已发布的数据")
	}
	wire := encodeEvent(b.history[0])
	if !strings.HasPrefix(wire, "id: "+b.epoch+".1\nevent: test.changed\ndata: ") || !strings.HasSuffix(wire, "\n\n") {
		t.Fatal("事件流编码不符合协议")
	}
}

func TestEventPublishSubscribeConcurrency(t *testing.T) {
	b := newEventBroker()
	defer b.close()
	var jobs sync.WaitGroup
	for worker := 0; worker < 6; worker++ {
		jobs.Go(func() {
			for number := 0; number < 80; number++ {
				_, subscriber, err := b.subscribe("")
				if err != nil {
					t.Error(err)
					return
				}
				if err := b.publish(Event{Type: "changed"}); err != nil {
					t.Error(err)
				}
				b.unsubscribe(subscriber)
			}
		})
	}
	jobs.Wait()
	if b.sequence != 480 || len(b.subscribers) != 0 {
		t.Fatal("并发发布或订阅回收丢失")
	}
}

type readEvent struct {
	ID, Type, Data string
}

func readFrame(reader *bufio.Reader) (readEvent, error) {
	var event readEvent
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return readEvent{}, err
		}
		line = strings.TrimSuffix(line, "\n")
		if line == "" {
			return event, nil
		}
		switch {
		case strings.HasPrefix(line, "id: "):
			event.ID = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "event: "):
			event.Type = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			event.Data = strings.TrimPrefix(line, "data: ")
		}
	}
}

func connectEvents(t *testing.T, f *serverFixture, server *httptest.Server, cursor string) (*http.Response, *bufio.Reader) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	t.Cleanup(cancel)
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.AddCookie(&http.Cookie{Name: cookieName, Value: f.session.Token})
	if cursor != "" {
		r.Header.Set("Last-Event-ID", cursor)
	}
	response, err := server.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { response.Body.Close() })
	if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") || response.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatal("事件流响应不完整")
	}
	reader := bufio.NewReader(response.Body)
	if _, err := readFrame(reader); err != nil {
		t.Fatal(err)
	}
	return response, reader
}

func TestSSEPublishResumeResetAndRevoke(t *testing.T) {
	f := newServerFixture(t, true)
	server := httptest.NewServer(f.server)
	t.Cleanup(func() { f.server.Close(); server.Close() })
	first, reader := connectEvents(t, f, server, "")
	reason := locales.Message{Code: "test.changed"}
	if err := f.server.Publish(Event{Type: "account_state", Reason: &reason, Data: json.RawMessage(`{"account_id":1}`)}); err != nil {
		t.Fatal(err)
	}
	frame, err := readFrame(reader)
	if err != nil || frame.Type != "account_state" || frame.ID == "" || !strings.Contains(frame.Data, `"account_id":1`) || !strings.Contains(frame.Data, `"args":{}`) {
		t.Fatal("已发布事件未通过实际HTTP到达")
	}
	first.Body.Close()
	if err := f.server.Publish(Event{Type: "task_changed"}); err != nil {
		t.Fatal(err)
	}
	resumed, reader := connectEvents(t, f, server, frame.ID)
	next, err := readFrame(reader)
	if err != nil || next.Type != "task_changed" || next.ID == frame.ID {
		t.Fatal("HTTP断线恢复未重放后续事件")
	}
	resumed.Body.Close()
	reset, reader := connectEvents(t, f, server, "另一进程.7")
	next, err = readFrame(reader)
	if err != nil || next.Type != "reset" || !strings.Contains(next.Data, "events.reset") {
		t.Fatal("另一进程游标必须通知重取快照")
	}
	if err := f.auth.RevokeToken(t.Context(), f.session.Identity, f.session.Identity.TokenID); err != nil {
		t.Fatal(err)
	}
	if err := f.server.Publish(Event{Type: "secret_after_revoke"}); err != nil {
		t.Fatal(err)
	}
	if frame, err := readFrame(reader); err == nil || frame.Type == "secret_after_revoke" {
		t.Fatal("吊销的会话不能继续收到业务数据")
	}
	reset.Body.Close()
}

func TestSSEIdleRevocationAndServerClose(t *testing.T) {
	for _, action := range []string{"吊销", "关闭", "取消"} {
		t.Run(action, func(t *testing.T) {
			f := newServerFixture(t, true)
			f.server.heartbeat = 10 * time.Millisecond
			server := httptest.NewServer(f.server)
			t.Cleanup(func() { f.server.Close(); server.Close() })
			response, reader := connectEvents(t, f, server, "")
			switch action {
			case "吊销":
				if err := f.auth.RevokeToken(t.Context(), f.session.Identity, f.session.Identity.TokenID); err != nil {
					t.Fatal(err)
				}
			case "关闭":
				f.server.Close()
			case "取消":
				response.Body.Close()
			}
			for {
				if _, err := readFrame(reader); err != nil {
					break
				}
			}
		})
	}
}

type deadlineWriter struct {
	*httptest.ResponseRecorder
	writeErr    error
	deadlineErr error
}

func (w *deadlineWriter) SetWriteDeadline(time.Time) error { return w.deadlineErr }
func (w *deadlineWriter) Write(value []byte) (int, error) {
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	return w.ResponseRecorder.Write(value)
}

func TestSSEWriteFailureAndHeadDoNotLeakSubscriptions(t *testing.T) {
	f := newServerFixture(t, true)
	for _, writer := range []*deadlineWriter{
		{ResponseRecorder: httptest.NewRecorder(), writeErr: io.ErrClosedPipe},
		{ResponseRecorder: httptest.NewRecorder(), deadlineErr: http.ErrNotSupported},
	} {
		r := httptest.NewRequest(http.MethodGet, "http://kakatta.test/api/v1/events", nil)
		r.RemoteAddr = "203.0.113.9:3456"
		r.AddCookie(&http.Cookie{Name: cookieName, Value: f.session.Token})
		f.server.ServeHTTP(writer, r)
		if len(f.server.broker.subscribers) != 0 {
			t.Fatal("写入失败后必须回收订阅")
		}
	}
	head := f.request(http.MethodHead, "/api/v1/events", "", true)
	requireStatus(t, head, http.StatusOK)
	if head.Body.Len() != 0 || len(f.server.broker.subscribers) != 0 {
		t.Fatal("HEAD不能变成常驻事件连接")
	}
	if err := f.auth.Logout(t.Context(), f.session.Identity); err != nil {
		t.Fatal(err)
	}
	result := f.request(http.MethodGet, "/api/v1/events", "", true)
	requireStatus(t, result, http.StatusUnauthorized)
	if !errors.Is(f.auth.Validate(t.Context(), f.session.Identity), auth.ErrUnauthenticated) {
		t.Fatal("测试未真正吊销会话")
	}
}
