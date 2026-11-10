package backup

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
)

type zipDirectory struct{ offset, size int64 }

func readAt(ctx context.Context, src io.ReaderAt, dst []byte, offset int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	n, err := src.ReadAt(dst, offset)
	if cancelled := ctx.Err(); cancelled != nil {
		return cancelled
	}
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return fileFailure(err)
	}
	if n != len(dst) || err != nil {
		return ErrInvalidArchive
	}
	return nil
}

func readZIPDirectory(ctx context.Context, src io.ReaderAt, size int64) (zipDirectory, error) {
	if size < 22 {
		return zipDirectory{}, ErrInvalidArchive
	}
	tail := make([]byte, min(size, int64(22+math.MaxUint16)))
	if err := readAt(ctx, src, tail, size-int64(len(tail))); err != nil {
		return zipDirectory{}, err
	}
	end := -1
	for i := len(tail) - 22; i >= 0; i-- {
		if binary.LittleEndian.Uint32(tail[i:]) == 0x06054b50 && i+22+int(binary.LittleEndian.Uint16(tail[i+20:])) == len(tail) {
			end = i
			break
		}
	}
	if end < 0 {
		return zipDirectory{}, ErrInvalidArchive
	}
	header := tail[end:]
	if binary.LittleEndian.Uint16(header[4:]) != 0 || binary.LittleEndian.Uint16(header[6:]) != 0 {
		return zipDirectory{}, ErrInvalidArchive
	}
	count := uint64(binary.LittleEndian.Uint16(header[10:]))
	if uint64(binary.LittleEndian.Uint16(header[8:])) != count {
		return zipDirectory{}, ErrInvalidArchive
	}
	directorySize := uint64(binary.LittleEndian.Uint32(header[12:]))
	offset := uint64(binary.LittleEndian.Uint32(header[16:]))
	endAt := size - int64(len(tail)) + int64(end)
	var locator [20]byte
	hasZIP64 := false
	if endAt >= int64(len(locator)) {
		if err := readAt(ctx, src, locator[:], endAt-int64(len(locator))); err != nil {
			return zipDirectory{}, err
		}
		hasZIP64 = binary.LittleEndian.Uint32(locator[:]) == 0x07064b50
	}
	if hasZIP64 {
		if binary.LittleEndian.Uint32(locator[4:]) != 0 || binary.LittleEndian.Uint32(locator[16:]) != 1 {
			return zipDirectory{}, ErrInvalidArchive
		}
		zip64At := binary.LittleEndian.Uint64(locator[8:])
		if endAt < 76 || zip64At != uint64(endAt-76) {
			return zipDirectory{}, ErrInvalidArchive
		}
		var extended [56]byte
		if err := readAt(ctx, src, extended[:], int64(zip64At)); err != nil {
			return zipDirectory{}, err
		}
		if binary.LittleEndian.Uint32(extended[:]) != 0x06064b50 || binary.LittleEndian.Uint64(extended[4:]) != 44 || binary.LittleEndian.Uint32(extended[16:]) != 0 || binary.LittleEndian.Uint32(extended[20:]) != 0 || binary.LittleEndian.Uint64(extended[24:]) != 2 || binary.LittleEndian.Uint64(extended[32:]) != 2 {
			return zipDirectory{}, ErrInvalidArchive
		}
		size64, offset64 := binary.LittleEndian.Uint64(extended[40:]), binary.LittleEndian.Uint64(extended[48:])
		if count != 2 && count != math.MaxUint16 || directorySize != math.MaxUint32 && directorySize != size64 || offset != math.MaxUint32 && offset != offset64 {
			return zipDirectory{}, ErrInvalidArchive
		}
		count, directorySize, offset, endAt = 2, size64, offset64, int64(zip64At)
	}
	// 在标准库按目录计数分配内存前，先限定成两条真实记录。
	if count != 2 || directorySize < 92 || directorySize > uint64(endAt) || offset != uint64(endAt)-directorySize {
		return zipDirectory{}, ErrInvalidArchive
	}
	directory := zipDirectory{offset: int64(offset), size: int64(directorySize)}
	if err := checkZIPDirectoryRecords(ctx, src, directory); err != nil {
		return zipDirectory{}, err
	}
	return directory, nil
}

func checkZIPDirectoryRecords(ctx context.Context, src io.ReaderAt, directory zipDirectory) error {
	position, end := directory.offset, directory.offset+directory.size
	for range 2 {
		var header [46]byte
		if position > end-int64(len(header)) {
			return ErrInvalidArchive
		}
		if err := readAt(ctx, src, header[:], position); err != nil {
			return err
		}
		if binary.LittleEndian.Uint32(header[:]) != 0x02014b50 {
			return ErrInvalidArchive
		}
		position += 46 + int64(binary.LittleEndian.Uint16(header[28:])) + int64(binary.LittleEndian.Uint16(header[30:])) + int64(binary.LittleEndian.Uint16(header[32:]))
		if position > end {
			return ErrInvalidArchive
		}
	}
	if position != end {
		return ErrInvalidArchive
	}
	return nil
}
