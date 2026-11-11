package web

import (
	"context"
	"errors"
	"strings"

	"github.com/chromedp/cdproto"
	"github.com/chromedp/chromedp"
)

// 用户区只记录结构和布尔事实，昵称原文不进入日志。
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
	confirmed, facts, nickErr := r.loginNicknameFacts(ctx)
	result["identity_available"] = nickErr == nil && facts != nil
	result["identity_confirmed"] = nickErr == nil && confirmed
	result["nick_selector"], result["nick_nodes"] = rules.Selectors.LoginNick, facts
	r.observation("login_header", map[string]any{stage: result})
}

func (r *Runner) loginNicknameFacts(ctx context.Context) (bool, []map[string]any, error) {
	return retryLoginNickname(ctx, func() (bool, []map[string]any, error) {
		nodes, err := r.H.FindNow(ctx, rules.Selectors.LoginNick)
		if err != nil {
			return false, nil, err
		}
		facts := []map[string]any{}
		confirmed := false
		for _, node := range nodes {
			visible, err := r.H.VisibleCheckedContext(ctx, node)
			if err != nil {
				return false, facts, err
			}
			hasText, loginText := loginTextFacts(node)
			confirmed = confirmed || visible && hasText && !loginText
			if len(facts) < 4 {
				facts = append(facts, map[string]any{"visible": visible, "has_text": hasText, "login_entry_text": loginText})
			}
		}
		return confirmed, facts, nil
	})
}

func retryLoginNickname(ctx context.Context, read func() (bool, []map[string]any, error)) (bool, []map[string]any, error) {
	for range 3 {
		if err := contextError(ctx); err != nil {
			return false, nil, err
		}
		confirmed, facts, err := read()
		if canceled := contextError(ctx); canceled != nil {
			return false, nil, canceled
		}
		if err == nil {
			return confirmed, facts, nil
		}
		var protocol *cdproto.Error
		if !errors.As(err, &protocol) || protocol.Code != -32000 || protocol.Message != "Could not find node with given id" {
			return false, facts, err
		}
	}
	// 扫码后的页面正在替换，读不清时下一轮再确认，不能借旧节点放行。
	return false, nil, nil
}

func loginTextFacts(node *chromedp.Node) (bool, bool) {
	var text strings.Builder
	for _, child := range node.Children {
		if child.NodeType == 3 {
			text.WriteString(child.NodeValue)
		}
	}
	value := strings.TrimSpace(text.String())
	return value != "", value == rules.Text.LoginEntry
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
		hasText, loginText := loginTextFacts(node)
		result = append(result, map[string]any{"depth": depth, "tag": tag, "class_prefix": classPrefix(node.AttributeValue("class")), "attribute_names": attributes, "has_text": hasText, "login_entry_text": loginText, "avatar_candidate": tag == "img"})
		for _, child := range node.Children {
			walk(child, depth+1)
		}
	}
	walk(root, 0)
	return result
}
