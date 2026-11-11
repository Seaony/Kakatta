package auth

import (
	"database/sql"
	"testing"
	"time"
)

func TestReadOnlyAuthenticationDoesNotRenewOrNeedWritableStorage(t *testing.T) {
	f := newFixture(t, true)
	web := f.session(t)
	api, err := f.s.CreateToken(t.Context(), web.Identity, "本地诊断")
	if err != nil {
		t.Fatal(err)
	}
	f.clock.add(time.Hour)
	f.exec(t, `CREATE TRIGGER readonly_auth_guard BEFORE UPDATE ON auth_tokens BEGIN SELECT RAISE(ABORT,'no writes'); END`)
	for _, test := range []struct {
		token string
		kind  Kind
		id    int64
	}{
		{web.Token, WebSession, web.Identity.TokenID},
		{api.Token, APIToken, api.ID},
	} {
		var beforeUsed, beforeExpires sql.NullInt64
		if err := f.db.QueryRow(`SELECT last_used_at,expires_at FROM auth_tokens WHERE id=?`, test.id).Scan(&beforeUsed, &beforeExpires); err != nil {
			t.Fatal(err)
		}
		identity, err := f.s.AuthenticateReadOnly(t.Context(), test.token, test.kind)
		if err != nil || identity.TokenID != test.id || identity.CookieExpiresAt != beforeExpires.Int64 {
			t.Fatalf("只读鉴权失败或自行延长过期：%+v，%v", identity, err)
		}
		var afterUsed, afterExpires sql.NullInt64
		if err := f.db.QueryRow(`SELECT last_used_at,expires_at FROM auth_tokens WHERE id=?`, test.id).Scan(&afterUsed, &afterExpires); err != nil || beforeUsed != afterUsed || beforeExpires != afterExpires {
			t.Fatalf("只读鉴权写入了凭据：%v", err)
		}
		identity, err = f.s.Authenticate(t.Context(), test.token, test.kind)
		requireError(t, err, ErrStorage)
		if identity.TokenID != 0 {
			t.Fatal("普通鉴权写入失败仍放行")
		}
	}
}

func TestReadOnlyAuthenticationKeepsAllCredentialChecks(t *testing.T) {
	for _, name := range []string{"类型不匹配", "未知类型", "无效令牌", "已吊销", "闲置到期", "绝对到期"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, true)
			web := f.session(t)
			token, kind := web.Token, WebSession
			switch name {
			case "类型不匹配":
				kind = APIToken
			case "未知类型":
				kind = "unknown"
			case "无效令牌":
				token += "x"
			case "已吊销":
				f.exec(t, `UPDATE auth_tokens SET revoked_at=? WHERE id=?`, f.clock.now().UnixMilli(), web.Identity.TokenID)
			case "闲置到期":
				f.clock.add(sessionIdle)
			case "绝对到期":
				f.clock.add(sessionAbsolute)
				f.exec(t, `UPDATE auth_tokens SET last_used_at=?,expires_at=? WHERE id=?`, f.clock.now().UnixMilli(), f.clock.now().Add(time.Hour).UnixMilli(), web.Identity.TokenID)
			}
			identity, err := f.s.AuthenticateReadOnly(t.Context(), token, kind)
			requireError(t, err, ErrUnauthenticated)
			if identity.TokenID != 0 {
				t.Fatal("无效凭据仍返回了身份")
			}
		})
	}
}
