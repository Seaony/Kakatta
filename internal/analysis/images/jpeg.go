package images

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"

	"github.com/disintegration/imaging"
	_ "golang.org/x/image/webp"

	"github.com/Seaony/Kakatta/internal/locales"
)

type jpegError struct{ reason locales.Message }

func (e *jpegError) Error() string { return e.reason.Code }
func (e *jpegError) Message() locales.Message {
	args := make(map[string]any, len(e.reason.Args))
	for key, value := range e.reason.Args {
		args[key] = value
	}
	return locales.Message{Code: e.reason.Code, Args: args}
}

func invalidImage() error {
	return &jpegError{reason: locales.Message{Code: "analysis.images.invalid_image", Args: map[string]any{}}}
}

func resourceLimit(field string) error {
	return &jpegError{reason: locales.Message{Code: "analysis.images.resource_limit", Args: map[string]any{"field": field}}}
}

// JPEG 在完整解码前限制原图大小，再按图片方向缩小并铺白底。
func JPEG(source []byte, limits Limits) ([]byte, error) {
	limits, err := normalizeLimits(limits)
	if err != nil {
		return nil, &jpegError{reason: locales.Message{Code: "analysis.images.invalid_configuration", Args: map[string]any{}}}
	}
	if int64(len(source)) > limits.SourceBytes {
		return nil, resourceLimit("source_bytes")
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(source))
	if err != nil {
		return nil, invalidImage()
	}
	if err := checkDimensions(config.Width, config.Height, limits.SourcePixels); err != nil {
		return nil, err
	}
	decoded, err := imaging.Decode(bytes.NewReader(source), imaging.AutoOrientation(true))
	if err != nil {
		return nil, invalidImage()
	}
	bounds := decoded.Bounds()
	if err := checkDimensions(bounds.Dx(), bounds.Dy(), limits.SourcePixels); err != nil {
		return nil, err
	}
	for _, step := range jpegResizePlan(bounds.Dx(), bounds.Dy()) {
		if bounds.Dx() >= bounds.Dy() {
			decoded = imaging.Resize(decoded, step.edge, 0, step.filter)
		} else {
			decoded = imaging.Resize(decoded, 0, step.edge, step.filter)
		}
		bounds = decoded.Bounds()
	}
	canvas := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(canvas, canvas.Bounds(), decoded, bounds.Min, draw.Over)
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, canvas, &jpeg.Options{Quality: JPEGQuality}); err != nil {
		return nil, invalidImage()
	}
	return encoded.Bytes(), nil
}

func checkDimensions(width, height int, pixels int64) error {
	if width <= 0 || height <= 0 {
		return invalidImage()
	}
	if int64(width) > pixels/int64(height) {
		return resourceLimit("source_pixels")
	}
	return nil
}

type jpegResizeStep struct {
	edge   int
	filter imaging.ResampleFilter
}

func jpegResizePlan(width, height int) []jpegResizeStep {
	var steps []jpegResizeStep
	// 极细长图先缩到有界尺寸，避免按原长边预分配大量滤镜权重。
	if max(width, height) > 4*MaxEdge {
		steps = append(steps, jpegResizeStep{4 * MaxEdge, imaging.NearestNeighbor})
	}
	if max(width, height) > MaxEdge {
		steps = append(steps, jpegResizeStep{MaxEdge, imaging.Lanczos})
	}
	return steps
}
