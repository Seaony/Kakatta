package notification

import (
	"context"
	"time"

	"github.com/Seaony/Kakatta/internal/notify"
)

func (q *channelQueues) acquire(ctx context.Context, identity channelIdentity) (*channelTurn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	q.mu.Lock()
	if q.now == nil {
		q.now = time.Now
	}
	if q.waitFor == nil {
		q.waitFor = waitChannelInterval
	}
	if q.startedAt.IsZero() {
		q.startedAt = q.now()
	}
	if q.lanes == nil {
		q.lanes = make(map[channelIdentity]*channelLane)
	}
	lane := q.lanes[identity]
	if lane == nil {
		lane = &channelLane{permit: make(chan struct{}, 1)}
		lane.permit <- struct{}{}
		q.lanes[identity] = lane
	}
	// 等待者也占一个引用，删除渠道时不能另建一条并行队列。
	lane.users++
	q.mu.Unlock()

	select {
	case <-ctx.Done():
		q.mu.Lock()
		lane.users--
		q.mu.Unlock()
		return nil, ctx.Err()
	case <-lane.permit:
	}

	turn := &channelTurn{queues: q, identity: identity, lane: lane}
	if err := ctx.Err(); err != nil {
		turn.finish("", false, time.Time{})
		return nil, err
	}
	return turn, nil
}

func (turn *channelTurn) wait(ctx context.Context, kind notify.Type) error {
	q := turn.queues
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		q.mu.Lock()
		gap := channelInterval(kind)
		until := q.startedAt.Add(gap)
		if turn.lane.lastGap > gap {
			gap = turn.lane.lastGap
		}
		if !turn.lane.lastFinishedAt.IsZero() {
			if next := turn.lane.lastFinishedAt.Add(gap); next.After(until) {
				until = next
			}
		}
		delay := until.Sub(q.now())
		q.mu.Unlock()
		if delay <= 0 {
			return ctx.Err()
		}
		if err := q.waitFor(ctx, delay); err != nil {
			return err
		}
	}
}

func (turn *channelTurn) finish(kind notify.Type, calledSender bool, endedAt time.Time) {
	q := turn.queues
	q.mu.Lock()
	if calledSender {
		turn.lane.lastFinishedAt = endedAt
		turn.lane.lastGap = channelInterval(kind)
	}
	turn.lane.permit <- struct{}{}
	turn.lane.users--
	q.mu.Unlock()
}

func (q *channelQueues) forget(identity channelIdentity) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	lane := q.lanes[identity]
	if lane != nil && lane.users != 0 {
		return false
	}
	delete(q.lanes, identity)
	return true
}

func channelInterval(kind notify.Type) time.Duration {
	switch kind {
	case notify.WeCom, notify.DingTalk:
		return 3 * time.Second
	case notify.Feishu:
		return time.Second
	default:
		return 0
	}
}

func waitChannelInterval(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}
