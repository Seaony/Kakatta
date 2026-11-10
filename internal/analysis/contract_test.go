package analysis

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/Seaony/Kakatta/internal/locales"
)

func TestFactsAndOverridesJSONContract(t *testing.T) {
	input := `{"specs":[{"name":"存储","value":null}],"brand_new":null,"musts":[{"id":"m1","status":"unknown","source":null,"evidence":null,"rev":1}],"bonuses":[],"checks":[{"key":"id_lock","status":"risk","source":"image","evidence":"第1张图：有激活锁","rev":1}],"seller_signals":[],"price_note":null,"extra_findings":[],"adjust":{"value":-2,"reason":"看图有顾虑"},"summary":"继续核查"}`
	var f Facts
	if err := json.Unmarshal([]byte(input), &f); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	var before, after map[string]any
	if err = json.Unmarshal([]byte(input), &before); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &after); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("事实JSON shape改变：\n%s", b)
	}
	var o UserOverrides
	if err = json.Unmarshal([]byte(`{"risks":{"id_lock":{"status":"ok","rev":1}}}`), &o); err != nil || (o.Risks["id_lock"].Status != CheckOK || o.Risks["id_lock"].Rev != 1) {
		t.Fatalf("用户覆盖格式错误：%+v %v", o, err)
	}
	b, err = json.Marshal(locales.Message{Code: "example"})
	if err != nil || string(b) != `{"code":"example","args":{}}` {
		t.Fatalf("空原因参数必须为对象：%s %v", b, err)
	}
}

func purityViolations(source string) ([]string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), "sample.go", source, 0)
	if err != nil {
		return nil, err
	}
	aliases := map[string]string{}
	out := []string{}
	for _, i := range f.Imports {
		path, err := strconv.Unquote(i.Path.Value)
		if err != nil {
			return nil, err
		}
		name := filepath.Base(path)
		if i.Name != nil {
			name = i.Name.Name
		}
		aliases[name] = path
		if name == "." && path == "time" {
			out = append(out, "import:time.dot")
		}
		if path == "os" || strings.HasPrefix(path, "os/") || path == "syscall" || path == "math/rand" || strings.HasPrefix(path, "math/rand/") || path == "crypto/rand" || path == "log" || strings.HasPrefix(path, "log/") || path == "net" || strings.HasPrefix(path, "net/") || strings.Contains(path, "/internal/analysis/client") || strings.Contains(path, "/internal/store") || strings.Contains(path, "/internal/browser") || strings.Contains(path, "/internal/xianyu") {
			out = append(out, "import:"+path)
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		s, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if x, ok := s.X.(*ast.Ident); ok && aliases[x.Name] == "time" && (s.Sel.Name == "Now" || s.Sel.Name == "Local" || s.Sel.Name == "Since" || s.Sel.Name == "Until" || s.Sel.Name == "Sleep" || s.Sel.Name == "After" || s.Sel.Name == "AfterFunc" || s.Sel.Name == "NewTimer" || s.Sel.Name == "Tick" || s.Sel.Name == "NewTicker") {
			out = append(out, "time."+s.Sel.Name)
		}
		return true
	})
	return out, nil
}

func TestPurityGuardRejectsExternalState(t *testing.T) {
	cases := []struct {
		source string
		bad    bool
	}{
		{`package sample; import clock "time"; var _ = clock.Now`, true},
		{`package sample; import "time"; var _ = time.Local`, true},
		{`package sample; import . "time"; var _ = Now`, true},
		{`package sample; import "time"; var _ = time.Since`, true},
		{`package sample; import _ "os/exec"`, true},
		{`package sample; import _ "net/http"`, true},
		{`package sample; import _ "os"`, true},
		{`package sample; import "time"; var _ = time.UnixMilli`, false},
		{`package sample; import "encoding/json"; var _ = json.Unmarshal`, false},
	}
	for _, c := range cases {
		got, err := purityViolations(c.source)
		if err != nil {
			t.Fatal(err)
		}
		if (len(got) > 0) != c.bad {
			t.Errorf("纯规则门禁：%q -> %v", c.source, got)
		}
	}
}

func TestProductionRulesHaveNoExternalState(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		count++
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		bad, err := purityViolations(string(b))
		if err != nil {
			t.Fatal(err)
		}
		if len(bad) > 0 {
			t.Errorf("%s 引入外部状态：%v", filepath.Base(path), bad)
		}
	}
	if count == 0 {
		t.Fatal("没有检查到生产文件")
	}
}

func TestProfileJSONKeepsKeywordEmptyAndAILists(t *testing.T) {
	b, err := json.Marshal(Profile{})
	if err != nil || string(b) != "{}" {
		t.Fatalf("关键词清单必须是空对象：%s %v", b, err)
	}
	b, err = json.Marshal(Profile{NextNo: 1})
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err = json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"specs", "musts", "bonuses", "risks", "allowed_kinds"} {
		if string(raw[key]) != "[]" {
			t.Errorf("清单列表 %s 应为数组：%s", key, raw[key])
		}
	}
}
