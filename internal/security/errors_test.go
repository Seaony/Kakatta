package security

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestPublicErrorsNeverEchoSensitiveInputs(t *testing.T) {
	c, err := NewCipher(fixedMasterKey())
	if err != nil {
		t.Fatal(err)
	}
	_, passwordErr := VerifyPassword(context.Background(), "private-password-marker", "private-phc-marker")
	_, keyErr := DecodeMasterKey("private-master-key-marker")
	_, cipherErr := NewCipher([]byte("private-master-key-marker"))
	_, aadErr := c.Seal([]byte("private-plaintext-marker"), "private:aad-marker", "column", "key")
	_, openAADErr := c.Open([]byte("private-ciphertext-marker"), "table", "column", "private:aad-marker")
	_, decryptErr := c.Open([]byte("private-ciphertext-marker"), "table", "column", "key")
	_, uriErr := TOTPURI(rfcTOTPSecret, "private:account-marker")
	_, _, secretErr := VerifyTOTP("private-totp-secret-marker", "287082", time.Unix(59, 0), -1)
	_, _, codeErr := VerifyTOTP(rfcTOTPSecret, "private-code-marker", time.Unix(59, 0), -1)
	for _, err := range []error{passwordErr, keyErr, cipherErr, aadErr, openAADErr, decryptErr, uriErr, secretErr, codeErr} {
		if err == nil {
			t.Fatal("测试输入没有触发预期错误")
		}
		for _, needle := range []string{"private-", "private:", rfcTOTPSecret, "287082", "otpauth://"} {
			if strings.Contains(err.Error(), needle) {
				t.Fatal("公开错误回显了敏感输入")
			}
		}
	}
}
