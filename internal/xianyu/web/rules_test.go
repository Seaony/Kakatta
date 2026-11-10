package web

import (
	"errors"
	"testing"
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
