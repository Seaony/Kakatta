package web

import (
	"encoding/json"
	"fmt"
	"sort"
	"unicode/utf8"
)

type missingFieldShape struct {
	Path     string `json:"path"`
	Type     string `json:"type"`
	Nonempty bool   `json:"nonempty"`
}

type missingFieldValue struct {
	missingFieldShape
	Value any `json:"value,omitempty"`
}

func (r *Runner) observeMissingSearchNick(main map[string]any) {
	if main == nil {
		return
	}
	ex := objectAt(main, "exContent")
	nick, _ := ex["userNickName"].(string)
	if nick != "" || r.searchNickHints >= 3 {
		return
	}
	r.searchNickHints++
	value, present := ex["userNickName"]
	path := "data.resultList[].data.item.main"
	hidden, hiddenPresent := objectAt(main, "clickParam", "args")["HideUserInfo"]
	r.observation("search_nick_missing_fields", map[string]any{
		fmt.Sprintf("sample_%d", r.searchNickHints): map[string]any{
			"expected": describeMissingField(path+".exContent.userNickName", value, present), "nearby": missingNearbyFields(main, path),
			"hide_user_info": describePublicMissingField(path+".clickParam.args.HideUserInfo", hidden, hiddenPresent, 64),
		},
	})
}

func (r *Runner) observeMissingDetailCredit(root map[string]any) {
	sellerValue, sellerPresent := objectAt(root, "data")["sellerDO"]
	seller := asObject(sellerValue)
	credit := objectAt(seller, "zhimaLevelInfo")
	text, _ := credit["levelName"].(string)
	if text != "" || r.detailCreditHints >= 3 {
		return
	}
	r.detailCreditHints++
	value, present := credit["levelName"]
	nearby := missingNearbyFields(sellerValue, "data.sellerDO")
	if !sellerPresent {
		nearby = []missingFieldShape{describeMissingField("data.sellerDO", nil, false)}
	}
	tagValue, tagPresent := seller["idleFishCreditTag"]
	tag := asObject(tagValue)
	level, levelPresent := objectAt(tag, "trackParams")["sellerLevel"]
	icon, iconPresent := tag["iconUrl"]
	auth, authPresent := seller["zhimaAuth"]
	authField := missingFieldValue{missingFieldShape: describeMissingField("data.sellerDO.zhimaAuth", auth, authPresent)}
	if value, ok := auth.(bool); ok {
		authField.Value = value
	}
	values := []missingFieldValue{
		describePublicMissingField("data.sellerDO.idleFishCreditTag.trackParams.sellerLevel", level, levelPresent, 64),
		describePublicMissingField("data.sellerDO.idleFishCreditTag.iconUrl", icon, iconPresent, 4096),
		authField,
	}
	for _, key := range []string{"itemCount", "hasSoldNumInteger", "avgReply30dLong", "newGoodRatioRate", "lastVisitTime"} {
		value, present := seller[key]
		values = append(values, describePublicMissingField("data.sellerDO."+key, value, present, 64))
	}
	tagFields := missingCreditTagFields(tagValue)
	if !tagPresent {
		tagFields = []missingFieldShape{describeMissingField("data.sellerDO.idleFishCreditTag", nil, false)}
	}
	r.observation("detail_credit_missing_fields", map[string]any{
		fmt.Sprintf("sample_%d", r.detailCreditHints): map[string]any{
			"expected": describeMissingField("data.sellerDO.zhimaLevelInfo.levelName", value, present), "nearby": nearby,
			"credit_tag_nearby": tagFields, "values": values,
		},
	})
}

// 只供上面固定的公开字段使用，动态对象不取值，长文字不截半。
func describePublicMissingField(path string, value any, present bool, maxText int) missingFieldValue {
	field := missingFieldValue{missingFieldShape: describeMissingField(path, value, present)}
	if !present {
		return field
	}
	switch value := value.(type) {
	case bool:
		field.Value = value
	case string:
		if utf8.RuneCountInString(value) <= maxText {
			field.Value = value
		}
	case json.Number:
		text := value.String()
		if len(text) > 0 && len(text) <= 64 && (text[0] == '-' || text[0] >= '0' && text[0] <= '9') && json.Valid([]byte(text)) {
			field.Value = value
		}
	}
	return field
}

// 信用标签单独看完整结构，不借用整体附近字段的40项截断，也不取字段值。
func missingCreditTagFields(root any) []missingFieldShape {
	fields := []missingFieldShape{}
	seen := map[missingFieldShape]bool{}
	appendField := func(path string, value any) {
		field := describeMissingField(path, value, true)
		if !seen[field] {
			seen[field] = true
			fields = append(fields, field)
		}
	}
	var walk func(any, string)
	walk = func(value any, path string) {
		appendField(path, value)
		switch value := value.(type) {
		case map[string]any:
			keys := []string{}
			for key, child := range value {
				if missingFieldKey(key) {
					keys = append(keys, key)
				} else {
					appendField(path+".[other]", child)
				}
			}
			sort.Strings(keys)
			for _, key := range keys {
				walk(value[key], path+"."+key)
			}
		case []any:
			for _, child := range value {
				walk(child, path+"[]")
			}
		}
	}
	walk(root, "data.sellerDO.idleFishCreditTag")
	return fields
}

func (r *Runner) observeMissingOwnItemCount(body []byte, known bool) {
	if known || r.ownCountHints >= 3 {
		return
	}
	r.ownCountHints++
	moduleValue, present := objectAt(responseObject(body), "data")["module"]
	module := asObject(moduleValue)
	item := objectAt(module, "tabs", "item")
	value, found := item["number"]
	nearby := missingNearbyFields(moduleValue, "data.module")
	if !present {
		nearby = []missingFieldShape{describeMissingField("data.module", nil, false)}
	}
	r.observation("own_item_count_missing_fields", map[string]any{
		fmt.Sprintf("sample_%d", r.ownCountHints): map[string]any{"expected": describeMissingField("data.module.tabs.item.number", value, found), "nearby": nearby},
	})
}

func describeMissingField(path string, value any, present bool) missingFieldShape {
	out := missingFieldShape{Path: path, Type: "missing"}
	if !present {
		return out
	}
	switch v := value.(type) {
	case nil:
		out.Type = "null"
	case string:
		out.Type = "string"
		out.Nonempty = v != ""
	case json.Number:
		out.Type = "number"
		out.Nonempty = true
	case bool:
		out.Type = "boolean"
		out.Nonempty = true
	case map[string]any:
		out.Type = "object"
		out.Nonempty = len(v) > 0
	case []any:
		out.Type = "array"
		out.Nonempty = len(v) > 0
	default:
		out.Type = "unknown"
	}
	return out
}

func missingNearbyFields(root any, path string) []missingFieldShape {
	out := []missingFieldShape{}
	var walk func(any, string, int)
	walk = func(value any, path string, depth int) {
		if len(out) >= 40 {
			return
		}
		out = append(out, describeMissingField(path, value, true))
		if depth >= 3 {
			return
		}
		switch v := value.(type) {
		case map[string]any:
			keys := []string{}
			var hidden any
			hasHidden := false
			for key, child := range v {
				if !missingFieldKey(key) {
					if !hasHidden {
						hidden, hasHidden = child, true
					}
					continue
				}
				if len(keys) < 40 {
					keys = append(keys, key)
				}
			}
			sort.Strings(keys)
			for _, key := range keys {
				walk(v[key], path+"."+key, depth+1)
			}
			if hasHidden && len(out) < 40 {
				out = append(out, describeMissingField(path+".[other]", hidden, true))
			}
		case []any:
			for _, child := range v[:min(len(v), 2)] {
				walk(child, path+"[]", depth+1)
			}
		}
	}
	walk(root, path, 0)
	return out
}

// 路径只保留普通字段名，不带数字账号、UUID 或任意文本键。
func missingFieldKey(key string) bool {
	if len(key) == 0 || len(key) > 48 {
		return false
	}
	hex := len(key) == 32
	for i, c := range []byte(key) {
		letter := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
		digit := c >= '0' && c <= '9'
		if !letter && c != '_' && (!digit || i == 0) {
			return false
		}
		hex = hex && (digit || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F')
	}
	return !hex
}
