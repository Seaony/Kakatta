package auth

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/Seaony/Kakatta/internal/security"
)

type authRecord struct {
	identity Identity
	created  sql.NullInt64
	used     sql.NullInt64
	expires  sql.NullInt64
	revoked  sql.NullInt64
}

func readAuth(ctx context.Context, q queryer, where string, args ...any) (authRecord, error) {
	var r authRecord
	var hash []byte
	var enabled sql.NullInt64
	err := q.QueryRowContext(ctx, "SELECT t.id, t.kind, t.token_hash, t.created_at, t.last_used_at, t.expires_at, t.revoked_at, a.username, a.totp_enabled_at FROM auth_tokens t JOIN admin a ON a.id = 1 WHERE "+where, args...).Scan(&r.identity.TokenID, &r.identity.Kind, &hash, &r.created, &r.used, &r.expires, &r.revoked, &r.identity.Username, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return authRecord{}, ErrUnauthenticated
	}
	if err != nil {
		return authRecord{}, stored(err)
	}
	if len(hash) != 32 {
		return authRecord{}, ErrUnauthenticated
	}
	copy(r.identity.hash[:], hash)
	r.identity.TOTPEnabled = enabled.Valid
	return r, nil
}

func (r authRecord) valid(now int64) bool {
	if r.revoked.Valid || !r.created.Valid {
		return false
	}
	switch r.identity.Kind {
	case APIToken:
		return !r.expires.Valid
	case WebSession:
		return r.used.Valid && r.expires.Valid && now < r.expires.Int64 && now < r.created.Int64+sessionAbsolute.Milliseconds()
	default:
		return false
	}
}

func (s *Service) Authenticate(ctx context.Context, token string, kind Kind) (Identity, error) {
	hash := security.HashToken(token)
	if (kind != WebSession && kind != APIToken) || !security.TokenMatches(token, hash) {
		return Identity{}, ErrUnauthenticated
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Identity{}, stored(err)
	}
	defer tx.Rollback()
	r, err := readAuth(ctx, tx, "t.token_hash = ? AND t.kind = ?", hash[:], kind)
	if err != nil {
		return Identity{}, err
	}
	now := s.now().UnixMilli()
	if !r.valid(now) {
		return Identity{}, ErrUnauthenticated
	}
	used := now
	if r.used.Valid {
		used = max(now, r.used.Int64)
	}
	var expiry any
	if kind == WebSession {
		r.identity.CookieExpiresAt = min(used+sessionIdle.Milliseconds(), r.created.Int64+sessionAbsolute.Milliseconds())
		expiry = r.identity.CookieExpiresAt
	}
	if _, err := tx.ExecContext(ctx, "UPDATE auth_tokens SET last_used_at = ?, expires_at = ? WHERE id = ?", used, expiry, r.identity.TokenID); err != nil {
		return Identity{}, stored(err)
	}
	if err := tx.Commit(); err != nil {
		return Identity{}, stored(err)
	}
	return r.identity, nil
}

func (s *Service) Validate(ctx context.Context, identity Identity) error {
	_, err := s.validIdentity(ctx, s.db, identity, s.now().UnixMilli())
	return err
}

func (s *Service) validIdentity(ctx context.Context, q queryer, identity Identity, now int64) (authRecord, error) {
	r, err := readAuth(ctx, q, "t.id = ? AND t.token_hash = ? AND t.kind = ?", identity.TokenID, identity.hash[:], identity.Kind)
	if err != nil {
		return authRecord{}, err
	}
	if !r.valid(now) || r.identity.Username != identity.Username {
		return authRecord{}, ErrUnauthenticated
	}
	return r, nil
}

func insertSession(ctx context.Context, tx *sql.Tx, a adminRecord, userAgent string, now int64) (Session, error) {
	token, hash := security.NewToken()
	expires := now + sessionIdle.Milliseconds()
	result, err := tx.ExecContext(ctx, "INSERT INTO auth_tokens (kind, token_hash, name, created_at, last_used_at, expires_at) VALUES (?, ?, ?, ?, ?, ?)", WebSession, hash[:], agentSummary(userAgent), now, now, expires)
	if err != nil {
		return Session{}, stored(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Session{}, stored(err)
	}
	return Session{Token: token, ExpiresAt: expires, Identity: Identity{TokenID: id, Kind: WebSession, Username: a.username, TOTPEnabled: a.enabled(), CookieExpiresAt: expires, hash: hash}}, nil
}

func (s *Service) ListTokens(ctx context.Context, identity Identity) ([]Token, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, stored(err)
	}
	defer tx.Rollback()
	if _, err := s.validIdentity(ctx, tx, identity, s.now().UnixMilli()); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT id, kind, name, created_at, last_used_at, expires_at, revoked_at FROM auth_tokens ORDER BY created_at DESC, id DESC")
	if err != nil {
		return nil, stored(err)
	}
	tokens := make([]Token, 0)
	for rows.Next() {
		var token Token
		if err := rows.Scan(&token.ID, &token.Kind, &token.Name, &token.CreatedAt, &token.LastUsedAt, &token.ExpiresAt, &token.RevokedAt); err != nil {
			rows.Close()
			return nil, stored(err)
		}
		token.Current = token.ID == identity.TokenID
		tokens = append(tokens, token)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, stored(err)
	}
	if err := rows.Close(); err != nil {
		return nil, stored(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, stored(err)
	}
	return tokens, nil
}

func (s *Service) CreateToken(ctx context.Context, identity Identity, name string) (IssuedToken, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return IssuedToken{}, ErrInvalidInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return IssuedToken{}, stored(err)
	}
	defer tx.Rollback()
	now := s.now().UnixMilli()
	if _, err := s.validIdentity(ctx, tx, identity, now); err != nil {
		return IssuedToken{}, err
	}
	token, hash := security.NewToken()
	result, err := tx.ExecContext(ctx, "INSERT INTO auth_tokens (kind, token_hash, name, created_at) VALUES (?, ?, ?, ?)", APIToken, hash[:], name, now)
	if err != nil {
		return IssuedToken{}, stored(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return IssuedToken{}, stored(err)
	}
	if err := tx.Commit(); err != nil {
		return IssuedToken{}, stored(err)
	}
	return IssuedToken{ID: id, Token: token, CreatedAt: now}, nil
}

func (s *Service) RevokeToken(ctx context.Context, identity Identity, id int64) error {
	if id < 1 {
		return ErrInvalidInput
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
	result, err := tx.ExecContext(ctx, "UPDATE auth_tokens SET revoked_at = COALESCE(revoked_at, ?) WHERE id = ?", now, id)
	if err != nil {
		return stored(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return stored(err)
	}
	if changed == 0 {
		return ErrNotFound
	}
	return storedCommit(tx)
}

func (s *Service) Logout(ctx context.Context, identity Identity) error {
	return s.RevokeToken(ctx, identity, identity.TokenID)
}

func storedCommit(tx *sql.Tx) error {
	if err := tx.Commit(); err != nil {
		return stored(err)
	}
	return nil
}
