package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/css"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"

	"github.com/Seaony/Kakatta/internal/pacer"
)

// ErrNotFound 表示页面上找不到要操作的元素。
var ErrNotFound = errors.New("页面上找不到元素")

// Human 用真实的鼠标和键盘事件操作页面，所有停顿都走 pacer。
// 点击坐标取元素中心附近的随机点，鼠标从上一个位置分几步移过去，而不是瞬间出现在目标上。
type Human struct {
	s        *Session
	queries  cdp.Session
	domRead  chan struct{}
	p        *pacer.Pacer
	keyDelay pacer.Range // 每个字之间的间隔
	x, y     float64     // 鼠标当前位置
}

// NewHuman 创建操作者。keyDelay 是逐字输入时每个字之间的间隔。
func NewHuman(s *Session, p *pacer.Pacer, keyDelay pacer.Range) *Human {
	return &Human{s: s, queries: chromedp.FromContext(s.Context()).Target, domRead: s.domReadPermit(), p: p, keyDelay: keyDelay, x: 200, y: 200}
}

// 重取文档会清掉前端编号，同一条读取链结束后才能开始下一条。
func (h *Human) lockDOM(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case h.domRead <- struct{}{}:
		return func() { <-h.domRead }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Find 等到至少一个匹配的节点，查询只走 DOM 命令。
func (h *Human) Find(caller context.Context, sel string, timeout time.Duration) (nodes []*chromedp.Node, err error) {
	ctx, cancelLifetime := h.s.operationContext(caller)
	defer cancelLifetime()
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	defer func() { err = h.findError(caller, tctx, sel, err) }()
	for {
		nodes, err := h.findNodes(tctx, sel)
		if err != nil {
			return nil, err
		}
		if len(nodes) > 0 {
			return nodes, nil
		}
		if err := h.p.Wait(tctx, pacer.Between(50*time.Millisecond, 50*time.Millisecond)); err != nil {
			return nil, err
		}
	}
}

// FindNow 每次重新取文档根节点，不使用上次页面的节点编号。
func (h *Human) FindNow(caller context.Context, sel string) (nodes []*chromedp.Node, err error) {
	ctx, cancelLifetime := h.s.operationContext(caller)
	defer cancelLifetime()
	defer func() { err = operationError(ctx, err) }()
	return h.findNodes(ctx, sel)
}

func (h *Human) findNodes(ctx context.Context, sel string) ([]*chromedp.Node, error) {
	tctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	unlock, err := h.lockDOM(tctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	return retryDOMRead(func() ([]*chromedp.Node, error) { return h.findNow(tctx, sel) })
}

func (h *Human) findError(caller, lookup context.Context, sel string, err error) error {
	if err == nil {
		return nil
	}
	if caller.Err() != nil || h.s.ctx.Err() != nil {
		return operationError(h.s.ctx, operationError(caller, err))
	}
	readErr := err
	for errors.Unwrap(readErr) != nil {
		readErr = errors.Unwrap(readErr)
	}
	// 只把查找自身的超时当作缺元素，协议错误仍须报给调用方。
	if lookup.Err() == context.DeadlineExceeded && readErr == context.DeadlineExceeded {
		return fmt.Errorf("%w：%s", ErrNotFound, sel)
	}
	return operationError(lookup, err)
}

var errDOMChanged = errors.New("查询期间页面内容已变化")

// 重取文档会清掉节点编号和搜索会话，只对这两种本地状态失效重新查询。
func retryDOMRead(query func() ([]*chromedp.Node, error)) ([]*chromedp.Node, error) {
	var nodes []*chromedp.Node
	var err error
	for range 3 {
		nodes, err = query()
		if err == nil || !errors.Is(err, errDOMChanged) &&
			!strings.Contains(err.Error(), "Could not find node with given id") &&
			!strings.Contains(err.Error(), "No search session with given id found") {
			return nodes, err
		}
	}
	return nil, err
}

func (h *Human) findNow(tctx context.Context, sel string) ([]*chromedp.Node, error) {
	if err := tctx.Err(); err != nil {
		return nil, err
	}
	depth := int64(-1)
	root, err := cdp.Call(tctx, h.queries, dom.GetDocument, dom.GetDocumentParams{Depth: &depth})
	if err != nil {
		return nil, err
	}
	snapshot := nodeSnapshot(root.Root)
	var ids []cdp.NodeID
	if strings.HasPrefix(sel, "//") {
		res, err := cdp.Call(tctx, h.queries, dom.PerformSearch, dom.PerformSearchParams{Query: sel})
		if err != nil {
			return nil, err
		}
		defer cdp.Call(tctx, h.queries, dom.DiscardSearchResults, dom.DiscardSearchResultsParams{SearchID: res.SearchID})
		if res.ResultCount == 0 {
			return nil, nil
		}
		hits, err := cdp.Call(tctx, h.queries, dom.GetSearchResults, dom.GetSearchResultsParams{SearchID: res.SearchID, FromIndex: 0, ToIndex: res.ResultCount})
		if err != nil {
			return nil, err
		}
		ids = hits.NodeIDs
	} else {
		res, err := cdp.Call(tctx, h.queries, dom.QuerySelectorAll, dom.QuerySelectorAllParams{NodeID: root.Root.NodeID, Selector: sel})
		if err != nil {
			return nil, err
		}
		ids = res.NodeIDs
	}
	out := make([]*chromedp.Node, 0, len(ids))
	byBackend := map[cdp.BackendNodeID]*chromedp.Node{}
	for _, n := range snapshot {
		byBackend[n.BackendNodeID] = n
	}
	for _, id := range ids {
		res, err := cdp.Call(tctx, h.queries, dom.DescribeNode, dom.DescribeNodeParams{NodeID: id})
		if err != nil {
			return nil, err
		}
		if n := byBackend[res.Node.BackendNodeID]; n != nil {
			out = append(out, n)
		} else {
			return nil, errDOMChanged
		}
	}
	return out, nil
}

func (h *Human) Box(n *chromedp.Node) (x, y, w, ht float64, err error) {
	return h.BoxContext(h.s.ctx, n)
}

func (h *Human) BoxContext(caller context.Context, n *chromedp.Node) (x, y, w, ht float64, err error) {
	ctx, cancel := h.s.readContext(caller)
	defer cancel()
	defer func() { err = operationError(ctx, err) }()
	if err := ctx.Err(); err != nil {
		return 0, 0, 0, 0, err
	}
	return h.boxContext(ctx, n)
}

// VisibleChecked 把真正不可见和读取失败分开，风控检查不能在读取失败时放行。
func (h *Human) VisibleChecked(n *chromedp.Node) (bool, error) {
	return h.VisibleCheckedContext(h.s.ctx, n)
}

func (h *Human) VisibleCheckedContext(caller context.Context, n *chromedp.Node) (visible bool, err error) {
	ctx, cancel := h.s.readContext(caller)
	defer cancel()
	defer func() { err = operationError(ctx, err) }()
	return h.visibleChecked(ctx, n)
}

func (h *Human) visibleChecked(ctx context.Context, n *chromedp.Node) (bool, error) {
	unlock, err := h.lockDOM(ctx)
	if err != nil {
		return false, err
	}
	defer unlock()
	return h.readVisible(ctx, n)
}

func (h *Human) readVisible(ctx context.Context, n *chromedp.Node) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	res, err := chromedp.Call(ctx, dom.GetBoxModel, dom.GetBoxModelParams{BackendNodeID: n.BackendNodeID})
	if err != nil {
		if strings.Contains(err.Error(), "Could not compute box model") {
			return false, nil
		}
		return false, fmt.Errorf("读元素位置 %d：%w", n.BackendNodeID, err)
	}
	if res.Model == nil || res.Model.Width <= 0 || res.Model.Height <= 0 {
		return false, nil
	}
	for current := n; current != nil && current.NodeType == 1; current = current.Parent {
		id, err := h.currentNodeID(ctx, current)
		if err != nil {
			return false, fmt.Errorf("刷新元素编号 %d：%w", current.BackendNodeID, err)
		}
		style, err := chromedp.Call(ctx, css.GetComputedStyleForNode, css.GetComputedStyleForNodeParams{NodeID: id})
		if err != nil {
			return false, fmt.Errorf("读元素样式 %d：%w", id, err)
		}
		for _, p := range style.ComputedStyle {
			if p.Name == "display" && p.Value == "none" || p.Name == "opacity" && p.Value == "0" ||
				current == n && p.Name == "visibility" && (p.Value == "hidden" || p.Value == "collapse") {
				return false, nil
			}
		}
	}
	return true, nil
}

func (h *Human) currentNodeID(ctx context.Context, n *chromedp.Node) (cdp.NodeID, error) {
	res, err := chromedp.Call(ctx, dom.PushNodesByBackendIDsToFrontend, dom.PushNodesByBackendIDsToFrontendParams{BackendNodeIDs: []cdp.BackendNodeID{n.BackendNodeID}})
	if err != nil {
		return 0, err
	}
	if len(res.NodeIDs) != 1 || res.NodeIDs[0] == 0 {
		return 0, fmt.Errorf("元素已不在当前页面里")
	}
	return res.NodeIDs[0], nil
}

// SearchText 只用 DOM 搜索可见文字，不把脚本或样式里的内容当作页面提示。
func (h *Human) SearchText(caller context.Context, text string) (nodes []*chromedp.Node, err error) {
	ctx, cancelLifetime := h.s.operationContext(caller)
	defer cancelLifetime()
	tctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	defer func() { err = operationError(tctx, err) }()
	unlock, err := h.lockDOM(tctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	return retryDOMRead(func() ([]*chromedp.Node, error) { return h.searchText(tctx, text) })
}

func (h *Human) searchText(ctx context.Context, text string) ([]*chromedp.Node, error) {
	tctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := tctx.Err(); err != nil {
		return nil, err
	}
	depth := int64(-1)
	root, err := cdp.Call(tctx, h.queries, dom.GetDocument, dom.GetDocumentParams{Depth: &depth})
	if err != nil {
		return nil, err
	}
	nodes := nodeSnapshot(root.Root)
	search := func(query string) ([]cdp.NodeID, error) {
		found, err := cdp.Call(tctx, h.queries, dom.PerformSearch, dom.PerformSearchParams{Query: query})
		if err != nil {
			return nil, err
		}
		defer cdp.Call(tctx, h.queries, dom.DiscardSearchResults, dom.DiscardSearchResultsParams{SearchID: found.SearchID})
		if found.ResultCount == 0 {
			return nil, nil
		}
		hits, err := cdp.Call(tctx, h.queries, dom.GetSearchResults, dom.GetSearchResultsParams{SearchID: found.SearchID, ToIndex: found.ResultCount})
		return hits.NodeIDs, err
	}
	ids, err := search(text)
	if err != nil {
		return nil, err
	}
	// 普通文字搜索找不到跨标签的提示，补查最深的文本容器，再核对它真正显示的文字。
	literal := "'" + text + "'"
	if strings.Contains(text, "'") {
		literal = "concat('" + strings.Join(strings.Split(text, "'"), "',\"'\",'") + "')"
	}
	extra, err := search("//*[contains(string(.)," + literal + ") and not(.//*[contains(string(.)," + literal + ")])]")
	if err != nil {
		return nil, err
	}
	ids = append(ids, extra...)
	out := []*chromedp.Node{}
	seen := map[cdp.NodeID]bool{}
	var visibleText func(*chromedp.Node) (string, error)
	visibleText = func(n *chromedp.Node) (string, error) {
		if strings.EqualFold(n.NodeName, "script") || strings.EqualFold(n.NodeName, "style") {
			return "", nil
		}
		if n.NodeType == 3 {
			return n.NodeValue, nil
		}
		if n.NodeType == 1 {
			visible, err := h.readVisible(tctx, n)
			if err != nil || !visible {
				return "", err
			}
		}
		var text strings.Builder
		for _, child := range n.Children {
			part, err := visibleText(child)
			if err != nil {
				return "", err
			}
			text.WriteString(part)
		}
		return text.String(), nil
	}
	byBackend := map[cdp.BackendNodeID]*chromedp.Node{}
	for _, n := range nodes {
		byBackend[n.BackendNodeID] = n
	}
	for _, id := range ids {
		described, err := cdp.Call(tctx, h.queries, dom.DescribeNode, dom.DescribeNodeParams{NodeID: id})
		if err != nil {
			return nil, err
		}
		n := byBackend[described.Node.BackendNodeID]
		if n == nil {
			return nil, errDOMChanged
		}
		if n.NodeType == 3 {
			n = n.Parent
		}
		if n == nil || seen[n.NodeID] || n.NodeType != 1 {
			continue
		}
		seen[n.NodeID] = true
		excluded := false
		for ancestor := n; ancestor != nil; ancestor = ancestor.Parent {
			if strings.EqualFold(ancestor.NodeName, "script") || strings.EqualFold(ancestor.NodeName, "style") {
				excluded = true
				break
			}
		}
		if excluded {
			continue
		}
		shown, err := visibleText(n)
		if err != nil {
			return nil, err
		}
		if strings.Contains(shown, text) {
			out = append(out, n)
		}
	}
	return out, nil
}

func nodeSnapshot(root *cdp.Node) map[cdp.NodeID]*chromedp.Node {
	out := map[cdp.NodeID]*chromedp.Node{}
	var build func(*cdp.Node, *chromedp.Node) *chromedp.Node
	build = func(raw *cdp.Node, parent *chromedp.Node) *chromedp.Node {
		n := &chromedp.Node{Node: raw, Parent: parent}
		out[raw.NodeID] = n
		for _, child := range raw.Children {
			n.Children = append(n.Children, build(child, n))
		}
		return n
	}
	build(root, nil)
	return out
}

func (h *Human) Visible(n *chromedp.Node) bool {
	visible, _ := h.VisibleChecked(n)
	return visible
}

func (h *Human) FirstVisible(caller context.Context, sel string, timeout time.Duration) (node *chromedp.Node, err error) {
	ctx, cancelLifetime := h.s.operationContext(caller)
	defer cancelLifetime()
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	defer func() { err = h.findError(caller, tctx, sel, err) }()
	for {
		nodes, err := h.findNodes(tctx, sel)
		if err != nil {
			return nil, err
		}
		for _, n := range nodes {
			visible, err := h.visibleChecked(tctx, n)
			if err != nil {
				return nil, err
			}
			if visible {
				return n, nil
			}
		}
		if err := h.p.Wait(tctx, pacer.Between(50*time.Millisecond, 50*time.Millisecond)); err != nil {
			return nil, err
		}
	}
}

// Click 把鼠标移到元素上并点一下。元素不在可视区域时先滚过去。
func (h *Human) Click(ctx context.Context, n *chromedp.Node) error {
	return h.click(ctx, n, true)
}

// ClickNoScroll 只点击完整处于当前画面内的元素，绝不滚动页面。
func (h *Human) ClickNoScroll(ctx context.Context, n *chromedp.Node) error {
	return h.click(ctx, n, false)
}

func (h *Human) click(caller context.Context, n *chromedp.Node, scroll bool) (err error) {
	ctx, cancel := h.s.operationContext(caller)
	defer cancel()
	defer func() { err = operationError(ctx, err) }()
	if err := ctx.Err(); err != nil {
		return err
	}
	if scroll {
		if _, err := chromedp.Call(ctx, dom.ScrollIntoViewIfNeeded, dom.ScrollIntoViewIfNeededParams{BackendNodeID: n.BackendNodeID}); err != nil {
			return fmt.Errorf("滚动到元素失败：%w", err)
		}
		if err := h.p.Wait(ctx, pacer.Between(150*time.Millisecond, 400*time.Millisecond)); err != nil {
			return err
		}
	}
	x, y, w, ht, err := h.boxContext(ctx, n)
	if err != nil {
		return err
	}
	if !scroll {
		metrics, err := chromedp.Call(ctx, page.GetLayoutMetrics, cdp.Empty{})
		if err != nil {
			return fmt.Errorf("读取当前画面大小失败：%w", err)
		}
		viewport := metrics.CSSVisualViewport
		if viewport == nil || viewport.ClientWidth <= 0 || viewport.ClientHeight <= 0 {
			return fmt.Errorf("无法读取当前画面大小")
		}
		if w <= 0 || ht <= 0 || x < viewport.OffsetX || y < viewport.OffsetY || x+w > viewport.OffsetX+viewport.ClientWidth || y+ht > viewport.OffsetY+viewport.ClientHeight {
			return fmt.Errorf("%w（元素不在当前画面内）", ErrNotFound)
		}
	}
	// 落点在元素中间 60% 的范围里随机。
	tx := x + w*(0.2+0.6*h.rand())
	ty := y + ht*(0.2+0.6*h.rand())
	if err := h.moveTo(ctx, tx, ty); err != nil {
		return err
	}
	if err := h.mouseContext(ctx, input.DispatchMouseEventParams{Type: chromedp.MousePressed, X: tx, Y: ty, Button: input.MouseButtonLeft, Buttons: 1, ClickCount: 1}); err != nil {
		return err
	}
	if err := h.p.Wait(ctx, pacer.Between(60*time.Millisecond, 160*time.Millisecond)); err != nil {
		return err
	}
	return h.mouseContext(ctx, input.DispatchMouseEventParams{Type: chromedp.MouseReleased, X: tx, Y: ty, Button: input.MouseButtonLeft, ClickCount: 1})
}

// wheelParams 是滚轮事件的参数。cdproto 的结构体在值为 0 时会省略 deltaX，而 Chrome 要求 deltaX 和 deltaY 都必须给，所以单独定义。
type wheelParams struct {
	Type   input.DispatchMouseEventType `json:"type"`
	X      float64                      `json:"x"`
	Y      float64                      `json:"y"`
	DeltaX float64                      `json:"deltaX"`
	DeltaY float64                      `json:"deltaY"`
}

var dispatchWheel = cdp.Command[wheelParams, cdp.Empty]{Method: input.CommandDispatchMouseEvent}

// Scroll 在页面当前位置用滚轮向下滚 dy 像素，分几下滚完。
func (h *Human) Scroll(caller context.Context, dy float64) (err error) {
	ctx, cancel := h.s.operationContext(caller)
	defer cancel()
	defer func() { err = operationError(ctx, err) }()
	if err := ctx.Err(); err != nil {
		return err
	}
	steps := 3 + int(h.rand()*3)
	for i := 0; i < steps; i++ {
		p := wheelParams{Type: chromedp.MouseWheel, X: h.x, Y: h.y, DeltaY: dy / float64(steps)}
		if _, err := chromedp.Call(ctx, dispatchWheel, p); err != nil {
			return fmt.Errorf("滚轮操作失败：%w", err)
		}
		if err := h.p.Wait(ctx, pacer.Between(80*time.Millisecond, 220*time.Millisecond)); err != nil {
			return err
		}
	}
	return nil
}

// Type 逐字输入。英文和数字用按键事件，中文用输入法提交；不伪造组合输入事件。
func (h *Human) Type(caller context.Context, text string) (err error) {
	ctx, cancel := h.s.operationContext(caller)
	defer cancel()
	defer func() { err = operationError(ctx, err) }()
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, r := range text {
		var err error
		if r < utf8.RuneSelf && r >= 0x20 {
			s := string(r)
			if err = h.keyContext(ctx, input.DispatchKeyEventParams{Type: input.DispatchKeyEventTypeKeyDown, Text: s, Key: s, UnmodifiedText: s}); err == nil {
				err = h.keyContext(ctx, input.DispatchKeyEventParams{Type: input.DispatchKeyEventTypeKeyUp, Key: s})
			}
		} else {
			_, err = chromedp.Call(ctx, input.InsertText, input.InsertTextParams{Text: string(r)})
		}
		if err != nil {
			return fmt.Errorf("输入失败：%w", err)
		}
		if err := h.p.Wait(ctx, h.keyDelay); err != nil {
			return err
		}
	}
	return nil
}

// Press 按一次功能键，比如 "Enter"、"Tab"。
func (h *Human) Press(caller context.Context, key string) (err error) {
	ctx, cancel := h.s.operationContext(caller)
	defer cancel()
	defer func() { err = operationError(ctx, err) }()
	if err := ctx.Err(); err != nil {
		return err
	}
	k, ok := specialKeys[key]
	if !ok {
		return fmt.Errorf("不支持的按键：%s", key)
	}
	// 不产生文字的键用 rawKeyDown，和 Chrome 处理真实按键时一样。
	typ := input.DispatchKeyEventTypeKeyDown
	if k.text == "" {
		typ = input.DispatchKeyEventTypeRawKeyDown
	}
	down := input.DispatchKeyEventParams{Type: typ, Key: key, Code: k.code, WindowsVirtualKeyCode: k.vk, NativeVirtualKeyCode: k.vk, Text: k.text}
	if err := h.keyContext(ctx, down); err != nil {
		return err
	}
	if err := h.p.Wait(ctx, pacer.Between(50*time.Millisecond, 120*time.Millisecond)); err != nil {
		return err
	}
	return h.keyContext(ctx, input.DispatchKeyEventParams{Type: input.DispatchKeyEventTypeKeyUp, Key: key, Code: k.code, WindowsVirtualKeyCode: k.vk, NativeVirtualKeyCode: k.vk})
}

var specialKeys = map[string]struct {
	code string
	vk   int64
	text string
}{
	"Enter":     {"Enter", 13, "\r"},
	"Tab":       {"Tab", 9, ""},
	"Backspace": {"Backspace", 8, ""},
}

// moveTo 把鼠标分几步移到目标点，每步之间有很短的停顿。
func (h *Human) moveTo(ctx context.Context, tx, ty float64) error {
	steps := 6 + int(h.rand()*8)
	sx, sy := h.x, h.y
	for i := 1; i <= steps; i++ {
		t := float64(i) / float64(steps)
		// 先快后慢，更像手动移动。
		e := 1 - (1-t)*(1-t)
		x := sx + (tx-sx)*e
		y := sy + (ty-sy)*e
		if err := h.mouseContext(ctx, input.DispatchMouseEventParams{Type: chromedp.MouseMoved, X: x, Y: y}); err != nil {
			return err
		}
		if err := h.p.Wait(ctx, pacer.Between(8*time.Millisecond, 25*time.Millisecond)); err != nil {
			return err
		}
	}
	return nil
}

func (h *Human) mouseContext(ctx context.Context, p input.DispatchMouseEventParams) error {
	if _, err := chromedp.Call(ctx, input.DispatchMouseEvent, p); err != nil {
		return fmt.Errorf("鼠标操作失败：%w", err)
	}
	h.x, h.y = p.X, p.Y
	return nil
}

func (h *Human) keyContext(ctx context.Context, p input.DispatchKeyEventParams) error {
	_, err := chromedp.Call(ctx, input.DispatchKeyEvent, p)
	return err
}

// boxContext 返回元素内容区在页面上的位置和大小。
func (h *Human) boxContext(ctx context.Context, n *chromedp.Node) (x, y, w, ht float64, err error) {
	res, err := chromedp.Call(ctx, dom.GetBoxModel, dom.GetBoxModelParams{BackendNodeID: n.BackendNodeID})
	if err != nil && !strings.Contains(err.Error(), "Could not compute box model") {
		return 0, 0, 0, 0, fmt.Errorf("读元素位置 %d：%w", n.BackendNodeID, err)
	}
	if err != nil || res.Model == nil || len(res.Model.Content) < 8 {
		return 0, 0, 0, 0, fmt.Errorf("%w（没有位置）", ErrNotFound)
	}
	q := res.Model.Content
	minX, minY, maxX, maxY := q[0], q[1], q[0], q[1]
	for i := 0; i < 8; i += 2 {
		minX, maxX = min(minX, q[i]), max(maxX, q[i])
		minY, maxY = min(minY, q[i+1]), max(maxY, q[i+1])
	}
	return minX, minY, maxX - minX, maxY - minY, nil
}

func (h *Human) rand() float64 { return h.p.Rand() }

// Value 读输入框当前显示的值。不在页面里跑脚本：从无障碍树里取这个节点的 value（XIANYU.md 3.3 节）。
func (h *Human) Value(n *chromedp.Node) (string, error) {
	return h.ValueContext(h.s.ctx, n)
}

func (h *Human) ValueContext(caller context.Context, n *chromedp.Node) (value string, err error) {
	ctx, cancel := h.s.readContext(caller)
	defer cancel()
	defer func() { err = operationError(ctx, err) }()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	no := false
	res, err := chromedp.Call(ctx, accessibility.GetPartialAXTree, accessibility.GetPartialAXTreeParams{BackendNodeID: n.BackendNodeID, FetchRelatives: &no})
	if err != nil {
		return "", fmt.Errorf("读取输入框的值失败：%w", err)
	}
	for _, ax := range res.Nodes {
		if ax.Value == nil || len(ax.Value.Value) == 0 {
			continue
		}
		var s string
		if err := json.Unmarshal(ax.Value.Value, &s); err == nil {
			return s, nil
		}
	}
	return "", nil
}

// Clear 容器里用 Ctrl+A 全选，再按退格。
func (h *Human) Clear(caller context.Context) (err error) {
	ctx, cancel := h.s.operationContext(caller)
	defer cancel()
	defer func() { err = operationError(ctx, err) }()
	if err := ctx.Err(); err != nil {
		return err
	}
	a := input.DispatchKeyEventParams{Type: input.DispatchKeyEventTypeRawKeyDown, Key: "a", Code: "KeyA", WindowsVirtualKeyCode: 65, NativeVirtualKeyCode: 65, Modifiers: 2}
	if err := h.keyContext(ctx, a); err != nil {
		return err
	}
	if err := h.p.Wait(ctx, pacer.Between(50*time.Millisecond, 120*time.Millisecond)); err != nil {
		return err
	}
	if err := h.keyContext(ctx, input.DispatchKeyEventParams{Type: input.DispatchKeyEventTypeKeyUp, Key: "a", Code: "KeyA", WindowsVirtualKeyCode: 65, NativeVirtualKeyCode: 65, Modifiers: 2}); err != nil {
		return err
	}
	return h.Press(ctx, "Backspace")
}
