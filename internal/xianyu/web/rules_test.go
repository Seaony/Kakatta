package web

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/xianyu"
)

func TestPageURLRisk(t *testing.T) {
	for _, tc := range []struct {
		url  string
		want error
	}{
		{"https://www.goofish.com/search?q=相机", nil},
		{"https://passport.goofish.com/login", ErrLoginExpired},
		{"https://www.goofish.com/mini_login", ErrLoginExpired},
		{"https://www.goofish.com/PUNISH?action=captcha", ErrNeedVerify},
		{"https://www.goofish.com/_____tmd_____", ErrNeedVerify},
		{"https://www.goofish.com/punish?action=deny", ErrDenied},
		{"https://www.goofish.com/_____tmd_____?ACTION=DENY", ErrDenied},
	} {
		if got := pageURLRisk(tc.url, "测试"); !errors.Is(got, tc.want) {
			t.Errorf("%s：得到 %v，应为 %v", tc.url, got, tc.want)
		}
	}
}

func TestKeywordComparisonNormalizesWidthAndWhitespace(t *testing.T) {
	for _, pair := range [][2]string{
		{"ＭａｃＢｏｏｋ　苹果", "MacBook 苹果"},
		{" 相机\t 镜头\n", "相机 镜头"},
	} {
		if normalizeKeyword(pair[0]) != normalizeKeyword(pair[1]) {
			t.Errorf("全角半角和连续空白不应造成关键词不一致：%q，%q", pair[0], pair[1])
		}
	}
	if normalizeKeyword("相机一") == normalizeKeyword("相机二") {
		t.Fatal("不同关键词不能被规整成相同的词")
	}
}

func TestBundledRulesAreVersionedData(t *testing.T) {
	if !reflect.DeepEqual(rules.WebRules, xianyu.WebProtocol()) {
		t.Fatal("网页层未使用统一规则")
	}
	if rules.Version != 3 || rules.API.UserItems != "mtop.idle.web.xyh.item.list" ||
		rules.API.UserRatings != "mtop.idle.web.trade.rate.list" ||
		rules.Selectors.SellerRatings != "//div[text()='信用及评价']/ancestor::li" {
		t.Fatal("卖家规则缺失或版本错误")
	}
	if rules.timeout("seller_items_response") != 8*time.Second || rules.timeout("seller_ratings_response") != 8*time.Second ||
		rules.timeout("profile") != 20*time.Second || rules.timeout("profile_response") != 15*time.Second {
		t.Fatal("卖家页面等待时间错误")
	}
}
