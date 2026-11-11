package remote_test

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/browser"
	"github.com/chromedp/cdproto/input"
	rt "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

//go:embed testdata/input-probe.js
var inputProbe string

const remoteInputText = "相机123é😀𠮷e\u0301👩🏽‍💻"

func inputChromePath(t *testing.T, forceBaseline bool) string {
	t.Helper()
	mode := os.Getenv("KAKATTA_RFB_CONTRACT_MODE")
	if mode == "" {
		t.Log("使用真实 browser.Start 固定环境，没有测试 wrapper")
		return ""
	}
	if mode != "baseline" && mode != "xim" {
		t.Fatal("必须明确选择 baseline 或 xim")
	}
	script := "#!/bin/sh\nunset GTK_IM_MODULE XMODIFIERS\n"
	if mode == "xim" && !forceBaseline {
		script += "export GTK_IM_MODULE=xim XMODIFIERS=@im=none\n"
	}
	script += "exec /usr/bin/google-chrome-stable \"$@\"\n"
	path := filepath.Join(t.TempDir(), "chrome")
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func inputPage(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.Listener.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:18081")
	if err != nil {
		t.Fatal(err)
	}
	server.Listener = listener
	server.Start()
	return server
}

func inputEvaluate(t *testing.T, chrome *browser.Session, expression string) json.RawMessage {
	t.Helper()
	yes := true
	reply, err := chromedp.Call(chrome.Context(), rt.Evaluate, rt.EvaluateParams{Expression: expression, ReturnByValue: &yes})
	if err != nil {
		t.Fatal(err)
	}
	if reply.ExceptionDetails != nil {
		t.Fatalf("本地采集脚本失败：%+v", reply.ExceptionDetails)
	}
	return reply.Result.Value
}

func recordInput(t *testing.T, chrome *browser.Session, label, expected string, ups *int) func() {
	t.Helper()
	inputEvaluate(t, chrome, inputProbe)
	before := inputEvaluate(t, chrome, "__rfbContract.navigatorSnapshot()")
	mode := os.Getenv("KAKATTA_RFB_CONTRACT_MODE")
	if mode == "" {
		mode = "production"
	}
	return func() {
		awaitInputKeyups(t, chrome, *ups)
		report := map[string]any{"mode": mode, "transport": label,
			"expected": expected, "expectedKeyups": *ups, "navigatorBefore": before,
			"navigatorAfter": inputEvaluate(t, chrome, "__rfbContract.navigatorSnapshot()"),
			"result":         inputEvaluate(t, chrome, "__rfbContract.read('#target')")}
		dir := os.Getenv("KAKATTA_RFB_CONTRACT_DIR")
		if dir == "" {
			dir = t.TempDir()
		}
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		file, err := os.CreateTemp(dir, mode+"-"+label+"-*.json")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(data); err != nil {
			file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		t.Log("采集结果：", file.Name())
		var captured struct {
			Events []struct {
				Type        string
				Key, Code   *string
				KeyCode     *int
				IsComposing *bool
				IsTrusted   bool
			}
		}
		if err := json.Unmarshal(report["result"].(json.RawMessage), &captured); err != nil {
			t.Fatal(err)
		}
		downs, releases := 0, 0
		for _, event := range captured.Events {
			if event.Type != "keydown" && event.Type != "keyup" {
				continue
			}
			if event.Type == "keydown" {
				downs++
			} else {
				releases++
			}
			if event.Key == nil || event.Code == nil || event.KeyCode == nil || event.IsComposing == nil || !event.IsTrusted {
				t.Errorf("按键字段不完整或不可信：%+v", event)
			}
		}
		if downs != *ups || releases != *ups {
			t.Errorf("真实按键数量不一致：按下 %d、释放 %d，均期望 %d；事件 %s", downs, releases, *ups, report["result"])
		}
		t.Logf("%s 按下/释放：%d/%d，期望：%d/%d", label, downs, releases, *ups, *ups)
	}
}

func awaitInputKeyups(t *testing.T, chrome *browser.Session, expected int) int {
	t.Helper()
	// 只等已经发出的释放事件，不重发输入。
	deadline := time.Now().Add(3 * time.Second)
	var count int
	for time.Now().Before(deadline) {
		if err := json.Unmarshal(inputEvaluate(t, chrome, "__rfbContract.events.filter(e=>e.type==='keyup').length"), &count); err != nil {
			t.Fatal(err)
		}
		if count == expected {
			return count
		}
		if count > expected {
			return count
		}
		time.Sleep(25 * time.Millisecond)
	}
	return count
}

func viewerKey(t *testing.T, client *browser.Session, key, code, text string, vk, modifiers int64, down bool) {
	t.Helper()
	kind := input.DispatchKeyEventTypeKeyUp
	if down {
		kind = input.DispatchKeyEventTypeRawKeyDown
		if text != "" {
			kind = input.DispatchKeyEventTypeKeyDown
		}
	}
	if !down {
		text = ""
	}
	_, err := chromedp.Call(client.Context(), input.DispatchKeyEvent, input.DispatchKeyEventParams{
		Type: kind, Key: key, Code: code, Text: text, UnmodifiedText: text,
		WindowsVirtualKeyCode: vk, NativeVirtualKeyCode: vk, Modifiers: modifiers})
	if err != nil {
		t.Fatal(err)
	}
}

func viewerShortcuts(t *testing.T, client *browser.Session) {
	for _, r := range "123" {
		key := string(r)
		code := "Digit" + key
		viewerKey(t, client, key, code, key, int64(r), 0, true)
		viewerKey(t, client, key, code, "", int64(r), 0, false)
	}
	for _, down := range []bool{true, false} {
		viewerKey(t, client, "Backspace", "Backspace", "", 8, 0, down)
	}
	viewerKey(t, client, "Control", "ControlLeft", "", 17, 2, true)
	viewerKey(t, client, "a", "KeyA", "", 65, 2, true)
	viewerKey(t, client, "a", "KeyA", "", 65, 2, false)
	viewerKey(t, client, "Control", "ControlLeft", "", 17, 0, false)
	for _, down := range []bool{true, false} {
		viewerKey(t, client, "Backspace", "Backspace", "", 8, 0, down)
	}
}

func viewerComposition(t *testing.T, client, target *browser.Session) {
	t.Helper()
	if got := awaitInputKeyups(t, target, 7); got != 7 {
		t.Fatalf("组合前快捷键未完整释放：%d", got)
	}
	inputEvaluate(t, client, inputProbe)
	for _, text := range []string{"相", "相机"} {
		_, err := chromedp.Call(client.Context(), input.ImeSetComposition, input.ImeSetCompositionParams{Text: text, SelectionStart: int64(len([]rune(text))), SelectionEnd: int64(len([]rune(text)))})
		if err != nil {
			t.Fatal(err)
		}
	}
	var disabled bool
	if err := json.Unmarshal(inputEvaluate(t, client, "document.querySelector('#text-send').disabled"), &disabled); err != nil {
		t.Fatal(err)
	}
	if !disabled {
		t.Fatal("组合期间发送按钮没有禁用")
	}
	// 这是本地客户端测试页面的组合状态，不对目标页伪造输入事件。
	inputEvaluate(t, client, "document.querySelector('#text-send').click()")
	var value string
	if err := json.Unmarshal(inputEvaluate(t, target, "document.querySelector('#target').value"), &value); err != nil {
		t.Fatal(err)
	}
	if value != "" {
		t.Fatalf("组合期间提前转发：%q", value)
	}
	if _, err := chromedp.Call(client.Context(), input.InsertText, input.InsertTextParams{Text: remoteInputText + "Z"}); err != nil {
		t.Fatal(err)
	}
	var valid bool
	check := fmt.Sprintf("__rfbContract.events.some(e=>e.type==='compositionstart') && __rfbContract.events.some(e=>e.type==='input'&&e.isComposing===true) && __rfbContract.events.some(e=>e.type==='compositionend') && document.querySelector('#text-input').value===%q", remoteInputText+"Z")
	if err := json.Unmarshal(inputEvaluate(t, client, check), &valid); err != nil {
		t.Fatal(err)
	}
	if !valid {
		t.Fatal("客户端组合流程或最终提交值不对")
	}
	events := inputEvaluate(t, client, "__rfbContract.read('#text-input')")
	dir := os.Getenv("KAKATTA_RFB_CONTRACT_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	name := strings.ReplaceAll(t.Name(), "/", "-")
	file, err := os.CreateTemp(dir, fmt.Sprintf("%s-%s-client-composition-*.json", os.Getenv("KAKATTA_RFB_CONTRACT_MODE"), name))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(events); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	t.Log("客户端组合记录：", file.Name())
}
