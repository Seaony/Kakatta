package service

import (
	"context"
	"errors"
	"syscall"

	"github.com/Seaony/Kakatta/internal/locales"
)

func (s *Service) RuntimeContext() context.Context {
	s.initRuntime()
	return s.runtimeCtx
}

// 故障说明只取内存中的安全类别，存储不可写时仍能读取。
func (s *Service) RuntimeFailure() *locales.Message {
	s.initRuntime()
	s.mu.Lock()
	err := s.runtimeErr
	if err == nil {
		err = s.prepareErr
	}
	s.mu.Unlock()
	if err == nil {
		return nil
	}
	code := "runtime.storage_unavailable"
	var sqliteCode interface{ Code() int }
	switch {
	case errors.Is(err, syscall.ENOSPC):
		code = "runtime.storage_full"
	case errors.As(err, &sqliteCode):
		switch sqliteCode.Code() & 255 {
		case 13:
			code = "runtime.storage_full"
		case 5, 6:
			code = "runtime.storage_locked"
		}
	}
	return &locales.Message{Code: code, Args: map[string]any{}}
}

func (s *Service) Health(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if failure := s.RuntimeFailure(); failure != nil {
		return &ServiceError{Kind: ErrorUnavailable, Reason: *failure}
	}
	s.mu.Lock()
	closed := s.closing
	s.mu.Unlock()
	if closed {
		return sessionError(ErrorUnavailable, "runtime.closed")
	}
	return s.db.PingContext(ctx)
}
