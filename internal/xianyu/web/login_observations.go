package web

import (
	"context"
	"strings"

	"github.com/chromedp/chromedp"
)

// 未取得已登录页面样本前，只记用户区结构，不能把头像存在当成已登录。
func (r *Runner) observeLoginHeader(ctx context.Context, stage string) {
	result := map[string]any{"selector": rules.Selectors.LoginHeader, "identity_confirmed": false}
	nodes, err := r.H.FindNow(ctx, rules.Selectors.LoginHeader)
	if err != nil {
		result["available"] = false
		r.observation("login_header", map[string]any{stage: result})
		return
	}
	result["available"], result["matched_count"] = true, len(nodes)
	structures := []map[string]any{}
	for _, node := range nodes {
		visible, err := r.H.VisibleCheckedContext(ctx, node)
		if err != nil {
			result["available"] = false
			break
		}
		structures = append(structures, map[string]any{"visible": visible, "nodes": loginHeaderNodes(node)})
		if len(structures) == 4 {
			break
		}
	}
	result["structures"] = structures
	r.observation("login_header", map[string]any{stage: result})
}

func loginHeaderNodes(root *chromedp.Node) []map[string]any {
	result := []map[string]any{}
	var walk func(*chromedp.Node, int)
	walk = func(node *chromedp.Node, depth int) {
		if depth > 6 || len(result) >= 40 || node.NodeType != 1 {
			return
		}
		tag := strings.ToLower(node.NodeName)
		if tag == "script" || tag == "style" {
			return
		}
		attributes := []string{}
		for i := 0; i+1 < len(node.Attributes); i += 2 {
			attributes = append(attributes, node.Attributes[i])
		}
		hasText, loginText := false, false
		for _, child := range node.Children {
			if child.NodeType == 3 {
				text := strings.TrimSpace(child.NodeValue)
				hasText = hasText || text != ""
				loginText = loginText || text == rules.Text.LoginEntry
			}
		}
		result = append(result, map[string]any{"depth": depth, "tag": tag, "class_prefix": classPrefix(node.AttributeValue("class")), "attribute_names": attributes, "has_text": hasText, "login_entry_text": loginText, "avatar_candidate": tag == "img"})
		for _, child := range node.Children {
			walk(child, depth+1)
		}
	}
	walk(root, 0)
	return result
}
