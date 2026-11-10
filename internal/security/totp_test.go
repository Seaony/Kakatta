package security

import (
	"encoding/base32"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"
)

const rfcTOTPSecret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"

func TestHOTPFromRFC4226(t *testing.T) {
	// RFC 4226 附录 D 的六位结果。
	key := []byte("12345678901234567890")
	for counter, expected := range []string{"755224", "287082", "359152", "969429", "338314", "254676", "287922", "162583", "399871", "520489"} {
		if got := hotp(key, uint64(counter)); got != expected {
			t.Fatalf("第 %d 个 HOTP 向量不匹配", counter)
		}
	}
}

func TestTOTPFromRFC6238(t *testing.T) {
	// RFC 6238 附录 B 的 SHA1 结果取末六位，动态截断值不变。
	for _, tc := range []struct {
		unix int64
		code string
	}{{59, "287082"}, {1111111109, "081804"}, {1111111111, "050471"}, {1234567890, "005924"}, {2000000000, "279037"}, {20000000000, "353130"}} {
		for _, now := range []time.Time{time.Unix(tc.unix, 0).UTC(), time.Unix(tc.unix, 0).In(time.FixedZone("test", 13*3600))} {
			step, ok, err := VerifyTOTP(rfcTOTPSecret, tc.code, now, -1)
			if err != nil || !ok || step != tc.unix/30 {
				t.Fatalf("时间 %d 的 TOTP 向量不匹配：%v", tc.unix, err)
			}
		}
	}
}

func TestTOTPWindowBoundariesAndReplay(t *testing.T) {
	for _, tc := range []struct {
		unix int64
		ok   bool
	}{{0, false}, {29, false}, {30, true}, {59, true}, {60, true}, {89, true}, {90, true}, {119, true}, {120, false}} {
		step, ok, err := VerifyTOTP(rfcTOTPSecret, "359152", time.Unix(tc.unix, 0), -1)
		if err != nil || ok != tc.ok || ok && step != 2 || !ok && step != -1 {
			t.Fatalf("时间 %d 的窗口结果错误：%v", tc.unix, err)
		}
	}
	for _, last := range []int64{2, 3, 1<<63 - 1} {
		if step, ok, err := VerifyTOTP(rfcTOTPSecret, "359152", time.Unix(60, 0), last); err != nil || ok || step != -1 {
			t.Fatal("重复或更早的时间步被再次接受")
		}
	}
	if step, ok, err := VerifyTOTP(rfcTOTPSecret, "755224", time.Unix(0, 0), -1); err != nil || !ok || step != 0 {
		t.Fatal("Unix 起点没有跳过负的候选时间步")
	}
}

func TestTOTPCollisionConsumesLatestMatchingStep(t *testing.T) {
	// 这个固定 RFC 密钥在相邻两步恰好都产生 911617，不能先返回较早的一步。
	const first, latest = int64(910737), int64(910738)
	for _, last := range []int64{-1, first} {
		step, ok, err := VerifyTOTP(rfcTOTPSecret, "911617", time.Unix(first*30, 0), last)
		if err != nil || !ok || step != latest {
			t.Fatal("相邻碰撞没有选择最大的匹配时间步")
		}
	}
	if step, ok, err := VerifyTOTP(rfcTOTPSecret, "911617", time.Unix(first*30, 0), latest); err != nil || ok || step != -1 {
		t.Fatal("相邻碰撞重复消费了已用的时间步")
	}
}

func TestTOTPGenerationAndURI(t *testing.T) {
	a, b := NewTOTPSecret(), NewTOTPSecret()
	if a == b {
		t.Fatal("不同绑定复用了密钥")
	}
	for _, secret := range []string{a, b} {
		key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
		if err != nil || len(key) != 20 || len(secret) != 32 || strings.ToUpper(secret) != secret {
			t.Fatal("生成密钥不是 160 位的规范 Base32")
		}
	}
	label := "用户 a+b@example.com/手机?x=1#% &"
	uri, err := TOTPURI(a, label)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(uri)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "otpauth" || u.Host != "totp" || u.Path != "/Kakatta:"+label || u.Fragment != "" || u.User != nil || !strings.Contains(u.EscapedPath(), "%2F") {
		t.Fatal("TOTP URI 的账号标签没有独立编码")
	}
	q := u.Query()
	if len(q) != 5 || q.Get("secret") != a || q.Get("issuer") != "Kakatta" || q.Get("algorithm") != "SHA1" || q.Get("digits") != "6" || q.Get("period") != "30" {
		t.Fatal("TOTP URI 参数不符合通用验证器格式")
	}
}

func TestTOTPRejectsMalformedInputs(t *testing.T) {
	now := time.Unix(59, 0)
	for _, secret := range []string{"", "private-totp-secret-marker", strings.ToLower(rfcTOTPSecret), rfcTOTPSecret + "=", rfcTOTPSecret[:31], "!" + rfcTOTPSecret[1:], "\n" + rfcTOTPSecret[1:]} {
		if err := ValidateTOTPSecret(secret); !errors.Is(err, ErrTOTPSecret) {
			t.Fatal("坏密钥通过了格式校验")
		}
		if step, ok, err := VerifyTOTP(secret, "287082", now, -1); step != -1 || ok || !errors.Is(err, ErrTOTPSecret) {
			t.Fatal("接受了不规范的 TOTP 密钥")
		}
		if uri, err := TOTPURI(secret, "admin"); uri != "" || !errors.Is(err, ErrTOTPSecret) {
			t.Fatal("坏密钥生成了绑定 URI")
		}
	}
	for _, code := range []string{"", "28708", "0287082", "28708 ", "28708\n", "+87082", "１２３４５６", "١٢٣٤٥٦", "秘密"} {
		if step, ok, err := VerifyTOTP(rfcTOTPSecret, code, now, -1); step != -1 || ok || !errors.Is(err, ErrTOTPInput) {
			t.Fatal("接受了不规范的六位数字")
		}
	}
	for _, tc := range []struct {
		now  time.Time
		last int64
	}{{time.Unix(-1, 0), -1}, {now, -2}} {
		if step, ok, err := VerifyTOTP(rfcTOTPSecret, "287082", tc.now, tc.last); step != -1 || ok || !errors.Is(err, ErrTOTPInput) {
			t.Fatal("接受了无效的时间或已用步标记")
		}
	}
	for _, label := range []string{"", "private:account-marker", "\xff"} {
		if uri, err := TOTPURI(rfcTOTPSecret, label); uri != "" || !errors.Is(err, ErrTOTPInput) {
			t.Fatal("接受了无法编码的账号标签")
		}
	}
	if step, ok, err := VerifyTOTP(rfcTOTPSecret, "000000", now, -1); step != -1 || ok || err != nil {
		t.Fatal("格式正确的错误验证码应作为验证失败返回")
	}
}

func TestValidateTOTPSecretAcceptsCanonicalKeys(t *testing.T) {
	for _, secret := range []string{rfcTOTPSecret, NewTOTPSecret()} {
		if err := ValidateTOTPSecret(secret); err != nil {
			t.Fatalf("规范密钥未通过格式校验：%v", err)
		}
	}
}

func FuzzTOTPInputs(f *testing.F) {
	f.Add(rfcTOTPSecret, "287082", int64(59), int64(-1))
	f.Add("", "", int64(-1), int64(-2))
	f.Add(rfcTOTPSecret, "\x00\xff", int64(1<<63-1), int64(1<<63-1))
	f.Fuzz(func(t *testing.T, secret, code string, unix, last int64) {
		step, ok, err := VerifyTOTP(secret, code, time.Unix(unix, 0), last)
		if ok && (err != nil || step < 0 || step <= last) || !ok && step != -1 {
			t.Fatal("TOTP 返回了矛盾的验证状态")
		}
	})
}
