package service

import "context"

// 这些假依赖没有自己的后台队列，执行者由业务 jobs 等待完成。
func quiesceTestNotifier(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return func() {}, nil
}

func (*finishNotifier) Quiesce(ctx context.Context) (func(), error) { return quiesceTestNotifier(ctx) }
func (*runStartNotifier) Quiesce(ctx context.Context) (func(), error) {
	return quiesceTestNotifier(ctx)
}
func (scheduleNotifier) Quiesce(ctx context.Context) (func(), error) { return quiesceTestNotifier(ctx) }
func (*runtimeCoreNotifier) Quiesce(ctx context.Context) (func(), error) {
	return quiesceTestNotifier(ctx)
}
func (*sessionEventNotifier) Quiesce(ctx context.Context) (func(), error) {
	return quiesceTestNotifier(ctx)
}
