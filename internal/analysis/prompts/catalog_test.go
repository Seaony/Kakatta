package prompts

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/analysis/client"
	"github.com/Seaony/Kakatta/internal/locales"
)

func copyCatalog(t *testing.T) fstest.MapFS {
	t.Helper()
	copy := fstest.MapFS{}
	if err := fs.WalkDir(catalogAssets, catalogRoot[:len(catalogRoot)-1], func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := catalogAssets.ReadFile(name)
		if err != nil {
			return err
		}
		copy[name] = &fstest.MapFile{Data: slices.Clone(data)}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return copy
}

func changeManifest(t *testing.T, files fstest.MapFS, change func(map[string]any)) {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(files[catalogRoot+"manifest.json"].Data, &value); err != nil {
		t.Fatal(err)
	}
	change(value)
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	files[catalogRoot+"manifest.json"].Data = data
}

func manifestEntry(value map[string]any, index int) map[string]any {
	return value["definitions"].([]any)[index].(map[string]any)
}

func docBlock(t *testing.T, source, header, fence string) string {
	t.Helper()
	_, rest, ok := strings.Cut(source, header)
	if !ok {
		t.Fatalf("文档缺少标题：%s", header)
	}
	_, rest, ok = strings.Cut(rest, "\n"+fence+"\n")
	if !ok {
		t.Fatalf("文档缺少代码围栏：%s", header)
	}
	body, _, ok := strings.Cut(rest, "\n```\n")
	if !ok {
		t.Fatalf("文档没有闭合围栏：%s", header)
	}
	return body + "\n"
}

func TestCatalogAssetsMatchDomainVerbatim(t *testing.T) {
	document, err := os.ReadFile("../../../docs/DOMAIN.md")
	if err != nil {
		t.Fatal(err)
	}
	definitions, err := Definitions()
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 4 {
		t.Fatalf("模板数=%d", len(definitions))
	}
	for i, tc := range []struct {
		kind                                   client.Kind
		promptHeader, schemaHeader, schemaName string
		keys                                   []string
	}{
		{client.Analysis, "**商品分析模板**", "**第三层的 JSON Schema**", "listing_facts", []string{"checks", "adjust", "summary"}},
		{client.TitleScreen, "**标题整理模板**", "**第二层的 JSON Schema**", "title_screen", []string{"items", "kind", "verdict"}},
		{client.Profile, "**需求清单模板**", "**生成需求清单的 JSON Schema**", "task_profile", []string{"specs", "risks"}},
		{client.Market, "**行情解读模板**", "", "", []string{}},
	} {
		definition := definitions[i]
		if definition.Kind != tc.kind || !reflect.DeepEqual(definition.RequiredKeys, tc.keys) || definition.Default != docBlock(t, string(document), tc.promptHeader, "```") {
			t.Fatalf("模板或固定键偏离DOMAIN：%s", tc.kind)
		}
		if tc.schemaHeader == "" {
			if definition.Schema != nil {
				t.Fatal("行情不能带Schema")
			}
			continue
		}
		if definition.Schema == nil || definition.Schema.Name != tc.schemaName || !bytes.Equal(definition.Schema.JSON, []byte(docBlock(t, string(document), tc.schemaHeader, "```json"))) {
			t.Fatalf("Schema偏离DOMAIN：%s", tc.kind)
		}
	}
}

func TestCatalogDamagedAssetsFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(fstest.MapFS)
	}{
		{"missing_manifest", func(f fstest.MapFS) { delete(f, catalogRoot+"manifest.json") }},
		{"invalid_manifest", func(f fstest.MapFS) { f[catalogRoot+"manifest.json"].Data = []byte(`{`) }},
		{"trailing_manifest", func(f fstest.MapFS) {
			f[catalogRoot+"manifest.json"].Data = append(f[catalogRoot+"manifest.json"].Data, []byte(`{}`)...)
		}},
		{"version", func(f fstest.MapFS) { changeManifest(t, f, func(v map[string]any) { v["asset_version"] = 2 }) }},
		{"missing_version", func(f fstest.MapFS) { changeManifest(t, f, func(v map[string]any) { delete(v, "asset_version") }) }},
		{"unknown_manifest_key", func(f fstest.MapFS) { changeManifest(t, f, func(v map[string]any) { v["execute"] = "script" }) }},
		{"unknown_definition_key", func(f fstest.MapFS) {
			changeManifest(t, f, func(v map[string]any) { manifestEntry(v, 0)["execute"] = "script" })
		}},
		{"unknown_schema_metadata_key", func(f fstest.MapFS) {
			changeManifest(t, f, func(v map[string]any) { manifestEntry(v, 0)["schema"].(map[string]any)["execute"] = "script" })
		}},
		{"missing_definition", func(f fstest.MapFS) {
			changeManifest(t, f, func(v map[string]any) { v["definitions"] = v["definitions"].([]any)[:3] })
		}},
		{"duplicate_definition", func(f fstest.MapFS) {
			changeManifest(t, f, func(v map[string]any) { v["definitions"].([]any)[3] = manifestEntry(v, 0) })
		}},
		{"unknown_kind", func(f fstest.MapFS) {
			changeManifest(t, f, func(v map[string]any) { manifestEntry(v, 0)["kind"] = "other" })
		}},
		{"template_path", func(f fstest.MapFS) {
			changeManifest(t, f, func(v map[string]any) { manifestEntry(v, 0)["template"] = "../analysis.txt" })
		}},
		{"missing_required_keys", func(f fstest.MapFS) {
			changeManifest(t, f, func(v map[string]any) { delete(manifestEntry(v, 3), "required_keys") })
		}},
		{"thinned_required_keys", func(f fstest.MapFS) {
			changeManifest(t, f, func(v map[string]any) { manifestEntry(v, 0)["required_keys"] = []string{"summary"} })
		}},
		{"missing_template", func(f fstest.MapFS) { delete(f, catalogRoot+"profile.txt") }},
		{"empty_default", func(f fstest.MapFS) { f[catalogRoot+"market.txt"].Data = []byte(" \n") }},
		{"invalid_utf8", func(f fstest.MapFS) { f[catalogRoot+"market.txt"].Data = []byte{0xff} }},
		{"default_missing_markers", func(f fstest.MapFS) { f[catalogRoot+"analysis.txt"].Data = []byte("没有输出格式") }},
		{"missing_schema_metadata", func(f fstest.MapFS) {
			changeManifest(t, f, func(v map[string]any) { manifestEntry(v, 0)["schema"] = nil })
		}},
		{"wrong_schema_name", func(f fstest.MapFS) {
			changeManifest(t, f, func(v map[string]any) { manifestEntry(v, 0)["schema"].(map[string]any)["name"] = "listing_facts_v1" })
		}},
		{"wrong_schema_file", func(f fstest.MapFS) {
			changeManifest(t, f, func(v map[string]any) { manifestEntry(v, 0)["schema"].(map[string]any)["file"] = "task_profile.json" })
		}},
		{"market_schema", func(f fstest.MapFS) {
			changeManifest(t, f, func(v map[string]any) { manifestEntry(v, 3)["schema"] = manifestEntry(v, 0)["schema"] })
		}},
		{"missing_schema_file", func(f fstest.MapFS) { delete(f, catalogRoot+"listing_facts.json") }},
		{"invalid_schema_json", func(f fstest.MapFS) { f[catalogRoot+"listing_facts.json"].Data = []byte(`{`) }},
		{"trailing_schema_json", func(f fstest.MapFS) {
			f[catalogRoot+"listing_facts.json"].Data = append(f[catalogRoot+"listing_facts.json"].Data, []byte(`{}`)...)
		}},
		{"incomplete_schema", func(f fstest.MapFS) { f[catalogRoot+"listing_facts.json"].Data = []byte(`{"type":"object"}`) }},
		{"unknown_schema_keyword", func(f fstest.MapFS) {
			f[catalogRoot+"listing_facts.json"].Data = bytes.Replace(f[catalogRoot+"listing_facts.json"].Data, []byte(`"type": "object"`), []byte(`"script": "run", "type": "object"`), 1)
		}},
		{"dangling_schema_ref", func(f fstest.MapFS) {
			f[catalogRoot+"listing_facts.json"].Data = bytes.ReplaceAll(f[catalogRoot+"listing_facts.json"].Data, []byte(`#/$defs/specs`), []byte(`#/$defs/missing`))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := copyCatalog(t)
			tc.change(files)
			if definitions, err := definitionsFrom(files); err == nil || definitions != nil {
				t.Fatalf("损坏资产仍返回了部分可用定义：%+v %v", definitions, err)
			}
		})
	}
}

func TestCatalogSaveRequiresExactQuotedKeys(t *testing.T) {
	definitions, err := Definitions()
	if err != nil {
		t.Fatal(err)
	}
	for _, definition := range definitions {
		for _, missing := range definition.RequiredKeys {
			t.Run(string(definition.Kind)+"/"+missing, func(t *testing.T) {
				content := strings.ReplaceAll(definition.Default, strconv.Quote(missing), missing)
				_, err := PrepareSave(definition.Kind, content)
				var invalid *analysis.Error
				if !errors.As(err, &invalid) || invalid.Message.Code != "analysis.prompts.missing_keys" || invalid.Message.Args["kind"] != string(definition.Kind) {
					t.Fatalf("缺键没有typed原因：%v", err)
				}
				keys, ok := invalid.Message.Args["keys"].(string)
				if !ok || !strings.Contains(keys, strconv.Quote(missing)) {
					t.Fatalf("没有指出缺的带引号键：%+v", invalid.Message)
				}
			})
		}
	}
	for _, content := range []string{`checks adjust summary`, `“checks” “adjust” “summary”`, `\"checks\" \"adjust\" \"summary\"`} {
		if _, err := PrepareSave(client.Analysis, content); err == nil {
			t.Fatalf("近似键名不能通过：%s", content)
		}
	}
	if _, err := PrepareSave(client.Kind("other"), `"checks" "adjust" "summary"`); err == nil {
		t.Fatal("未知用途不能保存")
	}
}

func TestCatalogSaveDefaultDeletesOverrideWithoutTrimming(t *testing.T) {
	definitions, err := Definitions()
	if err != nil {
		t.Fatal(err)
	}
	for _, definition := range definitions {
		if plan, err := PrepareSave(definition.Kind, definition.Default); err != nil || plan != nil {
			t.Fatalf("保存相同默认值应删覆盖：%s %v %v", definition.Kind, plan, err)
		}
		content := " \n" + definition.Default + "\t "
		plan, err := PrepareSave(definition.Kind, content)
		if err != nil || plan == nil || *plan != content {
			t.Fatalf("不能trim自定义内容：%s %v %v", definition.Kind, plan, err)
		}
	}
	for _, content := range []string{"", " \n\t"} {
		plan, err := PrepareSave(client.Market, content)
		if err != nil || plan == nil || *plan != content {
			t.Fatalf("行情模板不能增加额外限制：%q %v", content, err)
		}
	}
}

func TestCatalogResolveCopiesEffectiveContents(t *testing.T) {
	overrides := map[client.Kind]string{client.Analysis: `请返回 "checks" "adjust" "summary"`, client.Market: ""}
	snapshot, err := Resolve(overrides)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.AssetVersion != AssetVersion || len(snapshot.Contents) != 4 || snapshot.Contents[client.Analysis] != overrides[client.Analysis] || snapshot.Contents[client.Market] != "" {
		t.Fatalf("合并后的快照错误：%+v", snapshot)
	}
	overrides[client.Analysis] = "改坏输入"
	if snapshot.Contents[client.Analysis] == overrides[client.Analysis] {
		t.Fatal("快照与输入map共用")
	}
	snapshot.Contents[client.Profile] = "改坏输出"
	fresh, err := Resolve(nil)
	if err != nil || fresh.Contents[client.Profile] == "改坏输出" {
		t.Fatal("快照修改污染默认值")
	}
	for _, overrides := range []map[client.Kind]string{{client.Kind("other"): "x"}, {client.Analysis: ""}} {
		if result, err := Resolve(overrides); err == nil || result.Contents != nil {
			t.Fatalf("非法覆盖仍返回部分快照：%+v %v", result, err)
		}
	}
}

func TestCatalogDefinitionsAndRequestSchemaAreIndependent(t *testing.T) {
	definitions, err := Definitions()
	if err != nil {
		t.Fatal(err)
	}
	original := definitions[0].Default
	definitions[0].Default = "改坏模板"
	definitions[0].RequiredKeys[0] = "改坏键"
	definitions[0].Schema.Name = "改坏名称"
	definitions[0].Schema.JSON[0] = '!'
	fresh, err := Definitions()
	if err != nil || fresh[0].Default != original || fresh[0].RequiredKeys[0] != "checks" || fresh[0].Schema.Name != "listing_facts" || fresh[0].Schema.JSON[0] != '{' {
		t.Fatalf("定义被调用方污染：%+v %v", fresh, err)
	}
	snapshot, err := Resolve(map[client.Kind]string{client.TitleScreen: `自定义 "items" "kind" "verdict"`})
	if err != nil {
		t.Fatal(err)
	}
	before := make(map[client.Kind]string, len(snapshot.Contents))
	for kind, content := range snapshot.Contents {
		before[kind] = content
	}
	system, schema, err := requestParts(snapshot, client.TitleScreen)
	if err != nil || system != before[client.TitleScreen] || schema == nil || schema.Name != "title_screen" {
		t.Fatalf("没有使用有效快照：%s %+v %v", system, schema, err)
	}
	schema.JSON[0] = '!'
	schema.Name = "改坏"
	_, another, err := requestParts(snapshot, client.TitleScreen)
	if err != nil || another.JSON[0] != '{' || another.Name != "title_screen" || !reflect.DeepEqual(before, snapshot.Contents) {
		t.Fatal("requestParts污染Schema或输入快照")
	}
	_, marketSchema, err := requestParts(snapshot, client.Market)
	if err != nil || marketSchema != nil {
		t.Fatal("行情不应返回Schema")
	}
}

func TestCatalogRequestPartsRejectsInvalidSnapshots(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Snapshot)
		kind   client.Kind
	}{
		{"version", func(s *Snapshot) { s.AssetVersion++ }, client.TitleScreen},
		{"nil_contents", func(s *Snapshot) { s.Contents = nil }, client.TitleScreen},
		{"missing_kind", func(s *Snapshot) { delete(s.Contents, client.Profile) }, client.TitleScreen},
		{"extra_kind", func(s *Snapshot) { s.Contents[client.Kind("other")] = "x" }, client.TitleScreen},
		{"unknown_replaces_kind", func(s *Snapshot) { delete(s.Contents, client.Profile); s.Contents[client.Kind("other")] = "x" }, client.TitleScreen},
		{"bad_marker", func(s *Snapshot) { s.Contents[client.TitleScreen] = "x" }, client.TitleScreen},
		{"unknown_requested_kind", func(*Snapshot) {}, client.Kind("other")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot, err := Resolve(nil)
			if err != nil {
				t.Fatal(err)
			}
			tc.change(&snapshot)
			if system, schema, err := requestParts(snapshot, tc.kind); err == nil || system != "" || schema != nil {
				t.Fatalf("非法快照得到部分可用请求：%q %+v %v", system, schema, err)
			}
		})
	}
}

func TestCatalogConcurrentReadsRemainIndependent(t *testing.T) {
	var workers sync.WaitGroup
	for range 16 {
		workers.Go(func() {
			definitions, err := Definitions()
			if err != nil {
				t.Error(err)
				return
			}
			definitions[0].Schema.JSON[0] = '!'
			snapshot, err := Resolve(nil)
			if err != nil {
				t.Error(err)
				return
			}
			snapshot.Contents[client.Analysis] = "changed"
		})
	}
	workers.Wait()
	if snapshot, err := Resolve(nil); err != nil || snapshot.Contents[client.Analysis] == "changed" {
		t.Fatal("并发读取污染静态定义")
	}
}

func TestCatalogMissingKeysHasBothTranslations(t *testing.T) {
	_, err := PrepareSave(client.Analysis, "")
	var invalid *analysis.Error
	if !errors.As(err, &invalid) {
		t.Fatalf("应为typed原因：%v", err)
	}
	for _, language := range []locales.Language{locales.ZhCN, locales.En} {
		if text, err := locales.Format(language, invalid.Message); err != nil || text == "" {
			t.Fatalf("缺少%s原因文案：%v", language, err)
		}
	}
}
