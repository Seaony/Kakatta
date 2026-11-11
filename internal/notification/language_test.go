package notification

import (
	"bytes"
	"encoding/json"
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/settings"
)

func TestJapaneseNotificationUsesPersistedLanguageAndRecordsRealReceipt(t *testing.T) {
	for _, status := range []int{http.StatusNoContent, http.StatusUnauthorized} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			s := channelsService(t)
			if _, err := s.settings.Apply(t.Context(), settings.Values{"general.language": json.RawMessage(`"ja"`)}); err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				title, err := new(mime.WordDecoder).DecodeHeader(r.Header.Get("Title"))
				var body bytes.Buffer
				_, readErr := body.ReadFrom(r.Body)
				if err != nil || readErr != nil || title != "Kakattaテスト通知" || body.String() != "通知先で通知を受け取れるか確認するための固定サンプルです。" {
					t.Errorf("实际通知没有使用日语：title=%q body=%q err=%v/%v", title, body.String(), err, readErr)
				}
				w.WriteHeader(status)
				_, _ = w.Write([]byte("private-response"))
			}))
			defer server.Close()
			channel := channelsCreate(t, s, "ntfy", "日文通知先", server.URL+"/local-only")
			result, err := s.TestChannel(t.Context(), channel.ID)
			if err != nil || calls.Load() != 1 || result.DeliveryID == 0 || result.OK != (status == http.StatusNoContent) {
				t.Fatal("真实通知回执未持久化或发送次数不符", result, calls.Load(), err)
			}
			if status == http.StatusUnauthorized {
				if result.Error == nil {
					t.Fatal("失败回执缺少机器原因")
				}
				text, err := locales.Format(locales.Ja, *result.Error)
				if err != nil || !strings.Contains(text, "HTTP 401") || !strings.Contains(text, "通知サービス") || strings.Contains(text, "private-response") {
					t.Fatal("失败回执没有安全的日文原因", text, err)
				}
			}
			rows, err := s.ChannelDeliveries(t.Context(), channel.ID)
			if err != nil || len(rows) != 1 || rows[0].ID != result.DeliveryID || rows[0].OK == nil || *rows[0].OK != result.OK {
				t.Fatal("列表回执与实际发送结果不一致", rows, err)
			}
		})
	}
}
