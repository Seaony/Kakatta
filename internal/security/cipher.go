package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"strings"
)

const masterKeyBytes = 32

func NewMasterKey() []byte {
	key := make([]byte, masterKeyBytes)
	rand.Read(key)
	return key
}

func DecodeMasterKey(encoded string) ([]byte, error) {
	if len(encoded) != base64.StdEncoding.EncodedLen(masterKeyBytes) {
		return nil, ErrMasterKey
	}
	key, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(key) != masterKeyBytes || base64.StdEncoding.EncodeToString(key) != encoded {
		return nil, ErrMasterKey
	}
	return key, nil
}

type Cipher struct{ aead cipher.AEAD }

func NewCipher(masterKey []byte) (*Cipher, error) {
	if len(masterKey) != masterKeyBytes {
		return nil, ErrMasterKey
	}
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, ErrMasterKey
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, ErrMasterKey
	}
	return &Cipher{aead: aead}, nil
}

func (c *Cipher) Seal(plaintext []byte, table, column, rowKey string) ([]byte, error) {
	aad, err := secretAAD(table, column, rowKey)
	if err != nil {
		return nil, err
	}
	return c.aead.Seal(nil, nil, plaintext, aad), nil
}

func (c *Cipher) Open(blob []byte, table, column, rowKey string) ([]byte, error) {
	aad, err := secretAAD(table, column, rowKey)
	if err != nil {
		return nil, err
	}
	plaintext, err := c.aead.Open(nil, nil, blob, aad)
	if err != nil {
		return nil, ErrCiphertext
	}
	return plaintext, nil
}

func secretAAD(table, column, rowKey string) ([]byte, error) {
	for _, part := range []string{table, column, rowKey} {
		if part == "" || strings.Contains(part, ":") {
			return nil, ErrAAD
		}
	}
	return []byte(table + ":" + column + ":" + rowKey), nil
}
