package server

import (
	"context"
	"errors"
	"sync"
	"time"
)

type attemptResult uint8

const (
	attemptNeutral attemptResult = iota
	attemptFailure
	attemptSuccess
)

const (
	loginWindow    = time.Minute
	loginIPReset   = 15 * time.Minute
	loginMaxFailed = 20
	loginMaxActive = 4
)

var errLoginEvent = errors.New("登录保护事件保存失败")

type ipFailures struct {
	count uint8
	last  time.Time
	next  time.Time
}

type loginLimiter struct {
	mu       sync.Mutex
	now      func() time.Time
	event    func(context.Context, LoginFailureEvent) error
	highest  time.Time
	ips      map[string]ipFailures
	failed   []time.Time
	inflight int

	pending      []LoginFailureEvent
	eventRunning chan struct{}
	retryAt      time.Time
}

type loginAttempt struct {
	limiter *loginLimiter
	ip      string
	once    sync.Once
	err     error
}

func newLoginLimiter(now func() time.Time, event func(context.Context, LoginFailureEvent) error) *loginLimiter {
	if now == nil {
		now = time.Now
	}
	return &loginLimiter{now: now, event: event, ips: make(map[string]ipFailures)}
}

func (l *loginLimiter) begin(ctx context.Context, ip string) (*loginAttempt, time.Duration, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	if err := l.flushEvents(ctx, false); err != nil {
		return nil, 0, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	now := l.timeLocked()
	l.pruneLocked(now)
	if len(l.pending) != 0 {
		return nil, 0, errLoginEvent
	}
	var wait time.Duration
	if entry, ok := l.ips[ip]; ok && entry.next.After(now) {
		wait = entry.next.Sub(now)
	}
	if l.inflight >= loginMaxActive {
		wait = max(wait, time.Second)
	}
	if len(l.failed)+l.inflight >= loginMaxFailed {
		globalWait := time.Second
		if len(l.failed) >= loginMaxFailed {
			globalWait = l.failed[0].Add(loginWindow).Sub(now)
		}
		wait = max(wait, globalWait)
	}
	if wait > 0 {
		return nil, wait, nil
	}
	l.inflight++
	return &loginAttempt{limiter: l, ip: ip}, 0, nil
}

func (a *loginAttempt) finish(ctx context.Context, result attemptResult) error {
	a.once.Do(func() {
		l := a.limiter
		l.mu.Lock()
		now := l.timeLocked()
		l.pruneLocked(now)
		l.inflight--
		switch result {
		case attemptFailure:
			entry := l.ips[a.ip]
			entry.count = min(entry.count+1, 7)
			entry.last = now
			delay := min(time.Duration(1)<<int(entry.count-1), 60) * time.Second
			entry.next = now.Add(delay)
			l.ips[a.ip] = entry
			l.failed = append(l.failed, now)
			if len(l.failed) == loginMaxFailed {
				l.pending = append(l.pending, LoginFailureEvent{Active: true, Limit: loginMaxFailed, IP: a.ip})
			}
		case attemptSuccess:
			delete(l.ips, a.ip)
			l.pending = append(l.pending, LoginFailureEvent{Active: false})
		}
		l.mu.Unlock()
		a.err = l.flushEvents(ctx, true)
	})
	return a.err
}

func (l *loginLimiter) timeLocked() time.Time {
	now := l.now().UTC()
	if now.Before(l.highest) {
		return l.highest
	}
	l.highest = now
	return now
}

func (l *loginLimiter) pruneLocked(now time.Time) {
	first := 0
	for first < len(l.failed) && !l.failed[first].Add(loginWindow).After(now) {
		first++
	}
	if first > 0 {
		l.failed = append(l.failed[:0], l.failed[first:]...)
	}
	for ip, entry := range l.ips {
		if !entry.last.Add(loginIPReset).After(now) {
			delete(l.ips, ip)
		}
	}
}

func (l *loginLimiter) flushEvents(parent context.Context, wait bool) error {
	// 请求取消不能把已经触发的保护事件一起取消。
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	defer cancel()
	for {
		l.mu.Lock()
		now := l.timeLocked()
		if len(l.pending) == 0 {
			l.mu.Unlock()
			return nil
		}
		if running := l.eventRunning; running != nil {
			l.mu.Unlock()
			if !wait {
				return errLoginEvent
			}
			select {
			case <-running:
				continue
			case <-ctx.Done():
				return errLoginEvent
			}
		}
		if l.retryAt.After(now) {
			l.mu.Unlock()
			return errLoginEvent
		}
		event := l.pending[0]
		running := make(chan struct{})
		l.eventRunning = running
		l.mu.Unlock()

		err := errLoginEvent
		if l.event != nil {
			err = l.event(ctx, event)
		}

		l.mu.Lock()
		l.eventRunning = nil
		if err == nil {
			l.pending = l.pending[1:]
			l.retryAt = time.Time{}
		} else {
			l.retryAt = l.timeLocked().Add(time.Second)
		}
		close(running)
		l.mu.Unlock()
		if err != nil {
			return errLoginEvent
		}
	}
}
