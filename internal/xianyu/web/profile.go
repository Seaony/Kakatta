package web

import (
	"context"
	"fmt"
	"strings"

	"github.com/Seaony/Kakatta/internal/browser"
	"github.com/Seaony/Kakatta/internal/quota"
	"github.com/Seaony/Kakatta/internal/xianyu"
)

func (s Site) userHeadAPI() browser.Match {
	return browser.Match{URL: rules.API.UserHead}
}

// OwnProfile 打开自己的主页，读卖家头部接口拿昵称和头像（XIANYU.md 8.4 节）。算一次详情类访问。
// 不带 userId 的 /personal 是不是显示自己的资料还没用真实页面确认，所以调用方拿不到昵称时只记警告。
func (r *Runner) OwnProfile(ctx context.Context) (xianyu.SellerHead, error) {
	var h xianyu.SellerHead
	if err := r.acquire(ctx, quota.Detail, "自己的主页"); err != nil {
		return h, err
	}
	if err := r.check(ctx, "打开自己的主页前"); err != nil {
		return h, err
	}
	mark := r.S.Capture.Mark()
	if err := r.navigate(ctx, strings.TrimSuffix(r.Site.Home, "/")+"/personal", "打开自己的主页", rules.timeout("profile")); err != nil {
		if cerr := r.check(ctx, "打开自己的主页"); cerr != nil {
			return h, cerr
		}
		return h, fmt.Errorf("打开自己的主页：%w", err)
	}
	resp, err := r.waitAPI(ctx, mark, r.Site.userHeadAPI(), "自己的主页", rules.timeout("profile_response"))
	if err != nil {
		return h, err
	}
	h, err = xianyu.ParseSellerHead(resp.Body)
	if err != nil {
		return h, r.ReportFailure(ctx, "解析自己的资料", fmt.Errorf("解析自己的资料：%w", err), resp)
	}
	r.observeSellerHead(resp.Body)
	r.observeMissingOwnItemCount(resp.Body, h.ItemCountKnown)
	r.observation("own_profile", map[string]any{"without_user_id": true, "nickname_present": h.Nick != "", "avatar_present": h.Avatar != "", "identity_confirmed": false})
	if err := r.wait(ctx, "主页停留", r.Pace.Action); err != nil {
		return h, err
	}
	r.observeLoginHeader(ctx, "after_own_profile")
	return h, r.check(ctx, "自己的主页停留后")
}
