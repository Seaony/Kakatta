package modelservice

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"maps"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/analysis/client"
	"github.com/Seaony/Kakatta/internal/analysis/images"
	"github.com/Seaony/Kakatta/internal/analysis/prompts"
	"github.com/Seaony/Kakatta/internal/limits"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/service"
)

func (s *Service) TestProvider(ctx context.Context, providerID int64) (TrialReport, error) {
	report := TrialReport{Text: trialEmptyStep(), Vision: trialEmptyStep()}
	ctx, finish, err := s.beginJob(ctx)
	if err != nil {
		report.Reason = trialReason(err)
		return report, err
	}
	defer finish()
	if providerID < 1 || providerID > limits.MaxSafeInteger {
		err := invalidProvider("id")
		report.Reason = trialReason(err)
		return report, err
	}
	if _, err := s.GetProvider(ctx, providerID); err != nil {
		report.Reason = trialReason(err)
		return report, requestFailure(err)
	}
	snapshot, err := s.PromptSnapshot(ctx)
	if err != nil {
		report.Reason = trialReason(err)
		return report, requestFailure(err)
	}
	profile, titles, product := trialSample()
	title, err := prompts.BuildTitle(snapshot, nil, profile, titles)
	if err != nil {
		err = storageFailure(err)
		report.Reason = trialReason(err)
		return report, err
	}
	photo := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(images.SampleJPEG())
	vision, originals, err := prompts.BuildAnalysis(snapshot, nil, profile, product, nil, []string{photo})
	if err != nil {
		err = storageFailure(err)
		report.Reason = trialReason(err)
		return report, err
	}
	current, release, err := s.borrowClient(ctx)
	if err != nil {
		report.Reason = trialReason(err)
		return report, err
	}
	defer release()
	result, requestErr := current.Test(ctx, providerID, title, vision)
	var textErr, visionErr error
	report.Text, textErr = trialStep(result.Text)
	report.Vision, visionErr = trialStep(result.Vision)
	if report.Text.State == TrialPassed {
		_, report.Text.Warnings, textErr = analysis.ParseTitleScreen([]byte(result.Text.Result.Text), len(titles))
	}
	if report.Vision.State == TrialPassed {
		_, report.Vision.Warnings, visionErr = analysis.ParseFacts([]byte(result.Vision.Result.Text), profile, originals)
	}
	if mappingErr := errors.Join(textErr, visionErr); mappingErr != nil {
		if textErr != nil {
			report.Text.State, report.Text.Reason = TrialFailed, trialReason(storageFailure(textErr))
		}
		if visionErr != nil {
			report.Vision.State, report.Vision.Reason = TrialFailed, trialReason(storageFailure(visionErr))
		}
		err = errors.Join(requestErr, storageFailure(mappingErr))
		report.Reason = trialReason(err)
		return report, err
	}
	if requestErr != nil {
		report.Reason = trialReason(requestErr)
		if trialCapabilityFailure(requestErr) {
			return report, nil
		}
		return report, requestFailure(requestErr)
	}
	report.Passed = report.Text.State == TrialPassed && report.Vision.State == TrialPassed
	if !report.Passed {
		report.Reason = trialMessage("analysis.client.test_failed")
	}
	return report, nil
}

func trialEmptyStep() TrialStepReport {
	return TrialStepReport{State: TrialNotRun, Warnings: []locales.Message{}}
}

func trialStep(step client.TestStep) (TrialStepReport, error) {
	report := trialEmptyStep()
	if step.Err == nil && len(step.Result.Calls) == 0 {
		return report, nil
	}
	report.State = TrialFailed
	report.Reason = trialReason(step.Err)
	if step.Result.Duration < 0 {
		return report, errors.New("试分析耗时无效")
	}
	if step.Result.Duration > 0 || len(step.Result.Calls) > 0 {
		report.DurationMS = trialPointer(step.Result.Duration.Milliseconds())
	}
	if step.Result.Model != "" {
		report.Model = trialPointer(step.Result.Model)
	}
	if len(step.Result.Calls) == 0 {
		return report, nil
	}
	report.Thinking = trialPointer(step.Result.Thinking)
	report.UsageIncomplete = true
	var usage usageAccumulator
	for _, call := range step.Result.Calls {
		if !validCallEnd(call.End) {
			return report, errors.New("试分析调用结果无效")
		}
		row := usageRecord{ok: sql.NullInt64{Valid: true}, duration: sql.NullInt64{Int64: call.End.Duration.Milliseconds(), Valid: true}}
		if call.End.OK {
			row.ok.Int64 = 1
		}
		if call.End.Usage.Present {
			row.input = sql.NullInt64{Int64: call.End.Usage.InputTokens, Valid: true}
			row.output = sql.NullInt64{Int64: call.End.Usage.OutputTokens, Valid: true}
			if call.End.Usage.ReasoningTokens != nil {
				row.reasoning = sql.NullInt64{Int64: *call.End.Usage.ReasoningTokens, Valid: true}
			}
		}
		if err := usage.add(row); err != nil {
			return report, err
		}
	}
	report.InputTokens, report.OutputTokens = usage.total.InputTokens, usage.total.OutputTokens
	report.ReasoningTokens, report.UsageIncomplete = usage.total.ReasoningTokens, usage.incomplete
	last := step.Result.Calls[len(step.Result.Calls)-1]
	if last.Start.Model != "" {
		report.Model = trialPointer(last.Start.Model)
	}
	if compatRank(last.Start.JSONMode) >= 0 {
		report.JSONMode = trialPointer(last.Start.JSONMode)
	}
	if last.End.HTTPStatus != 0 {
		report.HTTPStatus = trialPointer(last.End.HTTPStatus)
	}
	if step.Err == nil && last.End.OK {
		report.State, report.Reason = TrialPassed, nil
	} else if report.Reason == nil {
		report.Reason = trialMessage("analysis.client.test_failed")
	}
	return report, nil
}

func trialCapabilityFailure(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, client.ErrInfrastructure) || errors.Is(err, client.ErrConfigChanged) {
		return false
	}
	var failure *client.Failure
	return errors.As(err, &failure)
}

func trialReason(err error) *locales.Message {
	if err == nil {
		return nil
	}
	for _, typed := range []struct {
		err  error
		code string
	}{{context.Canceled, "analysis.client.cancelled"}, {context.DeadlineExceeded, "analysis.client.timeout"}, {client.ErrConfigChanged, "analysis.client.config_changed"}, {client.ErrDailyLimit, "analysis.client.daily_limit"}, {client.ErrNoProvider, "analysis.client.no_provider"}} {
		if errors.Is(err, typed.err) {
			return trialMessage(typed.code)
		}
	}
	var business *service.ServiceError
	if errors.As(err, &business) {
		return &locales.Message{Code: business.Reason.Code, Args: maps.Clone(business.Reason.Args)}
	}
	if errors.Is(err, client.ErrInfrastructure) {
		return trialMessage("analysis.client.infrastructure")
	}
	var failure *client.Failure
	if errors.As(err, &failure) {
		return &locales.Message{Code: failure.Last.Code, Args: maps.Clone(failure.Last.Args)}
	}
	return trialMessage("service.storage_failure")
}

func trialMessage(code string) *locales.Message {
	return &locales.Message{Code: code, Args: map[string]any{}}
}

func trialPointer[T any](value T) *T { return &value }

func trialSample() (analysis.Profile, []prompts.TitleSource, prompts.ProductSource) {
	const title = "轻舟14 16G+512G 自用轻薄笔记本"
	profile := analysis.DefaultProfile()
	profile.Summary = "会帮你找：轻舟14，16G内存、512G存储，功能正常，带充电器"
	profile.Specs = []analysis.ProfileSpec{
		{Name: "型号", Options: []string{"轻舟14"}, Values: []string{"轻舟14"}, Source: analysis.SourceUser},
		{Name: "内存", Options: []string{"8G", "16G"}, Values: []string{"16G"}, Source: analysis.SourceUser},
		{Name: "存储", Options: []string{"256G", "512G"}, Values: []string{"512G"}, Source: analysis.SourceUser},
	}
	profile.Musts = []analysis.Requirement{{ID: "m1", Text: "功能正常", Source: analysis.SourceUser, Rev: 1}}
	profile.Bonuses = []analysis.Requirement{{ID: "b2", Text: "带充电器", Source: analysis.SourceUser, Rev: 1}}
	profile.Risks = append(profile.Risks, analysis.ProfileRisk{ID: "cosmetic_damage", Attitude: analysis.Mind, Source: analysis.SourceAI, Rev: 1})
	profile.NextNo = 3
	titles := []prompts.TitleSource{
		{Title: title, PriceCents: trialPointer(int64(180000)), Tags: []string{"包邮"}},
		{Title: "求购轻舟14笔记本一台", Tags: []string{}},
		{Title: "轻舟14笔记本保护壳", PriceCents: trialPointer(int64(3000)), Tags: []string{}},
		{Title: "轻舟14笔记本租赁", PriceCents: trialPointer(int64(10000)), Tags: []string{}},
		{Title: "轻舟14 8G+256G 二手笔记本", PriceCents: trialPointer(int64(120000)), Tags: []string{"包邮"}},
	}
	product := prompts.ProductSource{
		Title: title, PriceCents: trialPointer(int64(180000)), OriginalPriceCents: trialPointer(int64(320000)),
		Description:   trialPointer("个人升级换机，轻舟14笔记本，16G内存、512G存储。功能正常，外壳有细微划痕，带充电器和充电线。可走闲鱼平台交易，不接受站外付款。"),
		PublishedAtMS: trialPointer(int64(1700000000000)), Area: trialPointer("杭州"), Tags: []string{"包邮"}, WantCount: trialPointer(int64(3)), BrowseCount: trialPointer(int64(28)),
		Seller: &prompts.SellerSource{
			Nickname: trialPointer("小舟的闲置"), RegistrationDays: trialPointer(1800), Introduction: trialPointer("个人闲置，升级换机，支持闲鱼平台交易。"),
			ItemCount: trialPointer(int64(2)), OnSaleCount: trialPointer(int64(1)), SoldCount: trialPointer(int64(1)), RateCount: trialPointer(int64(1)), SellerGoodRate: trialPointer(1.0),
			RecentRates: []prompts.RecentRateSource{{Feedback: trialPointer("描述一致，发货很快"), Role: trialPointer("seller"), Kind: trialPointer("good"), PublishedAtMS: trialPointer(int64(1690000000000))}},
			RecentItems: []prompts.RecentItemSource{{Title: title, PriceCents: trialPointer(int64(180000)), Status: trialPointer("on_sale")}},
		},
	}
	return profile, titles, product
}
