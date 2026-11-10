package backup

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testPassword = "local-backup-password-不要输出"

func testManifest(t *testing.T) Manifest {
	t.Helper()
	m, err := NewManifest("test-version", 4, time.Date(2026, 11, 11, 8, 9, 10, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestArchiveMultiChunkFileRoundTrip(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.db")
	database := bytes.Repeat([]byte("local sqlite bytes\x00"), (3*ChunkSize)/19+37)
	if err := os.WriteFile(source, database, 0600); err != nil {
		t.Fatal(err)
	}
	var key [MasterKeyBytes]byte
	copy(key[:], "private-test-master-key-32-bytes!")
	m := testManifest(t)
	var archive bytes.Buffer
	if err := WriteArchive(t.Context(), &archive, source, key, m, testPassword); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "prepared")
	got, restoredKey, err := ReadArchive(t.Context(), bytes.NewReader(archive.Bytes()), int64(archive.Len()), testPassword, dir, 4)
	if err != nil || got != m || restoredKey != key {
		t.Fatalf("多块备份往返失败：%v", err)
	}
	restored, err := os.ReadFile(filepath.Join(dir, DatabaseEntry))
	if err != nil || !bytes.Equal(database, restored) {
		t.Fatal("数据库文件不一致", err)
	}
	for _, name := range []string{"", DatabaseEntry} {
		info, err := os.Stat(filepath.Join(dir, name))
		want := os.FileMode(0600)
		if name == "" {
			want = 0700
		}
		if err != nil || info.Mode().Perm() != want {
			t.Fatalf("候选权限错误：%s %v", name, err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != DatabaseEntry {
		t.Fatal("解包目录应只保存数据库，主密钥应留在内存", err)
	}
}

type archiveEntry struct {
	name string
	body []byte
	mode os.FileMode
}

func zipEntries(t *testing.T, entries ...archiveEntry) []byte {
	t.Helper()
	var result bytes.Buffer
	w := zip.NewWriter(&result)
	for _, entry := range entries {
		h := &zip.FileHeader{Name: entry.name, Method: zip.Store}
		if entry.mode != 0 {
			h.SetMode(entry.mode)
		}
		file, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(entry.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return result.Bytes()
}

func tarBytes(t *testing.T, mutate func(*tar.Header), extra bool) []byte {
	t.Helper()
	var result bytes.Buffer
	w := tar.NewWriter(&result)
	header := tarHeader(DatabaseEntry, 12)
	if mutate != nil {
		mutate(header)
	}
	if err := w.WriteHeader(header); err != nil {
		t.Fatal(err)
	}
	if header.Typeflag == tar.TypeReg {
		if _, err := w.Write([]byte("database-raw")); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.WriteHeader(tarHeader(MasterKeyEntry, MasterKeyBytes)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(bytes.Repeat([]byte{0x5a}, MasterKeyBytes)); err != nil {
		t.Fatal(err)
	}
	if extra {
		if err := w.WriteHeader(tarHeader(MasterKeyEntry, 1)); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte{1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return result.Bytes()
}

func wrapTar(t *testing.T, m Manifest, plain []byte) ([]byte, []byte) {
	t.Helper()
	raw, _ := json.Marshal(m)
	salt, _ := validateManifest(m, m.SchemaVersion)
	key, err := deriveKey(t.Context(), testPassword, salt)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(key[:])
	aead, _ := backupAEAD(key)
	var payload bytes.Buffer
	w, err := newChunkWriter(t.Context(), &payload, aead, sha256.Sum256(raw))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return raw, payload.Bytes()
}

func assertRejected(t *testing.T, archive []byte, password string, want error) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "unverified")
	_, key, err := ReadArchive(t.Context(), bytes.NewReader(archive), int64(len(archive)), password, dir, 4)
	if err == nil || want != nil && !errors.Is(err, want) {
		t.Fatalf("坏归档没有拒绝或原因错误：%v", err)
	}
	if key != [MasterKeyBytes]byte{} {
		t.Fatal("失败返回了未经验证的主密钥")
	}
	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("失败留下未验证的明文候选")
	}
	if strings.Contains(err.Error(), password) || strings.Contains(err.Error(), dir) {
		t.Fatal("错误信息暴露密码或私有路径")
	}
}

func TestArchiveAuthenticationAndFinalCRC(t *testing.T) {
	m := testManifest(t)
	raw, payload := wrapTar(t, m, tarBytes(t, nil, false))
	valid := zipEntries(t, archiveEntry{ManifestEntry, raw, 0600}, archiveEntry{PayloadEntry, payload, 0600})
	assertRejected(t, valid, "wrong-password", ErrAuthentication)
	corrupt := bytes.Clone(payload)
	corrupt[len(corrupt)-1] ^= 1
	assertRejected(t, zipEntries(t, archiveEntry{ManifestEntry, raw, 0600}, archiveEntry{PayloadEntry, corrupt, 0600}), testPassword, ErrAuthentication)
	for _, change := range []func(*Manifest){
		func(m *Manifest) { m.AppVersion = "changed" },
		func(m *Manifest) { m.CreatedAt = "2025-11-11T08:09:10Z" },
		func(m *Manifest) { m.SchemaVersion = 3 },
		func(m *Manifest) { m.KDF.Salt = base64.StdEncoding.EncodeToString(make([]byte, SaltBytes)) },
	} {
		changed := m
		change(&changed)
		changedRaw, _ := json.Marshal(changed)
		assertRejected(t, zipEntries(t, archiveEntry{ManifestEntry, changedRaw, 0600}, archiveEntry{PayloadEntry, payload, 0600}), testPassword, ErrAuthentication)
	}
	for _, name := range []string{ManifestEntry, PayloadEntry} {
		badCRC := bytes.Clone(valid)
		zr, err := zip.NewReader(bytes.NewReader(badCRC), int64(len(badCRC)))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range zr.File {
			if f.Name != name {
				continue
			}
			dataAt, _ := f.DataOffset()
			descriptorAt := int(dataAt) + int(f.CompressedSize64)
			binary.LittleEndian.PutUint32(badCRC[descriptorAt+4:], f.CRC32^0xffff)
			for pos := descriptorAt + 16; pos+46+len(name) <= len(badCRC); pos++ {
				if binary.LittleEndian.Uint32(badCRC[pos:]) == 0x02014b50 && string(badCRC[pos+46:pos+46+len(name)]) == name {
					binary.LittleEndian.PutUint32(badCRC[pos+16:], f.CRC32^0xffff)
					break
				}
			}
		}
		assertRejected(t, badCRC, testPassword, ErrInvalidArchive)
	}
}

func TestArchiveRejectsUnexpectedZIPAndTarEntries(t *testing.T) {
	m := testManifest(t)
	raw, payload := wrapTar(t, m, tarBytes(t, nil, false))
	for _, entries := range [][]archiveEntry{
		{{ManifestEntry, raw, 0600}},
		{{ManifestEntry, raw, 0600}, {ManifestEntry, raw, 0600}},
		{{ManifestEntry, raw, 0600}, {PayloadEntry, payload, 0600}, {"extra", nil, 0600}},
		{{"../manifest.json", raw, 0600}, {PayloadEntry, payload, 0600}},
		{{"\\manifest.json", raw, 0600}, {PayloadEntry, payload, 0600}},
		{{ManifestEntry, raw, os.ModeSymlink | 0600}, {PayloadEntry, payload, 0600}},
	} {
		assertRejected(t, zipEntries(t, entries...), testPassword, ErrInvalidArchive)
	}
	for _, mutate := range []func(*tar.Header){
		func(h *tar.Header) { h.Name = "../kakatta.db" },
		func(h *tar.Header) { h.Name = "/kakatta.db" },
		func(h *tar.Header) { h.Name = "\\kakatta.db" },
		func(h *tar.Header) { h.Name = MasterKeyEntry },
		func(h *tar.Header) { h.Typeflag, h.Linkname, h.Size = tar.TypeSymlink, "../outside", 0 },
		func(h *tar.Header) { h.Typeflag, h.Linkname, h.Size = tar.TypeLink, "../outside", 0 },
		func(h *tar.Header) { h.Typeflag, h.Size = tar.TypeChar, 0 },
		func(h *tar.Header) { h.Typeflag, h.Size = tar.TypeBlock, 0 },
		func(h *tar.Header) { h.Typeflag, h.Size = tar.TypeFifo, 0 },
		func(h *tar.Header) { h.Typeflag, h.Size = tar.TypeDir, 0 },
		func(h *tar.Header) { h.PAXRecords = map[string]string{"comment": "unexpected"} },
		func(h *tar.Header) { h.Format = tar.FormatGNU },
	} {
		x, y := wrapTar(t, m, tarBytes(t, mutate, false))
		assertRejected(t, zipEntries(t, archiveEntry{ManifestEntry, x, 0600}, archiveEntry{PayloadEntry, y, 0600}), testPassword, ErrInvalidArchive)
	}
	for _, plain := range [][]byte{tarBytes(t, nil, true), append(tarBytes(t, nil, false), 0)} {
		x, y := wrapTar(t, m, plain)
		assertRejected(t, zipEntries(t, archiveEntry{ManifestEntry, x, 0600}, archiveEntry{PayloadEntry, y, 0600}), testPassword, ErrInvalidArchive)
	}
}

func TestArchiveRejectsWrongMasterKeyLength(t *testing.T) {
	m := testManifest(t)
	for _, length := range []int{0, MasterKeyBytes - 1, MasterKeyBytes + 1} {
		var plain bytes.Buffer
		w := tar.NewWriter(&plain)
		for _, entry := range []archiveEntry{{DatabaseEntry, []byte("db"), 0600}, {MasterKeyEntry, make([]byte, length), 0600}} {
			if err := w.WriteHeader(tarHeader(entry.name, int64(len(entry.body)))); err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write(entry.body); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		raw, payload := wrapTar(t, m, plain.Bytes())
		assertRejected(t, zipEntries(t, archiveEntry{ManifestEntry, raw, 0600}, archiveEntry{PayloadEntry, payload, 0600}), testPassword, ErrInvalidArchive)
	}
	plain := tarBytes(t, nil, false)
	keyStart := bytes.Index(plain, bytes.Repeat([]byte{0x5a}, MasterKeyBytes))
	if keyStart < 0 {
		t.Fatal("测试归档缺少主密钥正文")
	}
	raw, payload := wrapTar(t, m, plain[:keyStart+MasterKeyBytes-1])
	assertRejected(t, zipEntries(t, archiveEntry{ManifestEntry, raw, 0600}, archiveEntry{PayloadEntry, payload, 0600}), testPassword, ErrInvalidArchive)
}

func TestArchiveRefusesExistingDestinationAndUnsafeSource(t *testing.T) {
	m := testManifest(t)
	raw, payload := wrapTar(t, m, tarBytes(t, nil, false))
	archive := zipEntries(t, archiveEntry{ManifestEntry, raw, 0600}, archiveEntry{PayloadEntry, payload, 0600})
	dir := t.TempDir()
	marker := filepath.Join(dir, "keep")
	if err := os.WriteFile(marker, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	_, key, err := ReadArchive(t.Context(), bytes.NewReader(archive), int64(len(archive)), testPassword, dir, 4)
	if !errors.Is(err, ErrIO) || key != [MasterKeyBytes]byte{} {
		t.Fatal("复用已存在候选目录没有拒绝", err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "keep" {
		t.Fatal("删除了调用者已有目录")
	}
	source := filepath.Join(t.TempDir(), "db")
	if err := os.WriteFile(source, []byte("db"), 0600); err != nil {
		t.Fatal(err)
	}
	link := source + "-link"
	if err := os.Symlink(source, link); err != nil {
		t.Fatal(err)
	}
	if err := WriteArchive(t.Context(), io.Discard, link, [32]byte{}, m, testPassword); !errors.Is(err, ErrInvalidArchive) {
		t.Fatal("符号链接备份源没有拒绝", err)
	}
}

func TestArchiveZIPTailAndCountsAreStrict(t *testing.T) {
	m := testManifest(t)
	raw, payload := wrapTar(t, m, tarBytes(t, nil, false))
	valid := zipEntries(t, archiveEntry{ManifestEntry, raw, 0600}, archiveEntry{PayloadEntry, payload, 0600})
	assertRejected(t, append(bytes.Clone(valid), 0), testPassword, ErrInvalidArchive)
	for trim := 1; trim <= 22; trim++ {
		assertRejected(t, valid[:len(valid)-trim], testPassword, ErrInvalidArchive)
	}
	count := bytes.Clone(valid)
	binary.LittleEndian.PutUint16(count[len(count)-14:], 0xffff)
	binary.LittleEndian.PutUint16(count[len(count)-12:], 0xffff)
	assertRejected(t, count, testPassword, ErrInvalidArchive)
}
