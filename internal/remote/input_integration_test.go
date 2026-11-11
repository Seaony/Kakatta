package remote_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image/png"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/browser"
	"github.com/Seaony/Kakatta/internal/pacer"
	"github.com/Seaony/Kakatta/internal/remote"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/input"
	cdppage "github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/coder/websocket"
)

func TestRFBInputAgainstLocalChrome(t *testing.T) {
	if runtime.GOOS != "linux" || os.Getenv("KAKATTA_REMOTE_INPUT_TEST") != "1" {
		t.Skip("设置 KAKATTA_REMOTE_INPUT_TEST=1，在断网 Linux 容器里运行真实输入测试")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	page := inputPage(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><meta charset="utf-8"><title>本地远程输入测试</title><textarea id="target" aria-label="输入目标" style="position:fixed;inset:0;width:100vw;height:100vh;box-sizing:border-box;font:24px sans-serif"></textarea>`)
	}))
	defer page.Close()
	display, err := browser.StartDisplay(ctx, 460)
	if err != nil {
		t.Fatal(err)
	}
	defer display.Close()
	chrome, err := browser.Start(ctx, browser.Options{ExecPath: inputChromePath(t, false), ProfileDir: t.TempDir(), AccountID: 460, Env: []string{"DISPLAY=" + display.Name()}})
	if err != nil {
		t.Fatal(err)
	}
	defer chrome.Close()
	if err := chrome.Navigate(page.URL, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	keyups := 0
	finishContract := recordInput(t, chrome, "raw-rfb", remoteInputText+"\n", &keyups)
	human := browser.NewHuman(chrome, pacer.New(nil), pacer.Range{})
	target, err := human.FirstVisible(ctx, "#target", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	session, err := remote.Start(ctx, display.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	server := httptest.NewServer(session.Handler(func(*http.Request) bool { return true }))
	defer server.Close()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/vnc", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {server.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	conn := websocket.NetConn(ctx, ws, websocket.MessageBinary)
	defer conn.Close()
	width, height := rfbHandshake(t, conn)
	click := func(mask byte) {
		packet := []byte{5, mask, 0, 0, 0, 0}
		binary.BigEndian.PutUint16(packet[2:4], width/2)
		binary.BigEndian.PutUint16(packet[4:6], height/2)
		rfbWrite(t, conn, packet)
	}
	click(0)
	click(1)
	click(0)
	defer finishContract()
	key := func(keysym uint32, down bool) {
		if !down {
			keyups++
		}
		packet := make([]byte, 8)
		packet[0] = 4
		if down {
			packet[1] = 1
		}
		binary.BigEndian.PutUint32(packet[4:], keysym)
		rfbWrite(t, conn, packet)
	}
	press := func(keysym uint32) { key(keysym, true); key(keysym, false) }
	text := func(value string) {
		for _, r := range value {
			keysym := uint32(r)
			if r > 0xff {
				keysym |= 0x01000000
			}
			press(keysym)
		}
	}
	assertValue := func(want string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		var got string
		for time.Now().Before(deadline) {
			got, err = human.Value(target)
			if err != nil {
				t.Fatal(err)
			}
			if got == want {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatalf("真实 RFB 输入不一致：得到 %q，期望 %q", got, want)
	}
	text("123")
	assertValue("123")
	press(0xff08)
	assertValue("12")
	key(0xffe3, true)
	press('a')
	key(0xffe3, false)
	press(0xff08)
	assertValue("")
	text(remoteInputText)
	press(0xff0d)
	assertValue(remoteInputText + "\n")
}

func TestNoVNCViewerInputAgainstLocalChrome(t *testing.T) {
	if runtime.GOOS != "linux" || os.Getenv("KAKATTA_REMOTE_INPUT_TEST") != "1" {
		t.Skip("设置 KAKATTA_REMOTE_INPUT_TEST=1，在断网 Linux 容器里运行真实输入测试")
	}
	for _, size := range [][2]int64{{393, 852}, {359, 577}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) { testNoVNCViewerInput(t, size[0], size[1]) })
	}
}

func testNoVNCViewerInput(t *testing.T, width, height int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	page := inputPage(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><meta charset="utf-8"><textarea id="target" style="position:fixed;inset:0;width:100vw;height:100vh;box-sizing:border-box;background:#fff;color:#111;font:24px sans-serif"></textarea>`)
	}))
	defer page.Close()
	targetDisplay, err := browser.StartDisplay(ctx, 460)
	if err != nil {
		t.Fatal(err)
	}
	defer targetDisplay.Close()
	targetChrome, err := browser.Start(ctx, browser.Options{ExecPath: inputChromePath(t, false), ProfileDir: t.TempDir(), AccountID: 460, Env: []string{"DISPLAY=" + targetDisplay.Name()}})
	if err != nil {
		t.Fatal(err)
	}
	defer targetChrome.Close()
	if err := targetChrome.Navigate(page.URL, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	keyups := 7 + len([]rune(remoteInputText+"Z")) + 2 + 1 // x11vnc 为大写 Z 合成一对 Shift。
	finishContract := recordInput(t, targetChrome, fmt.Sprintf("novnc-%dx%d", width, height), remoteInputText+"\n", &keyups)
	remoteSession, err := remote.Start(ctx, targetDisplay.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer remoteSession.Close()
	server := httptest.NewServer(remoteSession.Handler(func(*http.Request) bool { return true }))
	defer server.Close()
	clientDisplay, err := browser.StartDisplay(ctx, 461)
	if err != nil {
		t.Fatal(err)
	}
	defer clientDisplay.Close()
	client, err := browser.Start(ctx, browser.Options{ExecPath: inputChromePath(t, true), ProfileDir: t.TempDir(), AccountID: 461, Env: []string{"DISPLAY=" + clientDisplay.Name()}, Width: 900, Height: 1000})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := chromedp.Call(client.Context(), emulation.SetDeviceMetricsOverride, emulation.SetDeviceMetricsOverrideParams{Width: width, Height: height, DeviceScaleFactor: 1, Mobile: true}); err != nil {
		t.Fatal(err)
	}
	if err := client.Navigate(server.URL+"/", 10*time.Second); err != nil {
		t.Fatal(err)
	}
	clientHuman := browser.NewHuman(client, pacer.New(nil), pacer.Range{})
	if _, err := clientHuman.FirstVisible(ctx, "#text-toggle:not([disabled])", 5*time.Second); err != nil {
		t.Fatal("实际 viewer 没有完成连接：", err)
	}
	var screen []float64
	var x, y float64
	for _, selector := range []string{"#screen", "#screen>div", "canvas"} {
		node, err := clientHuman.FirstVisible(ctx, selector, 3*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		model, err := chromedp.Call(client.Context(), dom.GetBoxModel, dom.GetBoxModelParams{BackendNodeID: node.BackendNodeID})
		if err != nil {
			t.Fatal(err)
		}
		box := model.Model.Border
		t.Logf("%d×%d 初始 %s：box=%v attributes=%v", width, height, selector, box, node.Attributes)
		if selector == "#screen" {
			screen = box
			x, y = (box[0]+box[2])/2, (box[1]+box[5])/2
			if box[5]-box[1] < 200 {
				t.Fatal("初始画面没有留出可操作的高度")
			}
		} else if selector == "#screen>div" {
			for i := range screen {
				if math.Abs(box[i]-screen[i]) > 1 {
					t.Fatal("内部滚动区域没有占满画面容器")
				}
			}
		} else if box[0] != screen[0] || box[1] != screen[1] || box[2]-box[0] != 1920 || box[5]-box[1] != 1080 {
			t.Fatal("1:1 画布没有从可见区域左上角开始")
		}
	}
	frameDeadline := time.Now().Add(3 * time.Second)
	frameVisible := false
	for time.Now().Before(frameDeadline) {
		screenshot, err := chromedp.Call(client.Context(), cdppage.CaptureScreenshot, cdppage.CaptureScreenshotParams{Format: cdppage.CaptureScreenshotFormatPng})
		if err != nil {
			t.Fatal(err)
		}
		image, err := png.Decode(bytes.NewReader(screenshot.Data))
		if err != nil {
			t.Fatal(err)
		}
		r, g, b, _ := image.At(int(x), int(y)).RGBA()
		if r > 0xe000 && g > 0xe000 && b > 0xe000 {
			frameVisible = true
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !frameVisible {
		t.Fatal("1:1 初始可见区域没有绘出远程输入页，仍是空白背景")
	}
	// 1:1 画布比客户端视口大，点击可见区域，不能随机点击整个画布。
	for _, event := range []input.DispatchMouseEventParams{
		{Type: chromedp.MouseMoved, X: x, Y: y},
		{Type: chromedp.MousePressed, X: x, Y: y, Button: input.MouseButtonLeft, Buttons: 1, ClickCount: 1},
		{Type: chromedp.MouseReleased, X: x, Y: y, Button: input.MouseButtonLeft, ClickCount: 1},
	} {
		if _, err := chromedp.Call(client.Context(), input.DispatchMouseEvent, event); err != nil {
			t.Fatal(err)
		}
	}
	click := func(selector string) {
		t.Helper()
		node, err := clientHuman.FirstVisible(ctx, selector, 3*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if err := clientHuman.Click(ctx, node); err != nil {
			t.Fatal(err)
		}
	}
	defer finishContract()
	viewerShortcuts(t, client)
	click("#text-toggle")
	click("#text-input")
	viewerComposition(t, client, targetChrome)
	click("#text-send")
	click("#text-backspace")
	click("#text-enter")
	targetHuman := browser.NewHuman(targetChrome, pacer.New(nil), pacer.Range{})
	target, err := targetHuman.FirstVisible(ctx, "#target", 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	var got string
	for time.Now().Before(deadline) {
		got, err = targetHuman.Value(target)
		if err != nil {
			t.Fatal(err)
		}
		if got == remoteInputText+"\n" {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("实际 noVNC viewer 输入不一致：得到 %q，期望 %q", got, remoteInputText+"\n")
}

func rfbWrite(t *testing.T, conn net.Conn, data []byte) {
	t.Helper()
	if _, err := conn.Write(data); err != nil {
		t.Fatal(err)
	}
}

func rfbHandshake(t *testing.T, conn net.Conn) (uint16, uint16) {
	t.Helper()
	read := func(count int) []byte {
		data := make([]byte, count)
		if _, err := io.ReadFull(conn, data); err != nil {
			t.Fatal(err)
		}
		return data
	}
	if version := string(read(12)); version != "RFB 003.008\n" {
		t.Fatalf("不支持的 RFB 版本：%q", version)
	}
	rfbWrite(t, conn, []byte("RFB 003.008\n"))
	security := read(int(read(1)[0]))
	if !slices.Contains(security, byte(1)) {
		t.Fatalf("本地 VNC 没有无密码认证类型：%v", security)
	}
	rfbWrite(t, conn, []byte{1})
	if binary.BigEndian.Uint32(read(4)) != 0 {
		t.Fatal("本地 VNC 拒绝了握手")
	}
	rfbWrite(t, conn, []byte{1})
	initial := read(24)
	nameLength := binary.BigEndian.Uint32(initial[20:24])
	if nameLength > 4096 {
		t.Fatal("本地 VNC 名称过长")
	}
	read(int(nameLength))
	return binary.BigEndian.Uint16(initial[:2]), binary.BigEndian.Uint16(initial[2:4])
}
