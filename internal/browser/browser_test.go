package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/Seaony/Kakatta/internal/pacer"
)

// 本地测试页：一个搜索框和按钮，回车或点击后页面自己发一次接口请求。
// 页面会记下收到的键盘、鼠标事件是不是浏览器认定的真实事件（isTrusted）。
const testPage = `<!doctype html><meta charset="utf-8">
<input id="q" style="width:300px;height:30px">
<button id="go" style="width:120px;height:40px">搜索</button>
<button id="slow" style="width:120px;height:40px">慢请求</button>
<div id="ghost" style="width:100px;height:100px;opacity:0">透明遮罩</div>
<div id="out"></div>
<script>
window.trusted = {key: null, click: null};
const q = document.getElementById('q');
q.addEventListener('keydown', e => { if (window.trusted.key === null) window.trusted.key = e.isTrusted; });
function search() {
  fetch('/h5/mtop.taobao.idlemtopsearch.pc.search/1.0/?q=' + encodeURIComponent(q.value))
    .then(r => r.json()).then(j => { document.getElementById('out').textContent = j.data.count; });
}
q.addEventListener('keydown', e => { if (e.key === 'Enter') search(); });
document.getElementById('slow').addEventListener('click', () => fetch('/h5/mtop.taobao.idlemtopsearch.pc.search/1.0/?slow=1'));
document.getElementById('go').addEventListener('click', e => { window.trusted.click = e.isTrusted; search(); });
</script>`

type queryLog struct {
	sync.Mutex
	queries []string
}

func (q *queryLog) append(value string) {
	q.Lock()
	defer q.Unlock()
	q.queries = append(q.queries, value)
}

func (q *queryLog) all() []string {
	q.Lock()
	defer q.Unlock()
	return append([]string(nil), q.queries...)
}

func requireBrowser(t *testing.T) {
	t.Helper()
	if os.Getenv("KAKATTA_BROWSER_TESTS") != "1" {
		t.Skip("设置 KAKATTA_BROWSER_TESTS=1 才运行本地浏览器测试")
	}
}

func startTestServer(t *testing.T) (*httptest.Server, *queryLog) {
	queries := &queryLog{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, testPage)
	})
	mux.HandleFunc("/h5/mtop.taobao.idlemtopsearch.pc.search/1.0/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("slow") == "1" {
			time.Sleep(300 * time.Millisecond) // 在下一次操作之后才返回
		} else {
			queries.append(r.URL.Query().Get("q"))
		}
		if r.URL.Query().Get("q") == "late" {
			time.Sleep(time.Second)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"ret": []string{"SUCCESS::调用成功"}, "data": map[string]any{"count": 30}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, queries
}

var searchAPI = Match{URL: "idlemtopsearch.pc.search", Method: "GET"}

// 需要本机装有 Chrome；没有就跳过。只访问本机的测试服务器。
func TestSessionAgainstLocalPage(t *testing.T) {
	requireBrowser(t)
	if _, err := FindChrome(); err != nil {
		t.Skip("本机没有 Chrome：", err)
	}
	srv, queries := startTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s, err := Start(ctx, Options{ProfileDir: t.TempDir(), Width: 1280, Height: 800})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Capture.Watch("mtop.taobao.idlemtopsearch.pc.search")

	if err := s.Navigate(srv.URL, 15*time.Second); err != nil {
		t.Fatal(err)
	}
	frame, err := s.ScreenshotJPEG(ctx)
	if err != nil {
		t.Fatal(err)
	}
	picture, err := jpeg.DecodeConfig(bytes.NewReader(frame))
	if err != nil || picture.Width == 0 || picture.Height == 0 {
		t.Fatalf("当前页面没有有效的 JPEG 画面：%+v %v", picture, err)
	}
	// 主进程加上渲染等子进程，至少几十 MB。
	if mem, err := TreeMemory(s.PID()); err != nil || mem < 20<<20 {
		t.Fatalf("浏览器内存统计不对：%d 字节，%v", mem, err)
	}

	// 固定端口连接时，浏览器不应该把自己标记为被自动化控制。
	webdriver, err := chromedp.Run(s.Context(), chromedp.Evaluate[bool](`navigator.webdriver === true`))
	if err != nil {
		t.Fatal(err)
	}
	if webdriver {
		t.Fatal("navigator.webdriver 是 true，浏览器被标记为自动化控制")
	}

	p := pacer.New(nil)
	h := NewHuman(s, p, pacer.Between(5*time.Millisecond, 15*time.Millisecond))

	box, err := h.FirstVisible(ctx, "#q", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Click(ctx, box); err != nil {
		t.Fatal(err)
	}
	if err := h.Type(ctx, "MacBook 苹果"); err != nil {
		t.Fatal(err)
	}
	mark := s.Capture.Mark()
	if err := h.Press(ctx, "Enter"); err != nil {
		t.Fatal(err)
	}
	resp, err := s.Capture.WaitAfter(ctx, mark, searchAPI, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Ret  []string `json:"ret"`
		Data struct {
			Count int `json:"count"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("响应正文不是 JSON：%v：%s", err, resp.Body)
	}
	if body.Data.Count != 30 || resp.Status != 200 {
		t.Fatalf("响应不对：状态 %d，正文 %s", resp.Status, resp.Body)
	}
	if got := queries.all(); len(got) != 1 || got[0] != "MacBook 苹果" {
		t.Fatalf("页面收到的关键词不对：%q", got)
	}

	// 点按钮再搜一次：新的响应必须排在 mark 之后，旧的不能被当成新的。
	mark = s.Capture.Mark()
	btn, err := h.FirstVisible(ctx, "#go", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Click(ctx, btn); err != nil {
		t.Fatal(err)
	}
	resp2, err := s.Capture.WaitAfter(ctx, mark, searchAPI, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if resp2.Seq <= resp.Seq {
		t.Fatalf("第二次的响应编号 %d 不大于第一次的 %d", resp2.Seq, resp.Seq)
	}

	trusted, err := chromedp.Run(s.Context(), chromedp.Evaluate[map[string]bool](`window.trusted`))
	if err != nil {
		t.Fatal(err)
	}
	if !trusted["key"] || !trusted["click"] {
		t.Fatalf("页面收到的不是真实事件：%v", trusted)
	}
}

func TestFindReportsMissingElement(t *testing.T) {
	requireBrowser(t)
	if _, err := FindChrome(); err != nil {
		t.Skip("本机没有 Chrome：", err)
	}
	srv, _ := startTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := Start(ctx, Options{ProfileDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Navigate(srv.URL, 15*time.Second); err != nil {
		t.Fatal(err)
	}
	h := NewHuman(s, pacer.New(nil), pacer.Between(0, 0))
	if _, err := h.Find(ctx, ".not-here", 500*time.Millisecond); err == nil {
		t.Fatal("找不到的元素应该返回错误")
	}
}

// 对照：用 chromedp 默认的 pipe 方式连接时，浏览器会把自己标记为被自动化控制。
// 这条测试证明上面「webdriver 不是 true」的检查确实能分辨两种连接方式。
func TestPipeModeIsMarkedAsAutomation(t *testing.T) {
	requireBrowser(t)
	execPath, err := FindChrome()
	if err != nil {
		t.Skip("本机没有 Chrome：", err)
	}
	srv, _ := startTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	opts := append(append([]chromedp.ExecAllocatorOption{}, keptFlags...),
		chromedp.ExecPath(execPath), chromedp.UserDataDir(t.TempDir()))
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, opts...)
	defer cancelAlloc()
	tabCtx, cancelTab := chromedp.NewContext(allocCtx)
	defer cancelTab()

	if err := chromedp.Do(tabCtx, chromedp.Navigate(srv.URL)); err != nil {
		t.Fatal(err)
	}
	webdriver, err := chromedp.Run(tabCtx, chromedp.Evaluate[bool](`navigator.webdriver === true`))
	if err != nil {
		t.Fatal(err)
	}
	if !webdriver {
		t.Fatal("pipe 方式下 navigator.webdriver 应该是 true，检查方法可能失效了")
	}
}

// 操作之前就发出、操作之后才返回的旧请求，不能被当成这次操作的结果；请求方法不对的也不能算。
func TestCaptureIgnoresRequestsSentBeforeMark(t *testing.T) {
	requireBrowser(t)
	if _, err := FindChrome(); err != nil {
		t.Skip("本机没有 Chrome：", err)
	}
	srv, _ := startTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	s, err := Start(ctx, Options{ProfileDir: t.TempDir(), Width: 1280, Height: 800})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Capture.Watch("mtop.taobao.idlemtopsearch.pc.search")
	if err := s.Navigate(srv.URL, 15*time.Second); err != nil {
		t.Fatal(err)
	}
	h := NewHuman(s, pacer.New(nil), pacer.Between(5*time.Millisecond, 15*time.Millisecond))

	// 慢请求先发出（300 毫秒后返回），然后打标记，再发一次 1 秒后才返回的请求。
	before := s.Capture.Mark()
	slow, err := h.FirstVisible(ctx, "#slow", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Click(ctx, slow); err != nil {
		t.Fatal(err)
	}
	// 等慢请求真正发出去再打标记。
	for !s.Capture.SentAfter(before, searchAPI) {
		if ctx.Err() != nil {
			t.Fatal("慢请求一直没有发出")
		}
		time.Sleep(20 * time.Millisecond)
	}
	mark := s.Capture.Mark()

	box, err := h.FirstVisible(ctx, "#q", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Click(ctx, box); err != nil {
		t.Fatal(err)
	}
	if err := h.Type(ctx, "late"); err != nil {
		t.Fatal(err)
	}
	if err := h.Press(ctx, "Enter"); err != nil {
		t.Fatal(err)
	}
	resp, err := s.Capture.WaitAfter(ctx, mark, searchAPI, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.URL, "q=late") {
		t.Fatalf("拿到的是打标记之前发出的旧请求：%s", resp.URL)
	}

	// 页面发的是 GET，按 POST 等不应该等到。
	post := Match{URL: searchAPI.URL, Method: "POST"}
	if s.Capture.SentAfter(0, post) {
		t.Fatal("页面没有发过 POST 请求，却被认成发过")
	}
	if _, err := s.Capture.WaitAfter(ctx, 0, post, 300*time.Millisecond); err != ErrResponseTimeout {
		t.Fatalf("按 POST 等应该超时，实际：%v", err)
	}
}

// 完全透明的元素不算可见，避免把透明遮罩当成风控弹层。
func TestTransparentElementIsNotVisible(t *testing.T) {
	requireBrowser(t)
	if _, err := FindChrome(); err != nil {
		t.Skip("本机没有 Chrome：", err)
	}
	srv, _ := startTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := Start(ctx, Options{ProfileDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Navigate(srv.URL, 15*time.Second); err != nil {
		t.Fatal(err)
	}
	h := NewHuman(s, pacer.New(nil), pacer.Between(0, 0))
	nodes, err := h.Find(ctx, "#ghost", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if h.Visible(nodes[0]) {
		t.Fatal("透明度为 0 的元素被当成了可见")
	}
	btn, err := h.Find(ctx, "#go", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !h.Visible(btn[0]) {
		t.Fatal("正常的按钮应该可见")
	}
}

func TestSumTreeCountsDescendantsOnly(t *testing.T) {
	procs := []proc{{1, 0, 5}, {10, 1, 100}, {11, 10, 20}, {12, 11, 3}, {20, 1, 999}}
	if got := sumTree(10, procs); got != 123 {
		t.Fatalf("应该只算 10 和它的子孙：%d", got)
	}
}

// 外层上下文结束（超时、Ctrl+C）时也要正常关闭浏览器，刚写入的登录 Cookie 不能丢。
func TestCookieSurvivesParentCancel(t *testing.T) {
	requireBrowser(t)
	if _, err := FindChrome(); err != nil {
		t.Skip("本机没有 Chrome：", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "keep", Value: "1", Path: "/", Expires: time.Now().Add(time.Hour)})
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()
	dir := t.TempDir()

	parent, cancel := context.WithCancel(context.Background())
	s, err := Start(parent, Options{ProfileDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Navigate(srv.URL, 15*time.Second); err != nil {
		t.Fatal(err)
	}
	cancel()
	s.Close() // 等后台的正常关闭做完

	s2, err := Start(context.Background(), Options{ProfileDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if ok, err := s2.HasCookie([]string{srv.URL}, "keep"); err != nil || !ok {
		t.Fatalf("重启后 Cookie 应该还在：%v %v", ok, err)
	}
}

// 搜索框里预填了文字时要能读出来并清空，再输入关键词不会和旧文字拼在一起。
func TestClearPrefilledInput(t *testing.T) {
	requireBrowser(t)
	if _, err := FindChrome(); err != nil {
		t.Skip("本机没有 Chrome：", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><meta charset="utf-8"><input id="q" value="推荐词 iPhone" style="width:300px">`)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	s, err := Start(ctx, Options{ProfileDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Navigate(srv.URL, 15*time.Second); err != nil {
		t.Fatal(err)
	}
	h := NewHuman(s, pacer.New(nil), pacer.Between(5*time.Millisecond, 10*time.Millisecond))
	n, err := h.FirstVisible(ctx, "#q", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Click(ctx, n); err != nil {
		t.Fatal(err)
	}
	if v, err := h.Value(n); err != nil || v != "推荐词 iPhone" {
		t.Fatalf("应读到预填的文字：%q %v", v, err)
	}
	if err := h.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	if err := h.Type(ctx, "相机"); err != nil {
		t.Fatal(err)
	}
	if v, _ := h.Value(n); v != "相机" {
		t.Fatalf("清空后再输入应该只有新关键词：%q", v)
	}
}

func TestFoundElementSurvivesFurtherDOMQueries(t *testing.T) {
	requireBrowser(t)
	srv, _ := startTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := Start(ctx, Options{ProfileDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Navigate(srv.URL, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	h := NewHuman(s, pacer.New(nil), pacer.Between(0, 0))
	input, err := h.FirstVisible(ctx, "#q", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.FindNow(ctx, "button"); err != nil {
		t.Fatal(err)
	}
	if err := h.Click(ctx, input); err != nil {
		t.Fatal("查询风控元素后不能再点先前找到的搜索框：", err)
	}
	if err := h.Type(ctx, "仍然有效"); err != nil {
		t.Fatal(err)
	}
	if value, err := h.Value(input); err != nil || value != "仍然有效" {
		t.Fatalf("节点没有持续指向原来的输入框：%s %v", value, err)
	}
}

func TestSearchTextAndVisibilityIgnoreHiddenOrAttributeMessages(t *testing.T) {
	requireBrowser(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><meta charset="utf-8">
<script>const note="非法访问";</script><style>/* 非法访问 */</style>
<div data-message="非法访问">正常页面</div>
<div style="opacity:0"><div class="baxia-dialog-mask" style="width:100px;height:100px">非法访问</div></div>
<div id="visible">请使用正常浏览器访问</div><div id="split">非法<span>访问</span></div>`)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := Start(ctx, Options{ProfileDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Navigate(srv.URL, time.Second*5); err != nil {
		t.Fatal(err)
	}
	h := NewHuman(s, pacer.New(nil), pacer.Between(0, 0))
	for _, text := range []string{"非法访问", "请使用正常浏览器访问"} {
		nodes, err := h.SearchText(ctx, text)
		if err != nil {
			t.Fatal(err)
		}
		var count int
		for _, node := range nodes {
			visible, err := h.VisibleChecked(node)
			if err != nil {
				t.Fatal(err)
			}
			if visible {
				count++
			}
		}
		want := 1
		if count != want {
			t.Fatalf("文字 %s 可见数量 %d，应为 %d", text, count, want)
		}
	}
}
