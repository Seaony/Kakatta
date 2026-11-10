package web

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/Seaony/Kakatta/internal/browser"
	"github.com/Seaony/Kakatta/internal/pacer"
	"github.com/Seaony/Kakatta/internal/quota"
)

// OpenHome 打开首页，停留，滚动一次，再停留（XIANYU.md 5.1 节）。
func (r *Runner) OpenHome(ctx context.Context) error {
	if err := r.acquire(quota.Search, "首页"); err != nil {
		return err
	}
	start := time.Now()
	if err := r.S.Navigate(r.Site.Home, 30*time.Second); err != nil {
		return fmt.Errorf("打开首页：%w", err)
	}
	r.Log("首页已打开", map[string]any{"耗时毫秒": time.Since(start).Milliseconds()})
	if err := r.check(ctx, "首页"); err != nil {
		return err
	}
	if err := r.wait(ctx, "首页停留", r.Pace.HomeStay); err != nil {
		return err
	}
	if err := r.H.Scroll(ctx, 200+r.P.Rand()*500); err != nil {
		return err
	}
	if err := r.wait(ctx, "首页滚动后停留", r.Pace.HomeStay); err != nil {
		return err
	}
	return r.check(ctx, "首页停留后")
}

// InputFact 记录首页上一个输入框的样子，用来确认搜索框的选择器。不含输入框里的值。
type InputFact struct {
	Selector    string  `json:"selector"`
	Placeholder string  `json:"placeholder,omitempty"`
	Type        string  `json:"type,omitempty"`
	Name        string  `json:"name,omitempty"`
	ClassPrefix string  `json:"class_prefix,omitempty"`
	Top         float64 `json:"top"`
	Width       float64 `json:"width"`
}

// InputFacts 列出页面顶部可见的输入框，给实验记录用（XIANYU.md 第十一节「首页搜索框的选择器」）。
func (r *Runner) InputFacts(ctx context.Context) []InputFact {
	nodes, err := r.H.FindNow(ctx, "input")
	if err != nil {
		return nil
	}
	var out []InputFact
	for _, n := range nodes {
		if !r.H.Visible(n) {
			continue
		}
		_, y, w, _, err := r.H.Box(n)
		if err != nil || y > 300 {
			continue
		}
		f := InputFact{
			Placeholder: n.AttributeValue("placeholder"),
			Type:        n.AttributeValue("type"),
			Name:        n.AttributeValue("name"),
			ClassPrefix: classPrefix(n.AttributeValue("class")),
			Top:         y,
			Width:       w,
		}
		f.Selector = factSelector(f)
		out = append(out, f)
	}
	return out
}

// findSearchInput 找首页搜索框：有指定的选择器就用它；没有就取页面顶部最宽的可见输入框（搜索框通常是顶部最宽的那个）。
func (r *Runner) findSearchInput(ctx context.Context) (*chromedp.Node, string, error) {
	if r.SearchInput != "" {
		n, err := r.H.FirstVisible(ctx, r.SearchInput, 10*time.Second)
		return n, r.SearchInput, err
	}
	nodes, err := r.H.Find(ctx, "input", 10*time.Second)
	if err != nil {
		return nil, "", err
	}
	var best *chromedp.Node
	var bestW float64
	for _, n := range nodes {
		t := n.AttributeValue("type")
		if t != "" && t != "text" && t != "search" {
			continue
		}
		if !r.H.Visible(n) {
			continue
		}
		_, y, w, _, err := r.H.Box(n)
		if err != nil || y > 300 {
			continue
		}
		if w > bestW {
			best, bestW = n, w
		}
	}
	if best == nil {
		return nil, "", fmt.Errorf("%w：首页顶部没有可见的输入框", browser.ErrNotFound)
	}
	return best, "自动：页面顶部最宽的输入框", nil
}

// Search 在首页搜索框里逐字输入关键词并回车，等到搜索结果页和第一条搜索接口响应（XIANYU.md 5.2–5.3 节）。
func (r *Runner) Search(ctx context.Context, keyword string) (*browser.Response, error) {
	if err := r.check(ctx, "搜索前"); err != nil {
		return nil, err
	}
	input, how, err := r.findSearchInput(ctx)
	if err != nil {
		return nil, fmt.Errorf("找首页搜索框：%w", err)
	}
	r.Log("找到搜索框", map[string]any{"方式": how, "placeholder": input.AttributeValue("placeholder")})
	if err := r.H.Click(ctx, input); err != nil {
		return nil, err
	}
	// 搜索框里已经有文字（比如推荐词）时先清空，不然会和关键词拼在一起（XIANYU.md 5.2 节）。placeholder 读出来是空的，不受影响。
	if v, err := r.H.Value(input); err == nil && v != "" {
		if err := r.H.Clear(ctx); err != nil {
			return nil, err
		}
		if v, err := r.H.Value(input); err != nil || v != "" {
			return nil, fmt.Errorf("没能清空搜索框里原有的文字（%v）", err)
		}
		r.Log("清空了搜索框", nil)
	}
	if err := r.H.Type(ctx, keyword); err != nil {
		return nil, err
	}
	if err := r.wait(ctx, "输入关键词后", r.Pace.Action); err != nil {
		return nil, err
	}
	if err := r.acquire(quota.Search, "提交搜索"); err != nil {
		return nil, err
	}
	if err := r.check(ctx, "提交搜索前"); err != nil {
		return nil, err
	}
	mark := r.S.Capture.Mark()
	before, _ := r.S.URL()
	if err := r.H.Press(ctx, "Enter"); err != nil {
		return nil, err
	}

	// 等主框架跳到搜索结果页。回车后如果是新开了标签页，这里会超时，日志里会写明，待真实页面确认后再处理（XIANYU.md 5.3 节）。
	landed, err := r.waitURL(ctx, func(u string) bool { return strings.Contains(u, "/search") }, 60*time.Second)
	if err != nil {
		if cerr := r.check(ctx, "提交搜索"); cerr != nil {
			return nil, cerr
		}
		return nil, fmt.Errorf("回车后没有进入搜索结果页（原网址 %s）：%w", before, err)
	}
	resp, err := r.waitAPI(ctx, mark, r.Site.searchAPI(), "搜索结果页", 30)
	if err != nil {
		return nil, err
	}
	// 核对关键词：结果页网址的 q 和任务关键词不一致，说明输入出了问题，不拿错误关键词的结果继续。
	if u, err := url.Parse(landed); err == nil {
		if q := u.Query().Get("q"); q != keyword {
			return nil, fmt.Errorf("结果页的关键词是 %q，和要搜的 %q 不一致", q, keyword)
		}
	}
	r.Log("进入搜索结果页", map[string]any{"网址": landed})
	if _, err := r.H.FirstVisible(ctx, "//*[normalize-space(.)='新发布' and not(.//*[normalize-space(.)='新发布'])]", 15*time.Second); err != nil {
		if cerr := r.check(ctx, "等筛选栏"); cerr != nil {
			return nil, cerr
		}
		return nil, fmt.Errorf("结果页上找不到「新发布」，页面结构可能变了：%w", err)
	}
	r.closeAd(ctx)
	return resp, nil
}

// closeAd 关掉结果页的广告弹窗，找不到就跳过；一般不产生接口请求，不计额度（XIANYU.md 5.4 节）。
func (r *Runner) closeAd(ctx context.Context) {
	n, err := r.H.FirstVisible(ctx, "div[class*='closeIconBg']", 3*time.Second)
	if err != nil {
		return
	}
	if err := r.H.Click(ctx, n); err == nil {
		r.Log("关闭了广告", nil)
	}
}

// publishOptions 是发布时间筛选的选项文字（scraper.py:803）。
var publishOptions = map[int]string{0: "最新", 1: "1天内", 3: "3天内", 7: "7天内", 14: "14天内"}

// SelectPublished 点「新发布」并选择发布时间，withinDays 为 0 表示「最新」（不限时间，只按最新排序）。
// 每次运行都要做：翻 1 页的前提是按最新排序（XIANYU.md 5.5 节第 1 步）。
func (r *Runner) SelectPublished(ctx context.Context, withinDays int) (*browser.Response, error) {
	option, ok := publishOptions[withinDays]
	if !ok {
		return nil, fmt.Errorf("不支持的发布时间：%d 天内", withinDays)
	}
	resp, err := r.filterStep(ctx, "发布时间筛选", true, func() error {
		// 打开菜单本身不产生请求，点选项才会。
		if err := r.clickText(ctx, "新发布"); err != nil {
			return fmt.Errorf("打开发布时间菜单：%w", err)
		}
		if err := r.wait(ctx, "打开菜单后", r.Pace.Action); err != nil {
			return err
		}
		return r.clickText(ctx, option)
	})
	if err == nil {
		r.Log("选了发布时间", map[string]any{"选项": option})
	}
	return resp, err
}

// filterStep 执行一次筛选操作并等新的搜索响应（XIANYU.md 5.5 节）：
//   - 请求已经发出、只是没返回：再等 10 秒，绝不重点；
//   - 根本没发出请求：可以重复做的步骤等一下重做一次，开关类步骤不重做；
//   - 最后取这次操作之后的最后一条搜索响应，因为一次筛选可能连发几个请求。
func (r *Runner) filterStep(ctx context.Context, name string, repeatable bool, act func() error) (*browser.Response, error) {
	api := r.Site.searchAPI()
	if err := r.acquire(quota.Search, name); err != nil {
		return nil, err
	}
	if err := r.check(ctx, name+"前"); err != nil {
		return nil, err
	}
	mark := r.S.Capture.Mark()
	if err := act(); err != nil {
		return nil, err
	}
	resp, err := r.waitAPI(ctx, mark, api, name, 20)
	if err != nil && !IsStop(err) {
		switch {
		case r.S.Capture.SentAfter(mark, api):
			resp, err = r.waitAPI(ctx, mark, api, name+"（继续等）", 10)
		case repeatable:
			r.Log(name+"没有反应，重做一次", nil)
			if werr := r.wait(ctx, "重做前", r.Pace.Action); werr != nil {
				return nil, werr
			}
			if aerr := r.acquire(quota.Search, name+"重试"); aerr != nil {
				return nil, aerr
			}
			mark = r.S.Capture.Mark()
			if aerr := act(); aerr != nil {
				return nil, aerr
			}
			resp, err = r.waitAPI(ctx, mark, api, name+"重试", 20)
		}
	}
	if err != nil {
		return nil, err
	}
	if err := r.wait(ctx, "筛选后", r.Pace.Action); err != nil {
		return nil, err
	}
	if last := r.S.Capture.LastAfter(mark, api); last != nil && last.Seq > resp.Seq {
		// 最后一条才是筛选后的结果；它失败了，前面成功的那条也不能当结果用（XIANYU.md 5.5 节）。
		checked, err := r.waitAPI(ctx, last.Seq-1, api, name+"（最后一条）", 5)
		if err != nil {
			return nil, err
		}
		resp = checked
	}
	return resp, nil
}

// clickText 点页面上文字完全等于 text 的最深一层可见元素，取文档顺序里的第一个（XIANYU.md 3.1 节）。
func (r *Runner) clickText(ctx context.Context, text string) error {
	sel := "//*[normalize-space(.)='" + text + "' and not(.//*[normalize-space(.)='" + text + "'])]"
	n, err := r.H.FirstVisible(ctx, sel, 10*time.Second)
	if err != nil {
		return err
	}
	if err := r.checkPage(ctx, "点「"+text+"」之前"); err != nil {
		return err
	}
	if err := r.H.Click(ctx, n); err != nil {
		return err
	}
	return r.checkPage(ctx, "点「"+text+"」之后")
}

// waitURL 每 200 毫秒看一次主框架网址，直到满足条件或超时。
func (r *Runner) waitURL(ctx context.Context, ok func(string) bool, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for {
		u, err := r.S.URL()
		if err == nil && ok(u) {
			return u, nil
		}
		if time.Now().After(deadline) {
			return u, errors.New("等待页面跳转超时")
		}
		if err := r.P.Wait(ctx, pacer.Between(200*time.Millisecond, 200*time.Millisecond)); err != nil {
			return "", err
		}
	}
}

func classPrefix(class string) string {
	var out []string
	for _, c := range strings.Fields(class) {
		// CSS Module 的 class 带哈希后缀，比如 search-input--a1B2c，只记前缀。
		if i := strings.Index(c, "--"); i > 0 {
			c = c[:i]
		}
		out = append(out, c)
	}
	return strings.Join(out, " ")
}

func factSelector(f InputFact) string {
	switch {
	case f.Placeholder != "":
		return fmt.Sprintf("input[placeholder=%q]", f.Placeholder)
	case f.Name != "":
		return fmt.Sprintf("input[name=%q]", f.Name)
	case f.ClassPrefix != "":
		return fmt.Sprintf("input[class*=%q]", strings.Fields(f.ClassPrefix)[0])
	}
	return "input"
}

// nextPageButton 是可点的「下一页」按钮（search_pagination.py:9-13）。
const nextPageButton = "button[class*='search-pagination-arrow-container']:has([class*='search-pagination-arrow-right']):not([disabled])"

// NextPage 翻到下一页，返回这一页的搜索响应（XIANYU.md 5.6 节）。没有下一页按钮时 ok 为 false、err 为 nil。
func (r *Runner) NextPage(ctx context.Context) (resp *browser.Response, ok bool, err error) {
	if n, _ := r.H.FindNow(ctx, nextPageButton); len(n) == 0 {
		return nil, false, nil
	}
	if err := r.wait(ctx, "翻页前", r.Pace.PageWait); err != nil {
		return nil, false, err
	}
	resp, err = r.filterStep(ctx, "翻页", true, func() error {
		btn, err := r.H.FirstVisible(ctx, nextPageButton, 5*time.Second)
		if err != nil {
			return fmt.Errorf("找下一页按钮：%w", err)
		}
		return r.H.Click(ctx, btn)
	})
	if err != nil {
		return nil, false, err
	}
	return resp, true, nil
}
