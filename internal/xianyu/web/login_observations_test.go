package web

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/chromedp"
)

func loginHeaderTestNode(tag string, attributes []string, children ...*chromedp.Node) *chromedp.Node {
	return &chromedp.Node{Node: &cdp.Node{NodeType: 1, NodeName: tag, Attributes: attributes}, Children: children}
}

func loginHeaderTestText(text string) *chromedp.Node {
	return &chromedp.Node{Node: &cdp.Node{NodeType: 3, NodeValue: text}}
}

func TestLoginHeaderStructureDoesNotExposeAccountTextOrAttributeValues(t *testing.T) {
	root := loginHeaderTestNode("DIV", []string{"class", "user-order-container--fixture", "id", "private-account-id"},
		loginHeaderTestNode("A", []string{"href", "/personal?userId=private-account-number"},
			loginHeaderTestNode("IMG", []string{"src", "https://local.invalid/private-avatar?token=private-token", "alt", "private-nickname"}),
			loginHeaderTestNode("DIV", []string{"class", "nick--fixture"}, loginHeaderTestText("private-nickname")),
			loginHeaderTestNode("SPAN", nil, loginHeaderTestText(rules.Text.LoginEntry))),
		loginHeaderTestNode("SCRIPT", nil, loginHeaderTestText("private-script")))
	nodes := loginHeaderNodes(root)
	encoded, err := json.Marshal(nodes)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private-") || strings.Contains(string(encoded), "local.invalid") {
		t.Fatal("用户区观察泄漏了账号、属性值或脚本")
	}
	var avatar, nickname, entry bool
	for _, node := range nodes {
		avatar = avatar || node["avatar_candidate"] == true
		nickname = nickname || node["class_prefix"] == "nick" && node["has_text"] == true
		entry = entry || node["login_entry_text"] == true
	}
	if !avatar || !nickname || !entry || len(nodes) != 5 {
		t.Fatal("必须保留供回填选择器的结构和布尔事实", nodes)
	}
}

func TestLoginHeaderStructureIsBounded(t *testing.T) {
	root := loginHeaderTestNode("DIV", nil)
	for range 100 {
		root.Children = append(root.Children, loginHeaderTestNode("DIV", nil))
	}
	if nodes := loginHeaderNodes(root); len(nodes) != 40 {
		t.Fatal("观察结构必须有固定上限", len(nodes))
	}
}
