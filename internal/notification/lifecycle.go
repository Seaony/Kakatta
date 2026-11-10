package notification

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/notify"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/settings"
)

func New(deps Dependencies) (*Service, error) {
	if deps.DB == nil || deps.Settings == nil || deps.Cipher == nil {
		return nil, errors.New("通知服务缺少数据库、设置或加密依赖")
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.Sender == nil {
		deps.Sender = notify.New(notify.Dependencies{})
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{
		db: deps.DB, settings: deps.Settings, cipher: deps.Cipher, sender: deps.Sender,
		now: deps.Now, publish: deps.Publish, verificationHeldUntil: deps.VerificationHeldUntil,
		ctx: ctx, cancel: cancel, intentInFlight: map[int64]struct{}{}, eventInFlight: map[int64]struct{}{},
		channelJobs: map[channelIdentity]*channelJobs{},
	}
	s.queues.now, s.queues.startedAt = deps.Now, deps.Now()
	return s, nil
}

func (s *Service) beginJob(ctx context.Context) (context.Context, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	s.mu.Lock()
	if s.closed || s.paused > 0 || s.storageErr != nil {
		err := s.storageErr
		if err == nil {
			err = notificationClosedError()
			if !s.closed && s.paused > 0 {
				err = notificationPausedError()
			}
		}
		s.mu.Unlock()
		return nil, nil, err
	}
	if s.activeJobs == 0 {
		s.jobsIdle = make(chan struct{})
	}
	s.activeJobs++
	serviceContext := s.ctx
	s.mu.Unlock()
	work, cancel := context.WithCancel(ctx)
	stop := func() bool { return true }
	if serviceContext != nil {
		stop = context.AfterFunc(serviceContext, cancel)
	}
	var once sync.Once
	done := func() {
		once.Do(func() {
			stop()
			cancel()
			s.mu.Lock()
			s.activeJobs--
			if s.activeJobs == 0 {
				close(s.jobsIdle)
			}
			s.mu.Unlock()
		})
	}
	return work, done, nil
}

func (s *Service) beginChannel(ctx context.Context, identity channelIdentity) (context.Context, func(), error) {
	work, jobDone, err := s.beginJob(ctx)
	if err != nil {
		return nil, nil, err
	}
	work, cancel := context.WithCancel(work)
	use := &channelJob{cancel: cancel}
	s.mu.Lock()
	if s.channelJobs == nil {
		s.channelJobs = map[channelIdentity]*channelJobs{}
	}
	state := s.channelJobs[identity]
	if state == nil {
		state = &channelJobs{active: map[*channelJob]struct{}{}, idle: make(chan struct{})}
		close(state.idle)
		s.channelJobs[identity] = state
	}
	if state.blocked {
		s.mu.Unlock()
		cancel()
		jobDone()
		return nil, nil, &service.ServiceError{Kind: service.ErrorConflict, Reason: locales.Message{Code: "channels.changed"}, Fields: []settings.Issue{}}
	}
	if len(state.active) == 0 {
		state.idle = make(chan struct{})
	}
	state.active[use] = struct{}{}
	s.mu.Unlock()
	var once sync.Once
	done := func() {
		once.Do(func() {
			cancel()
			s.mu.Lock()
			delete(state.active, use)
			if len(state.active) == 0 {
				close(state.idle)
			}
			s.mu.Unlock()
			jobDone()
		})
	}
	return work, done, nil
}

func (s *Service) stopChannel(ctx context.Context, identity channelIdentity) error {
	s.mu.Lock()
	if s.channelJobs == nil {
		s.channelJobs = map[channelIdentity]*channelJobs{}
	}
	state := s.channelJobs[identity]
	if state == nil {
		state = &channelJobs{active: map[*channelJob]struct{}{}, idle: make(chan struct{})}
		close(state.idle)
		s.channelJobs[identity] = state
	}
	state.blocked = true
	cancel := make([]context.CancelFunc, 0, len(state.active))
	for use := range state.active {
		cancel = append(cancel, use.cancel)
	}
	idle := state.idle
	s.mu.Unlock()
	for _, cancelOne := range cancel {
		cancelOne()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-idle:
		return nil
	}
}

func (s *Service) resumeChannel(identity channelIdentity) {
	s.mu.Lock()
	if state := s.channelJobs[identity]; state != nil {
		state.blocked = false
	}
	s.mu.Unlock()
}

func (s *Service) forgetChannel(identity channelIdentity) {
	s.mu.Lock()
	if state := s.channelJobs[identity]; state != nil && len(state.active) == 0 {
		delete(s.channelJobs, identity)
	}
	s.mu.Unlock()
	s.queues.forget(identity)
}

func notificationClosedError() error {
	return &service.ServiceError{Kind: service.ErrorUnavailable, Reason: locales.Message{Code: "notification.closed"}, Fields: []settings.Issue{}}
}

func notificationPausedError() error {
	return &service.ServiceError{Kind: service.ErrorUnavailable, Reason: locales.Message{Code: "notification.paused"}, Fields: []settings.Issue{}}
}

func notificationStorageError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var business *service.ServiceError
	if errors.As(err, &business) {
		return err
	}
	return &service.ServiceError{Kind: service.ErrorUnavailable, Reason: locales.Message{Code: "notification.storage_failure"}, Fields: []settings.Issue{}}
}

func (s *Service) storageFailure(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var business *service.ServiceError
	if errors.As(err, &business) && business.Reason.Code != "notification.storage_failure" {
		return err
	}
	wrapped := notificationStorageError(err)
	s.mu.Lock()
	if s.storageErr == nil {
		s.storageErr = wrapped
	}
	stored := s.storageErr
	s.mu.Unlock()
	return stored
}

func (s *Service) checkStorage() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return notificationClosedError()
	}
	if s.paused > 0 {
		return notificationPausedError()
	}
	return s.storageErr
}

func settlementContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}

func (s *Service) retainReceipt(receipt func(context.Context) error) {
	s.mu.Lock()
	s.receipts = append(s.receipts, receipt)
	s.mu.Unlock()
}

func (s *Service) saveReceipt(ctx context.Context, receipt func(context.Context) error) error {
	settle, cancel := settlementContext(ctx)
	defer cancel()
	if err := receipt(settle); err != nil {
		s.retainReceipt(receipt)
		// 外发后的任何保存失败都要关门，不能按普通业务取消放过。
		return s.storageFailure(&service.ServiceError{Kind: service.ErrorUnavailable,
			Reason: locales.Message{Code: "notification.storage_failure"}, Fields: []settings.Issue{}})
	}
	return nil
}

func (s *Service) lockSettlement(ctx context.Context) (func(), error) {
	s.mu.Lock()
	if s.settlementGate == nil {
		s.settlementGate = make(chan struct{}, 1)
		s.settlementGate <- struct{}{}
	}
	gate := s.settlementGate
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-gate:
		return func() { gate <- struct{}{} }, nil
	}
}

func (s *Service) waitJobs(ctx context.Context) error {
	s.mu.Lock()
	if s.activeJobs == 0 {
		s.mu.Unlock()
		return nil
	}
	idle := s.jobsIdle
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-idle:
		return nil
	}
}

// Quiesce 返回后没有在途回执；调用方完成维护后恢复接纳。
func (s *Service) Quiesce(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, notificationClosedError()
	}
	s.paused++
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
	resume := sync.OnceFunc(func() {
		s.mu.Lock()
		s.paused--
		if s.paused == 0 && !s.closed {
			s.ctx, s.cancel = context.WithCancel(context.Background())
		}
		s.mu.Unlock()
	})
	unlock, err := s.lockSettlement(ctx)
	if err != nil {
		resume()
		return nil, err
	}
	defer unlock()
	if err := s.waitJobs(ctx); err != nil {
		resume()
		return nil, err
	}
	if err := s.settleReceipts(ctx, false); err != nil {
		resume()
		return nil, err
	}
	return resume, nil
}

func (s *Service) Close(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
	unlock, err := s.lockSettlement(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	if err := s.waitJobs(ctx); err != nil {
		return err
	}
	return s.settleReceipts(ctx, true)
}

func (s *Service) settleReceipts(ctx context.Context, closing bool) error {
	s.mu.Lock()
	receipts := append([]func(context.Context) error(nil), s.receipts...)
	s.receipts = nil
	s.mu.Unlock()
	settle, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var failed []func(context.Context) error
	for _, receipt := range receipts {
		if err := receipt(settle); err != nil {
			failed = append(failed, receipt)
		}
	}
	s.mu.Lock()
	s.receipts = append(failed, s.receipts...)
	// 维护不会清掉存储故障门禁；退出后的补记成功沿用 Close 的返回口径。
	if closing && len(receipts) > 0 && len(s.receipts) == 0 {
		s.storageErr = nil
	}
	err := s.storageErr
	if !closing && len(receipts) > 0 && len(s.receipts) == 0 {
		err = nil
	}
	s.mu.Unlock()
	return err
}

func (s *Service) emit(kind string, data any) error {
	if s.publish == nil {
		return nil
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return s.publish(kind, nil, raw)
}

func (s *Service) loadFormatOptions(ctx context.Context) (formatOptions, int64, error) {
	snapshot, err := s.settings.Get(ctx)
	if err != nil {
		return formatOptions{}, 0, notificationStorageError(err)
	}
	var preference, zone, target, publicURL string
	var maximum int64
	for _, value := range []struct {
		key    string
		target any
	}{
		{"general.language", &preference}, {"general.timezone", &zone}, {"notify.link_target", &target},
		{"general.public_url", &publicURL}, {"notify.max_per_run", &maximum},
	} {
		if err := json.Unmarshal(snapshot.Values[value.key], value.target); err != nil {
			return formatOptions{}, 0, notificationStorageError(err)
		}
	}
	location, err := time.LoadLocation(zone)
	if err != nil {
		return formatOptions{}, 0, notificationStorageError(err)
	}
	return formatOptions{Language: locales.Select(preference, os.Getenv("LANG")), Location: location, LinkTarget: target, PublicURL: publicURL}, maximum, nil
}

func deliveryError(name string, result notify.Result) *locales.Message {
	if result.OK {
		return nil
	}
	cause := result.ErrorCode
	args := map[string]any{"channel": name}
	switch cause {
	case "invalid_config", "invalid_message", "cancelled", "timeout", "transport", "invalid_response", "smtp_rejected":
	case "http_status":
		if result.HTTPStatus < 100 || result.HTTPStatus > 599 {
			cause = "transport"
		} else {
			args["status"] = result.HTTPStatus
		}
	case "provider_rejected":
		code := result.ProviderCode
		value, err := strconv.ParseFloat(code, 64)
		if code == "" || len(code) > 32 || strings.Trim(code, "0123456789+-.eE") != "" || err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			cause = "provider_rejected_generic"
		} else {
			args["code"] = code
		}
	default:
		cause = "transport"
	}
	return &locales.Message{Code: "notification.delivery." + cause, Args: args}
}
