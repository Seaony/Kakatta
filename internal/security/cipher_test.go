package security

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func fixedMasterKey() []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	return key
}

func TestMasterKeyGenerationAndStrictDecoding(t *testing.T) {
	a, b := NewMasterKey(), NewMasterKey()
	if len(a) != 32 || len(b) != 32 || bytes.Equal(a, b) {
		t.Fatal("主密钥必须独立生成 32 字节随机值")
	}
	key := fixedMasterKey()
	encoded := base64.StdEncoding.EncodeToString(key)
	decoded, err := DecodeMasterKey(encoded)
	if err != nil || !bytes.Equal(key, decoded) {
		t.Fatal("不能读取规范 Base64 主密钥")
	}
	for _, input := range []string{"", "private-master-key-marker", strings.TrimRight(encoded, "="), encoded + "\n", " " + encoded, encoded[:42] + "9=", encoded[:10] + "\n" + encoded[11:]} {
		if got, err := DecodeMasterKey(input); got != nil || !errors.Is(err, ErrMasterKey) {
			t.Fatal("主密钥接受了不规范的编码")
		}
	}
	for size := 0; size < 65; size++ {
		if size == 32 {
			continue
		}
		if got, err := DecodeMasterKey(base64.StdEncoding.EncodeToString(make([]byte, size))); got != nil || !errors.Is(err, ErrMasterKey) {
			t.Fatalf("接受了 %d 字节主密钥", size)
		}
		if got, err := NewCipher(make([]byte, size)); got != nil || !errors.Is(err, ErrMasterKey) {
			t.Fatalf("接受了 %d 字节 AES 主密钥", size)
		}
	}
}

func TestCipherFormatAADAndIndependentNonce(t *testing.T) {
	key := fixedMasterKey()
	c, err := NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	standard, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	clear(key)
	for _, plaintext := range [][]byte{nil, {}, []byte("中文\x00密钥\xff"), bytes.Repeat([]byte{0, 1, 255}, 1000)} {
		a, err := c.Seal(plaintext, "channels", "config_enc", "fixed-uuid")
		if err != nil {
			t.Fatal(err)
		}
		b, err := c.Seal(plaintext, "channels", "config_enc", "fixed-uuid")
		if err != nil || len(a) != len(plaintext)+28 || bytes.Equal(a[:12], b[:12]) {
			t.Fatal("密文格式或随机 nonce 不符合约定")
		}
		got, err := c.Open(a, "channels", "config_enc", "fixed-uuid")
		if err != nil || !bytes.Equal(got, plaintext) {
			t.Fatal("加密值不能原样恢复")
		}
		got, err = standard.Open(nil, a[:12], a[12:], []byte("channels:config_enc:fixed-uuid"))
		if err != nil || !bytes.Equal(got, plaintext) {
			t.Fatal("标准 GCM 不能按约定的前缀和 AAD 打开密文")
		}
	}
}

func TestCipherTamperingAndWrongLocationNeverReturnPlaintext(t *testing.T) {
	c, err := NewCipher(fixedMasterKey())
	if err != nil {
		t.Fatal(err)
	}
	blob, err := c.Seal([]byte("private-plaintext-marker"), "ai_providers", "api_key_enc", "uuid-one")
	if err != nil {
		t.Fatal(err)
	}
	reject := func(blob []byte, table, column, row string) {
		t.Helper()
		if got, err := c.Open(blob, table, column, row); got != nil || !errors.Is(err, ErrCiphertext) {
			t.Fatal("未经验证的数据被返回或错误泄露了详细内容")
		}
	}
	for i := range blob {
		changed := bytes.Clone(blob)
		changed[i] ^= 1
		reject(changed, "ai_providers", "api_key_enc", "uuid-one")
	}
	for length := 0; length < 28; length++ {
		reject(blob[:length], "ai_providers", "api_key_enc", "uuid-one")
	}
	reject(append(bytes.Clone(blob), 0), "ai_providers", "api_key_enc", "uuid-one")
	for _, location := range [][3]string{{"channels", "api_key_enc", "uuid-one"}, {"ai_providers", "other_enc", "uuid-one"}, {"ai_providers", "api_key_enc", "uuid-two"}} {
		reject(blob, location[0], location[1], location[2])
	}
	other, err := NewCipher(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := other.Open(blob, "ai_providers", "api_key_enc", "uuid-one"); got != nil || !errors.Is(err, ErrCiphertext) {
		t.Fatal("错误主密钥返回了数据")
	}
}

func TestCipherRejectsAmbiguousAAD(t *testing.T) {
	c, err := NewCipher(fixedMasterKey())
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"", "private:aad-marker"} {
		for i := range 3 {
			location := [3]string{"table", "column", "uuid"}
			location[i] = part
			if got, err := c.Seal([]byte("value"), location[0], location[1], location[2]); got != nil || !errors.Is(err, ErrAAD) {
				t.Fatal("加密接受了有歧义的 AAD")
			}
			if got, err := c.Open(nil, location[0], location[1], location[2]); got != nil || !errors.Is(err, ErrAAD) {
				t.Fatal("解密接受了有歧义的 AAD")
			}
		}
	}
}

func TestCipherConcurrentUse(t *testing.T) {
	c, err := NewCipher(fixedMasterKey())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	failures := make(chan error, 32)
	for i := range 32 {
		wg.Go(func() {
			for range 8 {
				blob, err := c.Seal([]byte("value"), "table", "column_enc", strconv.Itoa(i))
				if err != nil {
					failures <- err
					return
				}
				plain, err := c.Open(blob, "table", "column_enc", strconv.Itoa(i))
				if err != nil || string(plain) != "value" {
					failures <- errors.New("并发解密失败")
					return
				}
			}
		})
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
}

func FuzzCipherOpen(f *testing.F) {
	c, err := NewCipher(fixedMasterKey())
	if err != nil {
		f.Fatal(err)
	}
	blob, err := c.Seal([]byte("test-value"), "table", "secret_enc", "1")
	if err != nil {
		f.Fatal(err)
	}
	f.Add(blob)
	f.Add([]byte{})
	f.Add([]byte("private-ciphertext-marker"))
	f.Fuzz(func(t *testing.T, input []byte) {
		plain, err := c.Open(input, "table", "secret_enc", "1")
		if err != nil && (plain != nil || !errors.Is(err, ErrCiphertext)) {
			t.Fatal("坏密文错误没有保持固定边界")
		}
	})
}
