package prompts

import (
	"os"
	"strings"
	"testing"

	"github.com/Seaony/Kakatta/internal/analysis/client"
)

func TestSellerSystemBoundaryMatchesDocumentAndEveryTemplate(t *testing.T) {
	document, err := os.ReadFile("../../../docs/DOMAIN.md")
	if err != nil {
		t.Fatal(err)
	}
	if docBlock(t, string(document), "**固定附加的系统说明**", "```") != SellerDataSystemSuffix+"\n" {
		t.Fatal("实际系统说明与规格原文不同")
	}
	definitions, err := Definitions()
	if err != nil {
		t.Fatal(err)
	}
	for _, definition := range definitions {
		for _, custom := range []bool{false, true} {
			content := definition.Default
			if custom {
				content = "自定义模板，保留输出键："
				for _, key := range definition.RequiredKeys {
					content += `"` + key + `" `
				}
			}
			snapshot, err := Resolve(map[client.Kind]string{definition.Kind: content})
			if err != nil {
				t.Fatal(err)
			}
			system, _, err := requestParts(snapshot, definition.Kind)
			if err != nil || system != content+"\n\n"+SellerDataSystemSuffix || strings.Count(system, SellerDataSystemSuffix) != 1 {
				t.Fatalf("%s 缺少统一系统说明或改写了模板：%v", definition.Kind, err)
			}
			if snapshot.Contents[definition.Kind] != content {
				t.Fatal("附加说明污染了原模板")
			}
		}
	}
}
