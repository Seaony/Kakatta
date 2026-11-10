package web

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

func TestBuiltEntryReferencesExistingLocalAssets(t *testing.T) {
	assets := Assets()
	entry, err := fs.ReadFile(assets, "index.html")
	if err != nil || !strings.Contains(string(entry), `<div id="root">`) {
		t.Fatalf("请先构建网页：%v", err)
	}
	links := regexp.MustCompile(`(?:src|href)="(/assets/[^"?]+)"`).FindAllStringSubmatch(string(entry), -1)
	if len(links) < 2 {
		t.Fatal("网页缺少构建好的脚本或样式")
	}
	for _, link := range links {
		if _, err := fs.ReadFile(assets, strings.TrimPrefix(link[1], "/")); err != nil {
			t.Fatalf("网页引用了缺失资源：%s", link[1])
		}
	}
}
