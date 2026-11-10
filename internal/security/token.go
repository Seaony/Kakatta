package security

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
)

func NewToken() (string, [32]byte) {
	var random [32]byte
	rand.Read(random[:])
	token := base64.RawURLEncoding.EncodeToString(random[:])
	return token, HashToken(token)
}

func HashToken(plaintext string) [32]byte { return sha256.Sum256([]byte(plaintext)) }

func TokenMatches(plaintext string, expectedHash [32]byte) bool {
	if len(plaintext) != base64.RawURLEncoding.EncodedLen(32) {
		return false
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(plaintext)
	if err != nil || len(decoded) != 32 || base64.RawURLEncoding.EncodeToString(decoded) != plaintext {
		return false
	}
	actual := HashToken(plaintext)
	return subtle.ConstantTimeCompare(actual[:], expectedHash[:]) == 1
}
