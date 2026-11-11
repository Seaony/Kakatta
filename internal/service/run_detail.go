package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/analysis/client"
	"github.com/Seaony/Kakatta/internal/analysis/images"
	"github.com/Seaony/Kakatta/internal/analysis/prompts"
	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/xianyu"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
	"golang.org/x/net/proxy"
)

type detailInput struct {
	Task     Task
	Finding  Finding
	Item     Listing
	Seller   *SellerProfile
	Blocked  bool
	Now      int64
	Snapshot settings.Snapshot
}

type detailMarketSnapshot struct {
	profile analysis.Profile
	result  *analysis.MarketResult
}

type detailModelJob struct {
	index     int
	profile   analysis.Profile
	originals analysis.EvidenceOriginals
	done      chan struct{}
	result    client.Result
	err       error
}

var errDetailWorkChanged = errors.New("详情工作已变化")

func (s *Service) runDetail(ctx context.Context, run *runExecution) error {
	return s.runDetailWith(ctx, run, func(ctx context.Context, target string, started func()) (xianyu.Detail, error) {
		if run.Resource == nil || run.Resource.Runner() == nil {
			return xianyu.Detail{}, storageFailure(errors.New("详情浏览器未就绪"))
		}
		return run.Resource.Runner().OpenDetailWithStart(ctx, target, started)
	})
}

func (s *Service) runDetailWith(ctx context.Context, run *runExecution, open func(context.Context, string, func()) (xianyu.Detail, error)) error {
	limit, err := accountSetting(run.Config, "max_details_per_run")
	if err != nil {
		return storageFailure(err)
	}
	var market detailMarketSnapshot
	modelCtx, cancelModels := context.WithCancel(ctx)
	var workers sync.WaitGroup
	stopModels := func() {
		cancelModels()
		workers.Wait()
	}
	defer stopModels()
	var pending []*detailModelJob
	flush := func(leave int, wait bool) (bool, error) {
		for len(pending) > leave {
			job := pending[0]
			if !wait {
				select {
				case <-job.done:
				default:
					return false, nil
				}
			}
			work := run.P[job.index]
			err := s.finishDetailModel(ctx, run, work, job)
			logRunFailure(run, "商品判断", err)
			pending[0] = nil
			pending = pending[1:]
			if errors.Is(err, client.ErrNoProvider) || errors.Is(err, client.ErrDailyLimit) {
				cause := "no_provider"
				if errors.Is(err, client.ErrDailyLimit) {
					cause = "daily_limit"
				}
				stopModels()
				return true, s.detailAwait(ctx, run.P[job.index:], cause)
			}
			var failure *client.Failure
			if errors.As(err, &failure) {
				err = s.detailFailure(ctx, work, failure.Last, failure.Retryable, false)
			}
			if err != nil {
				return true, err
			}
		}
		return false, nil
	}
	for index, work := range run.P {
		if err := ctx.Err(); err != nil {
			return err
		}
		if stop, err := flush(0, false); stop {
			return err
		}
		input, current, err := s.detailInput(ctx, work)
		if err != nil {
			return err
		}
		if !current {
			continue
		}
		if input.Item.IsDismissed || input.Blocked {
			if stop, err := flush(0, true); stop {
				return err
			}
			code := "findings.blocked_seller"
			if input.Item.IsDismissed {
				code = "findings.dismissed"
			}
			reason := findingMessage(code, nil)
			if err := s.detailConclusion(ctx, run, work, Conclusion{Skipped: &reason}); err != nil {
				return err
			}
			continue
		}
		if !detailCached(input.Item.DetailFetchedAt, input.Now) {
			if run.CachedOnly {
				break
			}
			if run.DetailCount+run.SellerPageCount >= limit {
				break
			}
			// Runner 自己申请额度并等待，编排层只限制本次打开数量。
			opened := false
			result, openErr := open(ctx, input.Item.DesktopURL, func() { opened = true })
			if run.Resource != nil {
				openErr = reportBrowserFailure(ctx, run.Resource.Runner(), "打开商品详情", openErr)
			}
			if opened {
				run.DetailCount++
			}
			if err := detailStop(ctx, openErr); err != nil {
				return err
			}
			if errors.Is(openErr, web.ErrQuota) {
				break
			}
			if openErr != nil {
				if stop, err := flush(0, true); stop {
					return err
				}
				if err := s.detailFailure(ctx, work, findingMessage("run.detail_failed", nil), true, false); err != nil {
					return err
				}
				continue
			}
			if err := s.detailSave(ctx, work, result); err != nil {
				return err
			}
			input, current, err = s.detailInput(ctx, work)
			if err != nil {
				return err
			}
			if !current {
				continue
			}
		}
		if input.Item.IsDismissed || input.Blocked {
			if stop, err := flush(0, true); stop {
				return err
			}
			code := "findings.blocked_seller"
			if input.Item.IsDismissed {
				code = "findings.dismissed"
			}
			reason := findingMessage(code, nil)
			if err := s.detailConclusion(ctx, run, work, Conclusion{Skipped: &reason}); err != nil {
				return err
			}
			continue
		}
		layer := analysis.JudgeKeyword(input.Item.Title, findingString(input.Item.Description), input.Task.MatchKeywords, input.Task.ExcludeKeywords)
		if input.Task.JudgeMode == analysis.ModeKeyword || layer.Rejected && layer.RejectKind == analysis.RejectExcluded {
			if stop, err := flush(0, true); stop {
				return err
			}
			if err := s.detailConclusion(ctx, run, work, Conclusion{Layer: &layer}); err != nil {
				return err
			}
			continue
		}
		pendingLimit, err := accountSetting(input.Snapshot.Values, "ai.max_concurrent_requests")
		if err != nil {
			return storageFailure(err)
		}
		seller, pictures, err := s.prepareDetailModel(ctx, run, work, input, limit)
		if err != nil {
			return err
		}
		// 先准备下一件，再等出一个提交位置。全局模型名额仍由模型服务分配。
		if int64(len(pending)) >= pendingLimit {
			if stop, err := flush(int(pendingLimit)-1, true); stop {
				return err
			}
		}
		fair, err := s.detailFair(ctx, work, input.Item, &market)
		if errors.Is(err, errDetailWorkChanged) {
			continue
		}
		if err != nil {
			return err
		}
		source := detailProductSource(input.Item, seller)
		request, originals, err := prompts.BuildAnalysis(run.Prompts, &work.TaskID, input.Task.Profile, source, fair, pictures)
		if err != nil {
			return storageFailure(err)
		}
		job := &detailModelJob{index: index, profile: input.Task.Profile, originals: originals, done: make(chan struct{})}
		runID := run.Ref.RunID
		if err := s.guardedWork(ctx, run, "商品判断", func(workCtx context.Context) error {
			if err := workCtx.Err(); err != nil {
				return err
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer close(job.done)
				job.result, job.err = s.completeRunModel(modelCtx, runID, request)
			}()
			return nil
		}); err != nil {
			return err
		}
		pending = append(pending, job)
	}
	_, err = flush(0, true)
	return err
}

func detailStop(ctx context.Context, err error) error {
	if web.IsStop(err) {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	var failure *ServiceError
	if errors.As(err, &failure) {
		return err
	}
	return nil
}

func detailCached(fetched *int64, now int64) bool {
	return fetched != nil && *fetched > 0 && *fetched <= now && now-*fetched < int64(24*time.Hour/time.Millisecond)
}

func (s *Service) detailInput(ctx context.Context, work WorkRef) (detailInput, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return detailInput{}, false, storageFailure(err)
	}
	defer tx.Rollback()
	current, err := titleWorkCurrentTx(ctx, tx, work, StageDetail)
	if err != nil || !current {
		return detailInput{}, false, err
	}
	var input detailInput
	input.Task, input.Finding, err = validateWorkTx(ctx, tx, work, StageDetail)
	if err != nil {
		return input, false, storageFailure(err)
	}
	input.Item, err = loadListingTx(ctx, tx, input.Finding.ItemID)
	if err != nil {
		return input, false, storageFailure(err)
	}
	if input.Item.SellerID != nil {
		input.Blocked, err = isSellerBlockedTx(ctx, tx, *input.Item.SellerID)
		if err == nil && !input.Blocked && !input.Item.IsDismissed && input.Task.JudgeMode == analysis.ModeAI {
			input.Seller, err = loadSellerTx(ctx, tx, *input.Item.SellerID)
		}
		if err != nil {
			return input, false, storageFailure(err)
		}
	}
	snapshot, clock, now, err := s.observeNowTx(ctx, tx)
	if err != nil {
		return input, false, err
	}
	input.Now = now.UnixMilli()
	input.Snapshot = snapshot
	if err := appsettings.SaveClock(ctx, tx, clock); err != nil {
		return input, false, storageFailure(err)
	}
	return input, true, storageFailure(tx.Commit())
}

func (s *Service) detailSave(ctx context.Context, work WorkRef, result xianyu.Detail) error {
	return s.detailWrite(ctx, work, func(tx *sql.Tx, now int64) error {
		observation := DetailObservation{ItemID: "", FetchedAt: now}
		_, finding, err := validateWorkTx(ctx, tx, work, StageDetail)
		if err != nil {
			return err
		}
		observation.ItemID = finding.ItemID
		if result.Images != nil {
			observation.ImageURLs = append([]string{}, result.Images...)
		}
		if result.Description != "" {
			observation.Description = &result.Description
		}
		if result.WantKnown {
			count := int64(result.WantCnt)
			observation.WantCount = &count
		}
		if result.BrowseKnown {
			count := int64(result.BrowseCnt)
			observation.BrowseCount = &count
		}
		if result.SellerID != "" {
			observation.SellerID = &result.SellerID
		}
		if result.RegDaysKnown && result.RegDays > 0 {
			days := int64(result.RegDays)
			observation.SellerRegistrationDays = &days
		}
		if result.Zhima != "" {
			observation.ZhimaLevel = &result.Zhima
		}
		observation.SellerDetail = result.SellerMetrics
		return saveDetailTx(ctx, tx, work, observation)
	})
}

func (s *Service) detailWrite(ctx context.Context, work WorkRef, write func(*sql.Tx, int64) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storageFailure(err)
	}
	defer tx.Rollback()
	current, err := titleWorkCurrentTx(ctx, tx, work, StageDetail)
	if err != nil || !current {
		return err
	}
	_, clock, now, err := s.observeNowTx(ctx, tx)
	if err != nil {
		return err
	}
	if err := write(tx, now.UnixMilli()); err != nil {
		return storageFailure(err)
	}
	if err := appsettings.SaveClock(ctx, tx, clock); err != nil {
		return storageFailure(err)
	}
	if err := tx.Commit(); err != nil {
		return storageFailure(err)
	}
	_ = s.emit("finding.changed", map[string]any{"task_id": work.TaskID, "finding_ids": []int64{work.FindingID}})
	return nil
}

func (s *Service) detailConclusion(ctx context.Context, run *runExecution, work WorkRef, result Conclusion) error {
	newConclusion := false
	layer1Rejected := false
	err := s.detailWrite(ctx, work, func(tx *sql.Tx, now int64) error {
		var err error
		newConclusion, err = s.saveConclusionTx(ctx, tx, work, result, now)
		if err == nil && newConclusion {
			finding, loadErr := loadFindingTx(ctx, tx, work.FindingID)
			if loadErr != nil {
				return loadErr
			}
			layer1Rejected = finding.Decision == analysis.DecisionRejected && finding.DecidedLayer != nil && *finding.DecidedLayer == 1
		}
		return err
	})
	if err == nil && newConclusion {
		run.FirstConclusionIDs = appendTitleIDs(run.FirstConclusionIDs, []int64{work.FindingID})
		run.AnalyzedCount++
		if layer1Rejected {
			run.SearchSaved.Layer1Rejected++
		}
	}
	return err
}

func (s *Service) detailFailure(ctx context.Context, work WorkRef, reason locales.Message, retryable, awaitAI bool) error {
	return s.detailWrite(ctx, work, func(tx *sql.Tx, now int64) error {
		return saveFindingFailureTx(ctx, tx, work, StageDetail, reason, retryable, awaitAI, now)
	})
}

func (s *Service) detailAwait(ctx context.Context, works []WorkRef, cause string) error {
	for _, work := range works {
		if err := s.detailFailure(ctx, work, findingMessage("await_ai", map[string]any{"cause": cause}), false, true); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) prepareDetailModel(ctx context.Context, run *runExecution, work WorkRef, input detailInput, limit int64) (*SellerProfile, []string, error) {
	if s.models == nil {
		return nil, nil, fmt.Errorf("%w：商品判断服务未配置", client.ErrInfrastructure)
	}
	seller, err := s.detailSeller(ctx, run, work, input, limit)
	if err != nil {
		return nil, nil, err
	}
	count, err := accountSetting(input.Snapshot.Values, "ai.image_count")
	if err != nil {
		return nil, nil, storageFailure(err)
	}
	transport, err := detailImageTransport(run.Spec.Proxy)
	if err != nil {
		return nil, nil, storageFailure(err)
	}
	defer transport.CloseIdleConnections()
	preparer, err := images.New(images.Dependencies{HTTP: &http.Client{Transport: transport}})
	if err != nil {
		return nil, nil, storageFailure(err)
	}
	var pictures images.Result
	err = s.guardedWork(ctx, run, "准备详情图片", func(workCtx context.Context) error {
		var prepareErr error
		pictures, prepareErr = preparer.Prepare(workCtx, input.Item.ImageURLs, count)
		return prepareErr
	})
	if err != nil {
		return nil, nil, err
	}
	for _, skipped := range pictures.Skipped {
		run.Warnings = append(run.Warnings, skipped.Reason)
	}
	return seller, pictures.DataURLs(), nil
}

func (s *Service) finishDetailModel(ctx context.Context, run *runExecution, work WorkRef, job *detailModelJob) error {
	err := s.guardedWork(ctx, run, "商品判断", func(workCtx context.Context) error {
		select {
		case <-job.done:
			return job.err
		case <-workCtx.Done():
			return workCtx.Err()
		}
	})
	if stopped := detailStop(ctx, err); stopped != nil {
		return stopped
	}
	if err != nil {
		return err
	}
	result := job.result
	facts, warnings, err := analysis.ParseFacts([]byte(result.Text), job.profile, job.originals)
	if err != nil {
		return fmt.Errorf("%w：商品判断返回未通过校验：%v", client.ErrInfrastructure, err)
	}
	run.Warnings = append(run.Warnings, warnings...)
	conclusion := Conclusion{RequestProfile: job.profile, Facts: &facts, Warnings: warnings}
	if result.ProviderID > 0 {
		conclusion.ProviderID = &result.ProviderID
	}
	if result.Model != "" {
		conclusion.Model = &result.Model
	}
	return s.detailConclusion(ctx, run, work, conclusion)
}

func detailImageTransport(fixed *url.URL) (*http.Transport, error) {
	dialer := &net.Dialer{}
	transport := &http.Transport{DialContext: dialer.DialContext}
	if fixed == nil {
		return transport, nil
	}
	copy := *fixed
	if copy.Hostname() == "" || copy.Opaque != "" || copy.Fragment != "" || copy.RawQuery != "" || copy.Path != "" && copy.Path != "/" {
		return nil, errors.New("图片代理配置无效")
	}
	switch copy.Scheme {
	case "http":
		transport.Proxy = http.ProxyURL(&copy)
	case "socks5":
		upstream, err := proxy.FromURL(&copy, dialer)
		if err != nil {
			return nil, errors.New("图片代理配置无效")
		}
		contextDialer, ok := upstream.(proxy.ContextDialer)
		if !ok {
			return nil, errors.New("图片代理不支持取消")
		}
		transport.DialContext = contextDialer.DialContext
	default:
		return nil, errors.New("图片代理类型无效")
	}
	return transport, nil
}

func (s *Service) detailFair(ctx context.Context, work WorkRef, item Listing, market *detailMarketSnapshot) (*analysis.FairPrice, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, storageFailure(err)
	}
	defer tx.Rollback()
	current, err := titleWorkCurrentTx(ctx, tx, work, StageDetail)
	if err != nil {
		return nil, err
	}
	if !current {
		return nil, errDetailWorkChanged
	}
	task, _, err := validateWorkTx(ctx, tx, work, StageDetail)
	if err != nil {
		return nil, storageFailure(err)
	}
	if market.result == nil {
		snapshot, clock, now, err := s.observeNowTx(ctx, tx)
		if err != nil {
			return nil, err
		}
		var timezone string
		if err := json.Unmarshal(snapshot.Values["general.timezone"], &timezone); err != nil {
			return nil, storageFailure(err)
		}
		result, err := loadMarketTx(ctx, tx, task, now.UnixMilli(), timezone)
		if err != nil {
			return nil, storageFailure(err)
		}
		if err := appsettings.SaveClock(ctx, tx, clock); err != nil {
			return nil, storageFailure(err)
		}
		market.profile, market.result = task.Profile, &result
	}
	specs, err := loadItemSpecsTx(ctx, tx, task.ID, item.ItemID)
	if err != nil {
		return nil, storageFailure(err)
	}
	var fair *analysis.FairPrice
	if specs != nil && specs.ConfigKey != nil {
		fair = fairForItem(market.profile, specs.ItemSpecs, *market.result)
	}
	return fair, storageFailure(tx.Commit())
}

func detailProductSource(item Listing, seller *SellerProfile) prompts.ProductSource {
	source := prompts.ProductSource{Title: item.Title, PriceCents: item.Price, Description: item.Description,
		PublishedAtMS: item.PublishedAt, Area: item.Area, Tags: item.Tags, WantCount: item.WantCount,
		BrowseCount: item.BrowseCount, URL: &item.DesktopURL}
	if item.OriginalPriceText != nil {
		if price, known := xianyu.ParsePriceCents(*item.OriginalPriceText); known {
			source.OriginalPriceCents = &price
		}
	}
	profile := &prompts.SellerSource{Nickname: item.SellerNick, ZhimaLevel: item.ZhimaLevel, DetailMetrics: item.SellerDetail}
	if item.SellerRegistrationDays != nil && *item.SellerRegistrationDays > 0 {
		days := int(*item.SellerRegistrationDays)
		profile.RegistrationDays = &days
	}
	if seller == nil {
		if profile.Nickname != nil || profile.ZhimaLevel != nil || profile.RegistrationDays != nil || profile.DetailMetrics != nil {
			source.Seller = profile
		}
		return source
	}
	if seller.Nick != nil {
		profile.Nickname = seller.Nick
	}
	profile.Introduction = seller.Introduction
	profile.SellerCredit, profile.BuyerCredit = seller.SellerCredit, seller.BuyerCredit
	profile.ItemCount, profile.OnSaleCount, profile.SoldCount = seller.ItemCount, seller.OnSaleCount, seller.SoldCount
	profile.RateCount, profile.SellerGoodRate, profile.BuyerGoodRate = seller.RateCount, seller.SellerGoodRate, seller.BuyerGoodRate
	if seller.RecentItems != nil {
		profile.RecentItems = []prompts.RecentItemSource{}
		for _, item := range seller.RecentItems {
			profile.RecentItems = append(profile.RecentItems, prompts.RecentItemSource{Title: item.Title, PriceCents: item.Price, Status: item.SaleStatus})
		}
	}
	if seller.RecentRates != nil {
		profile.RecentRates = []prompts.RecentRateSource{}
		for _, rate := range seller.RecentRates {
			profile.RecentRates = append(profile.RecentRates, prompts.RecentRateSource{Feedback: rate.Text, Role: rate.Role, Kind: rate.Kind})
		}
	}
	source.Seller = profile
	return source
}

func (s *Service) detailSeller(ctx context.Context, run *runExecution, work WorkRef, input detailInput, limit int64) (*SellerProfile, error) {
	return s.detailSellerWith(ctx, run, work, input, limit, func(ctx context.Context, sellerID string) (web.SellerPage, error) {
		if run.Resource == nil || run.Resource.Runner() == nil {
			return web.SellerPage{}, storageFailure(errors.New("卖家浏览器未就绪"))
		}
		return run.Resource.Runner().OpenSeller(ctx, sellerID)
	})
}

func (s *Service) detailSellerWith(ctx context.Context, run *runExecution, work WorkRef, input detailInput, limit int64, open func(context.Context, string) (web.SellerPage, error)) (*SellerProfile, error) {
	hours, err := accountSetting(input.Snapshot.Values, "crawl.seller_cache_hours")
	if err != nil {
		return nil, storageFailure(err)
	}
	if input.Seller != nil && input.Seller.FetchedAt <= input.Now && input.Now-input.Seller.FetchedAt < hours*int64(time.Hour/time.Millisecond) {
		return input.Seller, nil
	}
	if input.Item.SellerID == nil || *input.Item.SellerID == "" || run.CachedOnly {
		run.Warnings = append(run.Warnings, findingMessage("run.seller_missing", nil))
		return nil, nil
	}
	if run.DetailCount+run.SellerPageCount >= limit {
		run.Warnings = append(run.Warnings, findingMessage("run.seller_page_limit", nil))
		return nil, nil
	}
	page, err := open(ctx, *input.Item.SellerID)
	if page.Opened {
		run.SellerPageCount++
	}
	if stopped := detailStop(ctx, err); stopped != nil {
		return nil, stopped
	}
	run.Warnings = append(run.Warnings, page.Warnings...)
	if errors.Is(err, web.ErrQuota) {
		run.Warnings = append(run.Warnings, findingMessage("run.seller_quota", nil))
		return nil, nil
	}
	if err != nil {
		run.Warnings = append(run.Warnings, findingMessage("run.seller_failed", nil))
		return nil, nil
	}
	profile := sellerPageProfile(*input.Item.SellerID, page, input.Now)
	if profile == nil {
		run.Warnings = append(run.Warnings, findingMessage("run.seller_missing", nil))
		return nil, nil
	}
	if err := s.detailWrite(ctx, work, func(tx *sql.Tx, now int64) error {
		profile.FetchedAt = now
		return saveSellerTx(ctx, tx, work, *profile)
	}); err != nil {
		return nil, err
	}
	return profile, nil
}
