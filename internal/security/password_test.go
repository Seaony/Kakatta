package security

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

const referencePHC = "$argon2id$v=19$m=65536,t=3,p=4$AAECAwQFBgcICQoLDA0ODw$vUrOhcKV+zjYROFncankOSUoKsmCyGVr5Vou9s8g/Vg"
const referenceUnicodePHC = "$argon2id$v=19$m=65536,t=3,p=4$AAECAwQFBgcICQoLDA0ODw$n7JkuWU5d0xdRKQtL39GODUnjPBT4VKFAtkgq+Eb3Ls"

func TestPasswordReferenceAndExactBytes(t *testing.T) {
	// 固定向量由 argon2-cffi 25.1.0 的 C 实现按相同参数生成。
	// https://argon2-cffi.readthedocs.io/en/stable/api.html#argon2.low_level.hash_secret
	for _, tc := range []struct{ password, phc string }{
		{"password", referencePHC},
		{"  かかった 密码 e\u0301\x00  ", referenceUnicodePHC},
	} {
		ok, err := VerifyPassword(context.Background(), tc.password, tc.phc)
		if err != nil || !ok {
			t.Fatalf("独立参考向量无法验证：%v", err)
		}
	}
	for _, password := range []string{"", "  かかった 密码 e\u0301\x00  "} {
		phc, err := HashPassword(context.Background(), password)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(phc, passwordPrefix) || len(phc) != passwordPHCLength {
			t.Fatal("生成的 PHC 参数或长度不符合约定")
		}
		if ok, err := VerifyPassword(context.Background(), password, phc); err != nil || !ok {
			t.Fatalf("原样密码不能验证：%v", err)
		}
		if ok, err := VerifyPassword(context.Background(), password+"!", phc); err != nil || ok {
			t.Fatalf("错误密码被接受：%v", err)
		}
	}
	if ok, err := VerifyPassword(context.Background(), "  かかった 密码 é\x00  ", referenceUnicodePHC); err != nil || ok {
		t.Fatalf("密码被悄悄做了 Unicode 归一化：%v", err)
	}
	if ok, err := VerifyPassword(context.Background(), "かかった 密码 e\u0301\x00", referenceUnicodePHC); err != nil || ok {
		t.Fatalf("密码被悄悄去掉了两端空格：%v", err)
	}
}

func TestHashPasswordUsesFreshSalt(t *testing.T) {
	a, err := HashPassword(context.Background(), "same-password")
	if err != nil {
		t.Fatal(err)
	}
	b, err := HashPassword(context.Background(), "same-password")
	if err != nil {
		t.Fatal(err)
	}
	sa, _, err := parsePasswordHash(a)
	if err != nil {
		t.Fatal(err)
	}
	sb, _, err := parsePasswordHash(b)
	if err != nil || a == b || bytes.Equal(sa, sb) {
		t.Fatal("相同密码没有使用独立随机盐")
	}
}

func TestMalformedPHCRejectsBeforePasswordWork(t *testing.T) {
	valid := referencePHC
	salt, digest, err := parsePasswordHash(valid)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(valid, "$")
	badSaltBits := parts[4][:21] + "x"
	badHashBits := parts[5][:42] + "x"
	bad := []string{
		"", "secret-hash-marker", strings.Repeat("a", 1<<20), valid + "$extra", " " + valid, valid + "\n",
		strings.Replace(valid, "argon2id", "argon2i", 1), strings.Replace(valid, "v=19", "v=16", 1),
		strings.Replace(valid, "m=65536", "m=4294967295", 1), strings.Replace(valid, "m=65536", "m=00064", 1),
		strings.Replace(valid, "t=3", "t=0", 1), strings.Replace(valid, "p=4", "p=0", 1),
		strings.Replace(valid, "p=4", "p=1", 1),
		strings.Replace(valid, "t=3,p=4", "p=4,t=3", 1), strings.Replace(valid, "t=3", "t=3,t=3", 1),
		strings.Replace(valid, "p=4", "p=4,x=1", 1), strings.Replace(valid, parts[4], badSaltBits, 1),
		strings.Replace(valid, parts[5], badHashBits, 1), strings.Replace(valid, parts[4], parts[4]+"==", 1),
		strings.Replace(valid, parts[5], parts[5]+"=", 1),
		passwordPrefix + base64.RawStdEncoding.EncodeToString(salt[:15]) + "$" + parts[5],
		passwordPrefix + parts[4] + "$" + base64.RawStdEncoding.EncodeToString(digest[:31]),
		strings.Replace(valid, parts[4], "!"+parts[4][1:], 1), strings.Replace(valid, parts[5], "!"+parts[5][1:], 1),
		strings.Replace(valid, parts[4], "\n"+parts[4][1:], 1),
	}
	for i, phc := range bad {
		if err := ValidatePasswordHash(phc); !errors.Is(err, ErrPasswordHash) {
			t.Fatalf("第 %d 个畸形 PHC 通过了格式校验：%v", i, err)
		}
	}
	passwordSlot <- struct{}{}
	defer func() { <-passwordSlot }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for i, phc := range bad {
		if ok, err := VerifyPassword(ctx, "private-password-marker", phc); ok || !errors.Is(err, ErrPasswordHash) {
			t.Fatalf("第 %d 个畸形 PHC 进入了计算或被接受：%v", i, err)
		}
	}
}

func TestValidatePasswordHashAcceptsRealHashWithoutPasswordWork(t *testing.T) {
	phc, err := HashPassword(context.Background(), "仅用于格式校验的密码")
	if err != nil {
		t.Fatal(err)
	}
	passwordSlot <- struct{}{}
	done := make(chan struct{})
	result := make(chan error, 1)
	defer func() {
		select {
		case <-passwordSlot:
		default:
		}
		<-done
	}()
	go func() {
		result <- ValidatePasswordHash(phc)
		close(done)
	}()
	// 完成信号必须先于名额释放，等待期限只用于防止失败测试卡死。
	waitPasswordEvent(t, done)
	if err := <-result; err != nil {
		t.Fatalf("真实哈希未通过格式校验：%v", err)
	}
	if len(passwordSlot) != 1 {
		t.Fatal("格式校验改动了密码计算名额")
	}
}

type watchedContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *watchedContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func waitPasswordEvent(t *testing.T, event <-chan struct{}) {
	t.Helper()
	select {
	case <-event:
	case <-time.After(2 * time.Second):
		t.Fatal("密码计算没有进入预期阶段")
	}
}

func TestPasswordPublicCallsShareCancellableSlot(t *testing.T) {
	passwordSlot <- struct{}{}
	defer func() { <-passwordSlot }()
	for _, verify := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		watched := &watchedContext{Context: ctx, waiting: make(chan struct{})}
		done := make(chan error, 1)
		go func() {
			if verify {
				_, err := VerifyPassword(watched, "password", referencePHC)
				done <- err
			} else {
				_, err := HashPassword(watched, "password")
				done <- err
			}
		}()
		waitPasswordEvent(t, watched.waiting)
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("排队取消没有返回取消错误：%v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("排队的密码计算不能及时取消")
		}
	}
}

func TestPasswordRunningCancellationKeepsSlotUntilComputeEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started, finish, computed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	type result struct {
		key []byte
		err error
	}
	first := make(chan result, 1)
	go func() {
		key, err := passwordWork(ctx, func() []byte {
			close(started)
			<-finish
			close(computed)
			return []byte("private-result-marker")
		})
		first <- result{key, err}
	}()
	defer func() {
		select {
		case <-finish:
		default:
			close(finish)
		}
	}()
	waitPasswordEvent(t, started)
	cancel()
	secondCtx := &watchedContext{Context: context.Background(), waiting: make(chan struct{})}
	second := make(chan result, 1)
	go func() {
		key, err := passwordWork(secondCtx, func() []byte {
			select {
			case <-computed:
				return []byte("second")
			default:
				return []byte("overlap")
			}
		})
		second <- result{key, err}
	}()
	waitPasswordEvent(t, secondCtx.waiting)
	select {
	case <-first:
		t.Fatal("取消提前结束了仍在执行的计算")
	default:
	}
	select {
	case <-second:
		t.Fatal("取消提前释放了计算名额")
	default:
	}
	close(finish)
	a, b := <-first, <-second
	if a.key != nil || !errors.Is(a.err, context.Canceled) || b.err != nil || string(b.key) != "second" {
		t.Fatal("运行中取消没有串行完成并丢弃取消结果")
	}
}

func TestAlreadyCancelledPasswordWorkNeverComputes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	computed := false
	key, err := passwordWork(ctx, func() []byte { computed = true; return nil })
	if key != nil || computed || !errors.Is(err, context.Canceled) {
		t.Fatal("已取消的请求仍执行密码计算")
	}
}

func FuzzPasswordHashParser(f *testing.F) {
	for _, seed := range []string{"", referencePHC, "$argon2id$v=19$m=4294967295,t=0,p=0$bad$bad", "\x00\n"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, phc string) {
		salt, key, err := parsePasswordHash(phc)
		if err == nil && (len(phc) != passwordPHCLength || len(salt) != passwordSaltBytes || len(key) != passwordHashBytes) {
			t.Fatal("解析器接受了不完整的哈希")
		}
	})
}
