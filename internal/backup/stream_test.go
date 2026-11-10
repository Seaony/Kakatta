package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"strconv"
	"testing"
)

func streamFixture(t *testing.T, plain []byte) ([]byte, [32]byte, [32]byte) {
	t.Helper()
	key, hash := sha256.Sum256([]byte("local stream key")), sha256.Sum256([]byte("local manifest"))
	aead, err := backupAEAD(key)
	if err != nil {
		t.Fatal(err)
	}
	var dst bytes.Buffer
	w, err := newChunkWriter(t.Context(), &dst, aead, hash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return dst.Bytes(), key, hash
}

func readStream(ctx context.Context, encoded []byte, key, hash [32]byte) ([]byte, error) {
	aead, err := backupAEAD(key)
	if err != nil {
		return nil, err
	}
	r, err := newChunkReader(ctx, bytes.NewReader(encoded), aead, hash)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

func frameRanges(t *testing.T, encoded []byte) [][2]int {
	t.Helper()
	ranges := [][2]int{}
	for start := len(PayloadMagic) + 4; start < len(encoded); {
		end := start + 5 + int(binary.BigEndian.Uint32(encoded[start:])) + NonceBytes + TagBytes
		if end > len(encoded) {
			t.Fatal("测试帧边界错误")
		}
		ranges = append(ranges, [2]int{start, end})
		start = end
	}
	return ranges
}

func TestChunkBoundariesAndFinalMarker(t *testing.T) {
	for _, size := range []int{0, 1, ChunkSize - 1, ChunkSize, ChunkSize + 1, 3*ChunkSize + 17} {
		plain := bytes.Repeat([]byte{0xa5}, size)
		encoded, key, hash := streamFixture(t, plain)
		decoded, err := readStream(t.Context(), encoded, key, hash)
		if err != nil || !bytes.Equal(decoded, plain) {
			t.Fatalf("长度%d往返失败：%v", size, err)
		}
		ranges := frameRanges(t, encoded)
		for i, r := range ranges {
			want := uint8(0)
			if i == len(ranges)-1 {
				want = FrameFinal
			}
			if encoded[r[0]+4] != want {
				t.Fatalf("长度%d第%d块final标志错误", size, i)
			}
		}
	}
}

func TestChunkCorruptionReorderAndTruncation(t *testing.T) {
	encoded, key, hash := streamFixture(t, bytes.Repeat([]byte{0x3c}, 2*ChunkSize+7))
	ranges := frameRanges(t, encoded)
	cases := map[string][]byte{
		"missing_middle": append(append([]byte(nil), encoded[:ranges[1][0]]...), encoded[ranges[1][1]:]...),
		"missing_final":  bytes.Clone(encoded[:ranges[2][0]]),
		"extra_tail":     append(bytes.Clone(encoded), 0),
		"repeat_final":   append(bytes.Clone(encoded), encoded[ranges[2][0]:]...),
	}
	swapped := bytes.Clone(encoded[:ranges[0][0]])
	swapped = append(swapped, encoded[ranges[1][0]:ranges[1][1]]...)
	swapped = append(swapped, encoded[ranges[0][0]:ranges[0][1]]...)
	swapped = append(swapped, encoded[ranges[2][0]:]...)
	cases["swapped"] = swapped
	for _, position := range []int{0, 8, 11, ranges[0][0], ranges[0][0] + 4, ranges[0][0] + 5, ranges[0][0] + 16, ranges[0][1] - 1, len(encoded) - 1} {
		changed := bytes.Clone(encoded)
		changed[position] ^= 0x80
		cases["changed_"+strconv.Itoa(position)] = changed
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := readStream(t.Context(), input, key, hash); err == nil {
				t.Fatal("损坏流被接受")
			}
		})
	}
	for _, r := range ranges {
		for _, cut := range []int{r[0], r[0] + 1, r[0] + 4, r[0] + 5, r[0] + 6, r[0] + 16, r[1] - TagBytes, r[1] - 1} {
			if _, err := readStream(t.Context(), encoded[:cut], key, hash); err == nil {
				t.Fatalf("截断位置%d被接受", cut)
			}
		}
	}
	otherKey, otherHash := key, hash
	otherKey[0] ^= 1
	otherHash[0] ^= 1
	for _, pair := range [][2][32]byte{{otherKey, hash}, {key, otherHash}} {
		if _, err := readStream(t.Context(), encoded, pair[0], pair[1]); !errors.Is(err, ErrAuthentication) {
			t.Fatalf("错误密钥和认证失败没有同一错误：%v", err)
		}
	}
}

func TestChunkRejectsEmptyMiddleUnknownFlagsAndMessageLimit(t *testing.T) {
	encoded, key, hash := streamFixture(t, []byte("hello"))
	for _, changed := range []struct {
		length uint32
		flags  uint8
	}{{0, 0}, {ChunkSize + 1, FrameFinal}, {5, 2}, {5, 0}} {
		input := bytes.Clone(encoded)
		binary.BigEndian.PutUint32(input[len(PayloadMagic)+4:], changed.length)
		input[len(PayloadMagic)+8] = changed.flags
		if _, err := readStream(t.Context(), input, key, hash); err == nil {
			t.Fatal("非法长度或标志被接受")
		}
	}
	aead, _ := backupAEAD(key)
	r, err := newChunkReader(t.Context(), bytes.NewReader(encoded), aead, hash)
	if err != nil {
		t.Fatal(err)
	}
	r.index = MaxChunkCount
	if _, err := io.ReadAll(r); !errors.Is(err, ErrInvalidArchive) {
		t.Fatal("读取超出消息数上限未被拦住")
	}
	var out bytes.Buffer
	w, _ := newChunkWriter(t.Context(), &out, aead, hash)
	w.index = MaxChunkCount
	if err := w.Close(); !errors.Is(err, ErrInvalidArchive) {
		t.Fatal("写入超出消息数上限未被拦住")
	}
}

func TestChunkAuthenticatesFinalAndRejectsEmptyFinalAfterData(t *testing.T) {
	encoded, key, hash := streamFixture(t, bytes.Repeat([]byte{7}, 2*ChunkSize))
	ranges := frameRanges(t, encoded)
	for _, span := range ranges {
		changed := bytes.Clone(encoded)
		changed[span[0]+4] ^= FrameFinal
		if _, err := readStream(t.Context(), changed, key, hash); !errors.Is(err, ErrAuthentication) {
			t.Fatal("最终标志未参与认证", err)
		}
	}
	aead, _ := backupAEAD(key)
	withEmptyFinal := bytes.Clone(encoded[:ranges[1][0]])
	withEmptyFinal = append(withEmptyFinal, 0, 0, 0, 0, FrameFinal)
	withEmptyFinal = append(withEmptyFinal, aead.Seal(nil, nil, nil, frameAAD(hash, 1, 0, FrameFinal))...)
	if _, err := readStream(t.Context(), withEmptyFinal, key, hash); !errors.Is(err, ErrInvalidArchive) {
		t.Fatal("数据后的空最终块未拒绝", err)
	}
}

func TestChunkCancellationAndWriteFailure(t *testing.T) {
	encoded, key, hash := streamFixture(t, []byte("hello"))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := readStream(ctx, encoded, key, hash); !errors.Is(err, context.Canceled) {
		t.Fatalf("读取取消丢失：%v", err)
	}
	aead, _ := backupAEAD(key)
	if _, err := newChunkWriter(ctx, io.Discard, aead, hash); !errors.Is(err, context.Canceled) {
		t.Fatalf("写入取消丢失：%v", err)
	}
	failure := errors.New("本地写入失败")
	w, err := newChunkWriter(t.Context(), &failWriter{remaining: 20, err: failure}, aead, hash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); !errors.Is(err, failure) || !errors.Is(err, ErrIO) {
		t.Fatalf("写入原错误丢失：%v", err)
	}
}

type failWriter struct {
	remaining int
	err       error
}

func (w *failWriter) Write(data []byte) (int, error) {
	if len(data) > w.remaining {
		n := w.remaining
		w.remaining = 0
		return n, w.err
	}
	w.remaining -= len(data)
	return len(data), nil
}
