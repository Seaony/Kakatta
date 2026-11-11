package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/browser"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/xianyu"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

func (s *Service) runSearch(ctx context.Context, run *runExecution) (err error) {
	defer func() {
		if searchLayoutError(err) {
			run.LayoutError = true
		}
	}()
	if run.Resource == nil || run.Resource.Runner() == nil {
		return errors.New("搜索运行没有浏览器资源")
	}
	// 先取旧队列，不能把本次随后新建的发现混进 Q。
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storageFailure(err)
	}
	defer tx.Rollback()
	_, clock, now, err := s.observeNowTx(ctx, tx)
	if err != nil {
		return storageFailure(err)
	}
	backlog, err := loadBacklogTx(ctx, tx, run.Ref, now.UnixMilli())
	if err != nil {
		return storageFailure(err)
	}
	if err = appsettings.SaveClock(ctx, tx, clock); err != nil {
		return storageFailure(err)
	}
	if err = tx.Commit(); err != nil {
		return storageFailure(err)
	}
	run.Backlog = backlog
	if err = ctx.Err(); err != nil {
		return err
	}

	runner := run.Resource.Runner()
	if err = runner.OpenHome(ctx); err != nil {
		return reportBrowserFailure(ctx, runner, "打开首页", err)
	}
	if _, err = runner.Search(ctx, run.Task.Keyword); err != nil {
		return reportBrowserFailure(ctx, runner, "搜索商品", err)
	}
	filters := web.FilterOptions{
		PersonalOnly: run.Task.PersonalOnly, FreeShipping: run.Task.FreeShippingOnly,
		MinPriceCents: run.Task.MinPrice, MaxPriceCents: run.Task.MaxPrice,
	}
	if run.Task.RegionProvince != nil {
		filters.Province = *run.Task.RegionProvince
	}
	if run.Task.RegionCity != nil {
		filters.City = *run.Task.RegionCity
	}
	if run.Task.RegionDistrict != nil {
		filters.District = *run.Task.RegionDistrict
	}
	response, err := runner.ApplyFilters(ctx, filters)
	if err != nil {
		return reportBrowserFailure(ctx, runner, "选择搜索筛选", err)
	}
	var items []xianyu.SearchItem
	defer func() {
		if err != nil && len(items) > 0 {
			// 后页中断时搜索结果没有入库，日志仍要保留实际看到的商品数。
			seen := make(map[string]struct{}, len(items))
			for _, item := range items {
				seen[item.ID] = struct{}{}
			}
			count := int64(len(seen))
			run.Record.ScannedCount = &count
		}
	}()
	err = collectSearchPages(ctx, response, run.Task.MaxPages,
		func(ctx context.Context) (*browser.Response, bool, error) {
			response, more, err := runner.NextPage(ctx)
			return response, more, reportBrowserFailure(ctx, runner, "搜索翻页", err)
		},
		func(page int64, found []xianyu.SearchItem) {
			runner.Log("搜索结果已解析", map[string]any{"页": page, "商品数": int64(len(found))})
			run.PageCount = page
			if page == 1 {
				run.FirstPageCount = int64(len(found))
			}
			items = append(items, found...)
		},
		func(page int64, failure error) {
			run.LayoutError = run.LayoutError || searchLayoutError(failure)
			run.Warnings = append(run.Warnings, locales.Message{Code: "service.search_page_failed",
				Args: map[string]any{"page": page, "cause": failure.Error()}})
		}, runner.ReportFailure)
	if err != nil {
		return err
	}

	// 一起写入完整结果，基线十件和最新搜索集合都按整次搜索计算。
	tx, err = s.db.BeginTx(ctx, nil)
	if err != nil {
		return storageFailure(err)
	}
	defer tx.Rollback()
	_, clock, now, err = s.observeNowTx(ctx, tx)
	if err != nil {
		return storageFailure(err)
	}
	saved, err := s.persistSearchTx(ctx, tx, run.Ref, searchObservations(items, now.UnixMilli()), now.UnixMilli())
	if err != nil {
		return storageFailure(err)
	}
	if err = appsettings.SaveClock(ctx, tx, clock); err != nil {
		return storageFailure(err)
	}
	if err = tx.Commit(); err != nil {
		return storageFailure(err)
	}
	run.SearchSaved = saved
	_ = s.emit("finding.changed", map[string]any{"task_id": run.Ref.TaskID})
	return nil
}

func searchLayoutError(err error) bool {
	return errors.Is(err, xianyu.ErrSearchStructure) || errors.Is(err, browser.ErrNotFound)
}

func searchObservations(items []xianyu.SearchItem, observedAt int64) []SearchObservation {
	observations := make([]SearchObservation, 0, len(items))
	for _, item := range items {
		observation := SearchObservation{ItemID: item.ID, Title: item.Title, DesktopURL: item.WebURL,
			FreeShipping: item.FreeShip, Inspected: item.Inspected, ObservedAt: observedAt, Tags: []string{}}
		for _, field := range []struct {
			value  string
			target **string
		}{
			{item.PriceText, &observation.PriceText}, {item.OriPriceText, &observation.OriginalPriceText},
			{item.Image, &observation.ThumbnailURL}, {item.Area, &observation.Area}, {item.SellerNick, &observation.SellerNick},
		} {
			if field.value != "" {
				value := field.value
				*field.target = &value
			}
		}
		if item.PriceKnown {
			price := item.PriceCents
			observation.Price = &price
		}
		if item.PublishedKnown {
			publishedAt := item.PublishedAt.UnixMilli()
			observation.PublishedAt = &publishedAt
		}
		if item.WantKnown {
			want := int64(item.WantNum)
			observation.WantCount = &want
		}
		if item.FreeShip {
			observation.Tags = append(observation.Tags, "包邮")
		}
		if item.Inspected {
			observation.Tags = append(observation.Tags, "验货宝")
		}
		observations = append(observations, observation)
	}
	return observations
}

// 这里只收集完整筛选的页面，全部结束后才由调用方一次落盘。
func collectSearchPages(ctx context.Context, first *browser.Response, maxPages int64,
	next func(context.Context) (*browser.Response, bool, error),
	accept func(int64, []xianyu.SearchItem), warn func(int64, error),
	report ...func(context.Context, string, error, *browser.Response) error,
) error {
	if maxPages < 1 {
		return errors.New("搜索页数必须为正数")
	}
	response := first
	for page := int64(1); ; page++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		var items []xianyu.SearchItem
		var err error
		if response == nil {
			err = errors.New("搜索接口没有返回响应")
		} else {
			items, err = xianyu.ParseSearch(response.Body)
		}
		if err != nil {
			err = fmt.Errorf("解析第%d页搜索结果：%w", page, err)
			if len(report) != 0 {
				err = report[0](ctx, fmt.Sprintf("解析第%d页搜索结果", page), err, response)
				if errors.Is(err, web.ErrDiagnostics) {
					return err
				}
			}
			if page == 1 {
				return err
			}
			warn(page, err)
			return nil
		}
		accept(page, items)
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(items) == 0 || page == maxPages {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		var ok bool
		response, ok, err = next(ctx)
		var blocked *ServiceError
		if web.IsStop(err) || errors.As(err, &blocked) {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return err
			}
			if !errors.Is(err, web.ErrQuota) {
				warn(page+1, err)
			}
			return nil
		}
		if !ok {
			return nil
		}
	}
}
