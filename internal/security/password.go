package security

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	passwordPrefix    = "$argon2id$v=19$m=65536,t=3,p=4$"
	passwordSaltBytes = 16
	passwordHashBytes = 32
	passwordPHCLength = len(passwordPrefix) + 22 + 1 + 43
)

var passwordSlot = make(chan struct{}, 1)

func HashPassword(ctx context.Context, password string) (string, error) {
	var salt [passwordSaltBytes]byte
	rand.Read(salt[:])
	key, err := passwordWork(ctx, func() []byte {
		return argon2.IDKey([]byte(password), salt[:], 3, 64*1024, 4, passwordHashBytes)
	})
	if err != nil {
		return "", err
	}
	defer clear(key)
	return passwordPrefix + base64.RawStdEncoding.EncodeToString(salt[:]) + "$" + base64.RawStdEncoding.EncodeToString(key), nil
}

func VerifyPassword(ctx context.Context, password, phc string) (bool, error) {
	salt, expected, err := parsePasswordHash(phc)
	if err != nil {
		return false, err
	}
	key, err := passwordWork(ctx, func() []byte {
		return argon2.IDKey([]byte(password), salt, 3, 64*1024, 4, passwordHashBytes)
	})
	if err != nil {
		return false, err
	}
	defer clear(key)
	return subtle.ConstantTimeCompare(key, expected) == 1, nil
}

// ValidatePasswordHash 只检查现行 PHC 格式和参数，不执行密码计算。
func ValidatePasswordHash(phc string) error {
	salt, key, err := parsePasswordHash(phc)
	clear(salt)
	clear(key)
	return err
}

func parsePasswordHash(phc string) ([]byte, []byte, error) {
	// 参数和长度先限定，损坏的记录不能决定昂贵计算的资源开销。
	if len(phc) != passwordPHCLength || !strings.HasPrefix(phc, passwordPrefix) {
		return nil, nil, ErrPasswordHash
	}
	saltText, hashText, found := strings.Cut(phc[len(passwordPrefix):], "$")
	if !found || len(saltText) != 22 || len(hashText) != 43 {
		return nil, nil, ErrPasswordHash
	}
	salt, err := base64.RawStdEncoding.Strict().DecodeString(saltText)
	if err != nil || len(salt) != passwordSaltBytes || base64.RawStdEncoding.EncodeToString(salt) != saltText {
		return nil, nil, ErrPasswordHash
	}
	key, err := base64.RawStdEncoding.Strict().DecodeString(hashText)
	if err != nil || len(key) != passwordHashBytes || base64.RawStdEncoding.EncodeToString(key) != hashText {
		return nil, nil, ErrPasswordHash
	}
	return salt, key, nil
}

func passwordWork(ctx context.Context, compute func() []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case passwordSlot <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-passwordSlot }()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := compute()
	// 底层计算不能抢停，取消也要等它结束后才释放唯一名额。
	if err := ctx.Err(); err != nil {
		clear(key)
		return nil, err
	}
	return key, nil
}
