package backup

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func zip64Ending(t *testing.T, archive []byte) []byte {
	t.Helper()
	end := archive[len(archive)-22:]
	var extension [76]byte
	binary.LittleEndian.PutUint32(extension[:], 0x06064b50)
	binary.LittleEndian.PutUint64(extension[4:], 44)
	binary.LittleEndian.PutUint16(extension[12:], 45)
	binary.LittleEndian.PutUint16(extension[14:], 45)
	binary.LittleEndian.PutUint64(extension[24:], 2)
	binary.LittleEndian.PutUint64(extension[32:], 2)
	binary.LittleEndian.PutUint64(extension[40:], uint64(binary.LittleEndian.Uint32(end[12:])))
	binary.LittleEndian.PutUint64(extension[48:], uint64(binary.LittleEndian.Uint32(end[16:])))
	binary.LittleEndian.PutUint32(extension[56:], 0x07064b50)
	binary.LittleEndian.PutUint64(extension[64:], uint64(len(archive)-22))
	binary.LittleEndian.PutUint32(extension[72:], 1)
	result := append(bytes.Clone(archive[:len(archive)-22]), extension[:]...)
	end = bytes.Clone(end)
	binary.LittleEndian.PutUint16(end[8:], 0xffff)
	binary.LittleEndian.PutUint16(end[10:], 0xffff)
	binary.LittleEndian.PutUint32(end[12:], 0xffffffff)
	binary.LittleEndian.PutUint32(end[16:], 0xffffffff)
	return append(result, end...)
}

func TestArchiveZIP64AndStrictDirectory(t *testing.T) {
	m := testManifest(t)
	raw, payload := wrapTar(t, m, tarBytes(t, nil, false))
	valid := zipEntries(t, archiveEntry{ManifestEntry, raw, 0600}, archiveEntry{PayloadEntry, payload, 0600})
	largeFormat := zip64Ending(t, valid)
	if _, _, err := ReadArchive(t.Context(), bytes.NewReader(largeFormat), int64(len(largeFormat)), testPassword, t.TempDir()+"/candidate", 4); err != nil {
		t.Fatal("合法ZIP64结束记录未接受", err)
	}
	for _, field := range []int{len(largeFormat) - 98 + 4, len(largeFormat) - 98 + 16, len(largeFormat) - 98 + 24, len(largeFormat) - 98 + 32, len(largeFormat) - 98 + 40, len(largeFormat) - 98 + 48, len(largeFormat) - 42 + 4, len(largeFormat) - 42 + 8, len(largeFormat) - 42 + 16} {
		bad := bytes.Clone(largeFormat)
		bad[field] ^= 1
		assertRejected(t, bad, testPassword, ErrInvalidArchive)
	}
	third := zipEntries(t, archiveEntry{ManifestEntry, raw, 0600}, archiveEntry{PayloadEntry, payload, 0600}, archiveEntry{"third", nil, 0600})
	// 声明是两项，实际放三条中央目录；标准库不能先遍历并不断追加。
	binary.LittleEndian.PutUint16(third[len(third)-14:], 2)
	binary.LittleEndian.PutUint16(third[len(third)-12:], 2)
	if _, err := readZIPDirectory(t.Context(), bytes.NewReader(third), int64(len(third))); err == nil {
		t.Fatal("中央目录第三条记录没有预先拒绝")
	}
	directoryAt := int(binary.LittleEndian.Uint32(valid[len(valid)-6:]))
	for _, byteAt := range []int{directoryAt + 8, directoryAt + 9} {
		bad := bytes.Clone(valid)
		bad[byteAt] |= 0x40
		assertRejected(t, bad, testPassword, ErrInvalidArchive)
	}
}
