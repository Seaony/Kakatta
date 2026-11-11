package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/xianyu/fake"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

func TestControllerDiagnosticFaultFiles(t *testing.T) {
	cases := []struct {
		name, kind   string
		configure    func(*controller)
		response     func(*http.Request) (string, bool)
		missingInput bool
	}{
		{name: "搜索框缺失", kind: "element", missingInput: true},
		{name: "筛选无响应", kind: "element", configure: func(c *controller) {
			c.opts.Filters.PersonalOnly = true
			c.fixture.SetFilterBehavior("personal", fake.FilterBehavior{NoRequest: true})
		}},
		{name: "空结果", kind: "operation", response: func(r *http.Request) (string, bool) {
			return `{"ret":["SUCCESS"],"data":{"resultList":[]}}`, strings.Contains(r.URL.Path, "idlemtopsearch")
		}},
		{name: "详情字段缺失", kind: "parse", response: func(r *http.Request) (string, bool) {
			return `{"ret":["SUCCESS"],"data":{"renamedItem":{"desc":"故障商品原文"},"mobile":"13800138000","token":"private-fixture-token"}}`, strings.Contains(r.URL.Path, "idle.pc.detail")
		}},
		{name: "接口字段改名", kind: "parse", response: func(r *http.Request) (string, bool) {
			return `{"ret":["SUCCESS"],"data":{"renamedResultList":[],"phone":"13800138000","password":"private-fixture-password"}}`, strings.Contains(r.URL.Path, "idlemtopsearch")
		}},
		{name: "半截JSON", kind: "parse", configure: func(c *controller) { c.fixture.SetSearchFault(c.opts.Keyword, fake.SearchFault{TruncatedJSON: true}) }},
		{name: "验证码", kind: "risk", configure: func(c *controller) { c.fixture.SetMaskOnSearch(true) }},
		{name: "直接拒绝", kind: "risk", response: func(r *http.Request) (string, bool) {
			return `{"ret":["SUCCESS"],"data":{"url":"https://www.goofish.com/punish?action=deny"}}`, strings.Contains(r.URL.Path, "idlemtopsearch")
		}},
	}
	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := experimentController(t, 521+index)
			keepExperimentArtifacts(t, c)
			if tc.configure != nil {
				tc.configure(c)
			}
			if tc.response != nil || tc.missingInput {
				upstream, _ := url.Parse(c.fixture.Home())
				proxy := httputil.NewSingleHostReverseProxy(upstream)
				var local *httptest.Server
				proxy.ModifyResponse = func(response *http.Response) error {
					if !strings.Contains(response.Header.Get("Content-Type"), "text/html") {
						return nil
					}
					body, err := io.ReadAll(response.Body)
					response.Body.Close()
					if err != nil {
						return err
					}
					body = bytes.ReplaceAll(body, []byte(c.fixture.Home()[:len(c.fixture.Home())-1]), []byte(local.URL))
					response.Body = io.NopCloser(bytes.NewReader(body))
					response.ContentLength = int64(len(body))
					response.Header.Set("Content-Length", strconv.Itoa(len(body)))
					return nil
				}
				local = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if tc.missingInput && r.URL.Path == "/" {
						w.Header().Set("Content-Type", "text/html")
						io.WriteString(w, `<!doctype html><title>缺失搜索框</title><div style="height:3000px">没有输入框</div>`)
						return
					}
					if tc.response != nil {
						if body, yes := tc.response(r); yes {
							w.Header().Set("Content-Type", "application/json")
							io.WriteString(w, body)
							return
						}
					}
					proxy.ServeHTTP(w, r)
				}))
				defer local.Close()
				c.site = web.Site{Home: local.URL + "/", APIHost: strings.TrimPrefix(local.URL, "http://")}
			}
			ctx, stop := context.WithTimeout(t.Context(), 50*time.Second)
			defer stop()
			err := c.run(ctx)
			if tc.name == "详情字段缺失" {
				if err != nil {
					t.Fatalf("普通缺字段不应抹掉其他成功资料：%v", err)
				}
			} else if err == nil {
				t.Fatal("故障流程被报告为成功")
			}
			c.Close()
			files, readErr := filepath.Glob(filepath.Join(experimentLogDir(c.opts), "bundles", "*", "page.json"))
			if readErr != nil || len(files) == 0 {
				t.Fatalf("故障没有落盘包：%v %v", files, readErr)
			}
			found := false
			for _, file := range files {
				data, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				var meta struct {
					Kind       string `json:"kind"`
					URL, Title string
				}
				if err := json.Unmarshal(data, &meta); err != nil {
					t.Fatal(err)
				}
				if meta.Kind != tc.kind {
					continue
				}
				found = true
				for _, part := range []string{"screenshot.jpg", "page.html", "events.json"} {
					if info, err := os.Stat(filepath.Join(filepath.Dir(file), part)); err != nil || info.Size() == 0 {
						t.Fatalf("诊断包缺%s：%v", part, err)
					}
				}
				if meta.URL == "" || meta.Title == "" {
					t.Fatalf("故障网址或标题缺失：%s", data)
				}
				if tc.kind == "parse" {
					if response, err := os.ReadFile(filepath.Join(filepath.Dir(file), "response.json")); err != nil || len(response) == 0 {
						t.Fatalf("没有保存精确失败响应：%v", err)
					}
				}
			}
			if !found {
				t.Fatalf("没有%s诊断包：%v", tc.kind, files)
			}
			summaries, _ := filepath.Glob(filepath.Join(experimentLogDir(c.opts), "summaries", "*", "summary.json"))
			if len(summaries) != 1 {
				t.Fatalf("单次流程摘要数量不对：%v", summaries)
			}
			if tc.name == "验证码" {
				data, err := os.ReadFile(summaries[0])
				if err != nil {
					t.Fatal(err)
				}
				var summary experimentSummary
				if err := json.Unmarshal(data, &summary); err != nil {
					t.Fatal(err)
				}
				found := false
				seen := map[string]bool{}
				for _, signal := range summary.Signals {
					where, _ := signal["命中位置"].(string)
					value, _ := signal["值"].(string)
					if signal["类型"] == web.ErrNeedVerify.Error() && where != "" && strings.HasPrefix(value, "遮罩 ") {
						key := where + "|" + value
						if seen[key] {
							t.Fatalf("同一验证码命中被多层上报重复保存：%v", signal)
						}
						seen[key] = true
						found = true
					}
				}
				if !found {
					t.Fatalf("实际验证码现场没有写入摘要信号：信号=%v，原因=%s", summary.Signals, summary.Reason)
				}
			}
			assertExperimentDiagnosticSecretsAbsent(t, experimentLogDir(c.opts))
			t.Logf("故障=%s；事件=%s/events-*.jsonl；摘要=%s；诊断=%s/bundles", tc.name, experimentLogDir(c.opts), summaries[0], experimentLogDir(c.opts))
		})
	}
}

func TestControllerCrashAndFullDiskKeepCause(t *testing.T) {
	for index, mode := range []string{"Chrome崩溃", "磁盘满"} {
		t.Run(mode, func(t *testing.T) {
			c := experimentController(t, 540+index)
			keepExperimentArtifacts(t, c)
			c.fixture.SetFinalFilterDelay(time.Minute)
			var stderr bytes.Buffer
			c.stderr = &stderr
			done := make(chan error, 1)
			ctx, stop := context.WithTimeout(t.Context(), 40*time.Second)
			defer stop()
			go func() { done <- c.run(ctx) }()
			r := waitExperimentResource(t, c, done)
			waitExperiment(t, "等待筛选响应", func() bool {
				for _, q := range c.fixture.SearchQueries() {
					if strings.Contains(q, "final=1") {
						return true
					}
				}
				return false
			})
			before := c.fixture.RequestCount()
			if mode == "Chrome崩溃" {
				if err := syscall.Kill(r.chrome.PID(), syscall.SIGKILL); err != nil {
					t.Fatal(err)
				}
			} else {
				files, _ := filepath.Glob(filepath.Join(experimentLogDir(c.opts), "events-*.jsonl"))
				if len(files) != 1 {
					t.Fatal(files)
				}
				if err := os.Rename(files[0], filepath.Join(experimentLogDir(c.opts), "events-before-full.jsonl")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("/dev/full", files[0]); err != nil {
					t.Fatal(err)
				}
				c.log("磁盘满回归", map[string]any{"步骤": "等待筛选"})
			}
			err := <-done
			if err == nil {
				t.Fatal("故障后流程仍被报告为成功")
			}
			c.Close()
			if c.fixture.RequestCount() != before {
				t.Fatalf("故障停手后仍有新请求：%d→%d", before, c.fixture.RequestCount())
			}
			if mode == "Chrome崩溃" {
				files, _ := filepath.Glob(filepath.Join(experimentLogDir(c.opts), "events-*.jsonl"))
				data, _ := os.ReadFile(files[0])
				if !bytes.Contains(data, []byte("浏览器异常退出")) {
					t.Fatalf("只能看到取消，找不到浏览器根因：%s", data)
				}
				summaries, _ := filepath.Glob(filepath.Join(experimentLogDir(c.opts), "summaries", "*", "summary.json"))
				if len(summaries) != 1 {
					t.Fatal("崩溃后无摘要")
				}
				data, _ = os.ReadFile(summaries[0])
				if !bytes.Contains(data, []byte("browser_disconnected")) {
					t.Fatalf("崩溃被误记成人工取消：%s", data)
				}
			} else {
				if !errors.Is(c.recorder.Err(), syscall.ENOSPC) || !strings.Contains(stderr.String(), "已停止访问") {
					t.Fatalf("真实ENOSPC没有明显报错：%v %s", c.recorder.Err(), stderr.String())
				}
				if root := os.Getenv("KAKATTA_EXPERIMENT_ARTIFACTS"); root != "" {
					dir := filepath.Join(root, strings.ReplaceAll(t.Name(), "/", "-"))
					os.MkdirAll(dir, 0700)
					if err := os.WriteFile(filepath.Join(dir, "stderr.txt"), stderr.Bytes(), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			assertExperimentReleased(t, c, r)
		})
	}
}

func TestExperimentCheckRecordsUnreachableProxy(t *testing.T) {
	t.Setenv("LANG", "zh_CN.UTF-8")
	c := experimentController(t, 545)
	keepExperimentArtifacts(t, c)
	c.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	c.opts.Proxy, _ = url.Parse("http://private-user:private-proxy@" + address)
	c.opts.Check = true
	var output bytes.Buffer
	err = runSelfCheck(c.opts, c.settings, &output)
	if err == nil || !strings.Contains(output.String(), "无法连接代理端口") || strings.Contains(output.String(), "private-proxy") {
		t.Fatalf("代理不可达没有可定位的落盘输出：%v %s", err, output.String())
	}
	summaries, _ := filepath.Glob(filepath.Join(experimentLogDir(c.opts), "summaries", "*", "summary.json"))
	if len(summaries) != 1 {
		t.Fatalf("没有独立自检摘要：%v", summaries)
	}
	assertExperimentDiagnosticSecretsAbsent(t, experimentLogDir(c.opts))
}

func assertExperimentDiagnosticSecretsAbsent(t *testing.T, root string) {
	t.Helper()
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || strings.HasSuffix(path, ".jpg") {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, secret := range []string{"13800138000", "private-fixture-token", "private-fixture-password", "private-proxy"} {
			if bytes.Contains(content, []byte(secret)) {
				t.Errorf("诊断产物泄露%s：%s", secret, path)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func keepExperimentArtifacts(t *testing.T, c *controller) {
	t.Helper()
	root := os.Getenv("KAKATTA_EXPERIMENT_ARTIFACTS")
	if root == "" {
		return
	}
	t.Cleanup(func() {
		c.Close()
		dest := filepath.Join(root, strings.ReplaceAll(t.Name(), "/", "-"), "logs")
		source := experimentLogDir(c.opts)
		if err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(source, path)
			if err != nil {
				return err
			}
			target := filepath.Join(dest, relative)
			if entry.IsDir() {
				return os.MkdirAll(target, 0700)
			}
			if !entry.Type().IsRegular() {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			return os.WriteFile(target, data, 0600)
		}); err != nil {
			t.Error(err)
		}
	})
}
