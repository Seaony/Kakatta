package notification

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/notify"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/store"
)

func TestDeliveryFormatReceiptLevelsSurviveSaveRetryAndReopen(t *testing.T) {
	for level := range 6 {
		for _, ok := range []bool{false, true} {
			t.Run(fmt.Sprintf("level%d/success%t", level, ok), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "delivery.db")
				stored, err := store.Open(t.Context(), path)
				if err != nil {
					t.Fatal(err)
				}
				defer stored.Close()
				group := deliverySeed(t, stored.DB(), 1, intentRecommended)
				outcome := deliveryOutcome(ok)
				outcome.FormatLevel = level
				receipt := &taskDeliveryReceipt{ref: service.RunRef{TaskID: 1, SearchGeneration: 1}, group: group, kind: "recommended", outcome: outcome}
				s := &Service{db: stored.DB()}
				for range 2 {
					if err := s.saveTaskReceipt(t.Context(), receipt); err != nil {
						t.Fatal(err)
					}
				}
				if deliveryCount(t, stored.DB(), `SELECT count(*) FROM deliveries WHERE format_level=? AND ok=?`, level, ok) != 1 ||
					deliveryCount(t, stored.DB(), `SELECT count(*) FROM delivery_items`) != 1 ||
					deliveryCount(t, stored.DB(), `SELECT count(*) FROM delivery_intents`) != 1 {
					t.Fatal("格式级别丢失，或回执补存重复了投递和成员")
				}
				if err := stored.Close(); err != nil {
					t.Fatal(err)
				}
				reopened, err := store.Open(t.Context(), path)
				if err != nil {
					t.Fatal(err)
				}
				defer reopened.Close()
				s = &Service{db: reopened.DB()}
				if err := s.saveTaskReceipt(t.Context(), receipt); err != nil {
					t.Fatal(err)
				}
				if deliveryCount(t, reopened.DB(), `SELECT count(*) FROM deliveries WHERE format_level=? AND ok=?`, level, ok) != 1 {
					t.Fatal("重开后格式级别丢失或回执重复")
				}
				want := "pending"
				if ok {
					want = "sent"
				}
				var state string
				if err := reopened.DB().QueryRowContext(t.Context(), `SELECT state FROM notification_intents WHERE id=1`).Scan(&state); err != nil || state != want {
					t.Fatal("保存格式级别改变了真实送达语义", state, err)
				}
			})
		}
	}
}

func TestFlushRunRecordsFormatLevelAfterActualSendAndRestart(t *testing.T) {
	for _, example := range []struct {
		level, count, nameWidth int
		channel                 notify.Type
		quote                   string
	}{
		{0, 1, 0, notify.Webhook, "正常"},
		{1, 1, 0, notify.Webhook, strings.Repeat("卖家原文", 300)},
		{2, 1, 0, notify.ServerChan, "正常"},
		{3, 18, 0, notify.Telegram, strings.Repeat("证", 60)},
		{4, 24, 0, notify.Telegram, strings.Repeat("<&", 30)},
		{5, 80, 60, notify.Telegram, "正常"},
	} {
		for _, initialSuccess := range []bool{false, true} {
			t.Run(fmt.Sprintf("level%d/initialSuccess%t", example.level, initialSuccess), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "delivery.db")
				stored, err := store.Open(t.Context(), path)
				if err != nil {
					t.Fatal(err)
				}
				defer stored.Close()
				deliverySeed(t, stored.DB(), 1, intentRecommended)
				s := deliveryService(t, stored.DB(), nil)
				task, value := formatEvidenceFixture(example.count, example.nameWidth, example.quote)
				profile, facts := task.Profile, value.Finding.Analysis
				for index := range profile.Musts {
					profile.Musts[index].Source = analysis.SourceUser
				}
				profile.NextNo = example.count + 1
				profileJSON, _ := json.Marshal(profile)
				factsJSON, _ := json.Marshal(facts)
				planExec(t, stored.DB(), `UPDATE tasks SET judge_mode='ai',notify_policy='good',profile_json=? WHERE id=1`, string(profileJSON))
				if example.level == 2 {
					planExec(t, stored.DB(), `UPDATE tasks SET name=? WHERE id=1`, strings.Repeat("名", 30))
				}
				planExec(t, stored.DB(), `UPDATE findings SET decision='scored',score=88,grade='good',decided_layer=3,analysis_json=?,summary='核对原文' WHERE id=1`, string(factsJSON))
				planExec(t, stored.DB(), `UPDATE listings SET description=?,thumbnail_url=? WHERE item_id='商品A'`, example.quote, *value.Listing.ThumbnailURL)
				var calls atomic.Int64
				var accepting atomic.Bool
				accepting.Store(initialSuccess)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					var message map[string]any
					if err := json.NewDecoder(r.Body).Decode(&message); err != nil {
						t.Error(err)
					}
					body, _ := message["body"].(string)
					if example.level == 1 && (strings.Contains(body, example.quote) || !strings.Contains(body, "原文：") || !strings.Contains(body, "…")) {
						t.Error("实际发送没有截短长证据并保留可见标记")
					}
					if !accepting.Load() {
						w.WriteHeader(http.StatusServiceUnavailable)
					}
					_, _ = w.Write([]byte(`{"ok":true,"code":0}`))
				}))
				defer server.Close()
				sender := notify.New(notify.Dependencies{Endpoints: notify.Endpoints{ServerChanTurbo: server.URL}})
				s.sender = sender
				patch := ChannelPatch{"type": json.RawMessage(`"` + example.channel + `"`), "name": json.RawMessage(`"本地回执"`), "config": json.RawMessage(`{}`)}
				switch example.channel {
				case notify.Webhook:
					patch["config"] = json.RawMessage(`{"method":"POST","content_type":"JSON"}`)
					patch["secrets"] = json.RawMessage(`{"url":"` + server.URL + `"}`)
				case notify.Telegram:
					patch["config"] = json.RawMessage(`{"chat_id":"42","api_url":"` + server.URL + `"}`)
					patch["secrets"] = json.RawMessage(`{"token":"local-only"}`)
				case notify.ServerChan:
					patch["secrets"] = json.RawMessage(`{"send_key":"local-only"}`)
				}
				channel, err := s.CreateChannel(t.Context(), patch)
				if err != nil {
					t.Fatal(err)
				}
				planExec(t, stored.DB(), `INSERT INTO task_channels(task_id,channel_id) VALUES(1,?)`, channel.ID)
				ref := service.RunRef{TaskID: 1, SearchGeneration: 1}
				if _, err := s.FlushRun(t.Context(), ref, nil); err != nil {
					t.Fatal(err)
				}
				if calls.Load() != 1 || deliveryCount(t, stored.DB(), `SELECT count(*) FROM deliveries WHERE format_level=? AND ok=?`, example.level, initialSuccess) != 1 {
					t.Fatal("真实发送的格式级别未随成功或失败回执保存")
				}
				var savedFacts string
				if err := stored.DB().QueryRowContext(t.Context(), `SELECT analysis_json FROM findings WHERE id=1`).Scan(&savedFacts); err != nil || savedFacts != string(factsJSON) {
					t.Fatal("显示截短改写了原始分析证据", err)
				}
				cipher := s.cipher
				ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
				if err := s.Close(ctx); err != nil {
					cancel()
					t.Fatal(err)
				}
				cancel()
				if err := stored.Close(); err != nil {
					t.Fatal(err)
				}
				reopened, err := store.Open(t.Context(), path)
				if err != nil {
					t.Fatal(err)
				}
				defer reopened.Close()
				values, err := appsettings.New(t.Context(), reopened.DB(), settings.Environment{TZ: "UTC"}, time.Now)
				if err != nil {
					t.Fatal(err)
				}
				restored, err := New(Dependencies{DB: reopened.DB(), Settings: values, Cipher: cipher, Sender: sender})
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					defer cancel()
					if err := restored.Close(ctx); err != nil {
						t.Error(err)
					}
				}()
				accepting.Store(true)
				for range 2 {
					if _, err := restored.FlushRun(t.Context(), ref, nil); err != nil {
						t.Fatal(err)
					}
				}
				wantCalls := int64(1)
				if !initialSuccess {
					wantCalls = 2
				}
				if calls.Load() != wantCalls || deliveryCount(t, reopened.DB(), `SELECT count(*) FROM deliveries WHERE format_level=?`, example.level) != wantCalls ||
					deliveryCount(t, reopened.DB(), `SELECT count(*) FROM deliveries WHERE ok=1`) != 1 ||
					deliveryCount(t, reopened.DB(), `SELECT count(*) FROM notification_intents WHERE state='sent'`) != 1 {
					t.Fatal("重启补发丢失格式级别，或成功后重复了投递")
				}
			})
		}
	}
}

func TestDeliveryFormatCandidateRejectsInvalidLevelReadOnly(t *testing.T) {
	for _, level := range []any{-1, 6, 1.5} {
		t.Run(fmt.Sprint(level), func(t *testing.T) {
			f, cipher := candidateFixture(t)
			planExec(t, f, `PRAGMA ignore_check_constraints=ON`)
			planExec(t, f, `INSERT INTO deliveries(kind,format_level) VALUES('recommended',?)`, level)
			planExec(t, f, `PRAGMA ignore_check_constraints=OFF`)
			err := candidateRunReadOnly(t, t.Context(), f, cipher)
			if err == nil || !strings.Contains(err.Error(), "deliveries") {
				t.Fatal("恢复候选放过无效格式级别", level, err)
			}
		})
	}
}
