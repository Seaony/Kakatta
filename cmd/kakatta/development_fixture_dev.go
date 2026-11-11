//go:build kakatta_dev

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/Seaony/Kakatta/internal/analysis"
	modelservice "github.com/Seaony/Kakatta/internal/analysis/service"
	"github.com/Seaony/Kakatta/internal/devfixture"
	"github.com/Seaony/Kakatta/internal/notification"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/settings"
)

type developmentFixtureIDs struct {
	AccountID  int64 `json:"account_id"`
	ProviderID int64 `json:"provider_id"`
	ChannelID  int64 `json:"channel_id"`
	TaskID     int64 `json:"task_id"`
}

func configureDevelopmentFixture(ctx context.Context, g *runtimeGraph, metadata devfixture.Metadata) error {
	var raw string
	err := g.database.DB().QueryRowContext(ctx, `SELECT value FROM meta WHERE key='development.fixture'`).Scan(&raw)
	if err == nil {
		var ids developmentFixtureIDs
		if err := json.Unmarshal([]byte(raw), &ids); err != nil || ids.AccountID <= 0 || ids.ProviderID <= 0 || ids.ChannelID <= 0 || ids.TaskID <= 0 {
			return errors.New("开发样例对象记录损坏")
		}
		return refreshDevelopmentFixture(ctx, g, metadata, ids)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	values := settings.Values{
		"general.update_check":                 []byte(`false`),
		"crawl.quiet_hours":                    []byte(`{"enabled":false,"start":"01:00","end":"07:30"}`),
		"crawl.account_min_gap_seconds":        []byte(`2`),
		"crawl.account_min_gap_jitter_seconds": []byte(`0`),
		"crawl.account_hourly_visits":          []byte(`200`),
		"crawl.interval_jitter_percent":        []byte(`0`),
		"crawl.max_details_per_run":            []byte(`10`),
		"crawl.run_timeout_minutes":            []byte(`15`),
		"crawl.typing_interval_ms":             []byte(`{"min":1,"max":1}`),
		"ai.max_concurrent_requests":           []byte(`1`),
		"ai.image_count":                       []byte(`1`),
	}
	for _, name := range []string{"home_stay_seconds", "page_wait_seconds", "detail_pre_wait_seconds", "detail_stay_seconds", "action_wait_seconds"} {
		values["crawl."+name] = []byte(`{"min":0.01,"max":0.01}`)
	}
	if _, err := g.preferences.Apply(ctx, values); err != nil {
		return err
	}
	account, err := g.core.CreateAccount(ctx, map[string]json.RawMessage{"name": []byte(`"本地开发账号"`)})
	if err != nil {
		return err
	}
	textModel := "local-text"
	provider, err := g.models.CreateProvider(ctx, modelservice.ProviderInput{
		Name: "本地开发模型", BaseURL: metadata.ModelBaseURL, APIKey: &metadata.ModelKey,
		VisionModel: "local-vision", TextModel: &textModel, Enabled: true,
	})
	if err != nil {
		return err
	}
	secrets, err := json.Marshal(map[string]string{"url": metadata.PushURL})
	if err != nil {
		return err
	}
	channel, err := g.notifications.CreateChannel(ctx, notification.ChannelPatch{
		"name": []byte(`"本地开发Webhook"`), "type": []byte(`"webhook"`), "secrets": secrets,
	})
	if err != nil {
		return err
	}
	profile := analysis.DefaultProfile()
	profile.Summary = "示例：自用 MacBook Pro，核对描述与实际成色"
	profile.Specs = []analysis.ProfileSpec{{Name: "型号", Options: []string{"MacBook Pro"}, Values: []string{"MacBook Pro"}, Source: analysis.SourceUser, Edited: true}}
	profile.AllowedKinds = []analysis.Kind{analysis.KindSale}
	profileJSON, err := json.Marshal(profile)
	if err != nil {
		return err
	}
	accountJSON, _ := json.Marshal(account.ID)
	channelsJSON, _ := json.Marshal([]int64{channel.ID})
	task, err := g.core.CreateTask(ctx, service.TaskPatch{
		"name": []byte(`"示例 · MacBook监控"`), "keyword": []byte(`"MacBook"`), "enabled": []byte(`false`),
		"account_id": accountJSON, "account_pinned": []byte(`true`), "max_pages": []byte(`2`),
		"personal_only": []byte(`true`), "judge_mode": []byte(`"ai"`), "requirement": []byte(`"自用 MacBook Pro，核对描述与实际成色"`),
		"profile": profileJSON, "notify_policy": []byte(`"all_new"`), "channel_ids": channelsJSON, "analyze_on_first_run": []byte(`true`),
	})
	if err != nil {
		return err
	}
	ids := developmentFixtureIDs{AccountID: account.ID, ProviderID: provider.Provider.ID, ChannelID: channel.ID, TaskID: task.Task.ID}
	record, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	tx, err := g.database.DB().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// 只有开发样例首轮就推送；正式新任务仍按原规则建立基线。
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET baseline_pending=0 WHERE id=?`, ids.TaskID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('development.fixture',?)`, string(record)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_, err = g.core.OpenLogin(ctx, ids.AccountID)
	return err
}

func refreshDevelopmentFixture(ctx context.Context, g *runtimeGraph, metadata devfixture.Metadata, ids developmentFixtureIDs) error {
	providers, err := g.models.ListProviders(ctx)
	if err != nil {
		return err
	}
	for _, provider := range providers {
		if provider.ID == ids.ProviderID {
			_, err = g.models.UpdateProvider(ctx, provider.ID, modelservice.ProviderInput{
				Name: provider.Name, Preset: provider.Preset, BaseURL: metadata.ModelBaseURL,
				VisionModel: provider.VisionModel, TextModel: provider.TextModel, Enabled: provider.Enabled,
			})
			if err != nil {
				return err
			}
			break
		}
	}
	channels, err := g.notifications.ListChannels(ctx)
	if err != nil {
		return err
	}
	for _, channel := range channels {
		if channel.ID == ids.ChannelID {
			secrets, err := json.Marshal(map[string]string{"url": metadata.PushURL})
			if err != nil {
				return err
			}
			_, err = g.notifications.UpdateChannel(ctx, channel.ID, notification.ChannelPatch{"secrets": secrets})
			return err
		}
	}
	return nil
}
