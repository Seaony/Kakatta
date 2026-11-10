package browser

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// 所有页面读取都要走协议命令，防止便捷方法偷偷执行页面脚本。
func TestProductionBrowserDoesNotInjectScripts(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("找不到浏览器源码，不能检查：%v", err)
	}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		bad, err := uncheckedBrowserCalls(path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(bad) != 0 {
			t.Errorf("%s 使用了没有核对过底层命令的方法：%v", path, bad)
		}
	}
}

func uncheckedBrowserCalls(path string, source any) ([]string, error) {
	allowed := map[string]bool{}
	for _, name := range strings.Fields("NoFirstRun NoDefaultBrowserCheck Flag ExecPath UserDataDir WindowSize Env ModifyCmdFunc ProxyServer NewExecAllocator NewContext Listen Do Call CallBrowser Cancel Run FromContext") {
		allowed[name] = true
	}
	f, err := parser.ParseFile(token.NewFileSet(), path, source, 0)
	if err != nil {
		return nil, err
	}
	var bad []string
	pkgName := "chromedp"
	for _, imp := range f.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		if strings.Contains(path, "cdproto/runtime") {
			bad = append(bad, "页面脚本协议")
		}
		if path == "github.com/chromedp/chromedp" && imp.Name != nil {
			pkgName = imp.Name.Name
		}
	}
	ast.Inspect(f, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		fun := call.Fun
		switch generic := fun.(type) {
		case *ast.IndexExpr:
			fun = generic.X
		case *ast.IndexListExpr:
			fun = generic.X
		}
		selector, ok := fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if pkg, ok := selector.X.(*ast.Ident); ok && pkg.Name == pkgName && !allowed[selector.Sel.Name] {
			bad = append(bad, selector.Sel.Name)
		}
		return true
	})
	return bad, nil
}

func TestScriptCheckDetectsOrdinaryAndGenericHelpers(t *testing.T) {
	for _, tc := range []struct {
		body string
		want int
	}{
		{`cd.Call(ctx, page.GetFrameTree, args)`, 0},
		{`cd.Location()`, 1},
		{`cd.Evaluate[bool]("navigator.webdriver")`, 1},
		{`cd.CallFunctionOn[string]("function(){return location.href}", nil)`, 1},
		{`cd.Navigate("http://local")`, 1},
	} {
		source := "package sample; import cd \"github.com/chromedp/chromedp\"; func test(){" + tc.body + "}"
		bad, err := uncheckedBrowserCalls("sample.go", source)
		if err != nil || len(bad) != tc.want {
			t.Fatalf("静态检查没识别 %s：%v %v", tc.body, bad, err)
		}
	}
}

func TestProfileCommandMatchesExactDirectory(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{[]string{"chrome", "--user-data-dir=/data/browser/1"}, true},
		{[]string{"chrome", "--user-data-dir", "/data/browser/1"}, true},
		{[]string{"chrome", "--user-data-dir=/data/browser/10"}, false},
		{[]string{"chrome", "--other=/data/browser/1"}, false},
	} {
		if got := commandUsesProfile(tc.args, "/data/browser/1"); got != tc.want {
			t.Fatalf("配置目录判断错误：%v", tc.args)
		}
	}
}

func TestPrepareProfileRemovesStaleSingletonsOnly(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"SingletonLock", "SingletonCookie", "SingletonSocket", "Local State"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("old"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := prepareProfile(dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"SingletonLock", "SingletonCookie", "SingletonSocket"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("旧锁还在：%s %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "Local State")); err != nil {
		t.Fatal("不该删除浏览器登录配置", err)
	}
}

func TestBrowserDefaultsAndPortRange(t *testing.T) {
	o := Options{ProfileDir: t.TempDir()}
	if err := normalizeOptions(&o); err != nil {
		t.Fatal(err)
	}
	if o.AccountID != 1 || o.Width != 1920 || o.Height != 1080 || o.CacheMB != 256 || o.TimeZone != "Asia/Shanghai" {
		t.Fatalf("默认设置和规格不符：%+v", o)
	}
	for _, id := range []int{-1, 12768} {
		o.AccountID = id
		if normalizeOptions(&o) == nil {
			t.Fatal("账号编号会进入系统临时端口段")
		}
	}
}
