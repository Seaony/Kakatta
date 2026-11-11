package web

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"

	"github.com/Seaony/Kakatta/internal/browser"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/quota"
	"github.com/Seaony/Kakatta/internal/xianyu"
)

func (s Site) sellerURL(sellerID string) (string, error) {
	sellerID = strings.TrimSpace(sellerID)
	if sellerID == "" || strings.ContainsAny(sellerID, "\x00\r\n") {
		return "", fmt.Errorf("卖家编号无效")
	}
	base, err := url.Parse(s.Home)
	if err != nil || base == nil || base.Hostname() == "" || base.User != nil ||
		(base.Scheme != "http" && base.Scheme != "https") || base.RawQuery != "" || base.Fragment != "" {
		return "", fmt.Errorf("卖家主页站点无效")
	}
	target := base.ResolveReference(&url.URL{Path: "/personal", RawQuery: url.Values{"userId": {sellerID}}.Encode()})
	return target.String(), nil
}

func (s Site) userItemsAPI() browser.Match {
	return browser.Match{URL: rules.API.UserItems}
}

func (s Site) userRatingsAPI() browser.Match {
	return browser.Match{URL: rules.API.UserRatings}
}

// OpenSeller 只读取同一标签页里自动请求的第一页，不滚动加载更多。
func (r *Runner) OpenSeller(ctx context.Context, sellerID string) (out SellerPage, err error) {
	out = SellerPage{Warnings: []locales.Message{}}
	defer func() {
		if err != nil && !IsStop(err) {
			if canceled := sellerContextError(ctx); canceled != nil {
				err = canceled
			}
		}
	}()
	if err := sellerContextError(ctx); err != nil {
		return out, err
	}
	target, err := r.Site.sellerURL(sellerID)
	if err != nil {
		return out, err
	}
	if err := r.check(ctx, "打开卖家主页前"); err != nil {
		return out, err
	}
	if err := r.acquire(ctx, quota.Detail, "卖家主页"); err != nil {
		return out, err
	}
	if err := sellerContextError(ctx); err != nil {
		return out, err
	}
	// 商品接口可能早于头部返回，两者必须使用导航前的同一个标记。
	mark := r.S.Capture.Mark()
	out.Opened = true
	if err := r.watch(ctx, "打开卖家主页", func(navCtx context.Context) error {
		_, err := r.S.NavigateContext(navCtx, target, rules.timeout("profile"))
		return err
	}); err != nil {
		stop := out.recordFailure(ctx, "run.seller_page_failed", err)
		return out, stop
	}
	headResponse, err := r.waitAPI(ctx, mark, r.Site.userHeadAPI(), "卖家头部", rules.timeout("profile_response"))
	if err != nil {
		if stop := out.recordFailure(ctx, "run.seller_head_failed", err); stop != nil {
			return out, stop
		}
	} else {
		head, err := xianyu.ParseSellerHead(headResponse.Body)
		if err != nil {
			if failure := r.ReportFailure(ctx, "解析卖家头部", err, headResponse); errors.Is(failure, ErrDiagnostics) {
				return out, failure
			}
			out.warn("run.seller_head_failed")
		} else {
			r.observeSellerHead(headResponse.Body)
			out.Head = &head
		}
	}
	if err := r.wait(ctx, "卖家主页停留", r.Pace.Action); err != nil {
		stop := out.recordFailure(ctx, "run.seller_page_failed", err)
		return out, stop
	}
	itemsResponse, err := r.waitAPI(ctx, mark, r.Site.userItemsAPI(), "卖家商品", rules.timeout("seller_items_response"))
	if err != nil {
		if stop := out.recordFailure(ctx, "run.seller_items_failed", err); stop != nil {
			return out, stop
		}
	} else {
		items, err := xianyu.ParseSellerItems(itemsResponse.Body)
		if err != nil || !items.ListKnown {
			if err == nil {
				err = fmt.Errorf("卖家商品缺少列表字段")
			}
			if failure := r.ReportFailure(ctx, "解析卖家商品", err, itemsResponse); errors.Is(failure, ErrDiagnostics) {
				return out, failure
			}
			out.warn("run.seller_items_failed")
		} else {
			if items.Items == nil {
				items.Items = []xianyu.SellerItem{}
			}
			out.Items = &items
		}
	}
	if err := r.readSellerRatings(ctx, &out); err != nil {
		return out, err
	}
	if err := r.check(ctx, "读取卖家资料后"); err != nil {
		stop := out.recordFailure(ctx, "run.seller_page_failed", err)
		return out, stop
	}
	return out, nil
}

func (r *Runner) readSellerRatings(ctx context.Context, out *SellerPage) error {
	var tab *chromedp.Node
	err := r.watch(ctx, "卖家评价标签", func(findCtx context.Context) error {
		var err error
		tab, err = r.H.FirstVisible(findCtx, rules.Selectors.SellerRatings, rules.timeout("element"))
		if err != nil {
			return err
		}
		// 只为当前画面内的评价标签申请额度。
		visible, err := chromedp.Run(r.S.Context(), func(_ context.Context, target *chromedp.Target) (bool, error) {
			metrics, err := cdp.Call(findCtx, target, page.GetLayoutMetrics, cdp.Empty{})
			if err != nil {
				return false, err
			}
			viewport := metrics.CSSVisualViewport
			if viewport == nil || viewport.ClientWidth <= 0 || viewport.ClientHeight <= 0 {
				return false, fmt.Errorf("无法读取当前画面大小")
			}
			content, err := cdp.Call(findCtx, target, dom.GetContentQuads, dom.GetContentQuadsParams{BackendNodeID: tab.BackendNodeID})
			if err != nil {
				return false, err
			}
			if len(content.Quads) == 0 {
				return false, nil
			}
			for _, quad := range content.Quads {
				for i := 0; i < len(quad); i += 2 {
					if quad[i] < viewport.OffsetX || quad[i] > viewport.OffsetX+viewport.ClientWidth || quad[i+1] < viewport.OffsetY || quad[i+1] > viewport.OffsetY+viewport.ClientHeight {
						return false, nil
					}
				}
			}
			return true, nil
		})
		if err == nil && !visible {
			return browser.ErrNotFound
		}
		return err
	})
	if err != nil {
		if errors.Is(err, browser.ErrNotFound) && ctx.Err() == nil {
			out.warn("run.seller_ratings_missing")
			return nil
		}
		return out.recordFailure(ctx, "run.seller_ratings_failed", err)
	}
	if err := r.acquire(ctx, quota.Detail, "卖家评价"); err != nil {
		if errors.Is(err, ErrQuota) {
			out.warn("run.seller_ratings_quota")
			return nil
		}
		return err
	}
	mark := r.S.Capture.Mark()
	if err := r.watch(ctx, "点击卖家评价", func(clickCtx context.Context) error {
		return r.H.ClickNoScroll(clickCtx, tab)
	}); err != nil {
		return out.recordFailure(ctx, "run.seller_ratings_failed", err)
	}
	if err := r.wait(ctx, "卖家评价标签", r.Pace.Action); err != nil {
		return out.recordFailure(ctx, "run.seller_ratings_failed", err)
	}
	response, err := r.waitAPI(ctx, mark, r.Site.userRatingsAPI(), "卖家评价", rules.timeout("seller_ratings_response"))
	if err != nil {
		return out.recordFailure(ctx, "run.seller_ratings_failed", err)
	}
	ratings, err := xianyu.ParseSellerRatings(response.Body)
	if err != nil || !ratings.ListKnown {
		if err == nil {
			err = fmt.Errorf("卖家评价缺少列表字段")
		}
		if failure := r.ReportFailure(ctx, "解析卖家评价", err, response); errors.Is(failure, ErrDiagnostics) {
			return failure
		}
		out.warn("run.seller_ratings_failed")
		return nil
	}
	r.observeRatings(response.Body)
	if ratings.Recent == nil {
		ratings.Recent = []xianyu.Rating{}
	}
	out.Ratings = &ratings
	return nil
}

// 只保存固定原因，错误里的网址、响应和代理凭据不能进入运行提示。
func (p *SellerPage) recordFailure(ctx context.Context, code string, err error) error {
	if IsStop(err) {
		return err
	}
	if err := sellerContextError(ctx); err != nil {
		return err
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	p.warn(code)
	return nil
}

func (p *SellerPage) warn(code string) {
	p.Warnings = append(p.Warnings, locales.Message{Code: code, Args: map[string]any{}})
}

func sellerContextError(ctx context.Context) error {
	err := ctx.Err()
	if err == nil {
		return nil
	}
	if cause := context.Cause(ctx); cause != err {
		return errors.Join(err, cause)
	}
	return err
}
