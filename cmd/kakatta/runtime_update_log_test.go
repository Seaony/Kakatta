package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/Seaony/Kakatta/internal/analysis/client"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/update"
)

type maintenanceUpdateTimeout struct{}

func (maintenanceUpdateTimeout) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("private proxy address: %w", context.DeadlineExceeded)
}

func TestRuntimeMaintenanceUpdateWarningsPreserveOtherErrors(t *testing.T) {
	for _, tc := range []struct {
		name, code, level, text string
		err                     error
	}{
		{name: "更新网络失败", err: fmt.Errorf("private proxy: %w", &service.ServiceError{Kind: service.ErrorUnavailable, Reason: locales.Message{Code: "update.check_failed", Args: map[string]any{}}}), code: "update.check_failed", level: "WARN", text: "更新检查失败"},
		{name: "更新缓存存储失败", err: &service.ServiceError{Kind: service.ErrorUnavailable, Reason: locales.Message{Code: "service.storage_failure", Args: map[string]any{}}}, code: "service.storage_failure", level: "ERROR", text: "运行维护失败"},
		{name: "其他维护失败", err: errors.New("private database path"), code: "service.storage_failure", level: "ERROR", text: "运行维护失败"},
		{name: "正常", err: nil},
		{name: "取消", err: context.Canceled},
		{name: "每日额度", err: client.ErrDailyLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
			defer slog.SetDefault(previous)
			g := &runtimeGraph{ctx: context.Background()}
			g.logMaintenance(tc.err)
			if tc.level == "" {
				if output.Len() != 0 {
					t.Fatalf("不应记录的维护状态仍写日志：%s", &output)
				}
				return
			}
			var entry struct {
				Level  string          `json:"level"`
				Text   string          `json:"msg"`
				Reason locales.Message `json:"reason"`
			}
			if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
				t.Fatal(err)
			}
			if entry.Level != tc.level || entry.Text != tc.text || entry.Reason.Code != tc.code || strings.Contains(output.String(), "private") {
				t.Fatalf("维护日志分类或脱敏不对：%s", &output)
			}
		})
	}
}

func TestRuntimeMaintenanceUpdateResultsReachStatusAPI(t *testing.T) {
	for _, result := range []string{"no_release", "http_failure", "timeout"} {
		t.Run(result, func(t *testing.T) {
			g, checker, calls := maintenanceFixture(t, func(w http.ResponseWriter, _ *http.Request) {
				if result == "no_release" {
					w.WriteHeader(http.StatusNotFound)
				} else {
					w.WriteHeader(http.StatusServiceUnavailable)
				}
			}, nil)
			session := graphAdmin(t, g)
			if result == "timeout" {
				failed, err := update.New(update.Dependencies{DB: g.database.DB(), Settings: g.preferences, CurrentVersion: "v1.2.2", HTTP: &http.Client{Transport: maintenanceUpdateTimeout{}}})
				if err != nil {
					t.Fatal(err)
				}
				checker.API = failed
			}
			if err := g.handler.RegisterUpdate(checker); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
			defer slog.SetDefault(previous)
			err := checker.CheckDue(t.Context())
			g.logMaintenance(err)
			before := calls.Load()
			response := graphRequest(t, g, session, http.MethodGet, "/api/v1/system/update", "")
			if response.Code != http.StatusOK {
				t.Fatalf("不能读取更新状态：%d %s", response.Code, response.Body)
			}
			var status update.Status
			if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
				t.Fatal(err)
			}
			if result == "no_release" {
				if err != nil || output.Len() != 0 || status.Error != nil || status.Stale || status.LastSuccessAt == nil || status.UpdateAvailable == nil || *status.UpdateAvailable {
					t.Fatalf("无发布被误报失败：%+v，错误%v，日志%s", status, err, &output)
				}
			} else {
				if err == nil || status.Error == nil || status.Error.Code != "update.check_failed" || !status.Stale || status.LastSuccessAt != nil || status.UpdateAvailable != nil || !strings.Contains(output.String(), `"level":"WARN"`) || strings.Contains(output.String(), "运行维护失败") || strings.Contains(output.String(), "private") {
					t.Fatalf("网络失败状态或日志不对：%+v，错误%v，日志%s", status, err, &output)
				}
			}
			if calls.Load() != before || result == "timeout" && before != 0 {
				t.Fatalf("读取状态又发起检查：前%d，后%d", before, calls.Load())
			}
		})
	}
}
