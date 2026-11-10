package web

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/browser"
	"github.com/Seaony/Kakatta/internal/xianyu/fake"
)

func filterRunner(t *testing.T, fixture *fake.Xianyu) (*Runner, *countingBudget, context.Context) {
	t.Helper()
	runner, budget, ctx := startRunner(t, fixture)
	if err := runner.OpenHome(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Search(ctx, "相机"); err != nil {
		t.Fatal(err)
	}
	return runner, budget, ctx
}

func TestPriceInputPreservesIntegerAndCents(t *testing.T) {
	for _, tc := range []struct {
		cents int64
		text  string
	}{{0, "0"}, {1, "0.01"}, {10, "0.1"}, {100, "1"}, {500000, "5000"}, {950050, "9500.5"}, {500001, "5000.01"}} {
		if got := priceInput(tc.cents); got != tc.text {
			t.Errorf("%d 分得到 %q，应为 %q", tc.cents, got, tc.text)
		}
	}
}

func TestApplyFiltersRejectsInvalidOptionsBeforeBrowser(t *testing.T) {
	negative, low, high := int64(-1), int64(500), int64(1000)
	for _, opts := range []FilterOptions{{MinPriceCents: &negative}, {MinPriceCents: &high, MaxPriceCents: &low}, {City: "深圳"}, {Province: "广东", District: "南山"}} {
		if _, err := new(Runner).ApplyFilters(context.Background(), opts); err == nil {
			t.Fatal("无效条件必须在页面操作前拒绝")
		}
	}
}

func TestApplyFiltersSequenceAndQuota(t *testing.T) {
	f := newFake(t)
	r, budget, ctx := filterRunner(t, f)
	minimum, maximum := int64(500000), int64(950050)
	response, err := r.ApplyFilters(ctx, FilterOptions{PersonalOnly: true, FreeShipping: true, Province: "广东", City: "深圳", District: "南山", MinPriceCents: &minimum, MaxPriceCents: &maximum})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(response.URL, "step=price_tab") {
		t.Fatalf("必须返回全部筛选完成后的响应：%s", response.URL)
	}
	steps := []string{}
	for _, raw := range f.FilterActions() {
		q, _ := url.ParseQuery(raw)
		steps = append(steps, q.Get("step"))
		if q.Get("step") == "price_tab" && (q.Get("min") != "5000" || q.Get("max") != "9500.5") {
			t.Fatalf("价格不能多一个小数位或丢分：%s", raw)
		}
	}
	if got := strings.Join(steps, ","); got != "personal,shipping,region_open,region_province,region_city,region_district,region,price_tab" {
		t.Fatalf("筛选顺序不对：%s", got)
	}
	want := []string{"search:首页", "search:提交搜索", "search:发布时间筛选", "search:个人闲置", "search:包邮", "search:打开地区筛选", "search:地区选项：广东", "search:地区选项：深圳", "search:地区选项：南山", "search:提交地区筛选", "search:价格提交"}
	if strings.Join(budget.got, ",") != strings.Join(want, ",") {
		t.Fatalf("每个可能发请求的动作都应单独计数：%v", budget.got)
	}
}

func TestRegionWithoutDistrictSkipsEmptyLastColumn(t *testing.T) {
	f := newFake(t)
	f.SetRegionWithoutDistrict(true)
	r, budget, ctx := filterRunner(t, f)
	response, err := r.selectRegion(ctx, FilterOptions{Province: "广东", City: "东莞", District: "全东莞"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(response.URL, "step=region&") {
		t.Fatalf("未取得地区提交响应：%s", response.URL)
	}
	for _, action := range budget.got {
		if strings.Contains(action, "全东莞") {
			t.Fatal("没有第三级选项时不应点击或占额度")
		}
	}
}

func TestRegionIntermediateResponseCannotReplaceSubmission(t *testing.T) {
	f := newFake(t)
	f.SetFilterBehavior("region", fake.FilterBehavior{NoRequest: true})
	r, _, ctx := filterRunner(t, f)
	limited, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	response, err := r.selectRegion(limited, FilterOptions{Province: "广东"})
	if err == nil || response != nil {
		t.Fatalf("最终提交没有响应时不能用省份请求：%v %v", response, err)
	}
	if len(f.FilterActions()) != 3 {
		t.Fatalf("应确实走到最终提交才被拦住：%v", f.FilterActions())
	}
}

func TestPersonalToggleWithoutRequestDoesNotRepeat(t *testing.T) {
	f := newFake(t)
	f.SetFilterBehavior("personal", fake.FilterBehavior{NoRequest: true})
	r, _, ctx := filterRunner(t, f)
	response, err := r.ApplyFilters(ctx, FilterOptions{PersonalOnly: true, FreeShipping: true})
	if !errors.Is(err, browser.ErrResponseTimeout) || response != nil {
		t.Fatalf("开关没有响应必须失败：%v %v", response, err)
	}
	if actions := f.FilterActions(); len(actions) != 1 || !strings.Contains(actions[0], "step=personal") {
		t.Fatalf("不能再点一次或继续包邮：%v", actions)
	}
}

func TestPriceTabFallsBackToEnterOnlyWhenNoRequest(t *testing.T) {
	f := newFake(t)
	f.SetFilterBehavior("price_tab", fake.FilterBehavior{NoRequest: true})
	r, budget, ctx := filterRunner(t, f)
	minimum := int64(500000)
	response, err := r.selectPrice(ctx, &minimum, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(response.URL, "step=price_enter") {
		t.Fatalf("Tab未发请求时应回车：%s", response.URL)
	}
	if actions := f.FilterActions(); len(actions) != 2 || !strings.Contains(actions[0], "step=price_tab") || !strings.Contains(actions[1], "step=price_enter") {
		t.Fatalf("应各提交一次：%v", actions)
	}
	if len(budget.got) != 4 {
		t.Fatalf("Tab和回车都应占一次额度：%v", budget.got)
	}
}

func TestDelayedPriceTabNeverPressesEnter(t *testing.T) {
	f := newFake(t)
	f.SetFilterBehavior("price_tab", fake.FilterBehavior{Delay: 9 * time.Second})
	r, _, ctx := filterRunner(t, f)
	minimum := int64(500000)
	response, err := r.selectPrice(ctx, &minimum, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(response.URL, "step=price_tab") || len(f.FilterActions()) != 1 {
		t.Fatalf("请求已发出只能等，不能多按回车：%s %v", response.URL, f.FilterActions())
	}
}

func TestPriceChecksActualDisplayedValue(t *testing.T) {
	f := newFake(t)
	f.SetFilterBehavior("price_tab", fake.FilterBehavior{CorruptPrice: true})
	r, _, ctx := filterRunner(t, f)
	minimum := int64(500000)
	response, err := r.selectPrice(ctx, &minimum, nil)
	if err == nil || response != nil {
		t.Fatalf("页面把价格改错时必须失败：%v %v", response, err)
	}
}

func TestFiltersStopOnRiskBeforeRemainingSteps(t *testing.T) {
	f := newFake(t)
	f.SetFilterBehavior("personal", fake.FilterBehavior{Ret: "RGV587::需要验证"})
	r, _, ctx := filterRunner(t, f)
	response, err := r.ApplyFilters(ctx, FilterOptions{PersonalOnly: true, FreeShipping: true})
	if !errors.Is(err, ErrNeedVerify) || response != nil {
		t.Fatalf("筛选风控必须传出：%v %v", response, err)
	}
	if len(f.FilterActions()) != 1 {
		t.Fatalf("风控后不能继续筛选：%v", f.FilterActions())
	}
}
