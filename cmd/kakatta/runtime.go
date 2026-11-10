package main

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Seaony/Kakatta/internal/backup"
	"github.com/Seaony/Kakatta/internal/buildinfo"
	"github.com/Seaony/Kakatta/internal/config"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/server"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/store"
)

type runtimeManager struct {
	ctx     context.Context
	cancel  context.CancelFunc
	cfg     config.Config
	paths   config.Paths
	keyFile string
	logger  *slog.Logger
	backups *backup.Service
	failed  chan error
	fatal   sync.Once

	switchMu     sync.Mutex
	mu           sync.Mutex
	graph        *runtimeGraph
	accept       bool
	requests     context.Context
	stopRequests context.CancelFunc
	inflight     int
	drained      chan struct{}

	closeOnce sync.Once
	closeErr  error
}

type runtimeBackupGenerationKey struct{}

func newRuntimeManager(life context.Context, cfg config.Config, paths config.Paths, key [backup.MasterKeyBytes]byte, source backup.KeySource, keyFile string, logger *slog.Logger) (_ *runtimeManager, err error) {
	defer clear(key[:])
	ctx, cancel := context.WithCancel(life)
	m := &runtimeManager{ctx: ctx, cancel: cancel, cfg: cfg, paths: paths, keyFile: keyFile, logger: logger, failed: make(chan error, 1), drained: make(chan struct{})}
	close(m.drained)
	defer func() {
		if err != nil {
			cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
			err = errors.Join(err, m.Close(cleanup))
			stop()
		}
	}()
	m.backups, err = backup.New(backup.Dependencies{
		DataDir: paths.DataDir, AppVersion: buildinfo.Version, SchemaVersion: store.SchemaVersion(), Runtime: m,
		ValidateCandidate: func(ctx context.Context, path string, key [backup.MasterKeyBytes]byte, version int) error {
			defer clear(key[:])
			return validateRuntimeCandidate(ctx, path, key, version, cfg.Environment)
		},
	})
	if err != nil {
		return nil, err
	}
	g, err := m.buildGraph(key, source)
	if err != nil {
		return nil, err
	}
	m.graph = g
	if err = g.core.Prepare(ctx); err != nil {
		return nil, err
	}
	if err = g.start(); err != nil {
		return nil, err
	}
	m.resume(g)
	return m, nil
}

func (m *runtimeManager) buildGraph(key [backup.MasterKeyBytes]byte, source backup.KeySource) (*runtimeGraph, error) {
	defer clear(key[:])
	g, err := newRuntimeGraph(m.ctx, m.cfg, m.paths, key, source, rand.Text())
	if err != nil {
		return nil, err
	}
	if err = g.handler.RegisterTransfers(g.core); err == nil {
		err = g.handler.RegisterBackup(m.backups)
	}
	if err == nil {
		err = g.handler.RegisterUpdate(g.updates)
	}
	if err == nil {
		err = g.handler.RegisterMCP(g.core)
	}
	if err != nil {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if closeErr := g.close(cleanup); closeErr != nil {
			return nil, errors.Join(err, errRuntimeGraphNotDrained, closeErr)
		}
		return nil, err
	}
	return g, nil
}

func backupRequest(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	switch r.URL.Path {
	case "/api/v1/backup/export", "/api/v1/backup/restore", "/api/v1/backup/confirm":
		return true
	}
	return false
}

func (m *runtimeManager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	if !m.accept || m.ctx.Err() != nil {
		m.mu.Unlock()
		id := rand.Text()
		w.Header().Set("X-Request-Id", id)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		if r.Body != nil && r.Body != http.NoBody {
			w.Header().Set("Connection", "close")
		}
		server.WriteJSON(w, r, http.StatusServiceUnavailable, server.ErrorEnvelope{
			Error: locales.Message{Code: "http.unavailable", Args: map[string]any{}}, Fields: []settings.Issue{}, RequestID: id,
		})
		return
	}
	g, scope := m.graph, m.requests
	leased := !backupRequest(r)
	requestContext := r.Context()
	if leased {
		if m.inflight == 0 {
			m.drained = make(chan struct{})
		}
		m.inflight++
	} else {
		// 备份操作可能等待切换锁，所以不计入旧请求排空。只有确认需要跨代完成。
		requestContext = context.WithValue(requestContext, runtimeBackupGenerationKey{}, g.generation)
		if r.URL.Path == "/api/v1/backup/confirm" {
			scope = m.ctx
		}
	}
	m.mu.Unlock()
	ctx, cancel := context.WithCancel(requestContext)
	interrupted := make(chan struct{})
	stop := context.AfterFunc(scope, func() {
		defer close(interrupted)
		cancel()
		// 阻塞在网络读写中的请求也要退出，不能只取消数据库操作。
		controller := http.NewResponseController(w)
		now := time.Now()
		_ = controller.SetReadDeadline(now)
		_ = controller.SetWriteDeadline(now)
	})
	defer func() {
		if !stop() {
			<-interrupted
		}
		cancel()
		if leased {
			m.mu.Lock()
			m.inflight--
			if m.inflight == 0 {
				close(m.drained)
			}
			m.mu.Unlock()
		}
	}()
	g.handler.ServeHTTP(w, r.WithContext(ctx))
}

func (m *runtimeManager) pause(ctx context.Context) error {
	m.mu.Lock()
	m.accept = false
	if m.stopRequests != nil {
		m.stopRequests()
	}
	drained := m.drained
	m.mu.Unlock()
	select {
	case <-drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *runtimeManager) resume(g *runtimeGraph) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.graph = g
	m.requests, m.stopRequests = context.WithCancel(m.ctx)
	m.accept = m.ctx.Err() == nil
}

func (m *runtimeManager) stop(err error) {
	m.fatal.Do(func() {
		m.cancel()
		m.failed <- err
	})
}

func (m *runtimeManager) WithSnapshot(ctx context.Context, fn func(backup.SnapshotSource) error) error {
	m.switchMu.Lock()
	defer m.switchMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	g, ready := m.graph, m.accept && m.ctx.Err() == nil
	m.mu.Unlock()
	if !ready || g == nil || fn == nil {
		return backup.ErrClosed
	}
	if expected, bound := ctx.Value(runtimeBackupGenerationKey{}).(string); bound && expected != g.generation {
		return backup.ErrRestoreChanged
	}
	source := backup.SnapshotSource{Store: g.database, MasterKey: g.key, KeySource: g.keySource, Generation: g.generation}
	defer clear(source.MasterKey[:])
	return fn(source)
}

func (m *runtimeManager) Install(ctx context.Context, plan backup.RestorePlan) error {
	defer clear(plan.MasterKey[:])
	m.switchMu.Lock()
	defer m.switchMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	old, ready := m.graph, m.accept && m.ctx.Err() == nil
	m.mu.Unlock()
	if !ready || old == nil {
		return backup.ErrClosed
	}
	if plan.ExpectedGeneration != old.generation || plan.KeySource != old.keySource {
		return backup.ErrRestoreChanged
	}
	if plan.Authorize == nil {
		return backup.ErrRestoreAuthorization
	}
	lock, err := config.AcquireRestoreLock(m.paths.DataDir, true)
	if err != nil {
		return err
	}
	defer lock.Close()
	if _, err := os.Lstat(filepath.Join(m.paths.DataDir, config.RestoreStateName)); !errors.Is(err, os.ErrNotExist) {
		return errRestoreFilesState
	}
	if err := checkExternalKeyPaths(m.keyFile, m.paths.DataDir, "kakatta.db", "kakatta.db-wal", "kakatta.db-shm", "browser", "cache/images", ".restore", backup.WorkDirName); err != nil {
		return err
	}
	pause, cancelPause := context.WithTimeout(ctx, 30*time.Second)
	err = m.pause(pause)
	cancelPause()
	if err != nil {
		m.resume(old)
		return err
	}
	if err := plan.Authorize(ctx); err != nil {
		m.resume(old)
		return errors.Join(backup.ErrRestoreAuthorization, err)
	}
	oldKey := old.key
	defer clear(oldKey[:])
	// 用户断开连接后也要把已经开始的旧运行代收尾做完。
	settle, cancelSettle := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancelSettle()
	if err := old.drain(settle); err != nil {
		m.stop(err)
		return err
	}
	hostClock, err := readRuntimeClock(settle, old.database.DB())
	if err != nil {
		m.stop(err)
		return err
	}
	if err := old.close(settle); err != nil {
		m.stop(err)
		return err
	}
	targetKey := plan.MasterKey
	if old.keySource != backup.KeyDefaultFile {
		targetKey = oldKey
	}
	defer clear(targetKey[:])
	var files *restoreFiles
	var candidate *runtimeGraph
	rollback := func(cause error) error {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if candidate != nil {
			if err := candidate.close(cleanup); err != nil {
				m.stop(err)
				return errors.Join(cause, err)
			}
		}
		if files != nil {
			if err := files.rollback(cleanup); err != nil {
				m.stop(err)
				return errors.Join(cause, err)
			}
		}
		if m.ctx.Err() != nil {
			return errors.Join(cause, m.ctx.Err())
		}
		restored, err := m.buildGraph(oldKey, old.keySource)
		if err == nil {
			err = restored.core.Prepare(cleanup)
		}
		if err == nil {
			err = restored.start()
		}
		if err != nil {
			if restored != nil {
				err = errors.Join(err, restored.close(cleanup))
			}
			m.stop(err)
			return errors.Join(cause, err)
		}
		m.resume(restored)
		return cause
	}
	if err := prepareRuntimeCandidate(ctx, plan.DatabasePath, plan.MasterKey, targetKey, hostClock, m.cfg.Environment, time.Now()); err != nil {
		return rollback(err)
	}
	plan.MasterKey = targetKey
	files, err = stageRestoreFiles(ctx, m.paths.DataDir, plan)
	if err != nil {
		return rollback(err)
	}
	if err := files.install(ctx); err != nil {
		return rollback(err)
	}
	if _, err := config.CreateDataDirectories(m.paths.DataDir); err != nil {
		return rollback(err)
	}
	candidate, err = m.buildGraph(targetKey, old.keySource)
	if err != nil {
		if errors.Is(err, errRuntimeGraphNotDrained) {
			m.stop(err)
			return err
		}
		return rollback(err)
	}
	if err := candidate.core.Prepare(ctx); err != nil {
		return rollback(err)
	}
	if err := ctx.Err(); err != nil {
		return rollback(err)
	}
	mayHaveCommitted, err := files.commit()
	if err != nil && !mayHaveCommitted {
		return rollback(err)
	}
	// 提交记录可能已落盘时保留全部证据，重启按记录恢复。
	if err == nil {
		err = candidate.start()
	}
	if err != nil {
		m.mu.Lock()
		m.graph = candidate
		m.mu.Unlock()
		m.stop(err)
		return err
	}
	m.resume(candidate)
	if err := files.cleanupCommitted(context.Background()); err != nil {
		m.logger.Error("恢复已提交，旧副本等待清理", "reason", "restore.storage_failure")
	}
	return nil
}

func checkExternalKeyPaths(keyFile, dataDir string, resources ...string) error {
	if keyFile == "" {
		return nil
	}
	path, err := absoluteKeyPath(keyFile)
	if err != nil {
		return err
	}
	dataDir, err = filepath.EvalSymlinks(dataDir)
	if err != nil {
		return err
	}
	dataDir, err = filepath.Abs(dataDir)
	if err != nil {
		return err
	}
	managed := make(map[string]bool, len(resources))
	for _, name := range resources {
		managed[filepath.Join(dataDir, name)] = true
	}
	// 路径中途经过的目录也会被搬走，即使链接的最终目标在外部。
	separator := string(filepath.Separator)
	current, parts, links := separator, strings.Split(path, separator), 0
	for len(parts) != 0 {
		part := parts[0]
		parts = parts[1:]
		switch part {
		case "", ".":
			continue
		case "..":
			current = filepath.Dir(current)
			continue
		}
		next := filepath.Join(current, part)
		if managed[next] {
			return errors.New("外部主密钥路径不能经过备份或恢复要清理的位置")
		}
		info, err := os.Lstat(next)
		if err != nil {
			return errors.New("无法确认外部主密钥位置")
		}
		if info.Mode()&os.ModeSymlink == 0 {
			current = next
			continue
		}
		links++
		link, err := os.Readlink(next)
		if err != nil || links > 40 {
			return errors.New("无法确认外部主密钥位置")
		}
		if filepath.IsAbs(link) {
			current = separator
		}
		parts = append(strings.Split(link, separator), parts...)
	}
	info, err := os.Stat(current)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("外部主密钥必须是普通文件")
	}
	for resource := range managed {
		if other, err := os.Lstat(resource); err == nil && os.SameFile(info, other) {
			return errors.New("外部主密钥不能与恢复要替换的文件相同")
		}
	}
	return nil
}

func absoluteKeyPath(path string) (string, error) {
	if filepath.IsAbs(path) {
		return path, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return wd + string(filepath.Separator) + path, nil
}

func (m *runtimeManager) Close(ctx context.Context) error {
	m.closeOnce.Do(func() {
		m.cancel()
		if m.backups != nil {
			m.closeErr = m.backups.Close()
		}
		m.switchMu.Lock()
		defer m.switchMu.Unlock()
		if err := m.pause(ctx); err != nil {
			m.closeErr = errors.Join(m.closeErr, err)
			return
		}
		if m.graph != nil {
			m.closeErr = errors.Join(m.closeErr, m.graph.close(ctx))
		}
	})
	return m.closeErr
}
