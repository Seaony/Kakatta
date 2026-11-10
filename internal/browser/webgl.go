package browser

import (
	"fmt"

	"github.com/chromedp/chromedp"
)

// 实验只切换实际渲染后端，不改网站读到的厂商、设备或能力。
func webGLOptions(mode string) ([]chromedp.ExecAllocatorOption, []string, error) {
	switch mode {
	case "", "off":
		return nil, nil, nil
	case "llvmpipe":
		return []chromedp.ExecAllocatorOption{
			chromedp.Flag("use-gl", "angle"),
			chromedp.Flag("use-angle", "gl"),
			chromedp.Flag("ignore-gpu-blocklist", true),
		}, []string{"LIBGL_ALWAYS_SOFTWARE=1", "GALLIUM_DRIVER=llvmpipe"}, nil
	default:
		return nil, nil, fmt.Errorf("WebGL 实验模式只能是 off 或 llvmpipe")
	}
}
