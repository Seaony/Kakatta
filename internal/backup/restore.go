package backup

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

const restoreLifetime = 15 * time.Minute

var (
	ErrRestoreTicket        = errors.New("backup.restore_ticket_invalid")
	ErrRestoreExpired       = errors.New("backup.restore_expired")
	ErrRestoreChanged       = errors.New("backup.restore_changed")
	ErrRestoreAuthorization = errors.New("backup.restore_authorization_failed")
)

type restoreFailure struct{ code, cause error }

func (e *restoreFailure) Error() string   { return e.code.Error() }
func (e *restoreFailure) Unwrap() []error { return []error{e.code, e.cause} }

func safeRestoreFailure(code, err error) error {
	if err == context.Canceled || err == context.DeadlineExceeded {
		return err
	}
	return &restoreFailure{code, err}
}

type restoreCandidate struct {
	preview    RestorePreview
	dir        string
	owner      string
	generation string
	keySource  KeySource
	key        [MasterKeyBytes]byte
	timer      *time.Timer
}

func (s *Service) restoreGeneration(ctx context.Context) (string, KeySource, error) {
	var generation string
	var keySource KeySource
	called := false
	err := s.deps.Runtime.WithSnapshot(ctx, func(source SnapshotSource) error {
		clear(source.MasterKey[:])
		if called || source.Generation == "" || source.KeySource != KeyDefaultFile && source.KeySource != KeyEnvironment && source.KeySource != KeyExternalFile {
			return ErrInvalidDependencies
		}
		called = true
		generation, keySource = source.Generation, source.KeySource
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrInvalidDependencies) {
			return "", "", safeRestoreFailure(ErrInvalidDependencies, err)
		}
		return "", "", safeRestoreFailure(ErrIO, err)
	}
	if !called {
		return "", "", ErrInvalidDependencies
	}
	return generation, keySource, nil
}

func (s *Service) cleanRestore(candidate *restoreCandidate) error {
	if candidate == nil {
		return nil
	}
	if candidate.timer != nil {
		candidate.timer.Stop()
	}
	clear(candidate.key[:])
	if err := os.RemoveAll(candidate.dir); err != nil {
		failure := safeRestoreFailure(ErrIO, err)
		s.mu.Lock()
		if s.cleanupErr == nil {
			s.cleanupErr = failure
		}
		s.mu.Unlock()
		slog.Error("恢复候选清理失败", "reason", ErrIO.Error())
		return failure
	}
	return nil
}

func (s *Service) expireRestore(candidate *restoreCandidate) {
	_, done, err := s.begin(context.Background())
	if err != nil {
		return
	}
	defer done()
	s.mu.Lock()
	if s.pending != candidate {
		s.mu.Unlock()
		return
	}
	s.pending = nil
	s.mu.Unlock()
	_ = s.cleanRestore(candidate)
}

func (s *Service) PrepareRestore(ctx context.Context, src io.ReaderAt, size int64, password, owner string) (preview RestorePreview, err error) {
	if password == "" {
		return preview, ErrPasswordRequired
	}
	if src == nil || owner == "" || s.deps.ValidateCandidate == nil {
		return preview, ErrInvalidDependencies
	}
	ctx, done, err := s.begin(ctx)
	if err != nil {
		return preview, err
	}
	defer done()
	dir, err := os.MkdirTemp(s.workDir, restoreWorkPrefix)
	if err != nil {
		return preview, safeRestoreFailure(ErrIO, err)
	}
	candidate := &restoreCandidate{dir: dir, owner: owner}
	retained := false
	defer func() {
		if !retained {
			err = errors.Join(err, s.cleanRestore(candidate))
		}
	}()
	if err := os.Chmod(dir, 0700); err != nil {
		return preview, safeRestoreFailure(ErrIO, err)
	}
	private := filepath.Join(dir, "candidate")
	manifest, key, err := ReadArchive(ctx, src, size, password, private, s.deps.SchemaVersion)
	defer clear(key[:])
	if err != nil {
		return preview, err
	}
	path := filepath.Join(private, DatabaseEntry)
	if err := s.deps.ValidateCandidate(ctx, path, key, manifest.SchemaVersion); err != nil {
		return preview, safeRestoreFailure(ErrInvalidArchive, err)
	}
	if err := ctx.Err(); err != nil {
		return preview, err
	}
	generation, keySource, err := s.restoreGeneration(ctx)
	if err != nil {
		return preview, err
	}
	candidate.generation, candidate.keySource, candidate.key = generation, keySource, key
	candidate.preview = RestorePreview{Ticket: rand.Text(), Manifest: manifest, ExpiresAt: s.deps.Now().Add(restoreLifetime).UnixMilli()}
	s.mu.Lock()
	if s.closed || ctx.Err() != nil {
		s.mu.Unlock()
		if err := ctx.Err(); err != nil {
			return preview, err
		}
		return preview, ErrClosed
	}
	previous := s.pending
	s.pending = candidate
	candidate.timer = time.AfterFunc(restoreLifetime, func() { s.expireRestore(candidate) })
	retained = true
	s.mu.Unlock()
	_ = s.cleanRestore(previous)
	return candidate.preview, nil
}

func (s *Service) ConfirmRestore(ctx context.Context, ticket, owner string, authorize func(context.Context) error) (err error) {
	if ticket == "" || owner == "" {
		return ErrRestoreTicket
	}
	if authorize == nil {
		return ErrInvalidDependencies
	}
	ctx, done, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer done()
	s.mu.Lock()
	candidate := s.pending
	if candidate == nil || candidate.preview.Ticket != ticket || candidate.owner != owner {
		s.mu.Unlock()
		return ErrRestoreTicket
	}
	s.pending = nil
	candidate.timer.Stop()
	expired := s.deps.Now().UnixMilli() >= candidate.preview.ExpiresAt
	s.mu.Unlock()
	installed := false
	defer func() {
		if cleanup := s.cleanRestore(candidate); cleanup != nil && !installed {
			err = errors.Join(err, cleanup)
		}
	}()
	if expired {
		return ErrRestoreExpired
	}
	if err := authorize(ctx); err != nil {
		return safeRestoreFailure(ErrRestoreAuthorization, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	generation, _, err := s.restoreGeneration(ctx)
	if err != nil {
		return err
	}
	if generation != candidate.generation {
		return ErrRestoreChanged
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	plan := RestorePlan{Manifest: candidate.preview.Manifest, DatabasePath: filepath.Join(candidate.dir, "candidate", DatabaseEntry),
		MasterKey: candidate.key, KeySource: candidate.keySource, ExpectedGeneration: candidate.generation, Authorize: authorize}
	defer clear(plan.MasterKey[:])
	if err := s.deps.Runtime.Install(ctx, plan); err != nil {
		return safeRestoreFailure(ErrIO, err)
	}
	installed = true
	return nil
}
