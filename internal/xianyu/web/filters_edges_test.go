package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/browser"
)

func filterEdgeRunner(t *testing.T, markup string) (*Runner, *countingBudget, context.Context) {
	t.Helper()
	if os.Getenv("KAKATTA_BROWSER_TESTS") != "1" {
		t.Skip("筛选节点回归需在隔离容器设置 KAKATTA_BROWSER_TESTS=1")
	}
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><meta charset="utf-8"><style>input{width:100px;height:30px}.provItem--edge{padding:10px}</style>`+markup)
	}))
	t.Cleanup(page.Close)
	r, budget, ctx := startRunner(t, newFake(t))
	if err := r.S.Navigate(page.URL, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	return r, budget, ctx
}

func TestRegionColumnsIgnoreDirectNonDivElements(t *testing.T) {
	r, budget, ctx := filterEdgeRunner(t, `
<div class="ant-popover"><div class="areaWrap--edge">
  <span><div class="provItem--edge">干扰省</div></span>
  <div><div id="province" class="provItem--edge">广东</div></div>
  <button>列间按钮</button>
  <div><div id="city" class="provItem--edge">深圳</div></div>
  <span>列间说明</span>
  <div><div id="district" class="provItem--edge">南山</div></div>
</div></div>`)
	for level, target := range []struct{ name, id string }{{"广东", "province"}, {"深圳", "city"}, {"南山", "district"}} {
		node, skip, err := r.regionOption(ctx, level, target.name)
		if err != nil || skip || node == nil || node.AttributeValue("id") != target.id {
			t.Fatalf("第%d列应找到%s，不能被非 div 占位：节点%v，跳过%v，错误%v", level+1, target.id, node, skip, err)
		}
	}
	if len(budget.got) != 0 {
		t.Fatalf("查找节点不应产生访问：%v", budget.got)
	}
}

func TestRegionColumnsDoNotBorrowNestedDivAsColumn(t *testing.T) {
	r, _, ctx := filterEdgeRunner(t, `
<div class="ant-popover"><div class="areaWrap--edge">
  <div><div class="provItem--edge">广东</div></div>
  <div><div class="provItem--edge">深圳</div></div>
  <span><div><div class="provItem--edge">南山</div></div></span>
</div></div>`)
	if node, skip, err := r.regionOption(ctx, 2, "南山"); err == nil || node != nil || skip {
		t.Fatalf("只有两个直接 div 列时必须失败，不能借用 span 里的 div：节点%v，跳过%v，错误%v", node, skip, err)
	}
}

func TestRegionOptionStopsWhenCallerCancelledAfterAreaRead(t *testing.T) {
	r, budget, ctx := filterEdgeRunner(t, `
<div class="ant-popover"><div class="areaWrap--edge">
  <div><div id="province" class="provItem--edge">广东</div></div>
</div></div>`)
	caller, cancel := context.WithCancel(ctx)
	defer cancel()
	r.Log = func(event string, _ map[string]any) {
		if event == "找到地区筛选" {
			cancel()
		}
	}
	if node, skip, err := r.regionOption(caller, 0, "广东"); !errors.Is(err, context.Canceled) || node != nil || skip {
		t.Fatalf("地区容器读完后取消，不能继续读选项并返回成功：节点%v，跳过%v，错误%v", node, skip, err)
	}
	if r.S.Context().Err() != nil {
		t.Fatal("调用取消不应关闭整个浏览器会话")
	}
	r.Log = func(string, map[string]any) {}
	if node, skip, err := r.regionOption(ctx, 0, "广东"); err != nil || node == nil || skip {
		t.Fatalf("同一浏览器仍应能处理后续调用：节点%v，跳过%v，错误%v", node, skip, err)
	}
	if len(budget.got) != 0 {
		t.Fatalf("取消的读取不应产生访问：%v", budget.got)
	}
}

func TestPriceInputsUseFirstContainerInDocument(t *testing.T) {
	for _, test := range []struct {
		name       string
		firstStyle string
		firstInput string
		wantError  bool
	}{
		{"首个隐藏不能跳到第二个", "display:none", `<input id="minimum" placeholder="最低 ¥"><input id="maximum" placeholder="最高 ¥">`, true},
		{"首个可见只取内部两个输入", "", `<input id="minimum" placeholder="最低 ¥"><input id="maximum" placeholder="最高 ¥">`, false},
		{"首个缺输入不能借第二个", "", `<input id="minimum" placeholder="最低 ¥">`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			r, budget, ctx := filterEdgeRunner(t, `<input id="unrelated" placeholder="外部 ¥">
<div class="search-price-input-container--first" style="`+test.firstStyle+`">`+test.firstInput+`</div>
<div class="search-price-input-container--second"><input id="other-minimum" placeholder="最低 ¥"><input id="other-maximum" placeholder="最高 ¥"></div>`)
			inputs, err := r.priceInputs(ctx)
			if test.wantError {
				if !errors.Is(err, browser.ErrNotFound) || inputs != nil {
					t.Fatalf("第一输入区不可用必须失败，不能跳过或补框：%v %v", inputs, err)
				}
			} else if err != nil || len(inputs) != 2 || inputs[0].AttributeValue("id") != "minimum" || inputs[1].AttributeValue("id") != "maximum" {
				t.Fatalf("必须只取第一输入区的两个框：%v %v", inputs, err)
			}
			if len(budget.got) != 0 {
				t.Fatalf("定位失败前不应点击、输入或申请访问：%v", budget.got)
			}
		})
	}
}
