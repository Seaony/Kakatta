package backup

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/Seaony/Kakatta/internal/store"
)

func (s *Service) Export(ctx context.Context, password string, dst io.Writer) (err error) {
	if password == "" {
		return ErrPasswordRequired
	}
	if dst == nil {
		return ErrInvalidDependencies
	}
	ctx, done, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer done()
	dir, err := os.MkdirTemp(s.deps.DataDir, ".backup-export-")
	if err != nil {
		return fileFailure(err)
	}
	defer func() {
		if removeErr := os.RemoveAll(dir); removeErr != nil {
			failure := fileFailure(removeErr)
			err = errors.Join(err, failure)
			s.mu.Lock()
			if s.cleanupErr == nil {
				s.cleanupErr = failure
			}
			s.mu.Unlock()
		}
	}()
	if err := os.Chmod(dir, 0700); err != nil {
		return fileFailure(err)
	}
	raw, clean := filepath.Join(dir, "raw.db"), filepath.Join(dir, "clean.db")
	var key [MasterKeyBytes]byte
	defer clear(key[:])
	var generation string
	err = s.deps.Runtime.WithSnapshot(ctx, func(source SnapshotSource) error {
		defer clear(source.MasterKey[:])
		if source.Store == nil || source.Store.DB() == nil || source.Generation == "" || generation != "" ||
			(source.KeySource != KeyDefaultFile && source.KeySource != KeyEnvironment && source.KeySource != KeyExternalFile) {
			return ErrInvalidDependencies
		}
		if err := source.Store.Backup(ctx, raw); err != nil {
			return fileFailure(err)
		}
		key = source.MasterKey
		generation = source.Generation
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrInvalidDependencies) {
			return ErrInvalidDependencies
		}
		return fileFailure(err)
	}
	if generation == "" {
		return ErrInvalidDependencies
	}
	if err := s.cleanSnapshot(ctx, raw, clean); err != nil {
		return err
	}
	manifest, err := NewManifest(s.deps.AppVersion, s.deps.SchemaVersion, s.deps.Now())
	if err != nil {
		return err
	}
	return WriteArchive(ctx, dst, clean, key, manifest, password)
}

func (s *Service) cleanSnapshot(ctx context.Context, raw, clean string) (err error) {
	private, err := store.Open(ctx, raw)
	if err != nil {
		return fileFailure(err)
	}
	defer func() {
		if closeErr := private.Close(); closeErr != nil {
			err = errors.Join(err, fileFailure(closeErr))
		}
	}()
	var version int
	if err := private.DB().QueryRowContext(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&version); err != nil {
		return fileFailure(err)
	}
	if version != s.deps.SchemaVersion {
		return ErrUnsupportedVersion
	}
	tx, err := private.DB().BeginTx(ctx, nil)
	if err != nil {
		return fileFailure(err)
	}
	defer tx.Rollback()
	for _, statement := range []string{"DELETE FROM auth_tokens", "DELETE FROM quota_events"} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fileFailure(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fileFailure(err)
	}
	if err := private.Backup(ctx, clean); err != nil {
		return fileFailure(err)
	}
	return nil
}
