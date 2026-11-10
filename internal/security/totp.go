package security

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	totpSecretBytes = 20
	totpPeriod      = 30
	totpIssuer      = "Kakatta"
)

var totpEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

func NewTOTPSecret() string {
	var key [totpSecretBytes]byte
	rand.Read(key[:])
	return totpEncoding.EncodeToString(key[:])
}

func decodeTOTPSecret(secret string) ([]byte, error) {
	if len(secret) != totpEncoding.EncodedLen(totpSecretBytes) {
		return nil, ErrTOTPSecret
	}
	key, err := totpEncoding.DecodeString(secret)
	if err != nil || len(key) != totpSecretBytes || totpEncoding.EncodeToString(key) != secret {
		clear(key)
		return nil, ErrTOTPSecret
	}
	return key, nil
}

// ValidateTOTPSecret 只检查现行密钥格式，不生成或消费验证码。
func ValidateTOTPSecret(secret string) error {
	key, err := decodeTOTPSecret(secret)
	clear(key)
	return err
}

func TOTPURI(secret, accountLabel string) (string, error) {
	if _, err := decodeTOTPSecret(secret); err != nil {
		return "", err
	}
	if accountLabel == "" || strings.Contains(accountLabel, ":") || !utf8.ValidString(accountLabel) {
		return "", ErrTOTPInput
	}
	label := totpIssuer + ":" + accountLabel
	u := url.URL{
		Scheme:   "otpauth",
		Host:     "totp",
		Path:     "/" + label,
		RawPath:  "/" + url.PathEscape(label),
		RawQuery: url.Values{"secret": {secret}, "issuer": {totpIssuer}, "algorithm": {"SHA1"}, "digits": {"6"}, "period": {"30"}}.Encode(),
	}
	return u.String(), nil
}

func VerifyTOTP(secret, code string, now time.Time, lastAcceptedStep int64) (int64, bool, error) {
	key, err := decodeTOTPSecret(secret)
	if err != nil {
		return -1, false, err
	}
	defer clear(key)
	if len(code) != 6 || now.Unix() < 0 || lastAcceptedStep < -1 {
		return -1, false, ErrTOTPInput
	}
	for _, digit := range []byte(code) {
		if digit < '0' || digit > '9' {
			return -1, false, ErrTOTPInput
		}
	}
	step, matched := now.Unix()/totpPeriod, int64(-1)
	for candidate := step - 1; candidate <= step+1; candidate++ {
		if candidate < 0 {
			continue
		}
		expected := hotp(key, uint64(candidate))
		if subtle.ConstantTimeCompare([]byte(expected), []byte(code)) == 1 && candidate > lastAcceptedStep {
			matched = candidate
		}
	}
	// 存储层仍须原子提交这个时间步，纯计算不能独自防住并发消费。
	return matched, matched >= 0, nil
}

func hotp(key []byte, counter uint64) string {
	var moving [8]byte
	binary.BigEndian.PutUint64(moving[:], counter)
	mac := hmac.New(sha1.New, key)
	mac.Write(moving[:])
	digest := mac.Sum(nil)
	offset := digest[len(digest)-1] & 0xf
	value := binary.BigEndian.Uint32(digest[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", value%1000000)
}
