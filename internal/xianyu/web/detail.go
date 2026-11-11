package web

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Seaony/Kakatta/internal/quota"
	"github.com/Seaony/Kakatta/internal/xianyu"
)

// OpenDetail 在同一个标签页里打开商品详情，读取详情接口并解析（XIANYU.md 第六节）。
// 风控和登录失效返回 IsStop 为真的错误，调用方必须立即结束整次运行；其他错误只算这个商品失败。
func (r *Runner) OpenDetail(ctx context.Context, webURL string) (xianyu.Detail, error) {
	return r.OpenDetailWithStart(ctx, webURL, nil)
}

// OpenDetailWithStart 在开始导航时调用 started。预检查或额度拦住时不会调用。
func (r *Runner) OpenDetailWithStart(ctx context.Context, webURL string, started func()) (xianyu.Detail, error) {
	var d xianyu.Detail
	target, err := r.Site.itemURL(webURL)
	if err != nil {
		return d, err
	}
	if err := r.acquire(ctx, quota.Detail, "详情页"); err != nil {
		return d, err
	}
	if err := r.wait(ctx, "打开详情前", r.Pace.PreDetail); err != nil {
		return d, err
	}
	// 等待期间可能出了验证，打开之前再查一次，不带着风控去访问下一个页面。
	if err := r.check(ctx, "打开详情前"); err != nil {
		return d, err
	}
	mark := r.S.Capture.Mark()
	start := time.Now()
	if err := r.watch(ctx, "打开详情页", func(navCtx context.Context) error {
		_, err := r.S.NavigateContextWithStart(navCtx, target, rules.timeout("detail"), started)
		return err
	}); err != nil {
		if cerr := r.check(ctx, "打开详情页"); cerr != nil {
			return d, cerr
		}
		return d, fmt.Errorf("打开详情页：%w", err)
	}
	resp, err := r.waitAPI(ctx, mark, r.Site.detailAPI(), "详情页", rules.timeout("detail"))
	if err != nil {
		return d, err
	}
	d, err = xianyu.ParseDetail(resp.Body)
	if err != nil {
		return d, r.ReportFailure(ctx, "解析详情", fmt.Errorf("解析详情：%w", err), resp)
	}
	if !r.observeDetail(resp.Body) {
		if failure := r.ReportFailure(ctx, "详情字段缺失", fmt.Errorf("详情缺少 itemDO 或描述字符串字段"), resp); errors.Is(failure, ErrDiagnostics) {
			return d, failure
		}
	}
	r.Log("打开了详情页", map[string]any{"耗时毫秒": time.Since(start).Milliseconds(), "图片数": len(d.Images), "有描述": d.Description != ""})
	if err := r.wait(ctx, "详情页停留", r.Pace.Detail); err != nil {
		return d, err
	}
	// 停留期间页面可能弹出验证，离开前再查一次。
	return d, r.check(ctx, "详情页停留后")
}
