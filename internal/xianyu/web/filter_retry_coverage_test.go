package web

import (
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/Seaony/Kakatta/internal/browser"
	"github.com/Seaony/Kakatta/internal/xianyu/fake"
)

func shortenFilterRetryWaits(t *testing.T) {
	t.Helper()
	original := rules
	rules.Timeouts = make(map[string]int, len(original.Timeouts))
	for key, value := range original.Timeouts {
		rules.Timeouts[key] = value
	}
	rules.Timeouts["filter_response"] = 400
	rules.Timeouts["response_quiet"] = 40
	t.Cleanup(func() { rules = original })
}

func TestRepeatableFilterRetriesOnceAndChargesEachAttempt(t *testing.T) {
	for _, secondResponds := range []bool{true, false} {
		name := "第二次仍无请求"
		if secondResponds {
			name = "第二次成功"
		}
		t.Run(name, func(t *testing.T) {
			shortenFilterRetryWaits(t)
			fixture := newFake(t)
			runner, budget, ctx := filterRunner(t, fixture)
			calls := 0
			response, err := runner.filterStep(ctx, "可重做筛选", true, func() error {
				calls++
				if calls == 1 || !secondResponds {
					return nil
				}
				if err := runner.clickText(ctx, rules.Text.PublishMenu); err != nil {
					return err
				}
				return runner.clickText(ctx, rules.Text.PublishLatest)
			})
			if secondResponds {
				if err != nil || response == nil || !strings.Contains(response.URL, "final=1") {
					t.Fatalf("第二次筛选的最终响应未被接纳：%+v %v", response, err)
				}
			} else if !errors.Is(err, browser.ErrResponseTimeout) || response != nil {
				t.Fatalf("两次都没请求必须按超时结束：%+v %v", response, err)
			}
			want := []string{"search:首页", "search:提交搜索", "search:可重做筛选", "search:可重做筛选重试"}
			if calls != 2 || !reflect.DeepEqual(budget.got, want) {
				t.Fatalf("只能重做一次，且两次都扣额度：动作=%d，额度=%v", calls, budget.got)
			}
		})
	}
}

func TestRegionWithoutRequestsRetriesWholeSelectionOnlyOnce(t *testing.T) {
	shortenFilterRetryWaits(t)
	fixture := newFake(t)
	fixture.SetFilterBehavior("region", fake.FilterBehavior{NoRequest: true})
	runner, budget, ctx := filterRunner(t, fixture)
	response, err := runner.selectRegion(ctx, FilterOptions{Province: "广东"})
	if !errors.Is(err, browser.ErrResponseTimeout) || response != nil {
		t.Fatalf("地区提交两次都没请求必须超时：%+v %v", response, err)
	}
	steps := []string{}
	for _, action := range fixture.FilterActions() {
		query, err := url.ParseQuery(action)
		if err != nil {
			t.Fatal(err)
		}
		steps = append(steps, query.Get("step"))
	}
	if want := []string{"region_open", "region_province", "region", "region_open", "region_province", "region"}; !reflect.DeepEqual(steps, want) {
		t.Fatalf("地区只应完整选择两次：%v", steps)
	}
	want := []string{"search:首页", "search:提交搜索"}
	for range 2 {
		want = append(want, "search:打开地区筛选", "search:地区选项：广东", "search:提交地区筛选")
	}
	if !reflect.DeepEqual(budget.got, want) {
		t.Fatalf("地区重做的每个动作都必须单独扣额度：%v", budget.got)
	}
}
