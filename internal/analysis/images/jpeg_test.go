package images

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"os"
	"sync/atomic"
	"testing"

	"github.com/Seaony/Kakatta/internal/locales"
)

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var source bytes.Buffer
	if err := png.Encode(&source, img); err != nil {
		t.Fatal(err)
	}
	return source.Bytes()
}

func decodeJPEG(t *testing.T, source []byte) image.Image {
	t.Helper()
	img, err := jpeg.Decode(bytes.NewReader(source))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func expectImageError(t *testing.T, err error, code, field string) {
	t.Helper()
	var message interface{ Message() locales.Message }
	if !errors.As(err, &message) {
		t.Fatalf("错误应提供安全文案：%v", err)
	}
	reason := message.Message()
	if reason.Code != "analysis.images."+code || field != "" && reason.Args["field"] != field {
		t.Fatalf("错误分类不对：%+v", reason)
	}
	if err.Error() != reason.Code {
		t.Fatalf("错误不应泄露解码原文：%v", err)
	}
}

func TestJPEGSizeAndNoUpscaling(t *testing.T) {
	for _, dimensions := range []struct{ width, height, wantWidth, wantHeight int }{
		{2000, 1000, 1024, 512}, {1000, 2000, 512, 1024}, {3000, 1, 1024, 1}, {1, 3000, 1, 1024}, {10000, 1, 1024, 1}, {1, 10000, 1, 1024}, {17, 9, 17, 9}, {1, 1, 1, 1},
	} {
		source := encodePNG(t, image.NewNRGBA(image.Rect(0, 0, dimensions.width, dimensions.height)))
		result, err := JPEG(source, Limits{})
		if err != nil {
			t.Fatal(err)
		}
		bounds := decodeJPEG(t, result).Bounds()
		if bounds.Dx() != dimensions.wantWidth || bounds.Dy() != dimensions.wantHeight {
			t.Fatalf("%d×%d得到%d×%d，应为%d×%d", dimensions.width, dimensions.height, bounds.Dx(), bounds.Dy(), dimensions.wantWidth, dimensions.wantHeight)
		}
	}
}

func TestJPEGTransparentPixelsUseWhite(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 64, 32))
	draw.Draw(img, image.Rect(32, 0, 64, 32), image.NewUniform(color.NRGBA{R: 255, A: 128}), image.Point{}, draw.Src)
	result, err := JPEG(encodePNG(t, img), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	decoded := decodeJPEG(t, result)
	assertColorNear(t, decoded.At(16, 16), color.NRGBA{255, 255, 255, 255}, 3)
	assertColorNear(t, decoded.At(48, 16), color.NRGBA{255, 127, 127, 255}, 5)
}

func assertColorNear(t *testing.T, got color.Color, want color.NRGBA, tolerance int) {
	t.Helper()
	actual := color.NRGBAModel.Convert(got).(color.NRGBA)
	for _, pair := range [][2]uint8{{actual.R, want.R}, {actual.G, want.G}, {actual.B, want.B}} {
		if abs(int(pair[0])-int(pair[1])) > tolerance {
			t.Fatalf("颜色%v与期望%v相差超过%d", actual, want, tolerance)
		}
	}
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func TestJPEGUsesQuality80(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 32, 24))
	for y := 0; y < 24; y++ {
		for x := 0; x < 32; x++ {
			img.SetNRGBA(x, y, color.NRGBA{uint8(x * 7), uint8(y * 9), uint8((x + y) * 3), 255})
		}
	}
	result, err := JPEG(encodePNG(t, img), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	var expected, other bytes.Buffer
	if err := jpeg.Encode(&expected, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(&other, img, &jpeg.Options{Quality: 75}); err != nil {
		t.Fatal(err)
	}
	gotTable := jpegQuantizationTables(t, result)
	if !bytes.Equal(gotTable, jpegQuantizationTables(t, expected.Bytes())) || bytes.Equal(gotTable, jpegQuantizationTables(t, other.Bytes())) {
		t.Fatal("JPEG量化表必须对应质量80")
	}
}

func jpegQuantizationTables(t *testing.T, source []byte) []byte {
	t.Helper()
	var tables []byte
	for offset := 2; offset+4 <= len(source); {
		if source[offset] != 0xff {
			t.Fatal("非法JPEG段")
		}
		marker := source[offset+1]
		if marker == 0xda || marker == 0xd9 {
			break
		}
		size := int(binary.BigEndian.Uint16(source[offset+2 : offset+4]))
		if size < 2 || offset+2+size > len(source) {
			t.Fatal("JPEG段长度错误")
		}
		if marker == 0xdb {
			tables = append(tables, source[offset+4:offset+2+size]...)
		}
		offset += 2 + size
	}
	if len(tables) == 0 {
		t.Fatal("没有JPEG量化表")
	}
	return tables
}

func withOrientation(t *testing.T, source []byte, orientation uint16) []byte {
	t.Helper()
	payload := append([]byte("Exif\x00\x00"), []byte{'I', 'I', 42, 0, 8, 0, 0, 0, 1, 0, 0x12, 0x01, 3, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}...)
	binary.LittleEndian.PutUint16(payload[24:26], orientation)
	var output bytes.Buffer
	output.Write(source[:2])
	output.Write([]byte{0xff, 0xe1})
	if err := binary.Write(&output, binary.BigEndian, uint16(len(payload)+2)); err != nil {
		t.Fatal(err)
	}
	output.Write(payload)
	output.Write(source[2:])
	return output.Bytes()
}

func TestJPEGCorrectsAllEXIFDirections(t *testing.T) {
	colors := []color.NRGBA{{230, 20, 20, 255}, {20, 190, 30, 255}, {20, 40, 230, 255}, {240, 220, 20, 255}}
	img := image.NewNRGBA(image.Rect(0, 0, 80, 48))
	for index, rectangle := range []image.Rectangle{image.Rect(0, 0, 40, 24), image.Rect(40, 0, 80, 24), image.Rect(0, 24, 40, 48), image.Rect(40, 24, 80, 48)} {
		draw.Draw(img, rectangle, image.NewUniform(colors[index]), image.Point{}, draw.Src)
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, img, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	for index, positions := range [][4]int{{0, 1, 2, 3}, {1, 0, 3, 2}, {3, 2, 1, 0}, {2, 3, 0, 1}, {0, 2, 1, 3}, {2, 0, 3, 1}, {3, 1, 2, 0}, {1, 3, 0, 2}} {
		orientation := uint16(index + 1)
		t.Run(string(rune('0'+orientation)), func(t *testing.T) {
			result, err := JPEG(withOrientation(t, encoded.Bytes(), orientation), Limits{})
			if err != nil {
				t.Fatal(err)
			}
			decoded := decodeJPEG(t, result)
			width, height := 80, 48
			if orientation >= 5 {
				width, height = 48, 80
			}
			if decoded.Bounds().Dx() != width || decoded.Bounds().Dy() != height {
				t.Fatalf("方向%d尺寸不对：%v", orientation, decoded.Bounds())
			}
			for i, point := range []image.Point{{width / 4, height / 4}, {3 * width / 4, height / 4}, {width / 4, 3 * height / 4}, {3 * width / 4, 3 * height / 4}} {
				assertColorNear(t, decoded.At(point.X, point.Y), colors[positions[i]], 8)
			}
		})
	}
}

func TestJPEGDecodesKnownFormatsAndRejectsUnknown(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 16, 12))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.NRGBA{25, 70, 210, 255}), image.Point{}, draw.Src)
	var jpegSource, gifSource bytes.Buffer
	if err := jpeg.Encode(&jpegSource, img, nil); err != nil {
		t.Fatal(err)
	}
	if err := gif.Encode(&gifSource, img, nil); err != nil {
		t.Fatal(err)
	}
	webpSource, err := os.ReadFile("testdata/small.webp")
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range [][]byte{encodePNG(t, img), jpegSource.Bytes(), gifSource.Bytes(), webpSource} {
		result, err := JPEG(source, Limits{})
		if err != nil {
			t.Fatal(err)
		}
		if bounds := decodeJPEG(t, result).Bounds(); bounds.Dx() != 16 || bounds.Dy() != 12 {
			t.Fatalf("格式转换尺寸错误：%v", bounds)
		}
	}
	for _, source := range [][]byte{nil, []byte("not an image https://secret.invalid/key=private"), {0xff, 0xd8, 0xff, 0x00}} {
		_, err := JPEG(source, Limits{})
		expectImageError(t, err, "invalid_image", "")
	}
}

var boundaryDecodeCalls atomic.Int64
var boundaryConfigCalls atomic.Int64

const boundaryMagic = "KAKATTA-IMAGE-BOUNDARY"

func init() {
	image.RegisterFormat("kakatta-boundary", boundaryMagic, func(io.Reader) (image.Image, error) {
		boundaryDecodeCalls.Add(1)
		return image.NewRGBA(image.Rect(0, 0, 2, 2)), nil
	}, func(reader io.Reader) (image.Config, error) {
		boundaryConfigCalls.Add(1)
		data := make([]byte, len(boundaryMagic)+16)
		if _, err := io.ReadFull(reader, data); err != nil {
			return image.Config{}, err
		}
		width := int(binary.BigEndian.Uint64(data[len(boundaryMagic):]))
		height := int(binary.BigEndian.Uint64(data[len(boundaryMagic)+8:]))
		return image.Config{ColorModel: color.RGBAModel, Width: width, Height: height}, nil
	})
}

func boundarySource(width, height int) []byte {
	data := make([]byte, len(boundaryMagic)+16)
	copy(data, boundaryMagic)
	binary.BigEndian.PutUint64(data[len(boundaryMagic):], uint64(width))
	binary.BigEndian.PutUint64(data[len(boundaryMagic)+8:], uint64(height))
	return data
}

func TestJPEGChecksBoundsBeforeFullDecode(t *testing.T) {
	for _, dimensions := range [][2]int{{8001, 5000}, {math.MaxInt, math.MaxInt}, {0, 1}, {1, 0}, {-1, 1}} {
		boundaryDecodeCalls.Store(0)
		_, err := JPEG(boundarySource(dimensions[0], dimensions[1]), Limits{})
		code := "resource_limit"
		field := "source_pixels"
		if dimensions[0] <= 0 || dimensions[1] <= 0 {
			code, field = "invalid_image", ""
		}
		expectImageError(t, err, code, field)
		if boundaryDecodeCalls.Load() != 0 {
			t.Fatal("不合法尺寸不能进入完整解码")
		}
	}
	boundaryDecodeCalls.Store(0)
	boundaryConfigCalls.Store(0)
	source := append(boundarySource(2, 2), make([]byte, SourceBytes+1)...)
	_, err := JPEG(source, Limits{})
	expectImageError(t, err, "resource_limit", "source_bytes")
	if boundaryDecodeCalls.Load() != 0 || boundaryConfigCalls.Load() != 0 {
		t.Fatal("超过字节上限不能进入DecodeConfig或Decode")
	}
}

func TestJPEGLimitsOnlyBecomeStricter(t *testing.T) {
	source := encodePNG(t, image.NewNRGBA(image.Rect(0, 0, 2, 3)))
	if _, err := JPEG(source, Limits{SourceBytes: int64(len(source)), SourcePixels: 6}); err != nil {
		t.Fatal(err)
	}
	_, err := JPEG(source, Limits{SourceBytes: int64(len(source)) - 1})
	expectImageError(t, err, "resource_limit", "source_bytes")
	_, err = JPEG(source, Limits{SourcePixels: 5})
	expectImageError(t, err, "resource_limit", "source_pixels")
	for _, limits := range []Limits{{SourceBytes: SourceBytes + 1}, {SourcePixels: SourcePixels + 1}, {SourceBytes: -1}, {SourcePixels: -1}} {
		_, err := JPEG(source, limits)
		expectImageError(t, err, "invalid_configuration", "")
	}
}

func TestJPEGErrorMessageReturnsCopy(t *testing.T) {
	_, err := JPEG([]byte("x"), Limits{SourceBytes: 1, SourcePixels: 1})
	var message interface{ Message() locales.Message }
	if !errors.As(err, &message) {
		t.Fatal(err)
	}
	first := message.Message()
	first.Args["secret"] = "should not persist"
	if _, exists := message.Message().Args["secret"]; exists {
		t.Fatal("调用方不能改掉后续错误原因")
	}
}

func TestJPEGResizePlanBoundsLargeWeights(t *testing.T) {
	for _, size := range [][2]int{{1, int(SourcePixels)}, {int(SourcePixels), 1}, {5000, 8000}} {
		steps := jpegResizePlan(size[0], size[1])
		if len(steps) != 2 || steps[0].edge != 4096 || steps[0].filter.Support != 0 || steps[1].edge != 1024 || steps[1].filter.Support != 3 {
			t.Fatalf("大图必须先走无权重预分配的缩小路线：%+v", steps)
		}
	}
	if steps := jpegResizePlan(4096, 1); len(steps) != 1 || steps[0].edge != 1024 {
		t.Fatalf("有界尺寸直接缩至1024：%+v", steps)
	}
	if steps := jpegResizePlan(1000, 900); len(steps) != 0 {
		t.Fatal("小图不能放大或重采样")
	}
}
