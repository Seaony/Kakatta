//go:build kakatta_dev

package main

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	modelservice "github.com/Seaony/Kakatta/internal/analysis/service"
	"github.com/Seaony/Kakatta/internal/backup"
	"github.com/Seaony/Kakatta/internal/devfixture"
	"github.com/Seaony/Kakatta/internal/notification"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/settings"
)

type developmentFixtureResources struct{ calls atomic.Int64 }

func (f *developmentFixtureResources) Open(context.Context, service.ResourceSpec) (service.SessionResource, error) {
	f.calls.Add(1)
	return nil, errors.New("本测试不启动浏览器")
}

func developmentFixtureGraph(t *testing.T) (*runtimeGraph, *developmentFixtureResources) {
	t.Helper()
	cfg, paths, key := graphConfig(t)
	resources := &developmentFixtureResources{}
	g, err := newRuntimeGraphWithResources(t.Context(), cfg, paths, key, backup.KeyDefaultFile, "development-fixture-test", resources)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := g.close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	g.core.SetDevelopmentSchedulerPaused(true)
	graphStart(t, g)
	return g, resources
}

func readDevelopmentFixtureIDs(t *testing.T, g *runtimeGraph) map[string]int64 {
	t.Helper()
	var raw string
	if err := g.database.DB().QueryRowContext(t.Context(), `SELECT value FROM meta WHERE key='development.fixture'`).Scan(&raw); err != nil {
		t.Fatal("没有保存开发样例对象编号", err)
	}
	var ids map[string]int64
	if err := json.Unmarshal([]byte(raw), &ids); err != nil || len(ids) != 4 {
		t.Fatal("开发样例记录应只包含四个整数编号", err)
	}
	for _, key := range []string{"account_id", "provider_id", "channel_id", "task_id"} {
		if ids[key] <= 0 {
			t.Fatal("开发样例对象编号缺失", key)
		}
	}
	return ids
}

func configureDevelopmentTestFixture(t *testing.T, g *runtimeGraph, m devfixture.Metadata) map[string]int64 {
	t.Helper()
	if err := configureDevelopmentFixture(t.Context(), g, m); err != nil {
		t.Fatal(err)
	}
	return readDevelopmentFixtureIDs(t, g)
}

func TestDevelopmentFixtureFirstSetupUsesPublicServicesAndKeepsExistingTasks(t *testing.T) {
	g, resources := developmentFixtureGraph(t)
	existing, err := g.core.CreateTask(t.Context(), service.TaskPatch{"name": []byte(`"用户已有任务"`), "keyword": []byte(`"相机"`), "judge_mode": []byte(`"keyword"`), "enabled": []byte(`false`)})
	if err != nil {
		t.Fatal(err)
	}
	fixture := devfixture.New(t.Context(), "")
	t.Cleanup(fixture.Close)
	ids := configureDevelopmentTestFixture(t, g, fixture.Metadata())
	graphWait(t, func() bool { return resources.calls.Load() == 1 })
	graphWait(t, func() bool {
		return graphScalar(t, g, `SELECT count(*) FROM meta WHERE key LIKE 'runtime.session.%'`) == 0
	})
	if graphScalar(t, g, `SELECT count(*) FROM accounts`) != 1 || graphScalar(t, g, `SELECT count(*) FROM ai_providers`) != 1 || graphScalar(t, g, `SELECT count(*) FROM channels`) != 1 || graphScalar(t, g, `SELECT count(*) FROM tasks`) != 2 {
		t.Fatal("首次开发样例创建数量不对")
	}
	task, err := g.core.GetTask(t.Context(), ids["task_id"])
	if err != nil {
		t.Fatal(err)
	}
	if task.Keyword != "MacBook" || task.MaxPages != 2 || task.JudgeMode != "ai" || task.AccountID == nil || *task.AccountID != ids["account_id"] || !task.AccountPinned || task.NotifyPolicy != "all_new" || !task.AnalyzeOnFirstRun || task.BaselinePending || len(task.ChannelIDs) != 1 || task.ChannelIDs[0] != ids["channel_id"] || len(task.Profile.Specs) != 1 || task.Profile.Specs[0].Values[0] != "MacBook Pro" {
		t.Fatal("样例任务没有完整接入账号、模型清单和通知")
	}
	if graphScalar(t, g, `SELECT count(*) FROM tasks WHERE id=? AND name='用户已有任务' AND keyword='相机' AND baseline_pending=1`, existing.Task.ID) != 1 {
		t.Fatal("开发样例改动了已有任务")
	}
	snapshot, err := g.preferences.Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"crawl.account_min_gap_seconds": "2", "crawl.account_min_gap_jitter_seconds": "0", "crawl.interval_jitter_percent": "0", "crawl.max_details_per_run": "10", "crawl.run_timeout_minutes": "15", "ai.image_count": "1", "general.update_check": "false", "general.onboarding_done": "true"} {
		if string(snapshot.Values[key]) != want {
			t.Fatal("开发假页设置没有生效", key)
		}
	}
	if snapshot.Sources["general.onboarding_done"] != settings.SourceStored {
		t.Fatal("假数据样例齐备后必须持久保存完成状态")
	}
	for _, key := range []string{"home_stay_seconds", "page_wait_seconds", "detail_pre_wait_seconds", "detail_stay_seconds", "action_wait_seconds"} {
		if string(snapshot.Values["crawl."+key]) != `{"min":0.01,"max":0.01}` {
			t.Fatal("开发假页等待仍使用正式时长", key)
		}
	}
	trial, err := g.models.TestProvider(t.Context(), ids["provider_id"])
	if err != nil || !trial.Passed {
		t.Fatal("样例模型没有连到本地模型夹具", err)
	}
	push, err := g.notifications.TestChannel(t.Context(), ids["channel_id"])
	if err != nil || !push.OK {
		t.Fatal("样例推送没有连到本地回执夹具", err)
	}
}

func TestDevelopmentFixtureReloadCompletesOnlyUnstoredOnboarding(t *testing.T) {
	for _, test := range []struct {
		name   string
		stored string
		want   string
	}{
		{"旧假数据缺少完成标记", "", "true"},
		{"保留用户明确未完成", "false", "false"},
		{"保留已完成", "true", "true"},
	} {
		t.Run(test.name, func(t *testing.T) {
			g, resources := developmentFixtureGraph(t)
			fixture := devfixture.New(t.Context(), "")
			t.Cleanup(fixture.Close)
			ids := configureDevelopmentTestFixture(t, g, fixture.Metadata())
			graphWait(t, func() bool { return resources.calls.Load() == 1 })
			graphWait(t, func() bool {
				return graphScalar(t, g, `SELECT count(*) FROM meta WHERE key LIKE 'runtime.session.%'`) == 0
			})
			graphWait(t, func() bool {
				account, err := g.core.GetAccount(t.Context(), ids["account_id"])
				return err == nil && account.Session == nil
			})
			if test.stored == "" {
				if _, err := g.database.DB().ExecContext(t.Context(), `DELETE FROM settings WHERE key='general.onboarding_done'`); err != nil {
					t.Fatal(err)
				}
			} else if _, err := g.preferences.Apply(t.Context(), settings.Values{"general.onboarding_done": []byte(test.stored)}); err != nil {
				t.Fatal(err)
			}
			nextIDs := configureDevelopmentTestFixture(t, g, fixture.Metadata())
			snapshot, err := g.preferences.Get(t.Context())
			if err != nil || string(snapshot.Values["general.onboarding_done"]) != test.want || snapshot.Sources["general.onboarding_done"] != settings.SourceStored {
				t.Fatalf("重载完成标记：得到 %s/%s，期望 %s/stored，错误 %v", snapshot.Values["general.onboarding_done"], snapshot.Sources["general.onboarding_done"], test.want, err)
			}
			if !reflect.DeepEqual(nextIDs, ids) || resources.calls.Load() != 1 || graphScalar(t, g, `SELECT (SELECT count(*) FROM accounts)+(SELECT count(*) FROM ai_providers)+(SELECT count(*) FROM channels)+(SELECT count(*) FROM tasks)`) != 4 {
				t.Fatal("补完成标记不能重建样例对象或重复登录")
			}
			account, err := g.core.GetAccount(t.Context(), ids["account_id"])
			if err != nil || account.Session != nil {
				t.Fatal("补完成标记不能排队新的登录会话", err)
			}
		})
	}
}

func TestDevelopmentFixtureFirstSetupPreservesStoredOnboarding(t *testing.T) {
	for _, value := range []string{"false", "true"} {
		t.Run(value, func(t *testing.T) {
			g, _ := developmentFixtureGraph(t)
			if _, err := g.preferences.Apply(t.Context(), settings.Values{"general.onboarding_done": []byte(value)}); err != nil {
				t.Fatal(err)
			}
			fixture := devfixture.New(t.Context(), "")
			t.Cleanup(fixture.Close)
			configureDevelopmentTestFixture(t, g, fixture.Metadata())
			snapshot, err := g.preferences.Get(t.Context())
			if err != nil || string(snapshot.Values["general.onboarding_done"]) != value || snapshot.Sources["general.onboarding_done"] != settings.SourceStored {
				t.Fatal("首次建立开发样例不能覆盖用户明确保存的引导状态", err)
			}
		})
	}
}

func TestDevelopmentFixtureFailureDoesNotCompleteOnboarding(t *testing.T) {
	for _, stage := range []string{"first", "refresh"} {
		t.Run(stage, func(t *testing.T) {
			g, resources := developmentFixtureGraph(t)
			fixture := devfixture.New(t.Context(), "")
			t.Cleanup(fixture.Close)
			action := "INSERT"
			if stage == "refresh" {
				configureDevelopmentTestFixture(t, g, fixture.Metadata())
				graphWait(t, func() bool { return resources.calls.Load() == 1 })
				graphWait(t, func() bool {
					return graphScalar(t, g, `SELECT count(*) FROM meta WHERE key LIKE 'runtime.session.%'`) == 0
				})
				if _, err := g.database.DB().ExecContext(t.Context(), `DELETE FROM settings WHERE key='general.onboarding_done'`); err != nil {
					t.Fatal(err)
				}
				action = "UPDATE"
			}
			if _, err := g.database.DB().ExecContext(t.Context(), `CREATE TRIGGER fail_development_provider BEFORE `+action+` ON ai_providers BEGIN SELECT RAISE(ABORT,'本地写入失败'); END`); err != nil {
				t.Fatal(err)
			}
			if err := configureDevelopmentFixture(t.Context(), g, fixture.Metadata()); err == nil {
				t.Fatal("样例未成功配置时必须返回失败")
			}
			snapshot, err := g.preferences.Get(t.Context())
			if err != nil || string(snapshot.Values["general.onboarding_done"]) != "false" || snapshot.Sources["general.onboarding_done"] != settings.SourceDefault {
				t.Fatal("配置失败不能提前写入完成状态", err)
			}
		})
	}
}

func TestDevelopmentFixtureReloadOnlyRefreshesEndpoints(t *testing.T) {
	g, resources := developmentFixtureGraph(t)
	first := devfixture.New(t.Context(), "")
	t.Cleanup(first.Close)
	ids := configureDevelopmentTestFixture(t, g, first.Metadata())
	graphWait(t, func() bool { return resources.calls.Load() == 1 })
	graphWait(t, func() bool {
		return graphScalar(t, g, `SELECT count(*) FROM meta WHERE key LIKE 'runtime.session.%'`) == 0
	})
	if _, err := g.core.UpdateTask(t.Context(), ids["task_id"], service.TaskPatch{"name": []byte(`"自己改的任务"`), "keyword": []byte(`"保留关键词"`), "enabled": []byte(`false`)}); err != nil {
		t.Fatal(err)
	}
	provider, err := g.models.GetProvider(t.Context(), ids["provider_id"])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.models.UpdateProvider(t.Context(), provider.ID, modelservice.ProviderInput{Name: "自己改的模型", BaseURL: provider.BaseURL, VisionModel: "custom-vision", TextModel: provider.TextModel, Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.notifications.UpdateChannel(t.Context(), ids["channel_id"], notification.ChannelPatch{"name": []byte(`"自己改的渠道"`), "enabled": []byte(`false`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.preferences.Apply(t.Context(), settings.Values{"crawl.max_details_per_run": []byte(`3`)}); err != nil {
		t.Fatal(err)
	}
	second := devfixture.New(t.Context(), "")
	t.Cleanup(second.Close)
	configureDevelopmentTestFixture(t, g, second.Metadata())
	provider, err = g.models.GetProvider(t.Context(), provider.ID)
	if err != nil || provider.Name != "自己改的模型" || provider.VisionModel != "custom-vision" || provider.Enabled || provider.BaseURL != second.Metadata().ModelBaseURL {
		t.Fatal("刷新模型端点覆盖了用户配置", err)
	}
	channel, err := g.notifications.GetChannel(t.Context(), ids["channel_id"])
	if err != nil || channel.Enabled || channel.Name != "自己改的渠道" {
		t.Fatal("刷新推送端点覆盖了用户配置", err)
	}
	if _, err := g.notifications.UpdateChannel(t.Context(), channel.ID, notification.ChannelPatch{"enabled": []byte(`true`)}); err != nil {
		t.Fatal(err)
	}
	push, err := g.notifications.TestChannel(t.Context(), channel.ID)
	if err != nil || !push.OK || len(second.State()["pushes"].([]map[string]any)) != 1 {
		t.Fatal("重载没有使用新的本地推送端点", err)
	}
	if resources.calls.Load() != 1 || graphScalar(t, g, `SELECT count(*) FROM tasks WHERE id=? AND name='自己改的任务' AND keyword='保留关键词' AND enabled=0 AND baseline_pending=1`, ids["task_id"]) != 1 || graphScalar(t, g, `SELECT count(*) FROM settings WHERE key='crawl.max_details_per_run' AND value_json='3'`) != 1 {
		t.Fatal("重载重新登录、重置了任务基线或设置")
	}
}

func TestDevelopmentFixtureDoesNotRecreateDeletedObjects(t *testing.T) {
	g, resources := developmentFixtureGraph(t)
	fixture := devfixture.New(t.Context(), "")
	t.Cleanup(fixture.Close)
	ids := configureDevelopmentTestFixture(t, g, fixture.Metadata())
	graphWait(t, func() bool { return resources.calls.Load() == 1 })
	graphWait(t, func() bool {
		return graphScalar(t, g, `SELECT count(*) FROM meta WHERE key LIKE 'runtime.session.%'`) == 0
	})
	if err := g.core.DeleteTask(t.Context(), ids["task_id"]); err != nil {
		t.Fatal(err)
	}
	if err := g.core.DeleteAccount(t.Context(), ids["account_id"]); err != nil {
		t.Fatal(err)
	}
	if err := g.models.DeleteProvider(t.Context(), ids["provider_id"]); err != nil {
		t.Fatal(err)
	}
	if _, err := g.notifications.DeleteChannel(t.Context(), ids["channel_id"]); err != nil {
		t.Fatal(err)
	}
	configureDevelopmentTestFixture(t, g, fixture.Metadata())
	if graphScalar(t, g, `SELECT (SELECT count(*) FROM accounts)+(SELECT count(*) FROM ai_providers)+(SELECT count(*) FROM channels)+(SELECT count(*) FROM tasks)`) != 0 || resources.calls.Load() != 1 {
		t.Fatal("重载恢复了用户删除的样例或重复登录")
	}
	var raw string
	if err := g.database.DB().QueryRow(`SELECT value FROM meta WHERE key='development.fixture'`).Scan(&raw); err != nil || strings.Contains(raw, fixture.Metadata().ModelKey) || strings.Contains(raw, "http") {
		t.Fatal("样例记录包含端点或密钥", err)
	}
}
