package security

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

func TestTokenHashKnownVector(t *testing.T) {
	want, err := hex.DecodeString("ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad")
	if err != nil {
		t.Fatal(err)
	}
	got := HashToken("abc")
	if string(got[:]) != string(want) {
		t.Fatal("令牌摘要不符合 SHA-256 向量")
	}
}

func TestTokenGenerationAndMatching(t *testing.T) {
	a, ah := NewToken()
	b, bh := NewToken()
	if a == b || ah == bh {
		t.Fatal("不同令牌复用了随机值")
	}
	for _, token := range []string{a, b} {
		decoded, err := base64.RawURLEncoding.Strict().DecodeString(token)
		if err != nil || len(decoded) != 32 || len(token) != 43 {
			t.Fatal("生成令牌不是 32 字节的规范 Base64URL")
		}
	}
	if ah != HashToken(a) || bh != HashToken(b) || !TokenMatches(a, ah) || !TokenMatches(b, bh) || TokenMatches(a, bh) || TokenMatches(b, ah) {
		t.Fatal("令牌摘要和验证结果不一致")
	}
	for _, token := range []string{"", "private-token-marker", strings.Repeat("A", 42), strings.Repeat("A", 44), strings.Repeat("+", 43), strings.Repeat("A", 42) + "B", a + "=", a + "\n", " " + a, strings.Repeat("A", 1<<20)} {
		if TokenMatches(token, ah) || TokenMatches(token, HashToken(token)) {
			t.Fatal("不规范的令牌通过了验证")
		}
	}
}
