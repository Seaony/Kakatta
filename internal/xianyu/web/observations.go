package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/Seaony/Kakatta/internal/browser"
	"github.com/Seaony/Kakatta/internal/xianyu"
	"github.com/chromedp/chromedp"
)

func nodeContainer(n *chromedp.Node) map[string]any {
	if n == nil {
		return map[string]any{"found": false}
	}
	if n.Parent != nil {
		n = n.Parent
	}
	return map[string]any{"found": true, "tag": strings.ToLower(n.NodeName), "id": n.AttributeValue("id"), "class_prefix": classPrefix(n.AttributeValue("class"))}
}

func responseObject(body []byte) map[string]any {
	var out map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(xianyu.StripJSONP(body))))
	decoder.UseNumber()
	if decoder.Decode(&out) != nil {
		return nil
	}
	return out
}

func objectAt(root map[string]any, path ...string) map[string]any {
	for _, key := range path {
		root, _ = root[key].(map[string]any)
	}
	return root
}

func (r *Runner) observeDetail(body []byte) bool {
	root := responseObject(body)
	item := objectAt(root, "data", "itemDO")
	var descriptionPath string
	for _, field := range []string{"desc", "description", "richTextDesc"} {
		if _, ok := item[field].(string); ok {
			descriptionPath = "data.itemDO." + field
			break
		}
	}
	r.observation("detail_description_path", map[string]any{"found": descriptionPath != "", "path": descriptionPath})
	status := map[string]any{"found": false}
	for _, field := range []string{"itemStatus", "status", "saleStatus"} {
		if value, exists := item[field]; exists {
			switch value.(type) {
			case string, json.Number, bool:
				status = map[string]any{"found": true, "path": "data.itemDO." + field, "value": value}
			}
			if status["found"] == true {
				break
			}
		}
	}
	r.observation("detail_sale_status", status)
	return item != nil && descriptionPath != "" && objectAt(root, "data", "sellerDO") != nil
}

func (r *Runner) observeSellerHead(body []byte) {
	module := objectAt(responseObject(body), "data", "module")
	item := objectAt(module, "tabs", "item")
	value, known := item["number"]
	r.observation("seller_item_count", map[string]any{"path": "data.module.tabs.item.number", "field_present": known, "value": value, "meaning_confirmed": false})
	base := objectAt(module, "base")
	tags, _ := base["ylzTags"].([]any)
	pairs := []map[string]any{}
	for _, raw := range tags {
		tag, _ := raw.(map[string]any)
		attributes := objectAt(tag, "attributes")
		if attributes["role"] == "seller" {
			pairs = append(pairs, map[string]any{"level": attributes["level"], "text": tag["text"]})
		}
	}
	r.observation("seller_credit_mapping", map[string]any{"pairs": pairs, "observed_count": len(pairs)})
}

func (r *Runner) observeSearch(body []byte) {
	root := responseObject(body)
	data := objectAt(root, "data")
	list, _ := data["resultList"].([]any)
	sellerPaths := []string{}
	areas := map[string]int{}
	for _, entry := range list {
		main := objectAt(asObject(entry), "data", "item", "main")
		walkFields(main, "data.resultList[].data.item.main", func(path, key string, value any) {
			low := strings.ToLower(key)
			if low == "sellerid" || low == "userid" {
				sellerPaths = appendUnique(sellerPaths, path)
			}
			if low == "area" {
				if text, ok := value.(string); ok {
					areas[text]++
				}
			}
		})
	}
	r.observation("search_seller_id", map[string]any{"found": len(sellerPaths) > 0, "paths": sellerPaths, "sample_count": len(list)})
	r.observation("search_area_format", map[string]any{"samples": areas, "sample_count": len(list)})
}

func (r *Runner) observeRatings(body []byte) {
	data := objectAt(responseObject(body), "data")
	list, _ := data["cardList"].([]any)
	formats := map[string]int{}
	for _, entry := range list {
		card := objectAt(asObject(entry), "cardData")
		if value, exists := card["gmtCreate"]; exists {
			kind := fmt.Sprintf("%T", value)
			if _, ok := value.(json.Number); ok {
				kind = "numeric_timestamp"
			}
			if text, ok := value.(string); ok {
				switch {
				case len(text) == 10 && strings.Count(text, "-") == 2:
					kind = "date_text"
				case strings.Contains(text, ":"):
					kind = "datetime_text"
				default:
					kind = "other_text"
				}
			}
			formats[kind]++
		}
	}
	r.observation("rating_time_format", map[string]any{"formats": formats, "sample_count": len(list)})
}

func asObject(value any) map[string]any { out, _ := value.(map[string]any); return out }
func appendUnique(items []string, value string) []string {
	for _, item := range items {
		if item == value {
			return items
		}
	}
	return append(items, value)
}
func walkFields(root map[string]any, path string, visit func(string, string, any)) {
	for key, value := range root {
		next := path + "." + key
		visit(next, key, value)
		switch child := value.(type) {
		case map[string]any:
			walkFields(child, next, visit)
		case []any:
			for _, item := range child {
				if obj, ok := item.(map[string]any); ok {
					walkFields(obj, next+"[]", visit)
				}
			}
		}
	}
}

func (r *Runner) observeIframes(ctx context.Context, current string, force bool) {
	r.observationMu.Lock()
	if !force && r.observedFrameURL == current {
		r.observationMu.Unlock()
		return
	}
	r.observedFrameURL = current
	r.observationMu.Unlock()
	nodes, err := r.H.FindNow(ctx, "iframe")
	if err != nil {
		return
	}
	urls := []string{}
	for _, node := range nodes {
		if source := node.AttributeValue("src"); source != "" {
			urls = append(urls, diagnosticURL(source))
		}
	}
	r.observation("iframe_urls", map[string]any{"frame_count": len(nodes), "sources": urls})
}

func retCodes(ret []string) []string {
	codes := []string{}
	for _, item := range ret {
		code, _, _ := strings.Cut(item, "::")
		if len(code) > 64 {
			continue
		}
		known := strings.HasPrefix(code, "FAIL_") || strings.HasPrefix(code, "SUCCESS") || strings.HasPrefix(code, "RGV")
		for _, r := range code {
			if r != '_' && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
				known = false
			}
		}
		if known {
			codes = appendUnique(codes, code)
		}
	}
	return codes
}

func (r *Runner) signal(fields map[string]any) {
	r.observedEvent("风控信号", fields)
}

func (r *Runner) observedEvent(event string, fields map[string]any) {
	identity := fields
	if _, response := fields["响应序号"]; response {
		identity = make(map[string]any, len(fields))
		for key, value := range fields {
			if key != "步骤" {
				identity[key] = value
			}
		}
	}
	encoded, _ := json.Marshal(identity)
	key := event + ":" + string(encoded)
	r.observationMu.Lock()
	if r.observed == nil {
		r.observed = map[string]string{}
	}
	_, seen := r.observed[key]
	r.observed[key] = "1"
	r.observationMu.Unlock()
	if !seen && r.Log != nil {
		r.Log(event, fields)
	}
}

func (r *Runner) observeResponseRisk(where string, response *browser.Response, kind xianyu.RetKind, ret []string) {
	fields := responseRiskFields(where, response, kind, ret)
	if kind == xianyu.RetSessionExpired {
		fields["判定"] = "仅观察"
		r.observedEvent("接口登录观察", fields)
		return
	}
	r.signal(fields)
}

func (r *Runner) observeLoginEntry(ctx context.Context) {
	nodes, err := r.H.SearchText(ctx, rules.Text.LoginEntry)
	if err != nil {
		return
	}
	candidates := []map[string]any{}
	for _, node := range nodes {
		visible, err := r.H.VisibleCheckedContext(ctx, node)
		if err != nil {
			return
		}
		if visible {
			candidates = append(candidates, map[string]any{"tag": strings.ToLower(node.NodeName), "id": node.AttributeValue("id"), "class_prefix": classPrefix(node.AttributeValue("class"))})
		}
	}
	r.observation("login_selectors", map[string]any{"entry_visible_count": len(candidates), "entry_candidates": candidates})
}

func responseRiskFields(where string, response *browser.Response, kind xianyu.RetKind, ret []string) map[string]any {
	sources := []string{}
	for _, value := range ret {
		if strings.Contains(value, "FAIL_SYS_USER_VALIDATE") || strings.Contains(value, "RGV587") || strings.Contains(value, "FAIL_SYS_SESSION_EXPIRED") {
			sources = append(sources, "ret")
			break
		}
	}
	data := objectAt(responseObject(response.Body), "data")
	raw, _ := data["url"].(string)
	lower := strings.ToLower(raw)
	punished := strings.Contains(lower, "punish")
	action := "unknown"
	if punished {
		sources = append(sources, "data.url")
		if parsed, err := url.Parse(lower); err == nil {
			switch parsed.Query().Get("action") {
			case "deny":
				action = "deny"
			case "captcha":
				action = "captcha"
			}
		}
	}
	if len(sources) == 0 {
		sources = append(sources, "ret")
	}
	fields := map[string]any{"步骤": where, "来源": strings.Join(sources, ","), "信号": kind.String(), "ret_codes": retCodes(ret), "响应序号": response.Seq, "到达顺序": response.BodyOrder, "网址": diagnosticURL(response.URL), "处罚网址命中": punished}
	if punished {
		fields["处罚动作"] = action
	}
	return fields
}
