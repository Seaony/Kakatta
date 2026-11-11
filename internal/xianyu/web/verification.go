package web

import (
	"context"
	"math"
	"time"

	"github.com/Seaony/Kakatta/internal/remote"
)

type VerificationBox struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// CurrentVerificationBox 只读当前页面里可见的验证框，坐标是网页内容区坐标。
func (r *Runner) CurrentVerificationBox(ctx context.Context) (*VerificationBox, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, selector := range rules.Selectors.VerifyMasks {
		nodes, err := r.H.FindNow(ctx, selector)
		if err != nil {
			return nil, err
		}
		for _, node := range nodes {
			visible, err := r.H.VisibleCheckedContext(ctx, node)
			if err != nil {
				return nil, err
			}
			if !visible {
				continue
			}
			x, y, width, height, err := r.H.BoxContext(ctx, node)
			if err != nil {
				return nil, err
			}
			return &VerificationBox{X: x, Y: y, Width: width, Height: height}, nil
		}
	}
	return nil, nil
}

// VerificationViewport 换算到虚拟屏幕坐标，只给用户定位远程画面。
func (r *Runner) VerificationViewport(ctx context.Context) (*remote.Viewport, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	box, err := r.CurrentVerificationBox(ctx)
	if err != nil || box == nil {
		return nil, err
	}
	left, top, err := r.S.ContentScreenOrigin(ctx)
	if err != nil {
		return nil, err
	}
	return verificationScreenBox(*box, left, top), nil
}

func verificationScreenBox(box VerificationBox, originX, originY int64) *remote.Viewport {
	const width, height, padding = 1920, 1080, 24
	x0 := int(math.Floor(box.X)) + int(originX)
	y0 := int(math.Floor(box.Y)) + int(originY)
	x1 := int(math.Ceil(box.X+box.Width)) + int(originX)
	y1 := int(math.Ceil(box.Y+box.Height)) + int(originY)
	if box.Width <= 0 || box.Height <= 0 || x0 >= width || y0 >= height || x1 <= 0 || y1 <= 0 {
		return nil
	}
	x0, y0 = max(0, x0-padding), max(0, y0-padding)
	x1, y1 = min(width, x1+padding), min(height, y1+padding)
	return &remote.Viewport{X: x0, Y: y0, Width: x1 - x0, Height: y1 - y0}
}
