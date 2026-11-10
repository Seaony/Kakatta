package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"time"
	"unicode/utf8"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/css"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"

	"github.com/Seaony/Kakatta/internal/pacer"
)

// ErrNotFound 表示页面上找不到要操作的元素。
var ErrNotFound = errors.New("页面上找不到元素")

// Human 用真实的鼠标和键盘事件操作页面，所有停顿都走 pacer。
// 点击坐标取元素中心附近的随机点，鼠标从上一个位置分几步移过去，而不是瞬间出现在目标上。
type Human struct {
	s        *Session
	p        *pacer.Pacer
	keyDelay pacer.Range // 每个字之间的间隔
	x, y     float64     // 鼠标当前位置
}

// NewHuman 创建操作者。keyDelay 是逐字输入时每个字之间的间隔。
func NewHuman(s *Session, p *pacer.Pacer, keyDelay pacer.Range) *Human {
	return &Human{s: s, p: p, keyDelay: keyDelay, x: 200, y: 200}
}

// Find 在 timeout 内找出所有匹配的元素，找不到返回 ErrNotFound。
// sel 是 CSS 选择器；以 "//" 开头时按 XPath 找。
func (h *Human) Find(ctx context.Context, sel string, timeout time.Duration) ([]*chromedp.Node, error) {
	tctx, cancel := context.WithTimeout(h.s.ctx, timeout)
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()

	var nodes []*chromedp.Node
	var err error
	if len(sel) >= 2 && sel[:2] == "//" {
		nodes, err = chromedp.Run(tctx, chromedp.Nodes(chromedp.Search(sel)))
	} else {
		nodes, err = chromedp.Run(tctx, chromedp.Nodes(chromedp.CSSAll(sel)))
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w：%s", ErrNotFound, sel)
	}
	return nodes, nil
}

// FindNow 立即查一次，不等待；没有匹配时返回空列表。用来做风控检查这类「有就报、没有就过」的判断。
func (h *Human) FindNow(ctx context.Context, sel string) ([]*chromedp.Node, error) {
	tctx, cancel := context.WithTimeout(h.s.ctx, 5*time.Second)
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	if len(sel) >= 2 && sel[:2] == "//" {
		return chromedp.Run(tctx, chromedp.Nodes(chromedp.Search(sel), chromedp.AtLeast(0)))
	}
	return chromedp.Run(tctx, chromedp.Nodes(chromedp.CSSAll(sel), chromedp.AtLeast(0)))
}

// Box 返回元素在页面上的位置和大小，找不到位置时返回错误。
func (h *Human) Box(n *chromedp.Node) (x, y, w, ht float64, err error) { return h.box(n) }

// Visible 判断元素当前是否显示在页面上：有位置、宽高大于 0，并且自身没有被隐藏或设为完全透明。
// 只看元素自身的样式；父元素透明的情况交给具体流程按需要再查。
func (h *Human) Visible(n *chromedp.Node) bool {
	_, _, w, ht, err := h.box(n)
	if err != nil || w <= 0 || ht <= 0 {
		return false
	}
	res, err := chromedp.Call(h.s.ctx, css.GetComputedStyleForNode, css.GetComputedStyleForNodeParams{NodeID: n.NodeID})
	if err != nil {
		return false
	}
	for _, p := range res.ComputedStyle {
		switch {
		case p.Name == "display" && p.Value == "none",
			p.Name == "visibility" && (p.Value == "hidden" || p.Value == "collapse"),
			p.Name == "opacity" && p.Value == "0":
			return false
		}
	}
	return true
}

// FirstVisible 返回第一个可见的匹配元素。
func (h *Human) FirstVisible(ctx context.Context, sel string, timeout time.Duration) (*chromedp.Node, error) {
	nodes, err := h.Find(ctx, sel, timeout)
	if err != nil {
		return nil, err
	}
	for _, n := range nodes {
		if h.Visible(n) {
			return n, nil
		}
	}
	return nil, fmt.Errorf("%w（不可见）：%s", ErrNotFound, sel)
}

// Click 把鼠标移到元素上并点一下。元素不在可视区域时先滚过去。
func (h *Human) Click(ctx context.Context, n *chromedp.Node) error {
	if _, err := chromedp.Call(h.s.ctx, dom.ScrollIntoViewIfNeeded, dom.ScrollIntoViewIfNeededParams{NodeID: n.NodeID}); err != nil {
		return fmt.Errorf("滚动到元素失败：%w", err)
	}
	if err := h.p.Wait(ctx, pacer.Between(150*time.Millisecond, 400*time.Millisecond)); err != nil {
		return err
	}
	x, y, w, ht, err := h.box(n)
	if err != nil {
		return err
	}
	// 落点在元素中间 60% 的范围里随机。
	tx := x + w*(0.2+0.6*h.rand())
	ty := y + ht*(0.2+0.6*h.rand())
	if err := h.moveTo(ctx, tx, ty); err != nil {
		return err
	}
	if err := h.mouse(input.DispatchMouseEventParams{Type: chromedp.MousePressed, X: tx, Y: ty, Button: input.MouseButtonLeft, Buttons: 1, ClickCount: 1}); err != nil {
		return err
	}
	if err := h.p.Wait(ctx, pacer.Between(60*time.Millisecond, 160*time.Millisecond)); err != nil {
		return err
	}
	return h.mouse(input.DispatchMouseEventParams{Type: chromedp.MouseReleased, X: tx, Y: ty, Button: input.MouseButtonLeft, ClickCount: 1})
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
func (h *Human) Scroll(ctx context.Context, dy float64) error {
	steps := 3 + int(h.rand()*3)
	for i := 0; i < steps; i++ {
		p := wheelParams{Type: chromedp.MouseWheel, X: h.x, Y: h.y, DeltaY: dy / float64(steps)}
		if _, err := chromedp.Call(h.s.ctx, dispatchWheel, p); err != nil {
			return fmt.Errorf("滚轮操作失败：%w", err)
		}
		if err := h.p.Wait(ctx, pacer.Between(80*time.Millisecond, 220*time.Millisecond)); err != nil {
			return err
		}
	}
	return nil
}

// Type 逐字输入。英文和数字用按键事件；中文等用输入法提交的方式（Input.insertText），和真人用输入法打字时页面收到的事件一致。
func (h *Human) Type(ctx context.Context, text string) error {
	for _, r := range text {
		var err error
		if r < utf8.RuneSelf && r >= 0x20 {
			s := string(r)
			if err = h.key(input.DispatchKeyEventParams{Type: input.DispatchKeyEventTypeKeyDown, Text: s, Key: s, UnmodifiedText: s}); err == nil {
				err = h.key(input.DispatchKeyEventParams{Type: input.DispatchKeyEventTypeKeyUp, Key: s})
			}
		} else {
			_, err = chromedp.Call(h.s.ctx, input.InsertText, input.InsertTextParams{Text: string(r)})
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
func (h *Human) Press(ctx context.Context, key string) error {
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
	if err := h.key(down); err != nil {
		return err
	}
	if err := h.p.Wait(ctx, pacer.Between(50*time.Millisecond, 120*time.Millisecond)); err != nil {
		return err
	}
	return h.key(input.DispatchKeyEventParams{Type: input.DispatchKeyEventTypeKeyUp, Key: key, Code: k.code, WindowsVirtualKeyCode: k.vk, NativeVirtualKeyCode: k.vk})
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
		if err := h.mouse(input.DispatchMouseEventParams{Type: chromedp.MouseMoved, X: x, Y: y}); err != nil {
			return err
		}
		if err := h.p.Wait(ctx, pacer.Between(8*time.Millisecond, 25*time.Millisecond)); err != nil {
			return err
		}
	}
	return nil
}

func (h *Human) mouse(p input.DispatchMouseEventParams) error {
	if _, err := chromedp.Call(h.s.ctx, input.DispatchMouseEvent, p); err != nil {
		return fmt.Errorf("鼠标操作失败：%w", err)
	}
	h.x, h.y = p.X, p.Y
	return nil
}

func (h *Human) key(p input.DispatchKeyEventParams) error {
	_, err := chromedp.Call(h.s.ctx, input.DispatchKeyEvent, p)
	return err
}

// box 返回元素内容区在页面上的位置和大小。
func (h *Human) box(n *chromedp.Node) (x, y, w, ht float64, err error) {
	res, err := chromedp.Call(h.s.ctx, dom.GetBoxModel, dom.GetBoxModelParams{NodeID: n.NodeID})
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
	no := false
	res, err := chromedp.Call(h.s.ctx, accessibility.GetPartialAXTree, accessibility.GetPartialAXTreeParams{BackendNodeID: n.BackendNodeID, FetchRelatives: &no})
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

// Clear 清空当前获得焦点的输入框：全选再按退格（XIANYU.md 3.3 节）。
// macOS 上 Cmd+A 是由系统菜单处理的，按键事件本身不会触发全选，所以同时带上 selectAll 编辑命令，和真按 Cmd+A 的效果一样。
func (h *Human) Clear(ctx context.Context) error {
	mod, cmds := int64(2), []string(nil) // Ctrl
	if runtime.GOOS == "darwin" {
		mod, cmds = 4, []string{"selectAll"} // Cmd
	}
	a := input.DispatchKeyEventParams{Type: input.DispatchKeyEventTypeRawKeyDown, Key: "a", Code: "KeyA", WindowsVirtualKeyCode: 65, NativeVirtualKeyCode: 65, Modifiers: mod, Commands: cmds}
	if err := h.key(a); err != nil {
		return err
	}
	if err := h.key(input.DispatchKeyEventParams{Type: input.DispatchKeyEventTypeKeyUp, Key: "a", Code: "KeyA", WindowsVirtualKeyCode: 65, NativeVirtualKeyCode: 65, Modifiers: mod}); err != nil {
		return err
	}
	if err := h.p.Wait(ctx, pacer.Between(80*time.Millisecond, 200*time.Millisecond)); err != nil {
		return err
	}
	return h.Press(ctx, "Backspace")
}
