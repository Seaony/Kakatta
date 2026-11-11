package service

import (
	"context"
	"errors"
	"time"

	"github.com/Seaony/Kakatta/internal/browser"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

func (s *Service) logRunPhase(run *runExecution, phase, status string, started time.Time, cause error) {
	if run.Resource == nil || run.Resource.Runner() == nil || run.Resource.Runner().Log == nil {
		return
	}
	fields := map[string]any{"阶段": phase, "状态": status, "毫秒": time.Since(started).Milliseconds()}
	if cause != nil {
		fields["状态"], fields["原因"] = "failed", cause.Error()
	}
	run.Resource.Runner().Log("运行阶段", fields)
}

func (s *Service) logRunSummary(run *runExecution, cause error) {
	if run.Resource == nil || run.Resource.Runner() == nil || run.Resource.Runner().Log == nil {
		return
	}
	fields := map[string]any{
		"run_id": run.Ref.RunID, "task_id": run.Ref.TaskID, "record": run.Record,
		"page_count": run.PageCount, "item_count": run.SearchSaved.ScannedCount,
		"detail_count": run.DetailCount, "seller_page_count": run.SellerPageCount,
		"analyzed_count": run.AnalyzedCount, "recommended_count": run.RecommendedCount,
	}
	if cause != nil {
		fields["原因"] = cause.Error()
	}
	run.Resource.Runner().Log("运行摘要", fields)
}

func logRunFailure(run *runExecution, step string, cause error) {
	if cause != nil && run.Resource != nil && run.Resource.Runner() != nil && run.Resource.Runner().Log != nil {
		run.Resource.Runner().Log("步骤失败", map[string]any{"步骤": step, "分类": "model", "原因": cause.Error()})
	}
}

func reportBrowserFailure(ctx context.Context, runner *web.Runner, step string, cause error) error {
	if runner != nil && (errors.Is(cause, browser.ErrNotFound) || errors.Is(cause, browser.ErrResponseTimeout)) {
		return runner.ReportFailure(ctx, step, cause, nil)
	}
	return cause
}
