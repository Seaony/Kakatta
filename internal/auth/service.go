package auth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Seaony/Kakatta/internal/security"
)

const (
	setupLifetime     = 30 * time.Minute
	challengeLifetime = 5 * time.Minute
	sessionIdle       = 30 * 24 * time.Hour
	sessionAbsolute   = 90 * 24 * time.Hour
)

type Service struct {
	db             *sql.DB
	cipher         *security.Cipher
	now            func() time.Time
	mu             sync.Mutex
	initialHash    [32]byte
	initialExpires int64
	initialSet     bool
	challenges     map[[32]byte]challenge
	setups         map[[32]byte]pendingSetup
}

func (*Service) String() string   { return "认证服务" }
func (*Service) GoString() string { return "认证服务" }

type challenge struct {
	expires int64
	version [32]byte
	agent   string
}

type pendingSetup struct {
	expires int64
	version [32]byte
	actor   Identity
	secret  string
}

type adminRecord struct {
	username     string
	passwordHash string
	secret       []byte
	enabledAt    sql.NullInt64
	lastStep     sql.NullInt64
	generation   string
}

func (a adminRecord) enabled() bool { return a.enabledAt.Valid }

func (a adminRecord) version() [32]byte {
	h := sha256.New()
	h.Write([]byte(a.username))
	h.Write([]byte{0})
	h.Write([]byte(a.passwordHash))
	h.Write([]byte{0})
	h.Write(a.secret)
	h.Write([]byte{0})
	h.Write([]byte(a.generation))
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func New(ctx context.Context, deps Dependencies) (*Service, Bootstrap, error) {
	if deps.DB == nil {
		return nil, Bootstrap{}, ErrInvalidInput
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	s := &Service{
		db: deps.DB, cipher: deps.Cipher, now: deps.Now,
		challenges: make(map[[32]byte]challenge), setups: make(map[[32]byte]pendingSetup),
	}
	generation, _ := security.NewToken()
	if _, err := s.db.ExecContext(ctx, "INSERT INTO meta (key, value) VALUES ('auth.generation', ?) ON CONFLICT (key) DO NOTHING", generation); err != nil {
		return nil, Bootstrap{}, stored(err)
	}
	if _, err := readGeneration(ctx, s.db); err != nil {
		return nil, Bootstrap{}, stored(err)
	}
	_, err := s.admin(ctx)
	if err == nil {
		return s, Bootstrap{}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, Bootstrap{}, stored(err)
	}
	token, hash := security.NewToken()
	s.initialHash, s.initialSet = hash, true
	s.initialExpires = s.now().Add(setupLifetime).UnixMilli()
	expires := s.initialExpires
	return s, Bootstrap{Token: token, ExpiresAt: &expires}, nil
}

func (s *Service) Setup(ctx context.Context) (SetupStatus, error) {
	_, err := s.admin(ctx)
	if err == nil {
		return SetupStatus{Initialized: true}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return SetupStatus{}, stored(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.initialSet {
		return SetupStatus{}, nil
	}
	expires := s.initialExpires
	return SetupStatus{TokenExpiresAt: &expires}, nil
}

func (s *Service) Initialize(ctx context.Context, token, username, password, userAgent string) (Session, error) {
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return Session{}, ErrInvalidInput
	}
	s.mu.Lock()
	err := s.checkInitial(token)
	s.mu.Unlock()
	if err != nil {
		return Session{}, err
	}
	phc, err := security.HashPassword(ctx, password)
	if err != nil {
		return Session{}, stored(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkInitial(token); err != nil {
		return Session{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, stored(err)
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM admin").Scan(&count); err != nil {
		return Session{}, stored(err)
	}
	if count != 0 {
		return Session{}, ErrAlreadyInitialized
	}
	now := s.now().UnixMilli()
	if _, err := tx.ExecContext(ctx, "INSERT INTO admin (id, username, password_hash, created_at, password_changed_at) VALUES (1, ?, ?, ?, ?)", username, phc, now, now); err != nil {
		return Session{}, stored(err)
	}
	session, err := insertSession(ctx, tx, adminRecord{username: username}, userAgent, now)
	if err != nil {
		return Session{}, err
	}
	if err := tx.Commit(); err != nil {
		return Session{}, stored(err)
	}
	s.initialHash, s.initialSet = [32]byte{}, false
	return session, nil
}

func (s *Service) checkInitial(token string) error {
	if !s.initialSet {
		return ErrAlreadyInitialized
	}
	if s.now().UnixMilli() >= s.initialExpires {
		return ErrSetupExpired
	}
	if !security.TokenMatches(token, s.initialHash) {
		return ErrInvalidCredentials
	}
	return nil
}

func (s *Service) PasswordLogin(ctx context.Context, username, password, userAgent string) (LoginResult, error) {
	a, err := s.admin(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return LoginResult{}, ErrInvalidCredentials
	}
	if err != nil {
		return LoginResult{}, stored(err)
	}
	ok, err := security.VerifyPassword(ctx, password, a.passwordHash)
	if err != nil {
		return LoginResult{}, stored(err)
	}
	if !ok || username != a.username {
		return LoginResult{}, ErrInvalidCredentials
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return LoginResult{}, stored(err)
	}
	defer tx.Rollback()
	current, err := readAdmin(ctx, tx)
	if err != nil {
		return LoginResult{}, stored(err)
	}
	if current.version() != a.version() {
		return LoginResult{}, ErrInvalidCredentials
	}
	if current.enabled() && s.cipher == nil {
		return LoginResult{}, ErrStorage
	}
	now := s.now().UnixMilli()
	if !current.enabled() {
		session, err := insertSession(ctx, tx, current, userAgent, now)
		if err != nil {
			return LoginResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return LoginResult{}, stored(err)
		}
		return LoginResult{Session: &session}, nil
	}
	if err := tx.Commit(); err != nil {
		return LoginResult{}, stored(err)
	}
	token, hash := security.NewToken()
	expires := now + challengeLifetime.Milliseconds()
	s.mu.Lock()
	s.prune(now)
	s.challenges[hash] = challenge{expires: expires, version: current.version(), agent: agentSummary(userAgent)}
	s.mu.Unlock()
	return LoginResult{Challenge: token, ExpiresAt: expires}, nil
}

func (s *Service) admin(ctx context.Context) (adminRecord, error) { return readAdmin(ctx, s.db) }

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readAdmin(ctx context.Context, q queryer) (adminRecord, error) {
	var a adminRecord
	err := q.QueryRowContext(ctx, "SELECT username, password_hash, totp_secret_enc, totp_enabled_at, totp_last_step FROM admin WHERE id = 1").Scan(&a.username, &a.passwordHash, &a.secret, &a.enabledAt, &a.lastStep)
	if err != nil {
		return a, err
	}
	if a.enabledAt.Valid != (len(a.secret) != 0) || (!a.enabledAt.Valid && a.lastStep.Valid) {
		return adminRecord{}, ErrStorage
	}
	a.generation, err = readGeneration(ctx, q)
	if err != nil {
		return adminRecord{}, err
	}
	return a, nil
}

func readGeneration(ctx context.Context, q queryer) (string, error) {
	var generation string
	if err := q.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = 'auth.generation'").Scan(&generation); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrStorage
		}
		return "", err
	}
	if !security.TokenMatches(generation, security.HashToken(generation)) {
		return "", ErrStorage
	}
	return generation, nil
}

func changeGeneration(ctx context.Context, tx *sql.Tx) error {
	generation, _ := security.NewToken()
	result, err := tx.ExecContext(ctx, "UPDATE meta SET value = ? WHERE key = 'auth.generation'", generation)
	if err != nil {
		return stored(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return stored(err)
	}
	if changed != 1 {
		return ErrStorage
	}
	return nil
}

func stored(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return &storageError{cause: err}
}

func agentSummary(agent string) string {
	out := make([]rune, 0, min(len(agent), 200))
	for _, r := range strings.TrimSpace(agent) {
		if !unicode.IsControl(r) {
			out = append(out, r)
		}
		if len(out) == 200 {
			break
		}
	}
	return string(out)
}

func (s *Service) prune(now int64) {
	for key, c := range s.challenges {
		if now >= c.expires {
			delete(s.challenges, key)
		}
	}
	for key, setup := range s.setups {
		if now >= setup.expires {
			delete(s.setups, key)
		}
	}
}

func (s *Service) invalidatePending() {
	s.mu.Lock()
	clear(s.challenges)
	clear(s.setups)
	s.mu.Unlock()
}
