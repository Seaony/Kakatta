package analysis

import (
	"bytes"
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/Seaony/Kakatta/internal/locales"
	"golang.org/x/text/unicode/norm"
)

type rawObject map[string]json.RawMessage

func jsonObject(body []byte) (rawObject, error) {
	var result rawObject
	if err := json.Unmarshal(body, &result); err == nil && result != nil {
		return result, nil
	}
	for start, b := range body {
		if b != '{' {
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(body[start:]))
		var obj rawObject
		if dec.Decode(&obj) == nil && obj != nil {
			return obj, nil
		}
	}
	return nil, ruleError("analysis.invalid_json", nil)
}
func rawArray(b json.RawMessage) ([]json.RawMessage, bool) {
	var v []json.RawMessage
	err := json.Unmarshal(b, &v)
	return v, err == nil && v != nil
}
func rawString(b json.RawMessage) (string, bool) {
	var v string
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		return "", false
	}
	err := json.Unmarshal(b, &v)
	return v, err == nil
}
func nullableString(b json.RawMessage) (*string, bool) {
	if bytes.Equal(b, []byte("null")) {
		return nil, true
	}
	v, ok := rawString(b)
	if !ok {
		return nil, false
	}
	return ptr(v), true
}
func nullableBool(b json.RawMessage) (*bool, bool) {
	if bytes.Equal(b, []byte("null")) {
		return nil, true
	}
	if len(b) == 0 {
		return nil, false
	}
	var v bool
	if json.Unmarshal(b, &v) != nil {
		return nil, false
	}
	return ptr(v), true
}
func invalidField(field string) error {
	return ruleError("analysis.invalid_result", map[string]any{"field": field})
}
func ignored(item string) locales.Message {
	return message("analysis.item_ignored", map[string]any{"item": item})
}
func unknownField(field string) locales.Message {
	return message("analysis.unknown_field", map[string]any{"field": field})
}
func missingEvidence(item string) locales.Message {
	return message("analysis.evidence_missing", map[string]any{"item": item})
}

var imageReference = regexp.MustCompile(`第([-负]?(?:[0-9]+|[零〇一二两三四五六七八九十百千万亿]+))张图`)

// 只认完整的中文整数，省略“零”产生歧义的写法不猜。
func chineseImageNumber(text string) (int64, bool) {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "〇", "零"), "两", "二")
	if text == "零" {
		return 0, true
	}
	for _, unit := range []struct {
		name  string
		value int64
	}{{"亿", 100000000}, {"万", 10000}} {
		if !strings.Contains(text, unit.name) {
			continue
		}
		if strings.Count(text, unit.name) != 1 {
			return 0, false
		}
		left, right, _ := strings.Cut(text, unit.name)
		high, ok := chineseImageNumber(left)
		if !ok || high == 0 {
			return 0, false
		}
		low := int64(0)
		if right != "" {
			remainder, zero := strings.CutPrefix(right, "零")
			low, ok = chineseImageNumber(remainder)
			if !ok || low == 0 || low >= unit.value || zero != (low < unit.value/10) {
				return 0, false
			}
		}
		if high > (math.MaxInt64-low)/unit.value {
			return 0, false
		}
		return high*unit.value + low, true
	}
	digits := map[rune]int64{'零': 0, '一': 1, '二': 2, '三': 3, '四': 4, '五': 5, '六': 6, '七': 7, '八': 8, '九': 9}
	total, digit, previous := int64(0), int64(-1), int64(10000)
	used, zero := false, false
	for _, r := range text {
		if value, ok := digits[r]; ok {
			if value == 0 {
				if !used || digit >= 0 || zero {
					return 0, false
				}
				zero = true
			} else {
				if digit >= 0 {
					return 0, false
				}
				digit = value
			}
			continue
		}
		unit := int64(0)
		switch r {
		case '十':
			unit = 10
		case '百':
			unit = 100
		case '千':
			unit = 1000
		}
		if unit == 0 || unit >= previous || used && zero != (unit < previous/10) {
			return 0, false
		}
		if digit < 0 {
			if unit != 10 || used {
				return 0, false
			}
			digit = 1
		}
		total += digit * unit
		digit, previous, used, zero = -1, unit, true, false
	}
	if digit < 0 {
		return total, used && !zero
	}
	if used && zero != (previous > 10) {
		return 0, false
	}
	return total + digit, true
}

func imageEvidencePresent(evidence string, count *int) bool {
	if count == nil {
		return true
	}
	if *count <= 0 {
		return false
	}
	text := strings.Map(func(r rune) rune {
		if r == '−' {
			return '-'
		}
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, norm.NFKC.String(evidence))
	maximum := strconv.Itoa(*count)
	for _, match := range imageReference.FindAllStringSubmatch(text, -1) {
		index := match[1]
		if strings.HasPrefix(index, "-") || strings.HasPrefix(index, "负") {
			return false
		}
		if index[0] < '0' || index[0] > '9' {
			value, ok := chineseImageNumber(index)
			if !ok {
				continue
			}
			index = strconv.FormatInt(value, 10)
		}
		index = strings.TrimLeft(index, "0")
		if index == "" || len(index) > len(maximum) || len(index) == len(maximum) && index > maximum {
			return false
		}
	}
	return true
}

func missingImageEvidence(item string) locales.Message {
	return message("analysis.image_evidence_missing", map[string]any{"item": item})
}

func decodeSpecs(b json.RawMessage) ([]SpecValue, bool) {
	rows, ok := rawArray(b)
	if !ok {
		return nil, false
	}
	out := make([]SpecValue, 0, len(rows))
	for _, row := range rows {
		var obj rawObject
		if json.Unmarshal(row, &obj) != nil || obj == nil {
			return nil, false
		}
		name, ok := rawString(obj["name"])
		if !ok {
			return nil, false
		}
		value, ok := nullableString(obj["value"])
		if !ok {
			return nil, false
		}
		out = append(out, SpecValue{Name: name, Value: value})
	}
	return out, true
}
func ParseTitleScreen(body []byte, count int) (TitleScreen, []locales.Message, error) {
	out := TitleScreen{Items: []TitleItem{}}
	warnings := []locales.Message{}
	if count < 1 || count > 20 {
		return out, warnings, invalidField("count")
	}
	obj, err := jsonObject(body)
	if err != nil {
		return out, warnings, err
	}
	rows, ok := rawArray(obj["items"])
	if !ok {
		return out, warnings, invalidField("items")
	}
	out.Items = make([]TitleItem, count)
	for i := range out.Items {
		out.Items[i] = TitleItem{No: i + 1, Kind: KindSale, Specs: []SpecValue{}, Verdict: "pass"}
	}
	seen := map[int]bool{}
	for _, row := range rows {
		var fields rawObject
		var n int
		if json.Unmarshal(row, &fields) != nil || fields == nil || json.Unmarshal(fields["no"], &n) != nil || n < 1 || n > count {
			warnings = append(warnings, ignored("items"))
			continue
		}
		kind, kindOK := rawString(fields["kind"])
		specs, specsOK := decodeSpecs(fields["specs"])
		brand, brandOK := nullableBool(fields["brand_new"])
		verdict, verdictOK := rawString(fields["verdict"])
		reason, reasonOK := rawString(fields["reason"])
		if seen[n] || !kindOK || !validKind(Kind(kind)) || !specsOK || !brandOK || !verdictOK || (verdict != "pass" && verdict != "unmatched") || !reasonOK {
			warnings = append(warnings, ignored("items"))
			continue
		}
		seen[n] = true
		out.Items[n-1] = TitleItem{No: n, Kind: Kind(kind), Specs: specs, BrandNew: brand, Verdict: verdict, Reason: reason}
	}
	for i := range out.Items {
		if !seen[i+1] {
			warnings = append(warnings, message("analysis.item_missing", map[string]any{"no": i + 1}))
		}
	}
	return out, warnings, nil
}
func ParseFacts(body []byte, profile Profile, originals EvidenceOriginals) (Facts, []locales.Message, error) {
	out := Facts{Specs: []SpecValue{}, Musts: []RequirementFact{}, Bonuses: []RequirementFact{}, Checks: []CheckFact{}, SellerSignals: []SellerSignal{}, ExtraFindings: []ExtraFinding{}}
	warnings := []locales.Message{}
	if err := validateProfileRevisions(profile); err != nil {
		return out, warnings, err
	}
	obj, err := jsonObject(body)
	if err != nil {
		return out, warnings, err
	}
	rows, ok := rawArray(obj["checks"])
	if !ok {
		return out, warnings, invalidField("checks")
	}
	var adjustment rawObject
	var n float64
	if json.Unmarshal(obj["adjust"], &adjustment) != nil || adjustment == nil || len(adjustment["value"]) == 0 || bytes.Equal(adjustment["value"], []byte("null")) || json.Unmarshal(adjustment["value"], &n) != nil || math.IsNaN(n) || math.IsInf(n, 0) {
		return out, warnings, invalidField("adjust.value")
	}
	out.Summary, ok = rawString(obj["summary"])
	if !ok {
		return out, warnings, invalidField("summary")
	}
	reason, _ := rawString(adjustment["reason"])
	if strings.TrimSpace(reason) != "" {
		out.Adjust = Adjustment{Value: int(math.Max(-15, math.Min(8, math.Round(n)))), Reason: reason}
	}
	titleText := originals.Title + "\n" + originals.Description
	sellerText := originals.SellerIntroduction + "\n" + strings.Join(originals.RecentRateTexts, "\n")
	values := []SpecValue{}
	if raw, exists := obj["specs"]; exists && !bytes.Equal(raw, []byte("null")) {
		specRows, valid := rawArray(raw)
		if !valid {
			warnings = append(warnings, ignored("specs"))
		} else {
			allowed := map[string]bool{}
			for _, s := range profile.Specs {
				allowed[s.Name] = true
			}
			seen := map[string]bool{}
			for _, row := range specRows {
				vs, valid := decodeSpecs(append(append([]byte{'['}, row...), ']'))
				if !valid || len(vs) != 1 {
					warnings = append(warnings, ignored("specs"))
					continue
				}
				v := vs[0]
				if !allowed[v.Name] {
					warnings = append(warnings, unknownField(v.Name))
					continue
				}
				if seen[v.Name] {
					warnings = append(warnings, ignored(v.Name))
					continue
				}
				seen[v.Name] = true
				values = append(values, v)
			}
		}
	}
	brand, _ := nullableBool(obj["brand_new"])
	out.BrandNew = brand
	out.Specs = ResolveSpecs(profile, values, brand, SpecDetail).Specs
	out.Musts, warnings = parseRequirements(obj["musts"], profile.Musts, titleText, originals.ImageCount, warnings)
	out.Bonuses, warnings = parseRequirements(obj["bonuses"], profile.Bonuses, titleText, originals.ImageCount, warnings)
	allowed := map[string]int64{}
	for _, r := range profile.Risks {
		if !strings.HasPrefix(r.ID, "seller_") {
			allowed[r.ID] = profileRevision(r.Rev)
		}
	}
	byKey := map[string]CheckFact{}
	for _, row := range rows {
		var fields rawObject
		if json.Unmarshal(row, &fields) != nil || fields == nil {
			return out, warnings, invalidField("checks")
		}
		key, keyOK := rawString(fields["key"])
		status, statusOK := rawString(fields["status"])
		if !keyOK || !statusOK || !validCheck(CheckStatus(status)) {
			return out, warnings, invalidField("checks")
		}
		rev, known := allowed[key]
		if !known {
			warnings = append(warnings, unknownField(key))
			continue
		}
		if _, exists := byKey[key]; exists {
			warnings = append(warnings, ignored(key))
			continue
		}
		src, _ := nullableString(fields["source"])
		quote, _ := nullableString(fields["evidence"])
		fact := CheckFact{Key: key, Status: CheckStatus(status), Source: src, Evidence: quote, Rev: rev}
		validSource := src != nil && (*src == "text" || *src == "image")
		missingImage := src != nil && *src == "image" && quote != nil && !imageEvidencePresent(*quote, originals.ImageCount)
		if fact.Status != CheckUnknown && (!validSource || quote == nil || strings.TrimSpace(*quote) == "" || *src == "text" && !evidencePresent(titleText, *quote) || missingImage) {
			if missingImage {
				warnings = append(warnings, missingImageEvidence(key))
			} else {
				warnings = append(warnings, missingEvidence(key))
			}
			switch fact.Status {
			case CheckRisk:
				fact.Status = CheckSuspect
			case CheckOK:
				fact.Status = CheckUnknown
			}
			if !validSource {
				fact.Source = nil
			}
		}
		if fact.Status == CheckUnknown {
			fact.Source = nil
			fact.Evidence = nil
		}
		byKey[key] = fact
	}
	for _, r := range profile.Risks {
		if strings.HasPrefix(r.ID, "seller_") {
			continue
		}
		if f, exists := byKey[r.ID]; exists {
			out.Checks = append(out.Checks, f)
		} else {
			out.Checks = append(out.Checks, CheckFact{Key: r.ID, Status: CheckUnknown, Rev: profileRevision(r.Rev)})
		}
	}
	signalRows, _ := rawArray(obj["seller_signals"])
	signalSeen := map[string]bool{}
	for _, row := range signalRows {
		var signal SellerSignal
		if json.Unmarshal(row, &signal) != nil || !validSellerSignal(signal.Key) || signal.Evidence == "" {
			warnings = append(warnings, ignored("seller_signals"))
			continue
		}
		if !evidencePresent(sellerText, signal.Evidence) {
			warnings = append(warnings, missingEvidence(signal.Key))
			continue
		}
		if !signalSeen[signal.Key] {
			out.SellerSignals = append(out.SellerSignals, signal)
			signalSeen[signal.Key] = true
		}
	}
	if note, valid := rawString(obj["price_note"]); valid && note != "" {
		if evidencePresent(titleText, note) {
			out.PriceNote = ptr(note)
		} else {
			warnings = append(warnings, missingEvidence("price_note"))
		}
	}
	extraRows, _ := rawArray(obj["extra_findings"])
	existingNames := map[string]bool{}
	for _, r := range ResolveRisks(profile) {
		existingNames[evidenceText(r.Name)] = true
	}
	for _, row := range extraRows {
		var extra ExtraFinding
		if json.Unmarshal(row, &extra) != nil || evidenceText(extra.Name) == "" || strings.TrimSpace(extra.Evidence) == "" || (extra.Source != "text" && extra.Source != "image") {
			warnings = append(warnings, ignored("extra_findings"))
			continue
		}
		if existingNames[evidenceText(extra.Name)] {
			warnings = append(warnings, ignored(extra.Name))
			continue
		}
		if extra.Source == "text" && !evidencePresent(titleText, extra.Evidence) {
			warnings = append(warnings, missingEvidence(extra.Name))
			continue
		}
		if extra.Source == "image" && !imageEvidencePresent(extra.Evidence, originals.ImageCount) {
			warnings = append(warnings, missingImageEvidence(extra.Name))
			continue
		}
		if len(out.ExtraFindings) < 5 {
			out.ExtraFindings = append(out.ExtraFindings, extra)
		}
	}
	return out, warnings, nil
}
func parseRequirements(raw json.RawMessage, requirements []Requirement, original string, imageCount *int, warnings []locales.Message) ([]RequirementFact, []locales.Message) {
	rows, _ := rawArray(raw)
	byID := map[string]RequirementFact{}
	allowed := map[string]int64{}
	for _, r := range requirements {
		allowed[r.ID] = profileRevision(r.Rev)
	}
	for _, row := range rows {
		var model struct {
			ID       string            `json:"id"`
			Status   RequirementStatus `json:"status"`
			Source   *string           `json:"source"`
			Evidence *string           `json:"evidence"`
		}
		if json.Unmarshal(row, &model) != nil || model.ID == "" || (model.Status != ReqMet && model.Status != ReqUnmet && model.Status != ReqUnknown) {
			warnings = append(warnings, ignored("requirements"))
			continue
		}
		rev, known := allowed[model.ID]
		if !known {
			warnings = append(warnings, unknownField(model.ID))
			continue
		}
		fact := RequirementFact{ID: model.ID, Status: model.Status, Source: model.Source, Evidence: model.Evidence, Rev: rev}
		if _, exists := byID[fact.ID]; exists {
			warnings = append(warnings, ignored(fact.ID))
			continue
		}
		validSource := fact.Source != nil && (*fact.Source == "text" || *fact.Source == "image")
		missingImage := fact.Source != nil && *fact.Source == "image" && fact.Evidence != nil && !imageEvidencePresent(*fact.Evidence, imageCount)
		if fact.Status != ReqUnknown && (!validSource || fact.Evidence == nil || strings.TrimSpace(*fact.Evidence) == "" || *fact.Source == "text" && !evidencePresent(original, *fact.Evidence) || missingImage) {
			if missingImage {
				warnings = append(warnings, missingImageEvidence(fact.ID))
			} else {
				warnings = append(warnings, missingEvidence(fact.ID))
			}
			fact.Status = ReqUnknown
		}
		if fact.Status == ReqUnmet && fact.Source != nil && *fact.Source == "image" {
			fact.Status = ReqUnknown
		}
		if fact.Status == ReqUnknown {
			fact.Source = nil
			fact.Evidence = nil
		}
		byID[fact.ID] = fact
	}
	out := make([]RequirementFact, 0, len(requirements))
	for _, r := range requirements {
		if fact, exists := byID[r.ID]; exists {
			out = append(out, fact)
		} else {
			out = append(out, RequirementFact{ID: r.ID, Status: ReqUnknown, Rev: profileRevision(r.Rev)})
		}
	}
	return out, warnings
}
func validSellerSignal(key string) bool {
	switch key {
	case "desc_mismatch", "after_sale", "slow_ship", "partial_refund", "dealer_words":
		return true
	}
	return false
}
