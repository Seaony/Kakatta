package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/systeminfo"
	"github.com/chromedp/chromedp"
)

func TestWebGLModes(t *testing.T) {
	for _, mode := range []string{"", "off"} {
		flags, env, err := webGLOptions(mode)
		if err != nil || len(flags) != 0 || len(env) != 0 {
			t.Fatalf("默认模式改动了浏览器参数：%s %v", mode, err)
		}
	}
	if _, _, err := webGLOptions("swiftshader"); err == nil {
		t.Fatal("不允许选择未约定的后端")
	}
}

// 只在镜像的断网测试里检查真实渲染器，不在普通单元测试里启动浏览器。
func TestWebGLExperimentRenderers(t *testing.T) {
	if os.Getenv("KAKATTA_WEBGL_TESTS") != "1" {
		t.Skip("设置 KAKATTA_WEBGL_TESTS=1 才在断网容器里测试渲染器")
	}
	if runtime.GOOS != "linux" {
		t.Fatal("渲染器测试需要 Linux 容器")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><meta charset="utf-8"><title>本地渲染测试</title><canvas id="test"></canvas>`)
	}))
	defer srv.Close()
	for index, mode := range []string{"off", "llvmpipe"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			s, err := Start(ctx, Options{AccountID: 430 + index, ProfileDir: t.TempDir(), WebGL: mode})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if err := s.Navigate(srv.URL, 10*time.Second); err != nil {
				t.Fatal(err)
			}
			result, err := chromedp.Run(s.Context(), chromedp.Evaluate[map[string]any](`(() => {
const gl = document.getElementById('test').getContext('webgl');
if (!gl) return {available:false};
const debug = gl.getExtension('WEBGL_debug_renderer_info');
gl.clearColor(1,0,0,1); gl.clear(gl.COLOR_BUFFER_BIT);
const pixel = new Uint8Array(4); gl.readPixels(0,0,1,1,gl.RGBA,gl.UNSIGNED_BYTE,pixel);
return {available:true,vendor:debug?gl.getParameter(debug.UNMASKED_VENDOR_WEBGL):'',renderer:debug?gl.getParameter(debug.UNMASKED_RENDERER_WEBGL):'',pixel:Array.from(pixel).join(',')};
})()`))
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("本地页面 WebGL：%+v", result)
			if mode == "off" && result["available"] != false {
				t.Fatalf("默认无显卡环境却启用了 WebGL：%+v", result)
			}
			if mode == "llvmpipe" && (!strings.Contains(strings.ToLower(fmt.Sprint(result["renderer"])), "llvmpipe") || result["available"] != true || strings.Contains(strings.ToLower(fmt.Sprint(result["renderer"])), "swiftshader")) {
				t.Fatalf("没有用 Mesa llvmpipe：%+v", result)
			}
			if mode == "llvmpipe" && result["pixel"] != "255,0,0,255" {
				t.Fatal("真实 WebGL 绘制和像素读取失败")
			}
			info, err := chromedp.CallBrowser(s.Context(), systeminfo.GetInfo, cdp.Empty{})
			if err != nil {
				t.Fatal(err)
			}
			var aux struct {
				Renderer string `json:"glRenderer"`
			}
			var feature map[string]string
			if err := json.Unmarshal(info.GPU.AuxAttributes, &aux); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(info.GPU.FeatureStatus, &feature); err != nil {
				t.Fatal(err)
			}
			t.Logf("浏览器 GPU：renderer=%s，WebGL=%s", aux.Renderer, feature["webgl"])
			if err := s.Navigate("chrome://gpu", 10*time.Second); err != nil {
				t.Fatal(err)
			}
			gpuText, err := chromedp.Run(s.Context(), chromedp.Evaluate[string](`document.querySelector('info-view').shadowRoot.textContent`))
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("chrome://gpu 已读取，包含 llvmpipe=%t", strings.Contains(strings.ToLower(gpuText), "llvmpipe"))
			if mode == "llvmpipe" && !strings.Contains(strings.ToLower(gpuText), "llvmpipe") {
				t.Fatal("chrome://gpu 没显示 llvmpipe")
			}
		})
	}
}
