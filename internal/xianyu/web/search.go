package web

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
	"golang.org/x/text/unicode/norm"

	"github.com/Seaony/Kakatta/internal/browser"
	"github.com/Seaony/Kakatta/internal/quota"
)

// OpenHome 打开首页，停留，滚动一次，再停留（XIANYU.md 5.1 节）。
func (r *Runner) OpenHome(ctx context.Context) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := r.RequireLoginCookieContext(ctx); err != nil {
		return err
	}
	if err := r.acquire(ctx, quota.Search, "首页"); err != nil {
		return err
	}
	start := time.Now()
	if err := r.navigate(ctx, r.Site.Home, "打开首页", rules.timeout("home")); err != nil {
		if cerr := r.check(ctx, "打开首页"); cerr != nil {
			return cerr
		}
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
	if err := r.wait(ctx, "首页滚动后停留", r.Pace.Action); err != nil {
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
	if contextError(ctx) != nil {
		return nil
	}
	nodes, err := r.H.FindNow(ctx, rules.Selectors.SearchCandidates)
	if err != nil {
		return nil
	}
	var out []InputFact
	for _, n := range nodes {
		visible, err := r.H.VisibleCheckedContext(ctx, n)
		if err != nil {
			return nil
		}
		if !visible {
			continue
		}
		_, y, w, _, err := r.H.BoxContext(ctx, n)
		if err != nil && contextError(ctx) != nil {
			return nil
		}
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
	if err := contextError(ctx); err != nil {
		return nil, "", err
	}
	if r.SearchInput != "" {
		n, err := r.H.FirstVisible(ctx, r.SearchInput, rules.timeout("search_input"))
		return n, r.SearchInput, err
	}
	nodes, err := r.H.Find(ctx, rules.Selectors.SearchCandidates, rules.timeout("search_input"))
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
		visible, err := r.H.VisibleCheckedContext(ctx, n)
		if err != nil {
			return nil, "", err
		}
		if !visible {
			continue
		}
		_, y, w, _, err := r.H.BoxContext(ctx, n)
		if err != nil {
			return nil, "", err
		}
		if y > 300 {
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
	if err := r.check(ctx, "聚焦搜索框后"); err != nil {
		return nil, err
	}
	// 搜索框里已经有文字（比如推荐词）时先清空，不然会和关键词拼在一起（XIANYU.md 5.2 节）。placeholder 读出来是空的，不受影响。
	v, err := r.H.ValueContext(ctx, input)
	if err != nil {
		return nil, err
	}
	if v != "" {
		if err := r.H.Clear(ctx); err != nil {
			return nil, err
		}
		v, err := r.H.ValueContext(ctx, input)
		if err != nil {
			return nil, fmt.Errorf("读取清空后的搜索框：%w", err)
		}
		if v != "" {
			return nil, fmt.Errorf("没能清空搜索框里原有的文字")
		}
		r.Log("清空了搜索框", nil)
	}
	if err := r.H.Type(ctx, keyword); err != nil {
		return nil, err
	}
	if err := r.wait(ctx, "输入关键词后", r.Pace.Action); err != nil {
		return nil, err
	}
	if err := r.acquire(ctx, quota.Search, "提交搜索"); err != nil {
		return nil, err
	}
	if err := r.check(ctx, "提交搜索前"); err != nil {
		return nil, err
	}
	mark := r.S.Capture.Mark()
	navMark := r.S.NavigationMark()
	before, err := r.S.URLContext(ctx)
	if err != nil {
		return nil, err
	}
	if err := r.H.Press(ctx, "Enter"); err != nil {
		return nil, err
	}

	// 只接受回车后主框架自己的加载完成，旧页面和 iframe 不算。
	var landed string
	err = r.watch(ctx, "等待搜索结果页", func(waitCtx context.Context) error {
		var err error
		landed, err = r.S.WaitNavigation(waitCtx, navMark, func(u string) bool { return strings.Contains(u, "/search") }, rules.timeout("search_navigation"))
		return err
	})
	if err != nil {
		if cerr := r.check(ctx, "提交搜索"); cerr != nil {
			return nil, cerr
		}
		return nil, fmt.Errorf("回车后没有进入搜索结果页（原网址 %s）：%w", before, err)
	}
	resp, err := r.waitAPI(ctx, mark, r.Site.searchAPI(), "搜索结果页", rules.timeout("search_response"))
	if err != nil {
		return nil, err
	}
	// 核对关键词：结果页网址的 q 和任务关键词不一致，说明输入出了问题，不拿错误关键词的结果继续。
	u, err := url.Parse(landed)
	if err != nil {
		return nil, fmt.Errorf("搜索结果页网址无效：%w", err)
	}
	if q := u.Query().Get("q"); normalizeKeyword(q) != normalizeKeyword(keyword) {
		return nil, fmt.Errorf("结果页的关键词是 %q，和要搜的 %q 不一致", q, keyword)
	}
	r.Log("进入搜索结果页", map[string]any{"网址": landed})
	if _, err := r.H.FirstVisible(ctx, rules.Selectors.SearchReady, rules.timeout("search_ready")); err != nil {
		if cerr := r.check(ctx, "等筛选栏"); cerr != nil {
			return nil, cerr
		}
		return nil, fmt.Errorf("结果页上找不到「新发布」，页面结构可能变了：%w", err)
	}
	if err := r.closeAd(ctx); err != nil {
		return nil, err
	}
	return resp, r.check(ctx, "搜索完成")
}

func normalizeKeyword(s string) string {
	return strings.Join(strings.Fields(norm.NFKC.String(s)), " ")
}

// closeAd 关掉结果页的广告弹窗，找不到就跳过；一般不产生接口请求，不计额度（XIANYU.md 5.4 节）。
func (r *Runner) closeAd(ctx context.Context) error {
	n, err := r.H.FirstVisible(ctx, rules.Selectors.CloseAd, rules.timeout("advertisement"))
	if err != nil {
		if errors.Is(err, browser.ErrNotFound) {
			return r.check(ctx, "查找广告后")
		}
		return err
	}
	if err := r.check(ctx, "关闭广告前"); err != nil {
		return err
	}
	if err := r.H.Click(ctx, n); err != nil {
		return err
	}
	r.Log("关闭了广告", nil)
	return r.check(ctx, "关闭广告后")
}

// SelectPublished 总是点「最新」，withinDays 由本地补筛使用，不改变页面排序。
func (r *Runner) SelectPublished(ctx context.Context, withinDays int) (*browser.Response, error) {
	switch withinDays {
	case 0, 1, 3, 7, 14:
	default:
		return nil, fmt.Errorf("不支持的发布时间：%d 天内", withinDays)
	}
	option := rules.Text.PublishLatest
	resp, err := r.filterStep(ctx, "发布时间筛选", true, func() error {
		// 打开菜单本身不产生请求，点选项才会。
		if err := r.clickText(ctx, rules.Text.PublishMenu); err != nil {
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
	return r.searchStep(ctx, name, repeatable, rules.timeout("filter_extra"), act)
}

func (r *Runner) searchStep(ctx context.Context, name string, repeatable bool, extraWait time.Duration, act func() error) (*browser.Response, error) {
	api := r.Site.searchAPI()
	if err := r.acquire(ctx, quota.Search, name); err != nil {
		return nil, err
	}
	if err := r.check(ctx, name+"前"); err != nil {
		return nil, err
	}
	mark := r.S.Capture.Mark()
	if err := act(); err != nil {
		return nil, err
	}
	resp, err := r.waitAPIResponse(ctx, mark, api, name, rules.timeout("filter_response"), rules.timeout("response_quiet"))
	if errors.Is(err, browser.ErrResponseTimeout) {
		switch {
		case r.S.Capture.SentAfter(mark, api):
			resp, err = r.waitAPIResponse(ctx, mark, api, name+"（继续等）", extraWait, rules.timeout("response_quiet"))
		case repeatable:
			r.Log(name+"没有反应，重做一次", nil)
			if werr := r.wait(ctx, "重做前", r.Pace.Action); werr != nil {
				return nil, werr
			}
			if aerr := r.acquire(ctx, quota.Search, name+"重试"); aerr != nil {
				return nil, aerr
			}
			mark = r.S.Capture.Mark()
			if aerr := act(); aerr != nil {
				return nil, aerr
			}
			resp, err = r.waitAPIResponse(ctx, mark, api, name+"重试", rules.timeout("filter_response"), rules.timeout("response_quiet"))
		}
	}
	if err != nil {
		return nil, err
	}
	if err := r.wait(ctx, "筛选后", r.Pace.Action); err != nil {
		return nil, err
	}
	return resp, r.check(ctx, name+"后")
}

// clickText 点页面上文字完全等于 text 的最深一层可见元素，取文档顺序里的第一个（XIANYU.md 3.1 节）。
func (r *Runner) clickText(ctx context.Context, text string) error {
	sel := "//*[normalize-space(.)='" + text + "' and not(.//*[normalize-space(.)='" + text + "'])]"
	n, err := r.H.FirstVisible(ctx, sel, rules.timeout("element"))
	r.Log("查找页面元素", map[string]any{"文字": text, "找到": err == nil, "规则版本": rules.Version})
	if err != nil {
		return err
	}
	if err := r.check(ctx, "点「"+text+"」之前"); err != nil {
		return err
	}
	if err := r.H.Click(ctx, n); err != nil {
		return err
	}
	return r.check(ctx, "点「"+text+"」之后")
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

// NextPage 翻到下一页，返回这一页的搜索响应（XIANYU.md 5.6 节）。没有下一页按钮时 ok 为 false、err 为 nil。
func (r *Runner) NextPage(ctx context.Context) (resp *browser.Response, ok bool, err error) {
	if err := r.check(ctx, "翻页前"); err != nil {
		return nil, false, err
	}
	nodes, err := r.H.FindNow(ctx, rules.Selectors.NextPage)
	if err != nil {
		return nil, false, err
	}
	if len(nodes) == 0 {
		return nil, false, nil
	}
	if err := r.wait(ctx, "翻页前", r.Pace.PageWait); err != nil {
		return nil, false, err
	}
	resp, err = r.searchStep(ctx, "翻页", true, rules.timeout("page_extra"), func() error {
		btn, err := r.H.FirstVisible(ctx, rules.Selectors.NextPage, rules.timeout("page_button"))
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
