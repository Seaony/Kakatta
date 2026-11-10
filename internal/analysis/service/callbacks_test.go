package modelservice

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/Seaony/Kakatta/internal/analysis/client"
	"github.com/Seaony/Kakatta/internal/locales"
)

func TestCommittedProviderChangesSurviveCallbackFailures(t *testing.T) {
	f := newModelFixture(t)
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	f.onPublish = func(string) error { return errors.New("callback-private-key") }
	f.onSystem = func(context.Context, int64) error { return errors.New("callback-private-password") }
	input := modelInput()
	first, err := f.s.CreateProvider(t.Context(), input)
	if err != nil || first.Provider.ID < 1 || modelScalar[int](t, f, `SELECT count(*) FROM ai_providers`) != 1 {
		t.Fatalf("保存成功后错误地提示创建失败：%+v %v", first, err)
	}
	second, err := f.s.CreateProvider(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.s.Provider(t.Context(), first.Provider.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.ChangeProvider(t.Context(), p.Ref, client.ProviderChange{Kind: "blocked", Reason: &locales.Message{Code: "analysis.client.auth"}}); err != nil {
		t.Fatalf("已保存的阻断事实不应因通知失败中断供应商轮换：%v", err)
	}
	input.Name = "已保存新名称"
	updated, err := f.s.UpdateProvider(t.Context(), p.Ref.ID, input)
	if err != nil || updated.Provider.Name != input.Name || updated.Provider.ErrorState != nil {
		t.Fatalf("回调失败让成功更新伪装成存储失败：%+v %v", updated, err)
	}
	disabled, err := f.s.SetProviderEnabled(t.Context(), p.Ref.ID, false)
	if err != nil || disabled.Enabled {
		t.Fatalf("回调失败改变启停成功事实：%+v %v", disabled, err)
	}
	ordered, err := f.s.ReorderProviders(t.Context(), []int64{second.Provider.ID, p.Ref.ID})
	if err != nil || ordered[0].ID != second.Provider.ID || ordered[1].SortIndex != 1 {
		t.Fatalf("回调失败改变排序成功事实：%+v %v", ordered, err)
	}
	if err := f.s.DeleteProvider(t.Context(), p.Ref.ID); err != nil || modelScalar[int](t, f, `SELECT count(*) FROM ai_providers`) != 1 || modelScalar[int](t, f, `SELECT count(*) FROM system_events WHERE resolved_at IS NOT NULL`) != 1 {
		t.Fatalf("回调失败改变删除或历史事件事实：%v", err)
	}
	if len(f.notified) != 1 || !strings.Contains(logs.String(), "service.storage_failure") || strings.Contains(logs.String(), "callback-private") {
		t.Fatalf("回调失败没有安全记录或泄漏了原始错误：%s", logs.String())
	}
}
