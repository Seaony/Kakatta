package analysis

import (
	"fmt"
	"math"
	"strings"

	"github.com/Seaony/Kakatta/internal/locales"
	"golang.org/x/text/unicode/norm"
)

func keywordText(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(norm.NFKC.String(s))), " ")
}
func ParseKeywords(input string) []string {
	result := []string{}
	seen := map[string]bool{}
	for _, word := range strings.FieldsFunc(input, func(r rune) bool { return r == '\n' || r == '\r' || r == ',' || r == '，' }) {
		word = keywordText(word)
		if word != "" && !seen[word] {
			seen[word] = true
			result = append(result, word)
		}
	}
	return result
}
func asciiWord(r byte) bool { return r >= 'a' && r <= 'z' || r >= '0' && r <= '9' }
func keywordMatches(text, word string) bool {
	if word == "" {
		return false
	}
	bounded := true
	for _, r := range word {
		if r != ' ' && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			bounded = false
			break
		}
	}
	if !bounded {
		return strings.Contains(text, word)
	}
	for start := 0; start <= len(text)-len(word); {
		off := strings.Index(text[start:], word)
		if off < 0 {
			break
		}
		pos := start + off
		end := pos + len(word)
		if (pos == 0 || !asciiWord(text[pos-1])) && (end == len(text) || !asciiWord(text[end])) {
			return true
		}
		start = pos + 1
	}
	return false
}
func MatchingKeywords(text string, words []string) []string {
	text = keywordText(text)
	result := []string{}
	seen := map[string]bool{}
	for _, word := range words {
		word = keywordText(word)
		if !seen[word] && keywordMatches(text, word) {
			result = append(result, word)
			seen[word] = true
		}
	}
	return result
}
func failedLayer(kind RejectKind, code string, args map[string]any) LayerResult {
	return LayerResult{Rejected: true, RejectKind: kind, Reason: ptr(message(code, args)), MatchedKeywords: []string{}}
}
func moneyArg(v *int64) string {
	if v == nil {
		return "—"
	}
	n := *v
	if n%100 == 0 {
		return fmt.Sprintf("%d", n/100)
	}
	return fmt.Sprintf("%d.%02d", n/100, n%100)
}
func FirstLayer(in FirstLayerInput) LayerResult {
	if in.Price != nil && (in.MinPrice != nil && *in.Price < *in.MinPrice || in.MaxPrice != nil && *in.Price > *in.MaxPrice) {
		return failedLayer(RejectFilter, "analysis.filter_price", map[string]any{"min": moneyArg(in.MinPrice), "max": moneyArg(in.MaxPrice)})
	}
	if in.FreeShippingOnly && !in.FreeShipping {
		return failedLayer(RejectFilter, "analysis.filter_shipping", nil)
	}
	const dayMS int64 = 86400000
	if in.PublishDays > 0 && in.PublishedAt != nil && in.PublishDays <= math.MaxInt64/dayMS {
		delta := in.PublishDays * dayMS
		if in.NowMS >= math.MinInt64+delta && *in.PublishedAt < in.NowMS-delta {
			return failedLayer(RejectFilter, "analysis.filter_published", map[string]any{"days": in.PublishDays, "count": in.PublishDays})
		}
	}
	if !regionMatches(in) {
		return failedLayer(RejectFilter, "analysis.filter_region", nil)
	}
	if hits := MatchingKeywords(in.Title, in.Excludes); len(hits) > 0 {
		return failedLayer(RejectExcluded, "analysis.keyword_excluded", map[string]any{"word": hits[0]})
	}
	if in.Mode == ModeKeyword && len(in.Matches) > 0 && len(MatchingKeywords(in.Title, in.Matches)) == 0 {
		return failedLayer(RejectKeywordMiss, "analysis.keyword_miss", nil)
	}
	return LayerResult{MatchedKeywords: []string{}}
}
func regionText(s string) string {
	s = strings.TrimSpace(s)
	for _, suffix := range []string{"特别行政区", "自治区", "省", "市"} {
		if strings.HasSuffix(s, suffix) {
			return strings.TrimSuffix(s, suffix)
		}
	}
	return s
}
func regionMatches(in FirstLayerInput) bool {
	province, city, area := regionText(in.Province), regionText(in.City), regionText(in.Area)
	if in.City == "东三省" {
		city = in.City
	}
	if area == "" || province == "" && city == "" || province == "海外" || province == "全国" && city == "全国" {
		return true
	}
	// 搜索结果只有市名时，补上随程序发布的地区数据中的省名。
	for _, entry := range in.Cities {
		if c := regionText(entry.City); c != "" && area == c {
			area += " " + regionText(entry.Province)
			break
		}
	}
	groups := map[string][]string{"江浙沪": {"江苏", "浙江", "上海"}, "珠三角": {"广东"}, "京津冀": {"北京", "天津", "河北"}, "东三省": {"辽宁", "吉林", "黑龙江"}}
	if provinces, ok := groups[city]; ok {
		for _, p := range provinces {
			if strings.Contains(area, p) {
				return true
			}
		}
		return false
	}
	if strings.HasPrefix(city, "全") {
		return province != "" && strings.Contains(area, province)
	}
	return province != "" && province != "全国" && strings.Contains(area, province) || city != "" && strings.Contains(area, city)
}
func KeywordKind(title string) Kind {
	title = keywordText(title)
	if strings.HasPrefix(title, "求购") || strings.HasPrefix(title, "收购") || strings.Contains(title, "高价收") {
		return KindWanted
	}
	if rest, ok := strings.CutPrefix(title, "收"); ok {
		for _, r := range rest {
			if strings.ContainsRune("一个台部只套件副张", r) {
				return KindWanted
			}
			break
		}
	}
	if strings.Contains(title, "回收") {
		return KindRecycle
	}
	if strings.Contains(title, "出租") || strings.Contains(title, "租赁") {
		return KindRental
	}
	return ""
}
func JudgeKeyword(title, description string, matches, excludes []string) LayerResult {
	text := title + "\n" + description
	if hit := MatchingKeywords(text, excludes); len(hit) > 0 {
		return failedLayer(RejectExcluded, "analysis.keyword_excluded", map[string]any{"word": hit[0]})
	}
	hits := MatchingKeywords(text, matches)
	if len(hits) > 0 {
		return LayerResult{MatchedKeywords: hits, Reason: ptr(message("analysis.keyword_matched", map[string]any{"count": len(hits), "words": strings.Join(hits, "、")}))}
	}
	if len(matches) > 0 {
		return failedLayer(RejectKeywordMiss, "analysis.keyword_miss", nil)
	}
	return LayerResult{MatchedKeywords: []string{}, Reason: ptr(message("analysis.keyword_no_exclusion", nil))}
}
func ResolveSpecs(profile Profile, values []SpecValue, brandNew *bool, source SpecSource) ItemSpecs {
	out := ItemSpecs{Specs: make([]SpecValue, 0, len(profile.Specs)), BrandNew: clonePtr(brandNew), Source: source}
	byName := map[string]*string{}
	for _, v := range values {
		if _, exists := byName[v.Name]; !exists {
			byName[v.Name] = v.Value
		}
	}
	keys := make([]string, 0, len(profile.Specs))
	complete := true
	for _, s := range profile.Specs {
		var value *string
		if v := byName[s.Name]; v != nil {
			normalized := configText(*v)
			for _, option := range s.Options {
				if normalized != "" && configText(option) == normalized {
					value = ptr(option)
					break
				}
			}
		}
		out.Specs = append(out.Specs, SpecValue{Name: s.Name, Value: value})
		if value == nil {
			complete = false
		} else {
			keys = append(keys, configText(*value))
		}
	}
	if len(profile.Specs) == 0 {
		out.ConfigKey = ptr("*")
	} else if complete {
		out.ConfigKey = ptr(strings.Join(keys, " / "))
	}
	return out
}
func selectedSpec(profile Profile, values []SpecValue) *SpecValue {
	for i, s := range profile.Specs {
		if len(s.Values) == 0 || i >= len(values) || values[i].Value == nil {
			continue
		}
		want := configText(*values[i].Value)
		selected := false
		for _, v := range s.Values {
			if configText(v) == want {
				selected = true
				break
			}
		}
		if !selected {
			v := values[i]
			v.Value = clonePtr(v.Value)
			return &v
		}
	}
	return nil
}
func JudgeTitle(profile Profile, item TitleItem) TitleResult {
	copied := item
	copied.Specs = make([]SpecValue, 0, len(item.Specs))
	for _, v := range item.Specs {
		copied.Specs = append(copied.Specs, SpecValue{Name: v.Name, Value: clonePtr(v.Value)})
	}
	copied.BrandNew = clonePtr(item.BrandNew)
	out := TitleResult{Item: copied, Specs: ResolveSpecs(profile, item.Specs, item.BrandNew, SpecTitle), Result: LayerResult{MatchedKeywords: []string{}}, Warnings: []locales.Message{}}
	known, seen := map[string]bool{}, map[string]bool{}
	for _, spec := range profile.Specs {
		known[spec.Name] = true
	}
	for _, value := range item.Specs {
		if !known[value.Name] {
			out.Warnings = append(out.Warnings, unknownField(value.Name))
		} else if seen[value.Name] {
			out.Warnings = append(out.Warnings, ignored(value.Name))
		}
		seen[value.Name] = true
	}
	out.Specs.Kind = item.Kind
	out.Specs.Excluded = item.Verdict == "unmatched"
	allowed := item.Kind == KindSale
	for _, kind := range profile.AllowedKinds {
		if item.Kind == kind {
			allowed = true
		}
	}
	if !allowed {
		kind := item.Kind
		if !validKind(kind) {
			kind = KindOther
		}
		out.Result = failedLayer(RejectJunk, "analysis.junk."+string(kind), nil)
		out.Result.Kind = item.Kind
		return out
	}
	if mismatch := selectedSpec(profile, out.Specs.Specs); mismatch != nil {
		out.Result = failedLayer(RejectUnmatched, "analysis.spec_unmatched", map[string]any{"name": mismatch.Name, "value": *mismatch.Value})
		return out
	}
	if item.Verdict == "unmatched" {
		out.Specs.Excluded = true
		out.Result = failedLayer(RejectUnmatched, "analysis.title_unmatched", map[string]any{"reason": item.Reason})
	}
	return out
}
