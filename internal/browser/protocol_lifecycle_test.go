package browser

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

func TestProtocolFixturesCancelSessionWithoutClosingTransport(t *testing.T) {
	for _, fixture := range []struct {
		name   string
		create func(*testing.T) (*Session, context.CancelCauseFunc)
	}{
		{"generation", func(t *testing.T) (*Session, context.CancelCauseFunc) {
			h, _, stop := generationProtocol(t)
			return h.s, stop
		}},
		{"lookup", func(t *testing.T) (*Session, context.CancelCauseFunc) {
			h, _, stop := lookupProtocol(t)
			return h.s, stop
		}},
		{"navigation", func(t *testing.T) (*Session, context.CancelCauseFunc) {
			s, _, stop := navigationProtocol(t)
			return s, stop
		}},
		{"read", func(t *testing.T) (*Session, context.CancelCauseFunc) {
			h, _, _, stop := readProtocol(t)
			return h.s, stop
		}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			s, stop := fixture.create(t)
			cause := errors.New("测试会话已经关闭")
			stop(cause)
			if !errors.Is(s.ctx.Err(), context.Canceled) || !errors.Is(context.Cause(s.ctx), cause) {
				t.Fatal("取消函数没有直接结束测试会话并保留原因")
			}
			// 只检查假传输的寿命，避免把断开连接误当成会话取消。
			ctx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), time.Second)
			defer cancel()
			tree, err := chromedp.Call(ctx, page.GetFrameTree, cdp.Empty{})
			if err != nil || tree.FrameTree == nil {
				t.Fatalf("取消测试会话同时关闭了假传输：%v", err)
			}
			if !errors.Is(context.Cause(s.ctx), cause) {
				t.Fatal("检查传输改变了原会话取消原因")
			}
		})
	}
}
