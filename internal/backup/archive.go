package backup

import (
	"archive/tar"
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

func WriteArchive(ctx context.Context, dst io.Writer, databasePath string, masterKey [MasterKeyBytes]byte, manifest Manifest, password string) error {
	defer clear(masterKey[:])
	if password == "" {
		return ErrPasswordRequired
	}
	salt, err := validateManifest(manifest, manifest.SchemaVersion)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(manifest)
	if err != nil || len(raw) > ManifestMaxBytes {
		return ErrInvalidArchive
	}
	source, info, err := openDatabaseSource(databasePath)
	if err != nil {
		return err
	}
	defer source.Close()
	key, err := deriveKey(ctx, password, salt)
	if err != nil {
		return err
	}
	defer clear(key[:])
	aead, err := backupAEAD(key)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(dst)
	manifestWriter, err := createZIPEntry(zw, ManifestEntry)
	if err != nil {
		return fileFailure(err)
	}
	if err := writeFull(ctx, manifestWriter, raw); err != nil {
		return err
	}
	payload, err := createZIPEntry(zw, PayloadEntry)
	if err != nil {
		return fileFailure(err)
	}
	chunks, err := newChunkWriter(ctx, payload, aead, sha256.Sum256(raw))
	if err != nil {
		return err
	}
	defer func() { clear(chunks.plain); clear(chunks.sealed) }()
	tw := tar.NewWriter(chunks)
	if err := tw.WriteHeader(tarHeader(DatabaseEntry, info.Size())); err != nil {
		return err
	}
	if _, err := io.CopyN(tw, contextReader{ctx, source}, info.Size()); err != nil {
		return fileFailure(err)
	}
	var tail [1]byte
	if n, err := source.Read(tail[:]); n != 0 || err != io.EOF {
		return fileFailure(errors.New("备份源在读取期间改变"))
	}
	if err := tw.WriteHeader(tarHeader(MasterKeyEntry, MasterKeyBytes)); err != nil {
		return err
	}
	if err := writeFull(ctx, tw, masterKey[:]); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := chunks.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return fileFailure(err)
	}
	return ctx.Err()
}

func createZIPEntry(w *zip.Writer, name string) (io.Writer, error) {
	header := &zip.FileHeader{Name: name, Method: zip.Store}
	header.SetMode(0600)
	return w.CreateHeader(header)
}

func tarHeader(name string, size int64) *tar.Header {
	return &tar.Header{Name: name, Mode: 0600, Size: size, Typeflag: tar.TypeReg, ModTime: time.Unix(0, 0), Format: tar.FormatPAX}
}

func openDatabaseSource(path string) (*os.File, os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, nil, fileFailure(err)
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 {
		return nil, nil, ErrInvalidArchive
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, fileFailure(err)
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() || opened.Size() != info.Size() {
		file.Close()
		return nil, nil, ErrInvalidArchive
	}
	return file, opened, nil
}

// ReadArchive 验证归档并解出候选数据库，主密钥留在内存。
func ReadArchive(ctx context.Context, src io.ReaderAt, size int64, password, destinationDir string, maxSchemaVersion int) (manifest Manifest, masterKey [MasterKeyBytes]byte, err error) {
	if password == "" {
		return manifest, masterKey, ErrPasswordRequired
	}
	reader := contextReaderAt{ctx, src}
	_, err = readZIPDirectory(ctx, reader, size)
	if err != nil {
		return manifest, masterKey, err
	}
	zr, err := zip.NewReader(reader, size)
	if err != nil || len(zr.File) != 2 {
		return manifest, masterKey, archiveReadFailure(err)
	}
	files := map[string]*zip.File{}
	for _, file := range zr.File {
		if (file.Name != ManifestEntry && file.Name != PayloadEntry) || !file.Mode().IsRegular() || files[file.Name] != nil || (file.Method != zip.Store && file.Method != zip.Deflate) || file.Flags&^uint16(0x80e) != 0 {
			return manifest, masterKey, ErrInvalidArchive
		}
		files[file.Name] = file
	}
	manifestFile, err := files[ManifestEntry].Open()
	if err != nil {
		return manifest, masterKey, archiveReadFailure(err)
	}
	raw, readErr := io.ReadAll(io.LimitReader(contextReader{ctx, manifestFile}, ManifestMaxBytes+1))
	closeErr := manifestFile.Close()
	if readErr != nil || closeErr != nil {
		return manifest, masterKey, archiveReadFailure(errors.Join(readErr, closeErr))
	}
	manifest, salt, err := readManifest(raw, maxSchemaVersion)
	if err != nil {
		return Manifest{}, masterKey, err
	}
	key, err := deriveKey(ctx, password, salt)
	if err != nil {
		return Manifest{}, masterKey, err
	}
	defer clear(key[:])
	aead, err := backupAEAD(key)
	if err != nil {
		return Manifest{}, masterKey, err
	}
	payload, err := files[PayloadEntry].Open()
	if err != nil {
		return Manifest{}, masterKey, archiveReadFailure(err)
	}
	defer payload.Close()
	chunks, err := newChunkReader(ctx, payload, aead, sha256.Sum256(raw))
	if err != nil {
		return Manifest{}, masterKey, err
	}
	defer func() { clear(chunks.plain); clear(chunks.sealed) }()
	if err := ctx.Err(); err != nil {
		return Manifest{}, masterKey, err
	}
	if err := os.Mkdir(destinationDir, 0700); err != nil {
		return Manifest{}, masterKey, fileFailure(err)
	}
	defer func() {
		if err != nil {
			manifest = Manifest{}
			clear(masterKey[:])
			if removeErr := os.RemoveAll(destinationDir); removeErr != nil {
				err = errors.Join(err, fileFailure(removeErr))
			}
		}
	}()
	if err := os.Chmod(destinationDir, 0700); err != nil {
		return manifest, masterKey, fileFailure(err)
	}
	tr := tar.NewReader(chunks)
	header, nextErr := tr.Next()
	if nextErr != nil || !validTarHeader(header, DatabaseEntry) {
		return manifest, masterKey, archiveReadFailure(nextErr)
	}
	file, openErr := os.OpenFile(filepath.Join(destinationDir, DatabaseEntry), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if openErr != nil {
		return manifest, masterKey, fileFailure(openErr)
	}
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return manifest, masterKey, fileFailure(err)
	}
	_, copyErr := io.CopyN(file, contextReader{ctx, tr}, header.Size)
	syncErr := file.Sync()
	closeErr = file.Close()
	if syncErr != nil || closeErr != nil {
		return manifest, masterKey, fileFailure(errors.Join(copyErr, syncErr, closeErr))
	}
	if copyErr != nil {
		if errors.Is(copyErr, io.EOF) || errors.Is(copyErr, io.ErrUnexpectedEOF) {
			return manifest, masterKey, archiveReadFailure(copyErr)
		}
		return manifest, masterKey, fileFailure(copyErr)
	}
	header, nextErr = tr.Next()
	if nextErr != nil || !validTarHeader(header, MasterKeyEntry) {
		return manifest, masterKey, archiveReadFailure(nextErr)
	}
	if _, readErr := io.ReadFull(contextReader{ctx, tr}, masterKey[:]); readErr != nil {
		return manifest, masterKey, archiveReadFailure(readErr)
	}
	if _, nextErr := tr.Next(); nextErr != io.EOF {
		return manifest, masterKey, archiveReadFailure(nextErr)
	}
	var extra [1]byte
	if _, readErr := io.ReadFull(chunks, extra[:]); readErr != io.EOF {
		return manifest, masterKey, archiveReadFailure(readErr)
	}
	if err := payload.Close(); err != nil {
		return manifest, masterKey, archiveReadFailure(err)
	}
	dir, openErr := os.Open(destinationDir)
	if openErr != nil {
		return manifest, masterKey, fileFailure(openErr)
	}
	syncErr = dir.Sync()
	closeErr = dir.Close()
	if syncErr != nil || closeErr != nil {
		return manifest, masterKey, fileFailure(errors.Join(syncErr, closeErr))
	}
	return manifest, masterKey, ctx.Err()
}

func validTarHeader(header *tar.Header, name string) bool {
	if header == nil || header.Name != name || (header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA) || header.Linkname != "" || len(header.Xattrs) != 0 || (header.Format != tar.FormatUSTAR && header.Format != tar.FormatPAX) || header.Size <= 0 {
		return false
	}
	if name == MasterKeyEntry && header.Size != MasterKeyBytes {
		return false
	}
	for key, value := range header.PAXRecords {
		if key != "size" || value != strconv.FormatInt(header.Size, 10) {
			return false
		}
	}
	return true
}

type contextReaderAt struct {
	ctx context.Context
	src io.ReaderAt
}

func (r contextReaderAt) ReadAt(dst []byte, offset int64) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.src.ReadAt(dst, offset)
	if cancelled := r.ctx.Err(); cancelled != nil {
		return n, cancelled
	}
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return n, fileFailure(err)
	}
	return n, err
}
