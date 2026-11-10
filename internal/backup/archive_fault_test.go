package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type cancelReaderAt struct {
	io.ReaderAt
	cancel context.CancelFunc
	called bool
}

func (r *cancelReaderAt) ReadAt(dst []byte, offset int64) (int, error) {
	n, err := r.ReaderAt.ReadAt(dst, offset)
	if len(dst) >= ChunkSize {
		r.called = true
		r.cancel()
	}
	return n, err
}

type failedReaderAt struct{ err error }

func (r failedReaderAt) ReadAt([]byte, int64) (int, error) { return 0, r.err }

func TestArchiveCancellationAndIOFailures(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.WriteFile(source, bytes.Repeat([]byte{0x6a}, 2*ChunkSize), 0600); err != nil {
		t.Fatal(err)
	}
	m := testManifest(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var out bytes.Buffer
	if err := WriteArchive(ctx, &out, source, [MasterKeyBytes]byte{}, m, testPassword); !errors.Is(err, context.Canceled) || out.Len() != 0 {
		t.Fatal("取消后仍导出", err)
	}
	failure := errors.New(testPassword + source)
	if err := WriteArchive(t.Context(), &failWriter{remaining: 512, err: failure}, source, [MasterKeyBytes]byte{}, m, testPassword); !errors.Is(err, ErrIO) || !errors.Is(err, failure) || strings.Contains(err.Error(), testPassword) || strings.Contains(err.Error(), source) {
		t.Fatal("写失败没有安全保留原因", err)
	}
	if _, key, err := ReadArchive(t.Context(), failedReaderAt{failure}, 256, testPassword, filepath.Join(root, "io-candidate"), 4); !errors.Is(err, ErrIO) || !errors.Is(err, failure) || key != [MasterKeyBytes]byte{} || strings.Contains(err.Error(), testPassword) {
		t.Fatal("读失败没有安全保留原因", err)
	}
	if err := WriteArchive(t.Context(), &out, source, [MasterKeyBytes]byte{}, m, testPassword); err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancel(t.Context())
	defer cancel()
	reader := &cancelReaderAt{ReaderAt: bytes.NewReader(out.Bytes()), cancel: cancel}
	dir := filepath.Join(root, "cancelled")
	if _, key, err := ReadArchive(ctx, reader, int64(out.Len()), testPassword, dir, 4); !errors.Is(err, context.Canceled) || key != [MasterKeyBytes]byte{} || !reader.called {
		t.Fatal("分块中取消没有失败归零", err)
	}
	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("取消后留下明文候选")
	}
	if err := WriteArchive(t.Context(), io.Discard, source, [MasterKeyBytes]byte{}, m, ""); !errors.Is(err, ErrPasswordRequired) {
		t.Fatal("空密码导出没有拒绝", err)
	}
}

func TestArchiveDeflateAndTruncatedAuthenticatedTar(t *testing.T) {
	m := testManifest(t)
	plain := tarBytes(t, nil, false)
	raw, payload := wrapTar(t, m, plain)
	var encoded bytes.Buffer
	w := zip.NewWriter(&encoded)
	for _, entry := range []archiveEntry{{ManifestEntry, raw, 0600}, {PayloadEntry, payload, 0600}} {
		h := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		h.SetMode(0600)
		entryWriter, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entryWriter.Write(entry.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadArchive(t.Context(), bytes.NewReader(encoded.Bytes()), int64(encoded.Len()), testPassword, filepath.Join(t.TempDir(), "candidate"), 4); err != nil {
		t.Fatal("合法Deflate归档未接受", err)
	}
	for _, invalid := range [][]byte{plain[:1536+MasterKeyBytes-1], plain[:1536-1], append(bytes.Clone(plain), 0)} {
		x, y := wrapTar(t, m, invalid)
		assertRejected(t, zipEntries(t, archiveEntry{ManifestEntry, x, 0600}, archiveEntry{PayloadEntry, y, 0600}), testPassword, ErrInvalidArchive)
	}
	for _, invalid := range [][]byte{payload[:len(payload)-1], append(bytes.Clone(payload), 0)} {
		assertRejected(t, zipEntries(t, archiveEntry{ManifestEntry, raw, 0600}, archiveEntry{PayloadEntry, invalid, 0600}), testPassword, ErrInvalidArchive)
	}
}
