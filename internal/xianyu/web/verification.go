package web

import "context"

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
			visible, err := r.H.VisibleChecked(node)
			if err != nil {
				return nil, err
			}
			if !visible {
				continue
			}
			x, y, width, height, err := r.H.Box(node)
			if err != nil {
				return nil, err
			}
			return &VerificationBox{X: x, Y: y, Width: width, Height: height}, nil
		}
	}
	return nil, nil
}
