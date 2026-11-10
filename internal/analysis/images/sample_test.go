package images

import (
	"bytes"
	"image/jpeg"
	"testing"
)

func TestSampleJPEGIsNormalSizedAndIndependent(t *testing.T) {
	first := SampleJPEG()
	if len(first) < 1024 {
		t.Fatal("样例不能是占位像素")
	}
	decoded, err := jpeg.Decode(bytes.NewReader(first))
	if err != nil {
		t.Fatal(err)
	}
	bounds := decoded.Bounds()
	if max(bounds.Dx(), bounds.Dy()) != 1024 || min(bounds.Dx(), bounds.Dy()) < 512 {
		t.Fatalf("商品样例必须是正常尺寸：%v", bounds)
	}
	if decoded.At(500, 350) == decoded.At(20, 20) {
		t.Fatal("商品示意不能是纯色占位图")
	}
	pristine := append([]byte(nil), first...)
	first[0] ^= 0xff
	if !bytes.Equal(SampleJPEG(), pristine) {
		t.Fatal("样例调用方改动不能影响后续读取")
	}
}
