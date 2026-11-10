package auth

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/Seaony/Kakatta/internal/security"
)

func (s *Service) VerifyChallenge(ctx context.Context, token, code string) (Session, error) {
	if s.cipher == nil {
		return Session{}, ErrStorage
	}
	hash := security.HashToken(token)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UnixMilli()
	c, found := s.challenges[hash]
	if !found || !security.TokenMatches(token, hash) || now >= c.expires {
		delete(s.challenges, hash)
		return Session{}, ErrChallengeExpired
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, stored(err)
	}
	defer tx.Rollback()
	a, err := readAdmin(ctx, tx)
	if err != nil {
		return Session{}, stored(err)
	}
	if !a.enabled() || a.version() != c.version {
		delete(s.challenges, hash)
		return Session{}, ErrChallengeExpired
	}
	now = s.now().UnixMilli()
	if now >= c.expires {
		delete(s.challenges, hash)
		return Session{}, ErrChallengeExpired
	}
	step, err := s.verifyCode(a, code)
	if err != nil {
		return Session{}, err
	}
	if err := consumeStep(ctx, tx, a, step); err != nil {
		return Session{}, err
	}
	session, err := insertSession(ctx, tx, a, c.agent, now)
	if err != nil {
		return Session{}, err
	}
	if err := storedCommit(tx); err != nil {
		return Session{}, err
	}
	delete(s.challenges, hash)
	return session, nil
}

func (s *Service) BeginTOTP(ctx context.Context, identity Identity, password string) (TOTPSetup, error) {
	if s.cipher == nil {
		return TOTPSetup{}, ErrStorage
	}
	a, err := s.passwordIdentity(ctx, identity, password)
	if err != nil {
		return TOTPSetup{}, err
	}
	if a.enabled() {
		return TOTPSetup{}, ErrTOTPEnabled
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TOTPSetup{}, stored(err)
	}
	defer tx.Rollback()
	now := s.now().UnixMilli()
	if _, err := s.validIdentity(ctx, tx, identity, now); err != nil {
		return TOTPSetup{}, err
	}
	current, err := readAdmin(ctx, tx)
	if err != nil {
		return TOTPSetup{}, stored(err)
	}
	if current.version() != a.version() {
		return TOTPSetup{}, ErrInvalidCredentials
	}
	if err := storedCommit(tx); err != nil {
		return TOTPSetup{}, err
	}
	secret := security.NewTOTPSecret()
	label := strings.ReplaceAll(a.username, ":", " ")
	uri, err := security.TOTPURI(secret, label)
	if err != nil {
		return TOTPSetup{}, stored(err)
	}
	token, hash := security.NewToken()
	expires := now + challengeLifetime.Milliseconds()
	s.mu.Lock()
	s.prune(now)
	s.setups[hash] = pendingSetup{expires: expires, version: a.version(), actor: identity, secret: secret}
	s.mu.Unlock()
	return TOTPSetup{SetupToken: token, Secret: secret, URI: uri, ExpiresAt: expires}, nil
}

func (s *Service) EnableTOTP(ctx context.Context, identity Identity, token, code string) error {
	if s.cipher == nil {
		return ErrStorage
	}
	hash := security.HashToken(token)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UnixMilli()
	setup, found := s.setups[hash]
	if !found || !security.TokenMatches(token, hash) || now >= setup.expires || setup.actor.TokenID != identity.TokenID || setup.actor.hash != identity.hash || setup.actor.Kind != identity.Kind {
		return ErrChallengeExpired
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return stored(err)
	}
	defer tx.Rollback()
	if _, err := s.validIdentity(ctx, tx, identity, now); err != nil {
		return err
	}
	a, err := readAdmin(ctx, tx)
	if err != nil {
		return stored(err)
	}
	if a.enabled() {
		return ErrTOTPEnabled
	}
	if a.version() != setup.version {
		delete(s.setups, hash)
		return ErrChallengeExpired
	}
	now = s.now().UnixMilli()
	if now >= setup.expires {
		delete(s.setups, hash)
		return ErrChallengeExpired
	}
	step, ok, err := security.VerifyTOTP(setup.secret, code, s.now(), -1)
	if err != nil || !ok {
		return ErrInvalidCredentials
	}
	sealed, err := s.cipher.Seal([]byte(setup.secret), "admin", "totp_secret_enc", "1")
	if err != nil {
		return stored(err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE admin SET totp_secret_enc = ?, totp_enabled_at = ?, totp_last_step = ? WHERE id = 1", sealed, now, step); err != nil {
		return stored(err)
	}
	if err := changeGeneration(ctx, tx); err != nil {
		return err
	}
	if err := storedCommit(tx); err != nil {
		return err
	}
	clear(s.setups)
	clear(s.challenges)
	return nil
}

func (s *Service) DisableTOTP(ctx context.Context, identity Identity, password, code string) error {
	if s.cipher == nil {
		return ErrStorage
	}
	a, err := s.passwordIdentity(ctx, identity, password)
	if err != nil {
		return err
	}
	if !a.enabled() {
		return ErrTOTPDisabled
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return stored(err)
	}
	defer tx.Rollback()
	if _, err := s.validIdentity(ctx, tx, identity, s.now().UnixMilli()); err != nil {
		return err
	}
	current, err := readAdmin(ctx, tx)
	if err != nil {
		return stored(err)
	}
	if current.version() != a.version() {
		return ErrInvalidCredentials
	}
	step, err := s.verifyCode(current, code)
	if err != nil {
		return err
	}
	if err := consumeStep(ctx, tx, current, step); err != nil {
		return err
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

func (s *Service) verifyCode(a adminRecord, code string) (int64, error) {
	if s.cipher == nil {
		return 0, ErrStorage
	}
	secret, err := s.cipher.Open(a.secret, "admin", "totp_secret_enc", "1")
	if err != nil {
		return 0, stored(err)
	}
	defer clear(secret)
	last := int64(-1)
	if a.lastStep.Valid {
		last = a.lastStep.Int64
	}
	step, ok, err := security.VerifyTOTP(string(secret), code, s.now(), last)
	if err != nil {
		if errors.Is(err, security.ErrTOTPInput) {
			return 0, ErrInvalidCredentials
		}
		return 0, stored(err)
	}
	if !ok {
		return 0, ErrInvalidCredentials
	}
	return step, nil
}

func consumeStep(ctx context.Context, tx *sql.Tx, a adminRecord, step int64) error {
	var previous any
	if a.lastStep.Valid {
		previous = a.lastStep.Int64
	}
	result, err := tx.ExecContext(ctx, "UPDATE admin SET totp_last_step = ? WHERE id = 1 AND totp_last_step IS ? AND password_hash = ? AND totp_secret_enc = ?", step, previous, a.passwordHash, a.secret)
	if err != nil {
		return stored(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return stored(err)
	}
	if changed != 1 {
		return ErrInvalidCredentials
	}
	return nil
}
