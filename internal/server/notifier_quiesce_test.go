package server

import "context"

// 此假通知器始终直接返回失败，没有独立的在途发送或回执。
func (failedBusinessNotifier) Quiesce(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return func() {}, nil
}
