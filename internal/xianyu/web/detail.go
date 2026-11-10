package web

import (
	"context"
	"fmt"
	"time"

	"github.com/Seaony/Kakatta/internal/xianyu"
)

// OpenDetail 在同一个标签页里打开商品详情，读取详情接口并解析（XIANYU.md 第六节）。
// 风控和登录失效返回 IsStop 为真的错误，调用方必须立即结束整次运行；其他错误只算这个商品失败。
func (r *Runner) OpenDetail(ctx context.Context, webURL string) (xianyu.Detail, error) {
	var d xianyu.Detail
	if err := r.acquire(VisitDetail, "详情页"); err != nil {
		return d, err
	}
	if err := r.wait(ctx, r.Pace.PreDetail); err != nil {
		return d, err
	}
	mark := r.S.Capture.Mark()
	start := time.Now()
	if err := r.S.Navigate(webURL, 25*time.Second); err != nil {
		if cerr := r.check(ctx, mark, "打开详情页"); cerr != nil {
			return d, cerr
		}
		return d, fmt.Errorf("打开详情页：%w", err)
	}
	resp, err := r.waitAPI(ctx, mark, r.Site.detailAPI(), "详情页", 25)
	if err != nil {
		return d, err
	}
	d, err = xianyu.ParseDetail(resp.Body)
	if err != nil {
		return d, fmt.Errorf("解析详情：%w", err)
	}
	r.Log("打开了详情页", map[string]any{"耗时毫秒": time.Since(start).Milliseconds(), "图片数": len(d.Images), "有描述": d.Description != ""})
	if err := r.wait(ctx, r.Pace.Detail); err != nil {
		return d, err
	}
	// 停留期间页面可能弹出验证，离开前再查一次。
	return d, r.check(ctx, mark, "详情页停留后")
}
