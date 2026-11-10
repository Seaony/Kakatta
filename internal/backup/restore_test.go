package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/store"
)

type restoreRuntime struct {
	mu            sync.Mutex
	source        SnapshotSource
	beforeInstall func(context.Context, RestorePlan) error
	afterAuth     func(context.Context, RestorePlan) error
	afterSnapshot func() error
	snapshots     atomic.Int32
	installs      atomic.Int32
	closes        atomic.Int32
}

func (r *restoreRuntime) WithSnapshot(ctx context.Context, fn func(SnapshotSource) error) error {
	r.snapshots.Add(1)
	r.mu.Lock()
	source := r.source
	r.mu.Unlock()
	defer clear(source.MasterKey[:])
	if err := fn(source); err != nil {
		return err
	}
	if r.afterSnapshot != nil {
		return r.afterSnapshot()
	}
	return nil
}

func (r *restoreRuntime) Install(ctx context.Context, plan RestorePlan) error {
	defer clear(plan.MasterKey[:])
	r.installs.Add(1)
	if r.beforeInstall != nil {
		if err := r.beforeInstall(ctx, plan); err != nil {
			return err
		}
	}
	r.mu.Lock()
	generation := r.source.Generation
	r.mu.Unlock()
	if generation != plan.ExpectedGeneration {
		return ErrRestoreChanged
	}
	if plan.Authorize == nil {
		return ErrInvalidDependencies
	}
	if err := plan.Authorize(ctx); err != nil {
		return err
	}
	if r.afterAuth != nil {
		if err := r.afterAuth(ctx, plan); err != nil {
			return err
		}
	}
	r.mu.Lock()
	r.source.Generation += "-installed"
	r.mu.Unlock()
	return nil
}

func (r *restoreRuntime) Close() error { r.closes.Add(1); return nil }

func (r *restoreRuntime) generation(value string) {
	r.mu.Lock()
	r.source.Generation = value
	r.mu.Unlock()
}

type restoreFixture struct {
	s        *Service
	runtime  *restoreRuntime
	archive  *os.File
	size     int64
	key      [MasterKeyBytes]byte
	manifest Manifest
	now      atomic.Int64
	checks   atomic.Int32
	validate func(context.Context) error
}

func newRestoreFixture(t *testing.T, large bool) *restoreFixture {
	t.Helper()
	f := &restoreFixture{key: sha256.Sum256([]byte("private archive key"))}
	f.now.Store(time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC).UnixMilli())
	root := t.TempDir()
	live, err := store.Open(t.Context(), filepath.Join(root, "source.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	exportExec(t, live.DB(), `INSERT INTO meta(key,value) VALUES('restore.fixture','candidate')`)
	if large {
		exportExec(t, live.DB(), `INSERT INTO meta(key,value) VALUES('large.fixture',?)`, strings.Repeat("x", 3*ChunkSize))
	}
	snapshot := filepath.Join(root, "snapshot.db")
	if err := live.Backup(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	f.manifest, err = NewManifest("restore-fixture", store.SchemaVersion(), time.UnixMilli(f.now.Load()))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "source.zip")
	f.archive, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.archive.Close() })
	if err := WriteArchive(t.Context(), f.archive, snapshot, f.key, f.manifest, testPassword); err != nil {
		t.Fatal(err)
	}
	info, err := f.archive.Stat()
	if err != nil {
		t.Fatal(err)
	}
	f.size = info.Size()
	f.runtime = &restoreRuntime{source: SnapshotSource{MasterKey: sha256.Sum256([]byte("different current runtime key")), Generation: "generation-1", KeySource: KeyExternalFile}}
	data := t.TempDir()
	f.s, err = New(Dependencies{DataDir: data, AppVersion: "restore-test", SchemaVersion: store.SchemaVersion(), Runtime: f.runtime,
		Now: func() time.Time { return time.UnixMilli(f.now.Load()) },
		ValidateCandidate: func(ctx context.Context, path string, key [MasterKeyBytes]byte, schema int) error {
			defer clear(key[:])
			f.checks.Add(1)
			if key != f.key || schema != f.manifest.SchemaVersion {
				return errors.New("wrong private validation inputs")
			}
			if err := store.ValidateBackup(ctx, path, schema); err != nil {
				return err
			}
			if f.validate != nil {
				return f.validate(ctx)
			}
			return nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.s.Close() })
	return f
}

func (f *restoreFixture) prepare(t *testing.T, owner string) RestorePreview {
	t.Helper()
	preview, err := f.s.PrepareRestore(t.Context(), f.archive, f.size, testPassword, owner)
	if err != nil {
		t.Fatal("prepare failed", err)
	}
	return preview
}

func pendingRestore(s *Service) *restoreCandidate {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pending
}

func assertRestoreCleaned(t *testing.T, candidate *restoreCandidate) {
	t.Helper()
	if candidate.key != [MasterKeyBytes]byte{} {
		t.Fatal("candidate memory key was retained")
	}
	if _, err := os.Lstat(candidate.dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("candidate files were retained", err)
	}
}

func assertRestoreErrorSafe(t *testing.T, f *restoreFixture, err error) {
	t.Helper()
	if err == nil || strings.Contains(err.Error(), testPassword) || strings.Contains(err.Error(), f.s.deps.DataDir) || strings.Contains(err.Error(), hex.EncodeToString(f.key[:])) || strings.Contains(err.Error(), "private") {
		t.Fatal("restore error exposed private inputs")
	}
}

func TestRestorePrepareAndConfirmUseRealArchiveAndRuntimeMetadata(t *testing.T) {
	f := newRestoreFixture(t, false)
	currentKey := f.runtime.source.MasterKey
	if err := os.WriteFile(filepath.Join(f.s.deps.DataDir, MasterKeyEntry), []byte("wrong key file, must not be read"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, source := range []KeySource{KeyDefaultFile, KeyEnvironment, KeyExternalFile} {
		f.runtime.mu.Lock()
		f.runtime.source.KeySource = source
		expectedGeneration := f.runtime.source.Generation
		f.runtime.mu.Unlock()
		preview := f.prepare(t, "owner-a")
		candidate := pendingRestore(f.s)
		if preview.Ticket == "" || preview.Manifest != f.manifest || preview.ExpiresAt != f.now.Load()+int64(restoreLifetime/time.Millisecond) || candidate == nil || candidate.key != f.key || candidate.generation != expectedGeneration {
			t.Fatal("preview was not bound to the validated candidate")
		}
		if filepath.Dir(candidate.dir) != filepath.Join(f.s.deps.DataDir, ".backup-work") || !strings.HasPrefix(filepath.Base(candidate.dir), ".backup-restore-") {
			t.Fatal("恢复候选没有放在专用工作目录")
		}
		private := filepath.Join(candidate.dir, "candidate")
		for _, path := range []string{candidate.dir, private, filepath.Join(private, DatabaseEntry)} {
			info, err := os.Stat(path)
			mode := os.FileMode(0600)
			if info != nil && info.IsDir() {
				mode = 0700
			}
			if err != nil || info.Mode().Perm() != mode {
				t.Fatal("candidate permissions were not private", err)
			}
		}
		entries, err := os.ReadDir(private)
		if err != nil || len(entries) != 1 || entries[0].Name() != DatabaseEntry {
			t.Fatalf("等待确认时应只保存数据库（密钥来源 %s）：%v", source, err)
		}
		encoded, _ := json.Marshal(preview)
		for _, secret := range []string{testPassword, "owner-a", f.s.deps.DataDir, hex.EncodeToString(f.key[:])} {
			if strings.Contains(string(encoded), secret) {
				t.Fatal("preview exposed private state")
			}
		}
		var authorizations atomic.Int32
		f.runtime.afterAuth = func(ctx context.Context, plan RestorePlan) error {
			if plan.MasterKey != f.key || plan.KeySource != source || plan.ExpectedGeneration != expectedGeneration || plan.Manifest != f.manifest || plan.DatabasePath != filepath.Join(candidate.dir, "candidate", DatabaseEntry) || authorizations.Load() != 2 {
				t.Error("runtime received the wrong restore plan or authorizer")
			}
			if _, err := os.Stat(plan.DatabasePath); err != nil {
				t.Error("candidate was deleted before Install returned")
			}
			return nil
		}
		authorize := func(context.Context) error { authorizations.Add(1); return nil }
		if err := f.s.ConfirmRestore(t.Context(), preview.Ticket, "owner-a", authorize); err != nil {
			t.Fatal(err)
		}
		if authorizations.Load() != 2 || f.runtime.source.MasterKey != currentKey {
			t.Fatal("authorization was skipped or actual runtime key changed")
		}
		assertRestoreCleaned(t, candidate)
		if err := f.s.ConfirmRestore(t.Context(), preview.Ticket, "owner-a", authorize); !errors.Is(err, ErrRestoreTicket) {
			t.Fatal("ticket was reusable", err)
		}
	}
	if f.checks.Load() != 3 || f.runtime.installs.Load() != 3 || f.runtime.closes.Load() != 0 {
		t.Fatal("validation or runtime ownership was wrong")
	}
}

func TestRestoreReplacementKeepsOldPreviewUntilCompleteValidation(t *testing.T) {
	f := newRestoreFixture(t, false)
	first := f.prepare(t, "owner-a")
	old := pendingRestore(f.s)
	failure := errors.New("private callback " + testPassword + f.s.deps.DataDir)
	f.validate = func(context.Context) error { return failure }
	preview, err := f.s.PrepareRestore(t.Context(), f.archive, f.size, testPassword, "owner-b")
	if !errors.Is(err, ErrInvalidArchive) || !errors.Is(err, failure) || preview != (RestorePreview{}) || pendingRestore(f.s) != old || old.key != f.key {
		t.Fatal("partial validation replaced a valid preview", err)
	}
	assertRestoreErrorSafe(t, f, err)
	paths, _ := filepath.Glob(filepath.Join(f.s.workDir, ".backup-restore-*"))
	if len(paths) != 1 {
		t.Fatal("failed candidate files were retained")
	}
	f.validate = nil
	second := f.prepare(t, "owner-b")
	if first.Ticket == second.Ticket || pendingRestore(f.s) == old {
		t.Fatal("new candidate did not replace old ticket")
	}
	assertRestoreCleaned(t, old)
	if err := f.s.ConfirmRestore(t.Context(), first.Ticket, "owner-a", func(context.Context) error { return nil }); !errors.Is(err, ErrRestoreTicket) {
		t.Fatal(err)
	}
	if pendingRestore(f.s).preview.Ticket != second.Ticket {
		t.Fatal("old ticket consumed the new candidate")
	}
}

func TestRestoreRejectsEncryptedSQLiteWithChangedStructure(t *testing.T) {
	f := newRestoreFixture(t, false)
	preview := f.prepare(t, "owner-a")
	old := pendingRestore(f.s)
	path := filepath.Join(filepath.Dir(f.archive.Name()), "snapshot.db")
	damaged, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	exportExec(t, damaged.DB(), `DROP TABLE meta`)
	if err := damaged.Close(); err != nil {
		t.Fatal(err)
	}
	archive, err := os.OpenFile(filepath.Join(t.TempDir(), "damaged.zip"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	if err := WriteArchive(t.Context(), archive, path, f.key, f.manifest, testPassword); err != nil {
		t.Fatal(err)
	}
	info, err := archive.Stat()
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.s.PrepareRestore(t.Context(), archive, info.Size(), testPassword, "owner-b")
	if !errors.Is(err, ErrInvalidArchive) || got != (RestorePreview{}) || pendingRestore(f.s) != old || f.checks.Load() != 2 {
		t.Fatal("changed SQLite structure bypassed full validation", err)
	}
	assertRestoreErrorSafe(t, f, err)
	if pendingRestore(f.s).preview.Ticket != preview.Ticket || old.key != f.key {
		t.Fatal("invalid SQLite candidate replaced old preview")
	}
	paths, _ := filepath.Glob(filepath.Join(f.s.workDir, ".backup-restore-*"))
	if len(paths) != 1 {
		t.Fatal("invalid SQLite candidate files were retained")
	}
}

func TestRestoreTicketOwnerExpiryAndAuthorizerValidation(t *testing.T) {
	f := newRestoreFixture(t, false)
	preview := f.prepare(t, "owner-a")
	candidate := pendingRestore(f.s)
	var calls atomic.Int32
	authorize := func(context.Context) error { calls.Add(1); return nil }
	for _, request := range []struct{ ticket, owner string }{{preview.Ticket, "owner-b"}, {"unknown", "owner-a"}, {"", "owner-a"}, {preview.Ticket, ""}} {
		if err := f.s.ConfirmRestore(t.Context(), request.ticket, request.owner, authorize); !errors.Is(err, ErrRestoreTicket) {
			t.Fatal(err)
		}
		if pendingRestore(f.s) != candidate {
			t.Fatal("invalid owner or ticket consumed the candidate")
		}
	}
	if err := f.s.ConfirmRestore(t.Context(), preview.Ticket, "owner-a", nil); !errors.Is(err, ErrInvalidDependencies) || pendingRestore(f.s) != candidate {
		t.Fatal("nil authorizer was accepted", err)
	}
	f.now.Store(preview.ExpiresAt)
	if err := f.s.ConfirmRestore(t.Context(), preview.Ticket, "owner-a", authorize); !errors.Is(err, ErrRestoreExpired) {
		t.Fatal("expiry boundary accepted", err)
	}
	assertRestoreCleaned(t, candidate)
	if calls.Load() != 0 || f.runtime.installs.Load() != 0 {
		t.Fatal("expired ticket authorized or installed")
	}
	preview = f.prepare(t, "owner-a")
	f.now.Store(preview.ExpiresAt - 1)
	if err := f.s.ConfirmRestore(t.Context(), preview.Ticket, "owner-a", authorize); err != nil {
		t.Fatal("unexpired ticket rejected", err)
	}
}

func TestRestoreConcurrentConfirmInstallsTicketOnlyOnce(t *testing.T) {
	f := newRestoreFixture(t, false)
	preview := f.prepare(t, "owner-a")
	candidate := pendingRestore(f.s)
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	f.runtime.beforeInstall = func(context.Context, RestorePlan) error {
		close(entered)
		<-release
		return nil
	}
	var calls atomic.Int32
	authorize := func(context.Context) error { calls.Add(1); return nil }
	results := make(chan error, 2)
	go func() { results <- f.s.ConfirmRestore(t.Context(), preview.Ticket, "owner-a", authorize) }()
	awaitExportSignal(t, entered)
	started := make(chan struct{})
	go func() {
		close(started)
		results <- f.s.ConfirmRestore(t.Context(), preview.Ticket, "owner-a", authorize)
	}()
	awaitExportSignal(t, started)
	unblock()
	first, second := <-results, <-results
	if first != nil && second != nil || first == nil && second == nil {
		t.Fatal("same ticket did not have exactly one successful confirmation", first, second)
	}
	if failure := errors.Join(first, second); !errors.Is(failure, ErrRestoreTicket) {
		t.Fatal("duplicate confirmation did not reject consumed ticket", failure)
	}
	if f.runtime.installs.Load() != 1 || calls.Load() != 2 {
		t.Fatal("duplicate confirmation repeated install or authorization")
	}
	assertRestoreCleaned(t, candidate)
}

func TestRestoreConfirmFailuresConsumeAndCleanOnlyTheirCandidate(t *testing.T) {
	f := newRestoreFixture(t, false)
	for _, kind := range []string{"initial authorization", "second authorization", "generation", "install generation", "install"} {
		t.Run(kind, func(t *testing.T) {
			preview := f.prepare(t, "owner-a")
			candidate := pendingRestore(f.s)
			failure := errors.New("private install " + testPassword + f.s.deps.DataDir)
			f.runtime.beforeInstall, f.runtime.afterAuth = nil, nil
			if kind == "generation" {
				f.runtime.generation(candidate.generation + "-changed")
			}
			if kind == "install generation" {
				f.runtime.beforeInstall = func(context.Context, RestorePlan) error {
					f.runtime.generation(candidate.generation + "-changed")
					return nil
				}
			}
			if kind == "install" {
				f.runtime.afterAuth = func(context.Context, RestorePlan) error { return failure }
			}
			var calls int
			authorize := func(context.Context) error {
				calls++
				if kind == "initial authorization" && calls == 1 || kind == "second authorization" && calls == 2 {
					return failure
				}
				return nil
			}
			err := f.s.ConfirmRestore(t.Context(), preview.Ticket, "owner-a", authorize)
			assertRestoreErrorSafe(t, f, err)
			if kind == "generation" || kind == "install generation" {
				if !errors.Is(err, ErrRestoreChanged) {
					t.Fatal("generation failure chain lost", err)
				}
			} else if !errors.Is(err, failure) {
				t.Fatal("callback failure chain lost", err)
			}
			assertRestoreCleaned(t, candidate)
			if err := f.s.ConfirmRestore(t.Context(), preview.Ticket, "owner-a", authorize); !errors.Is(err, ErrRestoreTicket) {
				t.Fatal("failed ticket was reusable", err)
			}
		})
	}
}

func TestRestorePrepareFailuresNeverReturnPartialPreview(t *testing.T) {
	f := newRestoreFixture(t, false)
	for _, kind := range []string{"password", "cancelled", "validation cancellation", "snapshot cancellation", "snapshot", "missing validator"} {
		t.Run(kind, func(t *testing.T) {
			ctx := t.Context()
			password := testPassword
			validate := f.s.deps.ValidateCandidate
			failure := errors.New("private snapshot " + testPassword + f.s.deps.DataDir)
			var cancel context.CancelFunc
			want := ErrIO
			switch kind {
			case "password":
				password, want = "wrong-password", ErrAuthentication
			case "cancelled":
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = context.Canceled
			case "validation cancellation":
				ctx, cancel = context.WithCancel(ctx)
				f.validate = func(context.Context) error { cancel(); return nil }
				want = context.Canceled
			case "snapshot cancellation":
				ctx, cancel = context.WithCancel(ctx)
				f.runtime.afterSnapshot = func() error { cancel(); return nil }
				want = context.Canceled
			case "snapshot":
				f.runtime.afterSnapshot = func() error { return failure }
			case "missing validator":
				f.s.deps.ValidateCandidate, want = nil, ErrInvalidDependencies
			}
			if cancel != nil {
				defer cancel()
			}
			preview, err := f.s.PrepareRestore(ctx, f.archive, f.size, password, "owner-a")
			f.validate, f.runtime.afterSnapshot, f.s.deps.ValidateCandidate = nil, nil, validate
			if !errors.Is(err, want) || preview != (RestorePreview{}) || pendingRestore(f.s) != nil {
				t.Fatal("prepare left a partial preview", err)
			}
			if kind == "snapshot" && !errors.Is(err, failure) {
				t.Fatal("snapshot failure cause lost")
			}
			assertRestoreErrorSafe(t, f, err)
			paths, _ := filepath.Glob(filepath.Join(f.s.workDir, ".backup-restore-*"))
			if len(paths) != 0 {
				t.Fatal("failed prepare left candidate files")
			}
		})
	}
}

func waitRestorePendingGone(t *testing.T, s *Service) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for pendingRestore(s) != nil {
		if time.Now().After(deadline) {
			t.Fatal("restore timer did not expire candidate")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestRestoreTimerExpiryLateCallbackAndCloseOwnCleanup(t *testing.T) {
	f := newRestoreFixture(t, false)
	preview := f.prepare(t, "owner-a")
	expired := pendingRestore(f.s)
	expired.timer.Reset(time.Millisecond)
	waitRestorePendingGone(t, f.s)
	_, done, err := f.s.begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	assertRestoreCleaned(t, expired)
	done()
	if err := f.s.ConfirmRestore(t.Context(), preview.Ticket, "owner-a", func(context.Context) error { return nil }); !errors.Is(err, ErrRestoreTicket) {
		t.Fatal(err)
	}
	f.prepare(t, "owner-a")
	old := pendingRestore(f.s)
	f.prepare(t, "owner-b")
	current := pendingRestore(f.s)
	f.s.expireRestore(old)
	if pendingRestore(f.s) != current {
		t.Fatal("old timer consumed a new candidate")
	}
	current.timer.Reset(0)
	closed := make(chan error, 2)
	go func() { closed <- f.s.Close() }()
	go func() { closed <- f.s.Close() }()
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	assertRestoreCleaned(t, current)
	if f.runtime.closes.Load() != 0 {
		t.Fatal("backup service closed the shared runtime")
	}
}

func TestRestoreCloseCancelsPrepareAndJoinsValidationAndQueuedOperations(t *testing.T) {
	f := newRestoreFixture(t, false)
	preview := f.prepare(t, "owner-a")
	old := pendingRestore(f.s)
	entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	f.validate = func(ctx context.Context) error {
		close(entered)
		<-ctx.Done()
		close(cancelled)
		<-release
		return ctx.Err()
	}
	prepared := make(chan error, 1)
	go func() {
		_, err := f.s.PrepareRestore(t.Context(), f.archive, f.size, testPassword, "owner-b")
		prepared <- err
	}()
	awaitExportSignal(t, entered)
	queuedCtx, stop := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer stop()
	if err := f.s.Export(queuedCtx, testPassword, io.Discard); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("restore did not share export slot", err)
	}
	confirmed := make(chan error, 1)
	go func() {
		confirmed <- f.s.ConfirmRestore(t.Context(), preview.Ticket, "owner-a", func(context.Context) error { return nil })
	}()
	closed := make(chan error, 1)
	go func() { closed <- f.s.Close() }()
	awaitExportSignal(t, cancelled)
	select {
	case <-closed:
		t.Fatal("Close did not wait for validation callback")
	default:
	}
	unblock()
	if err := <-prepared; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := <-confirmed; !errors.Is(err, context.Canceled) && !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	assertRestoreCleaned(t, old)
	paths, _ := filepath.Glob(filepath.Join(f.s.workDir, ".backup-restore-*"))
	if len(paths) != 0 || f.runtime.installs.Load() != 0 {
		t.Fatal("Close retained a candidate or installed queued work")
	}
	if _, err := f.s.PrepareRestore(t.Context(), f.archive, f.size, testPassword, "owner-a"); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}

func TestRestoreCloseWaitsForInstallBeforeCleaningItsOwnedFiles(t *testing.T) {
	f := newRestoreFixture(t, false)
	preview := f.prepare(t, "owner-a")
	candidate := pendingRestore(f.s)
	entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	f.runtime.beforeInstall = func(ctx context.Context, _ RestorePlan) error {
		close(entered)
		<-ctx.Done()
		close(cancelled)
		<-release
		return ctx.Err()
	}
	confirmed := make(chan error, 1)
	go func() {
		confirmed <- f.s.ConfirmRestore(t.Context(), preview.Ticket, "owner-a", func(context.Context) error { return nil })
	}()
	awaitExportSignal(t, entered)
	closed := make(chan error, 1)
	go func() { closed <- f.s.Close() }()
	awaitExportSignal(t, cancelled)
	if _, err := os.Stat(candidate.dir); err != nil || candidate.key != f.key {
		t.Fatal("Close deleted files still owned by Install")
	}
	select {
	case <-closed:
		t.Fatal("Close did not join Install")
	default:
	}
	unblock()
	if err := <-confirmed; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	assertRestoreCleaned(t, candidate)
}

func TestRestoreCommittedSuccessOnlyRecordsCleanupFailure(t *testing.T) {
	f := newRestoreFixture(t, false)
	preview := f.prepare(t, "owner-a")
	candidate := pendingRestore(f.s)
	original := f.s.workDir
	moved := original + "-moved"
	changed := false
	defer func() {
		if changed {
			_ = os.Remove(original)
			if err := os.Rename(moved, original); err != nil {
				t.Error(err)
			}
		}
	}()
	f.runtime.afterAuth = func(context.Context, RestorePlan) error {
		if err := os.Rename(original, moved); err != nil {
			return err
		}
		changed = true
		return os.WriteFile(original, nil, 0600)
	}
	if err := f.s.ConfirmRestore(t.Context(), preview.Ticket, "owner-a", func(context.Context) error { return nil }); err != nil {
		t.Fatal("committed restore reported failure", err)
	}
	if candidate.key != [MasterKeyBytes]byte{} {
		t.Fatal("cleanup failure retained memory key")
	}
	if err := f.s.Close(); !errors.Is(err, ErrIO) {
		t.Fatal("cleanup problem was not retained for Close", err)
	} else {
		assertRestoreErrorSafe(t, f, err)
	}
}

type boundedRestoreReader struct {
	source  io.ReaderAt
	largest atomic.Int64
}

func (r *boundedRestoreReader) ReadAt(dst []byte, offset int64) (int, error) {
	if len(dst) > 2*ChunkSize {
		return 0, errors.New("private unbounded restore read")
	}
	for previous := r.largest.Load(); int64(len(dst)) > previous; previous = r.largest.Load() {
		if r.largest.CompareAndSwap(previous, int64(len(dst))) {
			break
		}
	}
	return r.source.ReadAt(dst, offset)
}

func TestRestoreLargeRealSQLiteArchiveUsesBoundedReads(t *testing.T) {
	f := newRestoreFixture(t, true)
	reader := &boundedRestoreReader{source: f.archive}
	preview, err := f.s.PrepareRestore(t.Context(), reader, f.size, testPassword, "owner-a")
	if err != nil || preview.Ticket == "" || f.size <= 2*ChunkSize || reader.largest.Load() == 0 || reader.largest.Load() > 2*ChunkSize {
		t.Fatal("restore did not stream a real multi-chunk SQLite archive", err)
	}
}
