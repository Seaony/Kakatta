package web

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/Seaony/Kakatta/internal/browser"
	"github.com/Seaony/Kakatta/internal/pacer"
	"github.com/Seaony/Kakatta/internal/quota"
	"github.com/Seaony/Kakatta/internal/xianyu"
)

type FilterOptions struct {
	PersonalOnly, FreeShipping   bool
	Province, City, District     string
	MinPriceCents, MaxPriceCents *int64
}

// ApplyFilters 只返回所有筛选完成后的响应，任一步失败都不能沿用前面的结果。
func (r *Runner) ApplyFilters(ctx context.Context, options FilterOptions) (*browser.Response, error) {
	if options.MinPriceCents != nil && *options.MinPriceCents < 0 || options.MaxPriceCents != nil && *options.MaxPriceCents < 0 {
		return nil, fmt.Errorf("价格不能为负数")
	}
	if options.MinPriceCents != nil && options.MaxPriceCents != nil && *options.MinPriceCents > *options.MaxPriceCents {
		return nil, fmt.Errorf("最低价不能高于最高价")
	}
	if options.Province == "" && (options.City != "" || options.District != "") || options.City == "" && options.District != "" {
		return nil, fmt.Errorf("地区必须按省、市、区的顺序填写")
	}
	response, err := r.SelectPublished(ctx, 0)
	if err != nil {
		return nil, err
	}
	for _, toggle := range []struct {
		enabled bool
		text    string
	}{{options.PersonalOnly, rules.Text.Personal}, {options.FreeShipping, rules.Text.Shipping}} {
		if !toggle.enabled {
			continue
		}
		response, err = r.filterStep(ctx, toggle.text, false, func() error { return r.clickText(ctx, toggle.text) })
		if err != nil {
			return nil, err
		}
	}
	if options.Province != "" {
		response, err = r.selectRegion(ctx, options)
		if err != nil {
			return nil, err
		}
	}
	if options.MinPriceCents != nil || options.MaxPriceCents != nil {
		response, err = r.selectPrice(ctx, options.MinPriceCents, options.MaxPriceCents)
		if err != nil {
			return nil, err
		}
	}
	return response, r.check(ctx, "筛选完成")
}

func (r *Runner) selectRegion(ctx context.Context, options FilterOptions) (*browser.Response, error) {
	for attempt := 0; attempt < 2; attempt++ {
		if err := r.acquire(ctx, quota.Search, "打开地区筛选"); err != nil {
			return nil, err
		}
		if err := r.clickText(ctx, rules.Text.Region); err != nil {
			return nil, err
		}
		if err := r.wait(ctx, "打开地区后", r.Pace.Action); err != nil {
			return nil, err
		}
		for level, name := range []string{options.Province, options.City, options.District} {
			if name == "" {
				break
			}
			node, skip, err := r.regionOption(ctx, level, name)
			if err != nil {
				return nil, err
			}
			if skip {
				continue
			}
			if err := r.acquire(ctx, quota.Search, "地区选项："+name); err != nil {
				return nil, err
			}
			if err := r.clickFilterNode(ctx, node, "地区选项："+name); err != nil {
				return nil, err
			}
			if err := r.wait(ctx, "选择地区后", r.Pace.Action); err != nil {
				return nil, err
			}
		}
		scope, err := r.regionArea(ctx)
		if err != nil {
			return nil, err
		}
		var submit *chromedp.Node
		for _, node := range matchingNodes(scope.popover, func(n *chromedp.Node) bool {
			return strings.Contains(n.AttributeValue("class"), rules.Selectors.RegionSubmitClass) && rules.regionSubmit.MatchString(strings.TrimSpace(nodeText(n)))
		}) {
			visible, err := r.H.VisibleCheckedContext(ctx, node)
			if err != nil {
				return nil, err
			}
			if visible {
				submit = node
			}
		}
		if submit == nil {
			return nil, fmt.Errorf("%w：地区提交按钮", browser.ErrNotFound)
		}
		if err := r.acquire(ctx, quota.Search, "提交地区筛选"); err != nil {
			return nil, err
		}
		mark := r.S.Capture.Mark()
		if err := r.clickFilterNode(ctx, submit, "提交地区筛选"); err != nil {
			return nil, err
		}
		response, err := r.waitAPIResponse(ctx, mark, r.Site.searchAPI(), "地区筛选", rules.timeout("filter_response"), rules.timeout("response_quiet"))
		if errors.Is(err, browser.ErrResponseTimeout) {
			if r.S.Capture.SentAfter(mark, r.Site.searchAPI()) {
				response, err = r.waitAPIResponse(ctx, mark, r.Site.searchAPI(), "地区筛选继续等待", rules.timeout("filter_extra"), rules.timeout("response_quiet"))
			} else if attempt == 0 {
				if err := r.wait(ctx, "重做地区前", r.Pace.Action); err != nil {
					return nil, err
				}
				continue
			}
		}
		if err != nil {
			return nil, err
		}
		if err := r.wait(ctx, "地区筛选后", r.Pace.Action); err != nil {
			return nil, err
		}
		return response, nil
	}
	return nil, browser.ErrResponseTimeout
}

type regionScope struct{ popover, area *chromedp.Node }

func (r *Runner) regionArea(ctx context.Context) (*regionScope, error) {
	deadline := time.Now().Add(rules.timeout("region_popover"))
	foundPopover := false
	for {
		if err := r.check(ctx, "查找地区弹层"); err != nil {
			return nil, err
		}
		popovers, err := r.H.FindNow(ctx, rules.Selectors.RegionPopover)
		if err != nil {
			return nil, err
		}
		var chosen *chromedp.Node
		for priority := 0; priority < 3 && chosen == nil; priority++ {
			for _, node := range popovers {
				visible, err := r.H.VisibleCheckedContext(ctx, node)
				if err != nil {
					return nil, err
				}
				if !visible {
					continue
				}
				matches := false
				switch priority {
				case 0:
					matches = len(matchingNodes(node, func(n *chromedp.Node) bool {
						return strings.Contains(n.AttributeValue("class"), rules.Selectors.RegionAreaClass)
					})) > 0
				case 1:
					matches = strings.Contains(nodeText(node), rules.Text.RegionRelocate)
				case 2:
					matches = strings.Contains(nodeText(node), rules.Text.RegionView)
				}
				if matches {
					chosen = node
				}
			}
		}
		if chosen != nil {
			if !foundPopover {
				deadline = time.Now().Add(rules.timeout("region_area"))
				foundPopover = true
			}
			for _, node := range matchingNodes(chosen, func(n *chromedp.Node) bool {
				return strings.Contains(n.AttributeValue("class"), rules.Selectors.RegionAreaClass)
			}) {
				visible, err := r.H.VisibleCheckedContext(ctx, node)
				if err != nil {
					return nil, err
				}
				if visible {
					r.Log("找到地区筛选", map[string]any{"选择器": rules.Selectors.RegionPopover, "规则版本": rules.Version})
					return &regionScope{popover: chosen, area: node}, nil
				}
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%w：地区弹层或输入区", browser.ErrNotFound)
		}
		if err := r.P.Wait(ctx, pacer.Between(rules.timeout("element_poll"), rules.timeout("element_poll"))); err != nil {
			return nil, err
		}
	}
}

func (r *Runner) regionOption(ctx context.Context, level int, name string) (*chromedp.Node, bool, error) {
	deadline := time.Now().Add(rules.timeout("region_option"))
	for {
		scope, err := r.regionArea(ctx)
		if err != nil {
			return nil, false, err
		}
		columns := []*chromedp.Node{}
		for _, child := range scope.area.Children {
			if child.NodeType == 1 && strings.EqualFold(child.NodeName, "div") {
				columns = append(columns, child)
			}
		}
		if len(columns) <= level {
			return nil, false, fmt.Errorf("地区筛选缺少第 %d 列", level+1)
		}
		visibleCount := 0
		for _, node := range matchingNodes(columns[level], func(n *chromedp.Node) bool {
			return strings.Contains(n.AttributeValue("class"), rules.Selectors.RegionOptionClass)
		}) {
			visible, err := r.H.VisibleCheckedContext(ctx, node)
			if err != nil {
				return nil, false, err
			}
			if !visible {
				continue
			}
			visibleCount++
			if strings.TrimSpace(nodeText(node)) == name {
				return node, false, nil
			}
		}
		if level == 2 && visibleCount == 0 {
			return nil, true, nil
		}
		if time.Now().After(deadline) {
			return nil, false, fmt.Errorf("%w：地区选项 %s", browser.ErrNotFound, name)
		}
		if err := r.P.Wait(ctx, pacer.Between(rules.timeout("element_poll"), rules.timeout("element_poll"))); err != nil {
			return nil, false, err
		}
	}
}

func (r *Runner) selectPrice(ctx context.Context, minimum, maximum *int64) (*browser.Response, error) {
	inputs, err := r.priceInputs(ctx)
	if err != nil {
		return nil, err
	}
	before := [2]string{}
	for i, node := range inputs {
		before[i], err = r.H.ValueContext(ctx, node)
		if err != nil {
			return nil, err
		}
	}
	last := 0
	for i, value := range []*int64{minimum, maximum} {
		if value == nil {
			continue
		}
		inputs, err = r.priceInputs(ctx)
		if err != nil {
			return nil, err
		}
		if err := r.clickFilterNode(ctx, inputs[i], "聚焦价格输入框"); err != nil {
			return nil, err
		}
		if err := r.H.Clear(ctx); err != nil {
			return nil, err
		}
		if err := r.H.Type(ctx, priceInput(*value)); err != nil {
			return nil, err
		}
		if err := r.wait(ctx, "价格输入后", r.Pace.Action); err != nil {
			return nil, err
		}
		last = i
	}
	if err := r.acquire(ctx, quota.Search, "价格提交"); err != nil {
		return nil, err
	}
	mark := r.S.Capture.Mark()
	if err := r.H.Press(ctx, "Tab"); err != nil {
		return nil, err
	}
	response, err := r.waitAPIResponse(ctx, mark, r.Site.searchAPI(), "价格筛选", rules.timeout("price_fallback"), rules.timeout("response_quiet"))
	if errors.Is(err, browser.ErrResponseTimeout) {
		if !r.S.Capture.SentAfter(mark, r.Site.searchAPI()) {
			inputs, err = r.priceInputs(ctx)
			if err != nil {
				return nil, err
			}
			if err := r.clickFilterNode(ctx, inputs[last], "价格回车前重新聚焦"); err != nil {
				return nil, err
			}
			if err := r.acquire(ctx, quota.Search, "价格回车提交"); err != nil {
				return nil, err
			}
			mark = r.S.Capture.Mark()
			if err := r.H.Press(ctx, "Enter"); err != nil {
				return nil, err
			}
			response, err = r.waitAPIResponse(ctx, mark, r.Site.searchAPI(), "价格回车筛选", rules.timeout("filter_response"), rules.timeout("response_quiet"))
		} else {
			remaining := rules.timeout("filter_response") - rules.timeout("price_fallback")
			response, err = r.waitAPIResponse(ctx, mark, r.Site.searchAPI(), "价格继续等待", remaining, rules.timeout("response_quiet"))
		}
		if errors.Is(err, browser.ErrResponseTimeout) && r.S.Capture.SentAfter(mark, r.Site.searchAPI()) {
			response, err = r.waitAPIResponse(ctx, mark, r.Site.searchAPI(), "价格延长等待", rules.timeout("filter_extra"), rules.timeout("response_quiet"))
		}
	}
	if err != nil {
		return nil, err
	}
	inputs, err = r.priceInputs(ctx)
	if err != nil {
		return nil, err
	}
	for i, target := range []*int64{minimum, maximum} {
		actual, err := r.H.ValueContext(ctx, inputs[i])
		if err != nil {
			return nil, err
		}
		if target == nil {
			if actual != before[i] {
				return nil, fmt.Errorf("未设置的价格边界被页面改动")
			}
		} else if value, ok := xianyu.ParsePriceCents(actual); !ok || value != *target {
			return nil, fmt.Errorf("价格输入结果和目标不一致：%s", actual)
		}
	}
	if err := r.wait(ctx, "价格筛选后", r.Pace.Action); err != nil {
		return nil, err
	}
	return response, nil
}

func (r *Runner) priceInputs(ctx context.Context) ([]*chromedp.Node, error) {
	containers, err := r.H.Find(ctx, rules.Selectors.PriceContainer, rules.timeout("element"))
	if err != nil {
		return nil, err
	}
	container := containers[0]
	visible, err := r.H.VisibleCheckedContext(ctx, container)
	if err != nil {
		return nil, err
	}
	if !visible {
		return nil, fmt.Errorf("%w：价格输入区不可见", browser.ErrNotFound)
	}
	all, err := r.H.FindNow(ctx, rules.Selectors.PriceInput)
	if err != nil {
		return nil, err
	}
	inputs := []*chromedp.Node{}
	for _, node := range all {
		inside := false
		for parent := node.Parent; parent != nil; parent = parent.Parent {
			if parent.BackendNodeID == container.BackendNodeID {
				inside = true
				break
			}
		}
		if !inside {
			continue
		}
		visible, err := r.H.VisibleCheckedContext(ctx, node)
		if err != nil {
			return nil, err
		}
		if visible {
			inputs = append(inputs, node)
		}
	}
	if len(inputs) < 2 {
		return nil, fmt.Errorf("%w：两个价格输入框", browser.ErrNotFound)
	}
	r.Log("找到价格筛选", map[string]any{"选择器": rules.Selectors.PriceContainer, "规则版本": rules.Version})
	return inputs[:2], nil
}

func (r *Runner) clickFilterNode(ctx context.Context, node *chromedp.Node, step string) error {
	if err := r.check(ctx, step+"前"); err != nil {
		return err
	}
	r.Log("找到筛选元素", map[string]any{"步骤": step, "文字": strings.TrimSpace(nodeText(node)), "规则版本": rules.Version})
	if err := r.H.Click(ctx, node); err != nil {
		return err
	}
	return r.check(ctx, step+"后")
}

func priceInput(cents int64) string {
	whole := strconv.FormatInt(cents/100, 10)
	if cents%100 == 0 {
		return whole
	}
	return whole + "." + strings.TrimRight(fmt.Sprintf("%02d", cents%100), "0")
}

func matchingNodes(root *chromedp.Node, match func(*chromedp.Node) bool) []*chromedp.Node {
	var nodes []*chromedp.Node
	var walk func(*chromedp.Node)
	walk = func(node *chromedp.Node) {
		if match(node) {
			nodes = append(nodes, node)
		}
		for _, child := range node.Children {
			walk(child)
		}
	}
	walk(root)
	return nodes
}

func nodeText(node *chromedp.Node) string {
	if strings.EqualFold(node.NodeName, "script") || strings.EqualFold(node.NodeName, "style") {
		return ""
	}
	if node.NodeType == 3 {
		return node.NodeValue
	}
	var text strings.Builder
	for _, child := range node.Children {
		text.WriteString(nodeText(child))
	}
	return text.String()
}
