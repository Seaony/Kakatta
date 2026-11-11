//go:build kakatta_dev

package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

type developmentState struct{ paused atomic.Bool }

func (s *Service) SetDevelopmentSchedulerPaused(paused bool) {
	s.initRuntime()
	s.development.paused.Store(paused)
	s.wakeScheduler()
}

func (s *Service) DevelopmentSchedulerPaused() bool {
	return s.automaticSchedulingPaused()
}

func (s *Service) automaticSchedulingPaused() bool { return s.development.paused.Load() }

// 切换只关闭新准入，已开始的运行和登录按原流程结束。
func (s *Service) PrepareDevelopmentReload(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	_, resumeBarrier, err := s.cleanupProtection(ctx)
	if err != nil {
		return nil, err
	}
	prepared := false
	defer func() {
		if !prepared {
			resumeBarrier()
		}
	}()
	if err := s.waitRuntime(ctx, func() bool {
		if s.closing || s.runtimeErr != nil {
			return true
		}
		if len(s.runs) != 0 {
			return false
		}
		for _, session := range s.sessions {
			if session.View.Phase != SessionQueued && session.View.Phase != SessionVerifyHold {
				return false
			}
		}
		return true
	}); err != nil {
		return nil, err
	}
	s.mu.Lock()
	err = s.runtimeErr
	if s.closing {
		err = sessionError(ErrorUnavailable, "runtime.closed")
	}
	sessions := make([]*sessionWork, 0, len(s.sessions))
	for _, session := range s.sessions {
		sessions = append(sessions, session)
	}
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	queued := s.queuedStarts()
	closed := make(chan error, 1)
	go func() {
		for _, session := range sessions {
			if err := ctx.Err(); err != nil {
				closed <- err
				return
			}
			if err := s.closeSessionWork(ctx, session); err != nil {
				closed <- err
				return
			}
		}
		for _, request := range queued {
			s.removeQueued(request)
		}
		closed <- nil
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case err := <-closed:
		if err != nil {
			return nil, err
		}
	}
	// 会话关闭后再等剩余执行者，验证监视器也会沿原来的取消路径退出。
	idle := make(chan struct{})
	go func() { s.jobs.Wait(); close(idle) }()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-idle:
	}
	s.mu.Lock()
	err = s.runtimeErr
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if missingRuntimeDependency(s.notifier) {
		return nil, storageFailure(errors.New("通知维护服务缺失"))
	}
	resumeNotifier, err := s.notifier.Quiesce(ctx)
	if err != nil {
		return nil, err
	}
	if resumeNotifier == nil {
		return nil, storageFailure(errors.New("通知维护恢复入口缺失"))
	}
	if err := ctx.Err(); err != nil {
		resumeNotifier()
		return nil, err
	}
	prepared = true
	return sync.OnceFunc(func() { resumeNotifier(); resumeBarrier() }), nil
}
