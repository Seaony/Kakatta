package backup

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"io"
)

func backupAEAD(key [MasterKeyBytes]byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, ErrInvalidArchive
	}
	return cipher.NewGCMWithRandomNonce(block)
}

func frameAAD(hash [32]byte, index uint64, length uint32, flags uint8) []byte {
	aad := make([]byte, len(AADDomain)+4+32+8+4+1)
	copy(aad, AADDomain)
	offset := len(AADDomain)
	binary.BigEndian.PutUint32(aad[offset:], FormatVersion)
	offset += 4
	copy(aad[offset:], hash[:])
	offset += 32
	binary.BigEndian.PutUint64(aad[offset:], index)
	binary.BigEndian.PutUint32(aad[offset+8:], length)
	aad[len(aad)-1] = flags
	return aad
}

type chunkWriter struct {
	ctx    context.Context
	dst    io.Writer
	aead   cipher.AEAD
	hash   [32]byte
	index  uint64
	plain  []byte
	sealed []byte
	closed bool
	err    error
}

func newChunkWriter(ctx context.Context, dst io.Writer, aead cipher.AEAD, hash [32]byte) (*chunkWriter, error) {
	var header [len(PayloadMagic) + 4]byte
	copy(header[:], PayloadMagic)
	binary.BigEndian.PutUint32(header[len(PayloadMagic):], FormatVersion)
	if err := writeFull(ctx, dst, header[:]); err != nil {
		return nil, err
	}
	return &chunkWriter{ctx: ctx, dst: dst, aead: aead, hash: hash, plain: make([]byte, 0, ChunkSize), sealed: make([]byte, 0, ChunkSize+NonceBytes+TagBytes)}, nil
}

func (w *chunkWriter) Write(data []byte) (int, error) {
	if w.closed || w.err != nil {
		if w.err != nil {
			return 0, w.err
		}
		return 0, ErrInvalidArchive
	}
	used := 0
	for len(data) > 0 {
		if err := w.ctx.Err(); err != nil {
			w.err = err
			return used, err
		}
		if len(w.plain) == ChunkSize {
			if w.err = w.flush(0); w.err != nil {
				return used, w.err
			}
		}
		n := min(ChunkSize-len(w.plain), len(data))
		w.plain = append(w.plain, data[:n]...)
		data, used = data[n:], used+n
	}
	return used, nil
}

func (w *chunkWriter) flush(flags uint8) error {
	if w.index >= MaxChunkCount {
		return ErrInvalidArchive
	}
	var header [5]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(w.plain)))
	header[4] = flags
	w.sealed = w.aead.Seal(w.sealed[:0], nil, w.plain, frameAAD(w.hash, w.index, uint32(len(w.plain)), flags))
	if err := writeFull(w.ctx, w.dst, header[:]); err != nil {
		return err
	}
	if err := writeFull(w.ctx, w.dst, w.sealed); err != nil {
		return err
	}
	clear(w.plain)
	w.plain = w.plain[:0]
	w.index++
	return nil
}

func (w *chunkWriter) Close() error {
	if w.closed {
		return w.err
	}
	w.closed = true
	if w.err == nil {
		w.err = w.flush(FrameFinal)
	}
	clear(w.plain)
	clear(w.sealed)
	return w.err
}

type chunkReader struct {
	ctx    context.Context
	src    io.Reader
	aead   cipher.AEAD
	hash   [32]byte
	index  uint64
	sealed []byte
	plain  []byte
	read   int
	final  bool
	done   bool
	err    error
}

func newChunkReader(ctx context.Context, src io.Reader, aead cipher.AEAD, hash [32]byte) (*chunkReader, error) {
	var header [len(PayloadMagic) + 4]byte
	if _, err := io.ReadFull(contextReader{ctx, src}, header[:]); err != nil {
		return nil, archiveReadFailure(err)
	}
	if string(header[:len(PayloadMagic)]) != PayloadMagic || binary.BigEndian.Uint32(header[len(PayloadMagic):]) != FormatVersion {
		return nil, ErrInvalidArchive
	}
	return &chunkReader{ctx: ctx, src: src, aead: aead, hash: hash, sealed: make([]byte, ChunkSize+NonceBytes+TagBytes), plain: make([]byte, 0, ChunkSize)}, nil
}

func (r *chunkReader) Read(dst []byte) (int, error) {
	if len(dst) == 0 {
		return 0, nil
	}
	if r.err != nil {
		return 0, r.err
	}
	if err := r.ctx.Err(); err != nil {
		r.err = err
		return 0, err
	}
	if r.read == len(r.plain) {
		if r.final || r.done {
			if !r.done {
				var tail [1]byte
				_, err := io.ReadFull(contextReader{r.ctx, r.src}, tail[:])
				if err != io.EOF {
					r.err = archiveReadFailure(err)
					if err == nil {
						r.err = ErrInvalidArchive
					}
					return 0, r.err
				}
				r.done = true
				clear(r.plain)
			}
			return 0, io.EOF
		}
		if r.err = r.next(); r.err != nil {
			return 0, r.err
		}
		if len(r.plain) == 0 {
			return r.Read(dst)
		}
	}
	n := copy(dst, r.plain[r.read:])
	r.read += n
	return n, nil
}

func (r *chunkReader) next() error {
	if r.index >= MaxChunkCount {
		return ErrInvalidArchive
	}
	var header [5]byte
	if _, err := io.ReadFull(contextReader{r.ctx, r.src}, header[:]); err != nil {
		return archiveReadFailure(err)
	}
	length, flags := binary.BigEndian.Uint32(header[:]), header[4]
	if length > ChunkSize || flags&^FrameFinal != 0 || flags == 0 && length != ChunkSize || flags == FrameFinal && length == 0 && r.index != 0 {
		return ErrInvalidArchive
	}
	n := int(length) + NonceBytes + TagBytes
	if _, err := io.ReadFull(contextReader{r.ctx, r.src}, r.sealed[:n]); err != nil {
		return archiveReadFailure(err)
	}
	clear(r.plain)
	plain, err := r.aead.Open(r.plain[:0], nil, r.sealed[:n], frameAAD(r.hash, r.index, length, flags))
	if err != nil {
		return ErrAuthentication
	}
	r.plain, r.read, r.final = plain, 0, flags == FrameFinal
	r.index++
	return nil
}

type contextReader struct {
	ctx context.Context
	src io.Reader
}

func (r contextReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.src.Read(data)
	if cancelled := r.ctx.Err(); cancelled != nil {
		return n, cancelled
	}
	return n, err
}

func writeFull(ctx context.Context, dst io.Writer, data []byte) error {
	for len(data) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := dst.Write(data)
		if err != nil {
			return fileFailure(err)
		}
		if n < 1 || n > len(data) {
			return fileFailure(io.ErrShortWrite)
		}
		data = data[n:]
	}
	return ctx.Err()
}

func archiveReadFailure(err error) error {
	for _, known := range []error{context.Canceled, context.DeadlineExceeded, ErrAuthentication} {
		if errors.Is(err, known) {
			return known
		}
	}
	if errors.Is(err, ErrIO) {
		return fileFailure(err)
	}
	return ErrInvalidArchive
}
