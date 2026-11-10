package backup

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestManifestStrictJSONAndFixedKDF(t *testing.T) {
	m := testManifest(t)
	raw, _ := json.Marshal(m)
	bad := map[string][]byte{
		"duplicate_root": append([]byte(`{"format":"kakatta.backup",`), raw[1:]...),
		"duplicate_kdf":  bytes.Replace(raw, []byte(`"name":"argon2id"`), []byte(`"name":"argon2id","name":"argon2id"`), 1),
		"unknown_root":   append([]byte(`{"unknown":1,`), raw[1:]...),
		"unknown_kdf":    bytes.Replace(raw, []byte(`"name":"argon2id"`), []byte(`"unknown":1,"name":"argon2id"`), 1),
		"missing_root":   bytes.Replace(raw, []byte(`"format":"kakatta.backup",`), nil, 1),
		"missing_kdf":    bytes.Replace(raw, []byte(`"name":"argon2id",`), nil, 1),
		"extra_value":    append(bytes.Clone(raw), []byte(` {}`)...),
		"wrong_type":     bytes.Replace(raw, []byte(`"time":3`), []byte(`"time":"3"`), 1),
		"invalid_utf8":   bytes.Replace(raw, []byte("test-version"), []byte{0xff}, 1),
		"oversized":      bytes.Repeat([]byte(" "), ManifestMaxBytes+1),
		"empty":          nil,
	}
	for name, change := range map[string]func(*Manifest){
		"kdf_name":      func(m *Manifest) { m.KDF.Name = "other" },
		"kdf_time":      func(m *Manifest) { m.KDF.Time = ^uint32(0) },
		"kdf_memory":    func(m *Manifest) { m.KDF.MemoryKiB = ^uint32(0) },
		"kdf_threads":   func(m *Manifest) { m.KDF.Threads = ^uint32(0) },
		"salt_short":    func(m *Manifest) { m.KDF.Salt = base64.StdEncoding.EncodeToString(make([]byte, 15)) },
		"salt_long":     func(m *Manifest) { m.KDF.Salt = base64.StdEncoding.EncodeToString(make([]byte, 17)) },
		"salt_newline":  func(m *Manifest) { m.KDF.Salt += "\n" },
		"salt_unpadded": func(m *Manifest) { m.KDF.Salt = strings.TrimRight(m.KDF.Salt, "=") },
		"salt_pad_bits": func(m *Manifest) { m.KDF.Salt = "AAAAAAAAAAAAAAAAAAAAAB==" },
		"timestamp":     func(m *Manifest) { m.CreatedAt = "2026-11-11" },
		"empty_app":     func(m *Manifest) { m.AppVersion = "" },
		"future_format": func(m *Manifest) { m.Version++ },
		"wrong_format":  func(m *Manifest) { m.Format = "other" },
		"future_schema": func(m *Manifest) { m.SchemaVersion++ },
		"zero_schema":   func(m *Manifest) { m.SchemaVersion = 0 },
	} {
		changed := m
		change(&changed)
		bad[name], _ = json.Marshal(changed)
	}
	// 占住计算名额，证明坏参数不会排队或启动昂贵计算。
	kdfSlot <- struct{}{}
	defer func() { <-kdfSlot }()
	for name, input := range bad {
		t.Run(name, func(t *testing.T) {
			archive := zipEntries(t, archiveEntry{ManifestEntry, input, 0600}, archiveEntry{PayloadEntry, nil, 0600})
			ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
			defer cancel()
			_, key, err := ReadArchive(ctx, bytes.NewReader(archive), int64(len(archive)), testPassword, filepath.Join(t.TempDir(), "candidate"), 4)
			if !errors.Is(err, ErrInvalidArchive) && !errors.Is(err, ErrUnsupportedVersion) || key != [MasterKeyBytes]byte{} {
				t.Fatalf("坏清单未在计算前拒绝：%v", err)
			}
		})
	}
}

func TestKDFCancellationWaitAndActiveSlot(t *testing.T) {
	kdfSlot <- struct{}{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	key, err := deriveKey(ctx, testPassword, make([]byte, SaltBytes))
	if !errors.Is(err, context.Canceled) || key != [MasterKeyBytes]byte{} || len(kdfSlot) != 1 {
		t.Fatal("取消的等待改动了计算名额", err)
	}
	<-kdfSlot
	kdfSlot <- struct{}{}
	waitCtx, stopWait := context.WithTimeout(t.Context(), 10*time.Millisecond)
	_, err = deriveKey(waitCtx, testPassword, make([]byte, SaltBytes))
	stopWait()
	if !errors.Is(err, context.DeadlineExceeded) || len(kdfSlot) != 1 {
		t.Fatal("等待中的取消未保留原计算名额", err)
	}
	<-kdfSlot
	ctx, cancel = context.WithCancel(t.Context())
	completed := make(chan error, 1)
	go func() {
		key, err := deriveKey(ctx, testPassword, make([]byte, SaltBytes))
		defer clear(key[:])
		if key != [MasterKeyBytes]byte{} {
			completed <- errors.New("取消后返回了密钥")
			return
		}
		completed <- err
	}()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for len(kdfSlot) == 0 {
		select {
		case <-deadline.C:
			cancel()
			<-completed
			t.Fatal("未进入计算名额")
		default:
		}
		runtime.Gosched()
	}
	cancel()
	if err := <-completed; !errors.Is(err, context.Canceled) || len(kdfSlot) != 0 {
		t.Fatal("取消的计算未结束或遗留计算名额", err)
	}
	key, err = deriveKey(t.Context(), "", make([]byte, SaltBytes))
	if !errors.Is(err, ErrPasswordRequired) || key != [MasterKeyBytes]byte{} {
		t.Fatal("空备份密码没有拒绝", err)
	}
}

func TestManifestCreationAndLongPassword(t *testing.T) {
	m := testManifest(t)
	if _, err := NewManifest("", 4, time.Now()); !errors.Is(err, ErrInvalidArchive) {
		t.Fatal("空程序版本没有拒绝", err)
	}
	first, err := validateManifest(m, 4)
	next, _ := NewManifest(m.AppVersion, m.SchemaVersion, time.Now())
	second, _ := validateManifest(next, 4)
	if err != nil || len(first) != SaltBytes || bytes.Equal(first, second) || !strings.HasSuffix(next.CreatedAt, "Z") {
		t.Fatal("清单随机盐或UTC时间错误", err)
	}
	key, err := deriveKey(t.Context(), strings.Repeat("密", 1000), first)
	defer clear(key[:])
	if err != nil {
		t.Fatal("非空长密码被额外限制", err)
	}
}
