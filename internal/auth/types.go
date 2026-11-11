// Package auth 管理管理员、会话和验证码的存储事务，不处理 HTTP。
package auth

import (
	"database/sql"
	"errors"
	"time"

	"github.com/Seaony/Kakatta/internal/security"
)

type Kind string

const (
	WebSession Kind = "web_session"
	APIToken   Kind = "api_token"
)

var (
	ErrInvalidCredentials = errors.New("auth.invalid_credentials")
	ErrUnauthenticated    = errors.New("auth.unauthenticated")
	ErrSetupExpired       = errors.New("auth.setup_expired")
	ErrAlreadyInitialized = errors.New("auth.already_initialized")
	ErrChallengeExpired   = errors.New("auth.challenge_expired")
	ErrInvalidInput       = errors.New("auth.invalid_input")
	ErrNotFound           = errors.New("auth.not_found")
	ErrTOTPEnabled        = errors.New("auth.totp_enabled")
	ErrTOTPDisabled       = errors.New("auth.totp_disabled")
	ErrStorage            = errors.New("auth.storage")
)

type Dependencies struct {
	DB      *sql.DB
	Cipher  *security.Cipher
	Now     func() time.Time
	DataDir string
}

type Bootstrap struct {
	Token     string `json:"-"`
	ExpiresAt *int64 `json:"-"`
}

type SetupStatus struct {
	Initialized    bool   `json:"initialized"`
	TokenExpiresAt *int64 `json:"token_expires_at"`
}

type Identity struct {
	TokenID         int64  `json:"token_id"`
	Kind            Kind   `json:"auth_kind"`
	Username        string `json:"username"`
	TOTPEnabled     bool   `json:"totp_enabled"`
	CookieExpiresAt int64  `json:"-"`
	hash            [32]byte
}

type Session struct {
	Identity  Identity `json:"-"`
	Token     string   `json:"-"`
	ExpiresAt int64    `json:"-"`
}

type LoginResult struct {
	Session   *Session `json:"-"`
	Challenge string   `json:"-"`
	ExpiresAt int64    `json:"-"`
}

type Token struct {
	ID         int64  `json:"id"`
	Kind       Kind   `json:"kind"`
	Name       string `json:"name"`
	CreatedAt  *int64 `json:"created_at"`
	LastUsedAt *int64 `json:"last_used_at"`
	ExpiresAt  *int64 `json:"expires_at"`
	RevokedAt  *int64 `json:"revoked_at"`
	Current    bool   `json:"current"`
}

type IssuedToken struct {
	ID        int64  `json:"-"`
	Token     string `json:"-"`
	CreatedAt int64  `json:"-"`
}

type TOTPSetup struct {
	SetupToken string `json:"-"`
	Secret     string `json:"-"`
	URI        string `json:"-"`
	ExpiresAt  int64  `json:"-"`
}

type storageError struct{ cause error }

func (e *storageError) Error() string        { return ErrStorage.Error() }
func (e *storageError) Unwrap() error        { return e.cause }
func (e *storageError) Is(target error) bool { return target == ErrStorage }
func (e *storageError) GoString() string     { return e.Error() }

func (Bootstrap) String() string       { return "初始化凭据" }
func (Session) String() string         { return "登录会话" }
func (LoginResult) String() string     { return "登录结果" }
func (IssuedToken) String() string     { return "新令牌" }
func (TOTPSetup) String() string       { return "两步验证绑定凭据" }
func (b Bootstrap) GoString() string   { return b.String() }
func (s Session) GoString() string     { return s.String() }
func (r LoginResult) GoString() string { return r.String() }
func (t IssuedToken) GoString() string { return t.String() }
func (t TOTPSetup) GoString() string   { return t.String() }
