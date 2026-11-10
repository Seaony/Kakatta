package auth

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Seaony/Kakatta/internal/security"
)

func (s *Service) ChangePassword(ctx context.Context, identity Identity, oldPassword, newPassword string) error {
	if newPassword == "" {
		return ErrInvalidInput
	}
	a, err := s.passwordIdentity(ctx, identity, oldPassword)
	if err != nil {
		return err
	}
	phc, err := security.HashPassword(ctx, newPassword)
	if err != nil {
		return stored(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return stored(err)
	}
	defer tx.Rollback()
	now := s.now().UnixMilli()
	if _, err := s.validIdentity(ctx, tx, identity, now); err != nil {
		return err
	}
	current, err := readAdmin(ctx, tx)
	if err != nil {
		return stored(err)
	}
	if current.version() != a.version() {
		return ErrInvalidCredentials
	}
	if _, err := tx.ExecContext(ctx, "UPDATE admin SET password_hash = ?, password_changed_at = ? WHERE id = 1", phc, now); err != nil {
		return stored(err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE auth_tokens SET revoked_at = COALESCE(revoked_at, ?) WHERE id != ?", now, identity.TokenID); err != nil {
		return stored(err)
	}
	if err := changeGeneration(ctx, tx); err != nil {
		return err
	}
	if err := storedCommit(tx); err != nil {
		return err
	}
	s.invalidatePending()
	return nil
}

func (s *Service) passwordIdentity(ctx context.Context, identity Identity, password string) (adminRecord, error) {
	if err := s.Validate(ctx, identity); err != nil {
		return adminRecord{}, err
	}
	a, err := s.admin(ctx)
	if err != nil {
		return adminRecord{}, stored(err)
	}
	ok, err := security.VerifyPassword(ctx, password, a.passwordHash)
	if err != nil {
		return adminRecord{}, stored(err)
	}
	if !ok {
		return adminRecord{}, ErrInvalidCredentials
	}
	return a, nil
}

func (s *Service) ResetPassword(ctx context.Context, password string, disableTOTP bool) error {
	if password == "" {
		return ErrInvalidInput
	}
	phc, err := security.HashPassword(ctx, password)
	if err != nil {
		return stored(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return stored(err)
	}
	defer tx.Rollback()
	if _, err := readAdmin(ctx, tx); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrUnauthenticated
		}
		return stored(err)
	}
	now := s.now().UnixMilli()
	if _, err := tx.ExecContext(ctx, "UPDATE admin SET password_hash = ?, password_changed_at = ? WHERE id = 1", phc, now); err != nil {
		return stored(err)
	}
	if disableTOTP {
		if err := clearTOTP(ctx, tx); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE auth_tokens SET revoked_at = COALESCE(revoked_at, ?)", now); err != nil {
		return stored(err)
	}
	if err := changeGeneration(ctx, tx); err != nil {
		return err
	}
	if err := storedCommit(tx); err != nil {
		return err
	}
	s.invalidatePending()
	return nil
}

func (s *Service) DisableTOTPForCLI(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return stored(err)
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM admin WHERE id = 1").Scan(&count); err != nil {
		return stored(err)
	}
	if count != 1 {
		return ErrUnauthenticated
	}
	if err := clearTOTP(ctx, tx); err != nil {
		return err
	}
	if err := changeGeneration(ctx, tx); err != nil {
		return err
	}
	if err := storedCommit(tx); err != nil {
		return err
	}
	s.invalidatePending()
	return nil
}

func clearTOTP(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, "UPDATE admin SET totp_secret_enc = NULL, totp_enabled_at = NULL, totp_last_step = NULL WHERE id = 1")
	if err != nil {
		return stored(err)
	}
	return nil
}
