package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"database/sql"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/security"
	_ "modernc.org/sqlite"
)

var passwordFixture struct {
	sync.Once
	phc string
	err error
}

type testClock struct {
	sync.Mutex
	at time.Time
}

func (c *testClock) now() time.Time {
	c.Lock()
	defer c.Unlock()
	return c.at
}

func (c *testClock) add(d time.Duration) {
	c.Lock()
	c.at = c.at.Add(d)
	c.Unlock()
}

type fixture struct {
	db     *sql.DB
	s      *Service
	clock  *testClock
	cipher *security.Cipher
	boot   Bootstrap
	dsn    string
}

func newFixture(t *testing.T, initialized bool) *fixture {
	t.Helper()
	path := url.URL{Scheme: "file", Path: filepath.Join(t.TempDir(), "auth ?.db")}
	dsn := path.String() + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`
CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE admin (
 id INTEGER PRIMARY KEY CHECK (id = 1), username TEXT NOT NULL, password_hash TEXT NOT NULL,
 totp_secret_enc BLOB, totp_enabled_at INTEGER, totp_last_step INTEGER,
 created_at INTEGER NOT NULL, password_changed_at INTEGER NOT NULL
);
CREATE TABLE auth_tokens (
 id INTEGER PRIMARY KEY, kind TEXT NOT NULL CHECK (kind IN ('web_session', 'api_token')),
 token_hash BLOB NOT NULL UNIQUE, name TEXT NOT NULL, created_at INTEGER, last_used_at INTEGER,
 expires_at INTEGER, revoked_at INTEGER
);`)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := security.NewCipher(security.NewMasterKey())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{db: db, dsn: dsn, cipher: cipher, clock: &testClock{at: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)}}
	if initialized {
		passwordFixture.Do(func() {
			passwordFixture.phc, passwordFixture.err = security.HashPassword(context.Background(), "old password")
		})
		if passwordFixture.err != nil {
			t.Fatal(passwordFixture.err)
		}
		f.exec(t, "INSERT INTO admin (id, username, password_hash, created_at, password_changed_at) VALUES (1, 'owner', ?, ?, ?)", passwordFixture.phc, f.clock.now().UnixMilli(), f.clock.now().UnixMilli())
	}
	f.s, f.boot, err = New(context.Background(), Dependencies{DB: db, Cipher: cipher, Now: f.clock.now})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := f.db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) session(t *testing.T) Session {
	t.Helper()
	tx, err := f.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	a, err := readAdmin(context.Background(), tx)
	if err != nil {
		t.Fatal(err)
	}
	session, err := insertSession(context.Background(), tx, a, "Test browser\nprivate", f.clock.now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return session
}

func (f *fixture) count(t *testing.T, table string) int {
	t.Helper()
	var n int
	if err := f.db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func requireError(t *testing.T, got, want error) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Fatalf("错误 = %v，期望 %v", got, want)
	}
}

func codeAt(t *testing.T, secret string, at time.Time) string {
	t.Helper()
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil {
		t.Fatal(err)
	}
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(at.Unix()/30))
	mac := hmac.New(sha1.New, key)
	mac.Write(counter[:])
	digest := mac.Sum(nil)
	offset := digest[len(digest)-1] & 15
	value := binary.BigEndian.Uint32(digest[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", value%1000000)
}

func (f *fixture) enable(t *testing.T, identity Identity) TOTPSetup {
	t.Helper()
	setup, err := f.s.BeginTOTP(context.Background(), identity, "old password")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.EnableTOTP(context.Background(), identity, setup.SetupToken, codeAt(t, setup.Secret, f.clock.now())); err != nil {
		t.Fatal(err)
	}
	return setup
}

func TestInitializeTokenLifetimeAndAtomicSession(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, false)
	if len(f.boot.Token) != 43 || f.boot.ExpiresAt == nil {
		t.Fatal("缺少初始化凭据")
	}
	status, err := f.s.Setup(ctx)
	if err != nil || status.Initialized || *status.TokenExpiresAt != f.clock.now().Add(30*time.Minute).UnixMilli() {
		t.Fatalf("初始化状态错误: %#v %v", status, err)
	}
	_, err = f.s.Initialize(ctx, "wrong", "owner", "password", "browser")
	requireError(t, err, ErrInvalidCredentials)
	_, err = f.s.Initialize(ctx, f.boot.Token, "  ", "password", "browser")
	requireError(t, err, ErrInvalidInput)
	f.exec(t, "CREATE TRIGGER fail_session BEFORE INSERT ON auth_tokens BEGIN SELECT RAISE(ABORT, 'test failure'); END")
	_, err = f.s.Initialize(ctx, f.boot.Token, " owner ", "password", "browser")
	requireError(t, err, ErrStorage)
	if f.count(t, "admin") != 0 || f.count(t, "auth_tokens") != 0 {
		t.Fatal("会话写失败留下了半成品管理员")
	}
	f.exec(t, "DROP TRIGGER fail_session")
	session, err := f.s.Initialize(ctx, f.boot.Token, " owner ", "password", "browser")
	if err != nil {
		t.Fatal(err)
	}
	if session.Identity.Username != "owner" || session.Token == f.boot.Token || session.ExpiresAt != f.clock.now().Add(sessionIdle).UnixMilli() {
		t.Fatal("首次会话错误")
	}
	_, err = f.s.Authenticate(ctx, session.Token, WebSession)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.Initialize(ctx, f.boot.Token, "second", "password", "browser")
	requireError(t, err, ErrAlreadyInitialized)
	status, err = f.s.Setup(ctx)
	if err != nil || !status.Initialized || status.TokenExpiresAt != nil {
		t.Fatal("初始化成功后仍暴露令牌状态")
	}
	_, boot, err := New(ctx, Dependencies{DB: f.db, Cipher: f.cipher, Now: f.clock.now})
	if err != nil || boot.Token != "" || boot.ExpiresAt != nil {
		t.Fatal("已有管理员时重新生成初始化凭据")
	}
}

func TestInitializationExpiryAndRestart(t *testing.T) {
	f := newFixture(t, false)
	ctx := context.Background()
	f.clock.add(setupLifetime)
	_, err := f.s.Initialize(ctx, f.boot.Token, "owner", "password", "browser")
	requireError(t, err, ErrSetupExpired)
	newService, next, err := New(ctx, Dependencies{DB: f.db, Cipher: f.cipher, Now: f.clock.now})
	if err != nil || next.Token == "" || next.Token == f.boot.Token {
		t.Fatal("重启没有产生独立初始化凭据")
	}
	_, err = newService.Initialize(ctx, f.boot.Token, "owner", "password", "browser")
	requireError(t, err, ErrInvalidCredentials)
}

func TestPasswordLoginAndTokenKinds(t *testing.T) {
	f := newFixture(t, true)
	ctx := context.Background()
	for _, input := range [][2]string{{"owner", "wrong"}, {"other", "old password"}, {"", ""}} {
		_, err := f.s.PasswordLogin(ctx, input[0], input[1], "browser")
		requireError(t, err, ErrInvalidCredentials)
	}
	first, err := f.s.PasswordLogin(ctx, "owner", "old password", "Browser\nwith controls")
	if err != nil || first.Session == nil || first.Challenge != "" {
		t.Fatalf("密码登录失败: %v", err)
	}
	second, err := f.s.PasswordLogin(ctx, "owner", "old password", "browser")
	if err != nil || second.Session.Token == first.Session.Token {
		t.Fatal("登录没有重新生成会话ID")
	}
	_, err = f.s.Authenticate(ctx, first.Session.Token, APIToken)
	requireError(t, err, ErrUnauthenticated)
	for _, bad := range []string{"", "not-a-token", first.Session.Token + "x"} {
		_, err := f.s.Authenticate(ctx, bad, WebSession)
		requireError(t, err, ErrUnauthenticated)
	}
	var name string
	if err := f.db.QueryRow("SELECT name FROM auth_tokens WHERE id=?", first.Session.Identity.TokenID).Scan(&name); err != nil || strings.ContainsRune(name, '\n') {
		t.Fatal("浏览器摘要未去控制字符")
	}
}

func TestSessionIdleAndAbsoluteExpiration(t *testing.T) {
	f := newFixture(t, true)
	ctx := context.Background()
	session := f.session(t)
	f.clock.add(20 * 24 * time.Hour)
	if err := f.s.Validate(ctx, session.Identity); err != nil {
		t.Fatal(err)
	}
	var used, expires int64
	if err := f.db.QueryRow("SELECT last_used_at,expires_at FROM auth_tokens WHERE id=?", session.Identity.TokenID).Scan(&used, &expires); err != nil || expires != session.ExpiresAt || used != expires-sessionIdle.Milliseconds() {
		t.Fatal("Validate意外续期")
	}
	f.clock.add(9 * 24 * time.Hour)
	for i := 0; i < 3; i++ {
		if _, err := f.s.Authenticate(ctx, session.Token, WebSession); err != nil {
			t.Fatal(err)
		}
		if i != 2 {
			f.clock.add(29 * 24 * time.Hour)
		}
	}
	if err := f.db.QueryRow("SELECT expires_at FROM auth_tokens WHERE id=?", session.Identity.TokenID).Scan(&expires); err != nil || expires != used+sessionAbsolute.Milliseconds() {
		t.Fatal("会话超出了90天上限")
	}
	f.clock.add(3 * 24 * time.Hour)
	_, err := f.s.Authenticate(ctx, session.Token, WebSession)
	requireError(t, err, ErrUnauthenticated)
	other := f.session(t)
	f.clock.add(sessionIdle)
	_, err = f.s.Authenticate(ctx, other.Token, WebSession)
	requireError(t, err, ErrUnauthenticated)
}

func TestTokensListRevocationAndNoExpiry(t *testing.T) {
	f := newFixture(t, true)
	ctx := context.Background()
	session := f.session(t)
	issued, err := f.s.CreateToken(ctx, session.Identity, "automation")
	if err != nil || issued.ID < 1 || issued.Token == session.Token {
		t.Fatal("API令牌创建失败")
	}
	identity, err := f.s.Authenticate(ctx, issued.Token, APIToken)
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := f.s.ListTokens(ctx, identity)
	if err != nil || len(tokens) != 2 || tokens[0].ID != issued.ID || !tokens[0].Current || tokens[0].ExpiresAt != nil {
		t.Fatalf("令牌列表错误: %#v %v", tokens, err)
	}
	encoded, err := json.Marshal(tokens)
	if err != nil || strings.Contains(string(encoded), issued.Token) || strings.Contains(string(encoded), session.Token) {
		t.Fatal("列表泄露了令牌明文")
	}
	var storedHash []byte
	if err := f.db.QueryRow("SELECT token_hash FROM auth_tokens WHERE id=?", issued.ID).Scan(&storedHash); err != nil || string(storedHash) == issued.Token || len(storedHash) != 32 {
		t.Fatal("数据库没有只存哈希")
	}
	_, err = f.s.Authenticate(ctx, issued.Token, WebSession)
	requireError(t, err, ErrUnauthenticated)
	f.clock.add(400 * 24 * time.Hour)
	identity, err = f.s.Authenticate(ctx, issued.Token, APIToken)
	if err != nil {
		t.Fatal("API令牌不应按天过期", err)
	}
	if err := f.s.RevokeToken(ctx, identity, 99999); !errors.Is(err, ErrNotFound) {
		t.Fatal("不存在的令牌没有报错")
	}
	if err := f.s.Logout(ctx, identity); err != nil {
		t.Fatal(err)
	}
	requireError(t, f.s.Validate(ctx, identity), ErrUnauthenticated)
	_, err = f.s.Authenticate(ctx, issued.Token, APIToken)
	requireError(t, err, ErrUnauthenticated)
	_, err = f.s.CreateToken(ctx, identity, "after revocation")
	requireError(t, err, ErrUnauthenticated)
}

func TestAPITokenLastUsedStartsUnknownAndTracksFirstAuthentication(t *testing.T) {
	f := newFixture(t, true)
	ctx := context.Background()
	web := f.session(t)
	issued, err := f.s.CreateToken(ctx, web.Identity, "not yet used")
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := f.s.ListTokens(ctx, web.Identity)
	if err != nil {
		t.Fatal(err)
	}
	if tokens[0].ID != issued.ID || tokens[0].LastUsedAt != nil {
		t.Fatal("从未用过的API令牌应显示最近使用时间未知")
	}
	var used sql.NullInt64
	if err := f.db.QueryRow("SELECT last_used_at FROM auth_tokens WHERE id=?", issued.ID).Scan(&used); err != nil || used.Valid {
		t.Fatal("新建API令牌仍写入了使用时间")
	}
	encoded, err := json.Marshal(tokens[0])
	if err != nil || !strings.Contains(string(encoded), `"last_used_at":null`) {
		t.Fatal("未知使用时间未输出为null")
	}
	_, err = f.s.Authenticate(ctx, issued.Token, WebSession)
	requireError(t, err, ErrUnauthenticated)
	if err := f.db.QueryRow("SELECT last_used_at FROM auth_tokens WHERE id=?", issued.ID).Scan(&used); err != nil || used.Valid {
		t.Fatal("失败认证写入了使用时间")
	}
	f.clock.add(2 * time.Hour)
	if _, err := f.s.Authenticate(ctx, issued.Token, APIToken); err != nil {
		t.Fatal("新建的API令牌无法首次认证", err)
	}
	tokens, err = f.s.ListTokens(ctx, web.Identity)
	if err != nil || tokens[0].LastUsedAt == nil || *tokens[0].LastUsedAt != f.clock.now().UnixMilli() {
		t.Fatal("首次成功认证未记录实际UTC毫秒时间")
	}
}

func TestNullLastUsedAllowedOnlyForAPIToken(t *testing.T) {
	f := newFixture(t, true)
	ctx := context.Background()
	api, hash := security.NewToken()
	f.exec(t, "INSERT INTO auth_tokens (kind, token_hash, name, created_at) VALUES (?, ?, ?, ?)", APIToken, hash[:], "unused API", f.clock.now().UnixMilli())
	if _, err := f.s.Authenticate(ctx, api, APIToken); err != nil {
		t.Fatal("最近使用时间为空的API令牌被拒绝", err)
	}
	web := f.session(t)
	f.exec(t, "UPDATE auth_tokens SET last_used_at=NULL WHERE id=?", web.Identity.TokenID)
	_, err := f.s.Authenticate(ctx, web.Token, WebSession)
	requireError(t, err, ErrUnauthenticated)
}

func TestPasswordChangeAndCLIMaintenance(t *testing.T) {
	f := newFixture(t, true)
	ctx := context.Background()
	current, other := f.session(t), f.session(t)
	api, err := f.s.CreateToken(ctx, current.Identity, "automation")
	if err != nil {
		t.Fatal(err)
	}
	requireError(t, f.s.ChangePassword(ctx, current.Identity, "wrong", "new password"), ErrInvalidCredentials)
	if err := f.s.ChangePassword(ctx, current.Identity, "old password", "new password"); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Validate(ctx, current.Identity); err != nil {
		t.Fatal("修改密码吊销了当前会话", err)
	}
	requireError(t, f.s.Validate(ctx, other.Identity), ErrUnauthenticated)
	_, err = f.s.Authenticate(ctx, api.Token, APIToken)
	requireError(t, err, ErrUnauthenticated)
	_, err = f.s.PasswordLogin(ctx, "owner", "old password", "browser")
	requireError(t, err, ErrInvalidCredentials)
	login, err := f.s.PasswordLogin(ctx, "owner", "new password", "browser")
	if err != nil || login.Session == nil {
		t.Fatal(err)
	}
	maintenance, _, err := New(ctx, Dependencies{DB: f.db, Now: f.clock.now})
	if err != nil {
		t.Fatal(err)
	}
	if err := maintenance.ResetPassword(ctx, "recovered password", true); err != nil {
		t.Fatal(err)
	}
	requireError(t, f.s.Validate(ctx, current.Identity), ErrUnauthenticated)
	requireError(t, f.s.Validate(ctx, login.Session.Identity), ErrUnauthenticated)
	login, err = f.s.PasswordLogin(ctx, "owner", "recovered password", "browser")
	if err != nil || login.Session == nil {
		t.Fatal(err)
	}
}

func TestTOTPEnableLoginReplayAndDisable(t *testing.T) {
	f := newFixture(t, true)
	ctx := context.Background()
	current := f.session(t)
	setup := f.enable(t, current.Identity)
	var encrypted []byte
	var lastStep int64
	if err := f.db.QueryRow("SELECT totp_secret_enc,totp_last_step FROM admin WHERE id=1").Scan(&encrypted, &lastStep); err != nil || strings.Contains(string(encrypted), setup.Secret) || lastStep != f.clock.now().Unix()/30 {
		t.Fatal("绑定密钥未加密或验证码未消费")
	}
	login, err := f.s.PasswordLogin(ctx, "owner", "old password", "new browser")
	if err != nil || login.Session != nil || len(login.Challenge) != 43 {
		t.Fatal("TOTP没有先要求第二步", err)
	}
	_, err = f.s.VerifyChallenge(ctx, login.Challenge, codeAt(t, setup.Secret, f.clock.now()))
	requireError(t, err, ErrInvalidCredentials)
	f.clock.add(30 * time.Second)
	session, err := f.s.VerifyChallenge(ctx, login.Challenge, codeAt(t, setup.Secret, f.clock.now()))
	if err != nil || !session.Identity.TOTPEnabled {
		t.Fatal(err)
	}
	_, err = f.s.VerifyChallenge(ctx, login.Challenge, codeAt(t, setup.Secret, f.clock.now()))
	requireError(t, err, ErrChallengeExpired)
	restarted, _, err := New(ctx, Dependencies{DB: f.db, Cipher: f.cipher, Now: f.clock.now})
	if err != nil {
		t.Fatal(err)
	}
	second, err := restarted.PasswordLogin(ctx, "owner", "old password", "restart")
	if err != nil {
		t.Fatal(err)
	}
	_, err = restarted.VerifyChallenge(ctx, second.Challenge, codeAt(t, setup.Secret, f.clock.now()))
	requireError(t, err, ErrInvalidCredentials)
	f.clock.add(30 * time.Second)
	if err := f.s.DisableTOTP(ctx, current.Identity, "old password", codeAt(t, setup.Secret, f.clock.now())); err != nil {
		t.Fatal(err)
	}
	a, err := f.s.admin(ctx)
	if err != nil || a.enabled() || len(a.secret) != 0 || a.lastStep.Valid {
		t.Fatal("关闭TOTP未清空三字段")
	}
	if err := f.s.Validate(ctx, session.Identity); err != nil {
		t.Fatal("关闭TOTP不应额外吊销会话", err)
	}
	_, err = restarted.VerifyChallenge(ctx, second.Challenge, codeAt(t, setup.Secret, f.clock.now()))
	requireError(t, err, ErrChallengeExpired)
}

func TestTOTPConcurrentSingleConsumptionAndRollback(t *testing.T) {
	f := newFixture(t, true)
	ctx := context.Background()
	current := f.session(t)
	setup := f.enable(t, current.Identity)
	first, err := f.s.PasswordLogin(ctx, "owner", "old password", "one")
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.s.PasswordLogin(ctx, "owner", "old password", "two")
	if err != nil {
		t.Fatal(err)
	}
	f.clock.add(30 * time.Second)
	code := codeAt(t, setup.Secret, f.clock.now())
	f.exec(t, "CREATE TRIGGER fail_session BEFORE INSERT ON auth_tokens BEGIN SELECT RAISE(ABORT, 'test failure'); END")
	_, err = f.s.VerifyChallenge(ctx, first.Challenge, code)
	requireError(t, err, ErrStorage)
	var step int64
	if err := f.db.QueryRow("SELECT totp_last_step FROM admin").Scan(&step); err != nil || step != f.clock.now().Unix()/30-1 || f.count(t, "auth_tokens") != 1 {
		t.Fatal("创建会话失败后仍消费了验证码")
	}
	f.exec(t, "DROP TRIGGER fail_session")
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, token := range []string{first.Challenge, second.Challenge} {
		go func() {
			<-start
			_, err := f.s.VerifyChallenge(ctx, token, code)
			results <- err
		}()
	}
	close(start)
	var succeeded, rejected int
	for i := 0; i < 2; i++ {
		err := <-results
		if err == nil {
			succeeded++
		} else if errors.Is(err, ErrInvalidCredentials) {
			rejected++
		} else {
			t.Fatal(err)
		}
	}
	if succeeded != 1 || rejected != 1 || f.count(t, "auth_tokens") != 2 {
		t.Fatal("同一验证码被并发消费多次")
	}
}

func TestTOTPErrorsExpiryAndMaintenanceWithoutKey(t *testing.T) {
	f := newFixture(t, true)
	ctx := context.Background()
	current, other := f.session(t), f.session(t)
	setup, err := f.s.BeginTOTP(ctx, current.Identity, "old password")
	if err != nil {
		t.Fatal(err)
	}
	requireError(t, f.s.EnableTOTP(ctx, other.Identity, setup.SetupToken, codeAt(t, setup.Secret, f.clock.now())), ErrChallengeExpired)
	requireError(t, f.s.EnableTOTP(ctx, current.Identity, setup.SetupToken, "abc"), ErrInvalidCredentials)
	f.clock.add(challengeLifetime)
	requireError(t, f.s.EnableTOTP(ctx, current.Identity, setup.SetupToken, codeAt(t, setup.Secret, f.clock.now())), ErrChallengeExpired)
	setup = f.enable(t, current.Identity)
	login, err := f.s.PasswordLogin(ctx, "owner", "old password", "browser")
	if err != nil {
		t.Fatal(err)
	}
	f.clock.add(challengeLifetime)
	_, err = f.s.VerifyChallenge(ctx, login.Challenge, codeAt(t, setup.Secret, f.clock.now()))
	requireError(t, err, ErrChallengeExpired)
	maintenance, _, err := New(ctx, Dependencies{DB: f.db, Now: f.clock.now})
	if err != nil {
		t.Fatal(err)
	}
	_, err = maintenance.BeginTOTP(ctx, current.Identity, "old password")
	requireError(t, err, ErrStorage)
	_, err = maintenance.VerifyChallenge(ctx, login.Challenge, "000000")
	requireError(t, err, ErrStorage)
	if err := maintenance.DisableTOTPForCLI(ctx); err != nil {
		t.Fatal(err)
	}
	a, err := f.s.admin(ctx)
	if err != nil || a.enabled() || a.lastStep.Valid || len(a.secret) != 0 {
		t.Fatal("无密钥的维护命令未清空TOTP")
	}
	if err := f.s.Validate(ctx, current.Identity); err != nil {
		t.Fatal("关闭TOTP的维护命令额外吊销了会话")
	}
}

func TestPendingCredentialsInvalidatedByPasswordAndCLI(t *testing.T) {
	f := newFixture(t, true)
	ctx := context.Background()
	current := f.session(t)
	setup, err := f.s.BeginTOTP(ctx, current.Identity, "old password")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.ChangePassword(ctx, current.Identity, "old password", "old password"); err != nil {
		t.Fatal(err)
	}
	requireError(t, f.s.EnableTOTP(ctx, current.Identity, setup.SetupToken, codeAt(t, setup.Secret, f.clock.now())), ErrChallengeExpired)
	setup = f.enable(t, current.Identity)
	login, err := f.s.PasswordLogin(ctx, "owner", "old password", "browser")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.ResetPassword(ctx, "old password", false); err != nil {
		t.Fatal(err)
	}
	f.clock.add(30 * time.Second)
	_, err = f.s.VerifyChallenge(ctx, login.Challenge, codeAt(t, setup.Secret, f.clock.now()))
	requireError(t, err, ErrChallengeExpired)
	a, err := f.s.admin(ctx)
	if err != nil || !a.enabled() {
		t.Fatal("未要求关闭TOTP时重置密码改掉了TOTP")
	}
}

func TestStorageErrorsCancellationAndSecretSerialization(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, true)
	session := f.session(t)
	secret := "sensitive-value-do-not-print"
	for _, value := range []any{Bootstrap{Token: secret}, Session{Token: secret}, LoginResult{Challenge: secret}, IssuedToken{Token: secret}, TOTPSetup{Secret: secret, SetupToken: secret, URI: secret}} {
		encoded, err := json.Marshal(value)
		if err != nil || strings.Contains(string(encoded), secret) || strings.Contains(fmt.Sprint(value), secret) || strings.Contains(fmt.Sprintf("%#v", value), secret) {
			t.Fatal("敏感凭据进入默认序列化或日志")
		}
	}
	err := stored(errors.New(secret))
	if err.Error() != ErrStorage.Error() || !errors.Is(err, ErrStorage) || strings.Contains(fmt.Sprint(err), secret) {
		t.Fatal("存储错误泄露了底层输入")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = f.s.PasswordLogin(cancelled, "owner", "old password", "browser")
	requireError(t, err, context.Canceled)
	requireError(t, f.s.Validate(cancelled, session.Identity), context.Canceled)
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.Setup(ctx)
	requireError(t, err, ErrStorage)
	_, _, err = New(ctx, Dependencies{DB: f.db, Cipher: f.cipher})
	requireError(t, err, ErrStorage)
}

func TestSeparateMaintenanceInvalidatesPendingSetupWhileDisabled(t *testing.T) {
	f := newFixture(t, true)
	ctx := context.Background()
	current := f.session(t)
	setup, err := f.s.BeginTOTP(ctx, current.Identity, "old password")
	if err != nil {
		t.Fatal(err)
	}
	before, err := readGeneration(ctx, f.db)
	if err != nil {
		t.Fatal(err)
	}
	var passwordChanged int64
	if err := f.db.QueryRow("SELECT password_changed_at FROM admin").Scan(&passwordChanged); err != nil {
		t.Fatal(err)
	}
	otherDB, err := sql.Open("sqlite", f.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer otherDB.Close()
	otherDB.SetMaxOpenConns(1)
	maintenance, _, err := New(ctx, Dependencies{DB: otherDB, Now: f.clock.now})
	if err != nil {
		t.Fatal(err)
	}
	if err := maintenance.DisableTOTPForCLI(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := readGeneration(ctx, f.db)
	if err != nil || after == before {
		t.Fatal("另一个数据库连接未换代")
	}
	requireError(t, f.s.EnableTOTP(ctx, current.Identity, setup.SetupToken, codeAt(t, setup.Secret, f.clock.now())), ErrChallengeExpired)
	if err := f.s.Validate(ctx, current.Identity); err != nil {
		t.Fatal("CLI关闭TOTP不应吊销现有会话", err)
	}
	var unchanged int64
	if err := f.db.QueryRow("SELECT password_changed_at FROM admin").Scan(&unchanged); err != nil || unchanged != passwordChanged {
		t.Fatal("关闭TOTP篡改了密码变更时间")
	}
}

func TestMissingGenerationAndDamagedTOTPFailClosed(t *testing.T) {
	f := newFixture(t, true)
	ctx := context.Background()
	current := f.session(t)
	f.exec(t, "DELETE FROM meta WHERE key='auth.generation'")
	_, err := f.s.PasswordLogin(ctx, "owner", "old password", "browser")
	requireError(t, err, ErrStorage)
	_, err = f.s.Setup(ctx)
	requireError(t, err, ErrStorage)
	_, err = f.s.BeginTOTP(ctx, current.Identity, "old password")
	requireError(t, err, ErrStorage)
	if f.count(t, "auth_tokens") != 1 {
		t.Fatal("缺代数仍建立了新会话")
	}
	resumed, _, err := New(ctx, Dependencies{DB: f.db, Cipher: f.cipher, Now: f.clock.now})
	if err != nil {
		t.Fatal(err)
	}
	f.exec(t, "UPDATE admin SET totp_enabled_at=?", f.clock.now().UnixMilli())
	_, err = resumed.PasswordLogin(ctx, "owner", "old password", "browser")
	requireError(t, err, ErrStorage)
	_, _, err = New(ctx, Dependencies{DB: f.db, Cipher: f.cipher, Now: f.clock.now})
	requireError(t, err, ErrStorage)
}

func TestEveryMutationRejectsRevokedIdentity(t *testing.T) {
	f := newFixture(t, true)
	ctx := context.Background()
	current := f.session(t)
	setup, err := f.s.BeginTOTP(ctx, current.Identity, "old password")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.Logout(ctx, current.Identity); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.ListTokens(ctx, current.Identity)
	requireError(t, err, ErrUnauthenticated)
	_, err = f.s.CreateToken(ctx, current.Identity, "forbidden")
	requireError(t, err, ErrUnauthenticated)
	requireError(t, f.s.RevokeToken(ctx, current.Identity, current.Identity.TokenID), ErrUnauthenticated)
	requireError(t, f.s.ChangePassword(ctx, current.Identity, "old password", "new password"), ErrUnauthenticated)
	_, err = f.s.BeginTOTP(ctx, current.Identity, "old password")
	requireError(t, err, ErrUnauthenticated)
	requireError(t, f.s.EnableTOTP(ctx, current.Identity, setup.SetupToken, codeAt(t, setup.Secret, f.clock.now())), ErrUnauthenticated)
	requireError(t, f.s.DisableTOTP(ctx, current.Identity, "old password", "000000"), ErrUnauthenticated)
	if f.count(t, "auth_tokens") != 1 {
		t.Fatal("吊销后仍新建了令牌")
	}
}

func TestSecurityWritesRollbackTogether(t *testing.T) {
	ctx := context.Background()
	t.Run("密码和其余凭据吊销", func(t *testing.T) {
		f := newFixture(t, true)
		current, other := f.session(t), f.session(t)
		before, err := readGeneration(ctx, f.db)
		if err != nil {
			t.Fatal(err)
		}
		f.exec(t, "CREATE TRIGGER fail_revoke BEFORE UPDATE ON auth_tokens BEGIN SELECT RAISE(ABORT, 'revocation failed'); END")
		requireError(t, f.s.ChangePassword(ctx, current.Identity, "old password", "new password"), ErrStorage)
		a, err := f.s.admin(ctx)
		if err != nil || a.passwordHash != passwordFixture.phc || a.generation != before {
			t.Fatal("吊销失败却改了密码或代数")
		}
		if err := f.s.Validate(ctx, other.Identity); err != nil {
			t.Fatal("失败事务改了其他会话")
		}
	})
	t.Run("绑定和代数更新", func(t *testing.T) {
		f := newFixture(t, true)
		current := f.session(t)
		setup, err := f.s.BeginTOTP(ctx, current.Identity, "old password")
		if err != nil {
			t.Fatal(err)
		}
		code := codeAt(t, setup.Secret, f.clock.now())
		f.exec(t, "CREATE TRIGGER fail_generation BEFORE UPDATE ON meta BEGIN SELECT RAISE(ABORT, 'generation failed'); END")
		requireError(t, f.s.EnableTOTP(ctx, current.Identity, setup.SetupToken, code), ErrStorage)
		a, err := f.s.admin(ctx)
		if err != nil || a.enabled() || len(a.secret) != 0 || a.lastStep.Valid {
			t.Fatal("代数更新失败仍保存了绑定")
		}
		f.exec(t, "DROP TRIGGER fail_generation")
		if err := f.s.EnableTOTP(ctx, current.Identity, setup.SetupToken, code); err != nil {
			t.Fatal("失败后无法重新提交原绑定", err)
		}
	})
	t.Run("关闭和验证码消费", func(t *testing.T) {
		f := newFixture(t, true)
		current := f.session(t)
		setup := f.enable(t, current.Identity)
		f.clock.add(30 * time.Second)
		code := codeAt(t, setup.Secret, f.clock.now())
		f.exec(t, "CREATE TRIGGER fail_clear BEFORE UPDATE ON admin WHEN NEW.totp_secret_enc IS NULL BEGIN SELECT RAISE(ABORT, 'clear failed'); END")
		requireError(t, f.s.DisableTOTP(ctx, current.Identity, "old password", code), ErrStorage)
		a, err := f.s.admin(ctx)
		if err != nil || !a.enabled() || a.lastStep.Int64 != f.clock.now().Unix()/30-1 {
			t.Fatal("关闭失败后验证码消费未回滚")
		}
		f.exec(t, "DROP TRIGGER fail_clear")
		if err := f.s.DisableTOTP(ctx, current.Identity, "old password", code); err != nil {
			t.Fatal("关闭失败后同一码不能重试", err)
		}
	})
	t.Run("重置密码和关闭两步验证", func(t *testing.T) {
		f := newFixture(t, true)
		current := f.session(t)
		f.enable(t, current.Identity)
		f.exec(t, "CREATE TRIGGER fail_revoke BEFORE UPDATE ON auth_tokens BEGIN SELECT RAISE(ABORT, 'revocation failed'); END")
		requireError(t, f.s.ResetPassword(ctx, "new password", true), ErrStorage)
		a, err := f.s.admin(ctx)
		if err != nil || a.passwordHash != passwordFixture.phc || !a.enabled() {
			t.Fatal("重置失败后密码或两步验证未回滚")
		}
		if err := f.s.Validate(ctx, current.Identity); err != nil {
			t.Fatal("重置失败后会话被吊销", err)
		}
	})
	t.Run("会话续期写入失败不返回身份", func(t *testing.T) {
		f := newFixture(t, true)
		current := f.session(t)
		f.clock.add(time.Hour)
		f.exec(t, "CREATE TRIGGER fail_touch BEFORE UPDATE ON auth_tokens BEGIN SELECT RAISE(ABORT, 'touch failed'); END")
		identity, err := f.s.Authenticate(ctx, current.Token, WebSession)
		requireError(t, err, ErrStorage)
		if identity.TokenID != 0 {
			t.Fatal("续期失败仍返回身份")
		}
		if err := f.s.Validate(ctx, current.Identity); err != nil {
			t.Fatal("失败续期改坏了原会话", err)
		}
	})
}

func TestAPITokenCanChangePasswordAndKeepsOnlyItself(t *testing.T) {
	f := newFixture(t, true)
	ctx := context.Background()
	web := f.session(t)
	issued, err := f.s.CreateToken(ctx, web.Identity, "automation")
	if err != nil {
		t.Fatal(err)
	}
	api, err := f.s.Authenticate(ctx, issued.Token, APIToken)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.ChangePassword(ctx, api, "old password", "new password"); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Validate(ctx, api); err != nil {
		t.Fatal("改密码吊销了当前API令牌", err)
	}
	requireError(t, f.s.Validate(ctx, web.Identity), ErrUnauthenticated)
}

func TestConcurrentInitializationCreatesOnlyOneAdmin(t *testing.T) {
	f := newFixture(t, false)
	ctx := context.Background()
	other, boot, err := New(ctx, Dependencies{DB: f.db, Cipher: f.cipher, Now: f.clock.now})
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, input := range []struct {
		service *Service
		token   string
	}{{f.s, f.boot.Token}, {other, boot.Token}} {
		go func() {
			<-start
			_, err := input.service.Initialize(ctx, input.token, "owner", "password", "browser")
			results <- err
		}()
	}
	close(start)
	var success, already int
	for i := 0; i < 2; i++ {
		err := <-results
		if err == nil {
			success++
		} else if errors.Is(err, ErrAlreadyInitialized) {
			already++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || already != 1 || f.count(t, "admin") != 1 || f.count(t, "auth_tokens") != 1 {
		t.Fatal("并发初始化创建了多个管理员或会话")
	}
}

func TestTOTPLoginCannotDowngradeWithoutValidCipher(t *testing.T) {
	f := newFixture(t, true)
	ctx := context.Background()
	current := f.session(t)
	setup := f.enable(t, current.Identity)
	f.clock.add(30 * time.Second)
	withoutCipher, _, err := New(ctx, Dependencies{DB: f.db, Now: f.clock.now})
	if err != nil {
		t.Fatal(err)
	}
	login, err := withoutCipher.PasswordLogin(ctx, "owner", "old password", "browser")
	requireError(t, err, ErrStorage)
	if login.Session != nil || login.Challenge != "" {
		t.Fatal("缺少密钥仍完成或降级了登录")
	}
	wrongCipher, err := security.NewCipher(security.NewMasterKey())
	if err != nil {
		t.Fatal(err)
	}
	wrongKey, _, err := New(ctx, Dependencies{DB: f.db, Cipher: wrongCipher, Now: f.clock.now})
	if err != nil {
		t.Fatal(err)
	}
	login, err = wrongKey.PasswordLogin(ctx, "owner", "old password", "browser")
	if err != nil {
		t.Fatal(err)
	}
	_, err = wrongKey.VerifyChallenge(ctx, login.Challenge, codeAt(t, setup.Secret, f.clock.now()))
	requireError(t, err, ErrStorage)
	if f.count(t, "auth_tokens") != 1 {
		t.Fatal("密钥不正确时创建了会话")
	}
}
