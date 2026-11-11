package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/chromedp/cdproto/page"
)

func TestScreenshotUsesOnlyExistingPage(t *testing.T) {
	ctx, wire := runtimeProtocol(t, false)
	if err := initializeTarget(ctx); err != nil {
		t.Fatal(err)
	}
	s := &Session{ctx: ctx}
	before := len(wire.commands())
	image, err := s.ScreenshotJPEG(t.Context())
	if err != nil || !bytes.Equal(image, []byte{0xff, 0xd8, 0xff, 0xd9}) {
		t.Fatalf("当前页面截图读取失败：%x %v", image, err)
	}
	commands := wire.commands()[before:]
	if len(commands) != 1 || string(commands[0].Method) != page.CaptureScreenshot.Method || commands[0].SessionID != "main-session" {
		t.Fatalf("截图未直接发给现有页面：%v", commands)
	}
	var args page.CaptureScreenshotParams
	if err := json.Unmarshal(commands[0].Params, &args); err != nil || args.Format != page.CaptureScreenshotFormatJpeg || args.Clip != nil || args.CaptureBeyondViewport != nil {
		t.Fatalf("截图未限制为当前可视区域：%+v %v", args, err)
	}
	caller, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.ScreenshotJPEG(caller); !errors.Is(err, context.Canceled) || len(wire.commands()) != before+1 {
		t.Fatalf("取消后仍发送截图命令：%v", err)
	}
}
