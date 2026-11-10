package images

import _ "embed"

//go:embed assets/sample-photo.jpg
var sampleJPEG []byte

// SampleJPEG 返回虚构商品的本地照片，调用方可以独立使用。
func SampleJPEG() []byte { return append([]byte(nil), sampleJPEG...) }
