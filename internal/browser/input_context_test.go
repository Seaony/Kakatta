package browser

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func inputOperation(h *Human, ctx context.Context, mode string) error {
	switch mode {
	case "Scroll":
		return h.Scroll(ctx, 120)
	case "TypeASCII":
		return h.Type(ctx, "a")
	case "TypeIME":
		return h.Type(ctx, "中")
	case "Press":
		return h.Press(ctx, "Enter")
	case "Clear":
		return h.Clear(ctx)
	default:
		panic("没有这个输入测试")
	}
}

func TestHumanInputCancellationStopsPendingCommands(t *testing.T) {
	for _, mode := range []string{"Scroll", "TypeASCII", "TypeIME", "Press", "Clear"} {
		t.Run(mode, func(t *testing.T) {
			human, _, wire := clickProtocol(t)
			wire.holdMethod = "Input.dispatchKeyEvent"
			if mode == "Scroll" {
				wire.holdMethod = "Input.dispatchMouseEvent"
			} else if mode == "TypeIME" {
				wire.holdMethod = "Input.insertText"
			}
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			finished := make(chan error, 1)
			go func() { finished <- inputOperation(human, ctx, mode) }()
			select {
			case <-wire.entered:
			case <-time.After(time.Second):
				t.Fatal("输入没有进入指定协议命令")
			}
			cause := errors.New("风控停止输入")
			cancel(cause)
			select {
			case err := <-finished:
				if !errors.Is(err, context.Canceled) || !errors.Is(err, cause) {
					t.Fatalf("输入取消的分类或原因丢失：%v", err)
				}
			case <-time.After(100 * time.Millisecond):
				wire.Close()
				select {
				case <-finished:
				case <-time.After(time.Second):
					t.Fatal("断开假协议后仍无法回收输入")
				}
				t.Fatal("取消本次输入后仍卡在浏览器协议调用")
			}
			if human.s.ctx.Err() != nil {
				t.Fatal("取消输入关闭了整个浏览器会话")
			}
		})
	}
}

func TestHumanInputPreCanceledCallerSendsNoCommands(t *testing.T) {
	for _, mode := range []string{"Scroll", "TypeASCII", "TypeIME", "Press", "Clear"} {
		t.Run(mode, func(t *testing.T) {
			human, _, wire := clickProtocol(t)
			before := len(wire.commands())
			ctx, cancel := context.WithCancelCause(t.Context())
			cause := errors.New("本次输入已取消")
			cancel(cause)
			err := inputOperation(human, ctx, mode)
			if !errors.Is(err, context.Canceled) || !errors.Is(err, cause) {
				t.Fatalf("预取消输入的分类或原因丢失：%v", err)
			}
			if after := len(wire.commands()); after != before {
				t.Fatalf("预取消仍发送输入命令：%d→%d", before, after)
			}
		})
	}
}

func TestHumanInputSessionCancellationKeepsCause(t *testing.T) {
	for _, mode := range []string{"Scroll", "TypeASCII", "TypeIME", "Press", "Clear"} {
		t.Run(mode, func(t *testing.T) {
			human, _, wire := clickProtocol(t)
			wire.holdMethod = "Input.dispatchKeyEvent"
			if mode == "Scroll" {
				wire.holdMethod = "Input.dispatchMouseEvent"
			} else if mode == "TypeIME" {
				wire.holdMethod = "Input.insertText"
			}
			lifetime, stop := context.WithCancelCause(human.s.ctx)
			human.s.ctx = lifetime
			defer stop(nil)
			finished := make(chan error, 1)
			go func() { finished <- inputOperation(human, t.Context(), mode) }()
			select {
			case <-wire.entered:
			case <-time.After(time.Second):
				t.Fatal("输入没有进入指定协议命令")
			}
			cause := errors.New("账号会话已经关闭")
			stop(cause)
			if err := awaitLookup(t, finished, stop); !errors.Is(err, context.Canceled) || !errors.Is(err, cause) {
				t.Fatalf("会话取消的分类或原因丢失：%v", err)
			}
		})
	}
}

func TestHumanInputKeepsOriginalEventSequences(t *testing.T) {
	for _, mode := range []string{"Scroll", "TypeASCII", "TypeIME", "Press", "Clear"} {
		t.Run(mode, func(t *testing.T) {
			human, _, wire := clickProtocol(t)
			if err := inputOperation(human, t.Context(), mode); err != nil {
				t.Fatal(err)
			}
			var methods []string
			var params []map[string]any
			for _, cmd := range wire.commands() {
				if !strings.HasPrefix(string(cmd.Method), "Input.") {
					continue
				}
				var value map[string]any
				if err := json.Unmarshal(cmd.Params, &value); err != nil {
					t.Fatal(err)
				}
				methods = append(methods, string(cmd.Method))
				params = append(params, value)
			}
			switch mode {
			case "Scroll":
				if len(params) != 4 {
					t.Fatalf("滚轮分段次数改变：%d", len(params))
				}
				for _, step := range params {
					if step["type"] != "mouseWheel" || step["deltaX"] != float64(0) || step["deltaY"] != float64(30) {
						t.Fatalf("滚轮参数改变：%v", step)
					}
				}
			case "TypeASCII", "Press":
				if len(params) != 2 || params[0]["type"] != "keyDown" || params[1]["type"] != "keyUp" {
					t.Fatalf("按下和抬起顺序改变：%v", params)
				}
			case "TypeIME":
				if len(params) != 1 || methods[0] != "Input.insertText" || params[0]["text"] != "中" {
					t.Fatalf("中文输入没有使用输入法提交：%v %v", methods, params)
				}
			case "Clear":
				if len(params) != 4 || params[0]["key"] != "a" || params[0]["modifiers"] != float64(2) || params[1]["type"] != "keyUp" || params[2]["key"] != "Backspace" || params[2]["type"] != "rawKeyDown" || params[3]["type"] != "keyUp" {
					t.Fatalf("清空输入框的按键组合改变：%v", params)
				}
			}
		})
	}
}
