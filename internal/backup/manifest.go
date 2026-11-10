package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

var (
	ErrInvalidArchive     = errors.New("backup.invalid_archive")
	ErrAuthentication     = errors.New("backup.authentication_failed")
	ErrPasswordRequired   = errors.New("backup.password_required")
	ErrIO                 = errors.New("backup.io_failure")
	ErrUnsupportedVersion = errors.New("backup.unsupported_version")
)

type fileError struct{ cause error }

func (e *fileError) Error() string   { return ErrIO.Error() }
func (e *fileError) Unwrap() []error { return []error{ErrIO, e.cause} }

func fileFailure(err error) error {
	for _, known := range []error{context.Canceled, context.DeadlineExceeded, ErrAuthentication, ErrInvalidArchive} {
		if errors.Is(err, known) {
			return known
		}
	}
	return &fileError{cause: err}
}

func NewManifest(appVersion string, schemaVersion int, createdAt time.Time) (Manifest, error) {
	var salt [SaltBytes]byte
	_, _ = rand.Read(salt[:])
	m := Manifest{Format: Format, Version: FormatVersion, AppVersion: appVersion, SchemaVersion: schemaVersion,
		CreatedAt: createdAt.UTC().Format(time.RFC3339Nano), KDF: KDF{Name: KDFName, Salt: base64.StdEncoding.EncodeToString(salt[:]), Time: KDFTime, MemoryKiB: KDFMemoryKiB, Threads: KDFThreads}}
	if _, err := validateManifest(m, schemaVersion); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

func validateManifest(m Manifest, maxSchema int) ([]byte, error) {
	if m.Format != Format || m.Version != FormatVersion || m.SchemaVersion < 1 || maxSchema < 1 || m.SchemaVersion > maxSchema {
		return nil, ErrUnsupportedVersion
	}
	if m.AppVersion == "" || m.KDF.Name != KDFName || m.KDF.Time != KDFTime || m.KDF.MemoryKiB != KDFMemoryKiB || m.KDF.Threads != KDFThreads {
		return nil, ErrInvalidArchive
	}
	if _, err := time.Parse(time.RFC3339Nano, m.CreatedAt); err != nil {
		return nil, ErrInvalidArchive
	}
	salt, err := base64.StdEncoding.Strict().DecodeString(m.KDF.Salt)
	if err != nil || len(salt) != SaltBytes || base64.StdEncoding.EncodeToString(salt) != m.KDF.Salt {
		return nil, ErrInvalidArchive
	}
	return salt, nil
}

func readManifest(raw []byte, maxSchema int) (Manifest, []byte, error) {
	if len(raw) == 0 || len(raw) > ManifestMaxBytes || !utf8.Valid(raw) {
		return Manifest{}, nil, ErrInvalidArchive
	}
	fields, err := exactObject(raw, "format", "version", "app_version", "schema_version", "created_at", "kdf")
	if err != nil {
		return Manifest{}, nil, err
	}
	if _, err := exactObject(fields["kdf"], "name", "salt", "time", "memory_kib", "threads"); err != nil {
		return Manifest{}, nil, err
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Manifest{}, nil, ErrInvalidArchive
	}
	salt, err := validateManifest(m, maxSchema)
	return m, salt, err
}

func exactObject(raw []byte, keys ...string) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	start, err := dec.Token()
	if err != nil || start != json.Delim('{') {
		return nil, ErrInvalidArchive
	}
	fields := make(map[string]json.RawMessage, len(keys))
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return nil, ErrInvalidArchive
		}
		key, ok := token.(string)
		if !ok {
			return nil, ErrInvalidArchive
		}
		allowed := false
		for _, candidate := range keys {
			allowed = allowed || candidate == key
		}
		if _, duplicate := fields[key]; duplicate || !allowed {
			return nil, ErrInvalidArchive
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, ErrInvalidArchive
		}
		fields[key] = value
	}
	end, err := dec.Token()
	if err != nil || end != json.Delim('}') || len(fields) != len(keys) {
		return nil, ErrInvalidArchive
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, ErrInvalidArchive
	}
	return fields, nil
}

var kdfSlot = make(chan struct{}, 1)

func deriveKey(ctx context.Context, password string, salt []byte) ([MasterKeyBytes]byte, error) {
	var result [MasterKeyBytes]byte
	if password == "" {
		return result, ErrPasswordRequired
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	select {
	case kdfSlot <- struct{}{}:
	case <-ctx.Done():
		return result, ctx.Err()
	}
	defer func() { <-kdfSlot }()
	if err := ctx.Err(); err != nil {
		return result, err
	}
	secret := []byte(password)
	defer clear(secret)
	key := argon2.IDKey(secret, salt, KDFTime, KDFMemoryKiB, uint8(KDFThreads), MasterKeyBytes)
	defer clear(key)
	if err := ctx.Err(); err != nil {
		return result, err
	}
	copy(result[:], key)
	return result, nil
}
