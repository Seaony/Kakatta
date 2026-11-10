package server

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/update"
)

func TestBusinessUpdateReadsPersistentStatusWithoutCheckingNetwork(t *testing.T) {
	f := prepareBusinessFixture(t)
	transport := new(blockedNotificationTransport)
	checker, err := update.New(update.Dependencies{DB: f.store.DB(), Settings: f.settings.base, CurrentVersion: "v1.0.0",
		Now: func() time.Time { return time.UnixMilli(f.clock.Load()) }, HTTP: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	var absent *update.Checker
	if f.server.RegisterUpdate(nil) == nil || f.server.RegisterUpdate(absent) == nil {
		t.Fatal("空更新服务不能装配")
	}
	if err := f.server.RegisterUpdate(checker); err != nil {
		t.Fatal(err)
	}
	if f.server.RegisterUpdate(checker) == nil {
		t.Fatal("更新服务不能重复装配")
	}
	path := "/api/v1/system/update"
	requireStatus(t, f.request(http.MethodGet, path, "", false), http.StatusUnauthorized)
	requireStatus(t, f.request(http.MethodGet, path+"?check=true", "", true), http.StatusBadRequest)
	requireStatus(t, f.request(http.MethodGet, path, `{}`, true), http.StatusBadRequest)
	cache := modelHTTPJSON(t, map[string]any{"version": 1, "last_attempt_at": f.clock.Load(), "last_success_at": f.clock.Load(),
		"known_release": map[string]string{"version": "v1.1.0", "url": "https://github.com/Seaony/Kakatta/releases/tag/v1.1.0"}})
	if _, err := f.store.DB().ExecContext(t.Context(), `INSERT INTO meta(key,value) VALUES('update.check.v1',?)`, cache); err != nil {
		t.Fatal(err)
	}
	response := f.request(http.MethodGet, path, "", true)
	requireStatus(t, response, http.StatusOK)
	status := decodeBody[update.Status](t, response)
	if status.CurrentVersion != "v1.0.0" || !status.Enabled || status.Stale || status.KnownRelease == nil || status.KnownRelease.Version != "v1.1.0" || status.UpdateAvailable == nil || !*status.UpdateAvailable {
		t.Fatal("更新接口没有返回真实持久缓存")
	}
	f.advance(25 * time.Hour)
	response = f.request(http.MethodGet, path, "", true)
	requireStatus(t, response, http.StatusOK)
	status = decodeBody[update.Status](t, response)
	if !status.Stale || status.UpdateAvailable != nil || transport.calls.Load() != 0 {
		t.Fatal("过期状态只读，不能把GET当立即检查或把旧结果当新结果")
	}
	if _, err := f.settings.Apply(t.Context(), settings.Values{"general.update_check": json.RawMessage(`false`)}); err != nil {
		t.Fatal(err)
	}
	response = f.request(http.MethodGet, path, "", true)
	requireStatus(t, response, http.StatusOK)
	if value := decodeBody[update.Status](t, response); value.Enabled || value.NextCheckAt != nil {
		t.Fatal("关闭检查后的状态没有反映当前设置")
	}
	var after string
	if err := f.store.DB().QueryRowContext(t.Context(), `SELECT value FROM meta WHERE key='update.check.v1'`).Scan(&after); err != nil || after != cache {
		t.Fatal("读取更新状态不能重写缓存")
	}
	if _, err := f.store.DB().ExecContext(t.Context(), `UPDATE meta SET value='private-broken-cache' WHERE key='update.check.v1'`); err != nil {
		t.Fatal(err)
	}
	failed := f.request(http.MethodGet, path, "", true)
	requireStatus(t, failed, http.StatusServiceUnavailable)
	assertModelHTTPSafe(t, failed, "private-broken-cache")
}
