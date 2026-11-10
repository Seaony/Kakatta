// Package analysis 用已经取得的数据做判断，不访问网络或数据库。
package analysis

import (
	"encoding/json"
	"strings"
	"unicode"

	"github.com/Seaony/Kakatta/internal/locales"
	"golang.org/x/text/unicode/norm"
)

type Mode string

const (
	ModeAI      Mode = "ai"
	ModeKeyword Mode = "keyword"
)

type Kind string

const (
	KindSale      Kind = "sale"
	KindWanted    Kind = "wanted"
	KindAccessory Kind = "accessory"
	KindRecycle   Kind = "recycle"
	KindRental    Kind = "rental"
	KindRepair    Kind = "repair"
	KindBox       Kind = "box"
	KindMulti     Kind = "multi"
	KindBroken    Kind = "broken"
	KindOther     Kind = "other"
)

type Attitude string

const (
	Confirm Attitude = "confirm"
	Reject  Attitude = "reject"
	Mind    Attitude = "mind"
	Ignore  Attitude = "ignore"
)

type ProfileSource string

const (
	SourceUser   ProfileSource = "user"
	SourceAI     ProfileSource = "ai"
	SourceSystem ProfileSource = "system"
)

type SpecSource string

const (
	SpecTitle  SpecSource = "title"
	SpecDetail SpecSource = "detail"
	SpecUser   SpecSource = "user"
)

type CheckStatus string

const (
	CheckOK      CheckStatus = "ok"
	CheckRisk    CheckStatus = "risk"
	CheckSuspect CheckStatus = "suspect"
	CheckUnknown CheckStatus = "unknown"
)

type RequirementStatus string

const (
	ReqMet     RequirementStatus = "met"
	ReqUnmet   RequirementStatus = "unmet"
	ReqUnknown RequirementStatus = "unknown"
)

type EvidenceOrigin string

const (
	EvidenceText    EvidenceOrigin = "text"
	EvidenceImage   EvidenceOrigin = "image"
	EvidenceProgram EvidenceOrigin = "program"
	EvidenceUser    EvidenceOrigin = "user"
	EvidenceUnknown EvidenceOrigin = "unknown"
)

type Grade string

const (
	GradeSteal  Grade = "steal"
	GradeGood   Grade = "good"
	GradeFair   Grade = "fair"
	GradePricey Grade = "pricey"
	GradeAvoid  Grade = "avoid"
)

type Decision string

const (
	DecisionScored   Decision = "scored"
	DecisionMatched  Decision = "matched"
	DecisionRejected Decision = "rejected"
)

type RejectKind string

const (
	RejectFilter      RejectKind = "filter"
	RejectExcluded    RejectKind = "excluded"
	RejectKeywordMiss RejectKind = "keyword_miss"
	RejectJunk        RejectKind = "junk"
	RejectUnmatched   RejectKind = "unmatched"
)

type Profile struct {
	Summary      string        `json:"summary"`
	Specs        []ProfileSpec `json:"specs"`
	Musts        []Requirement `json:"musts"`
	Bonuses      []Requirement `json:"bonuses"`
	Risks        []ProfileRisk `json:"risks"`
	AllowedKinds []Kind        `json:"allowed_kinds"`
	NextNo       int           `json:"next_no"`
}

// 关键词任务存空对象，AI 清单的列表保持数组。
func (p Profile) MarshalJSON() ([]byte, error) {
	if p.Summary == "" && p.NextNo == 0 && len(p.Specs) == 0 && len(p.Musts) == 0 && len(p.Bonuses) == 0 && len(p.Risks) == 0 && len(p.AllowedKinds) == 0 {
		return []byte("{}"), nil
	}
	type profileJSON Profile
	out := profileJSON(p)
	if out.Specs == nil {
		out.Specs = []ProfileSpec{}
	}
	if out.Musts == nil {
		out.Musts = []Requirement{}
	}
	if out.Bonuses == nil {
		out.Bonuses = []Requirement{}
	}
	if out.Risks == nil {
		out.Risks = []ProfileRisk{}
	}
	if out.AllowedKinds == nil {
		out.AllowedKinds = []Kind{}
	}
	return json.Marshal(out)
}

type ProfileSpec struct {
	Name    string        `json:"name"`
	Options []string      `json:"options"`
	Values  []string      `json:"values"`
	Source  ProfileSource `json:"source"`
	Edited  bool          `json:"edited"`
}
type Requirement struct {
	ID     string        `json:"id"`
	Text   string        `json:"text"`
	Source ProfileSource `json:"source"`
	Edited bool          `json:"edited"`
	Rev    int64         `json:"rev"`
}
type ProfileRisk struct {
	ID       string        `json:"id"`
	Name     string        `json:"name,omitempty"`
	Hint     string        `json:"hint,omitempty"`
	Question string        `json:"question,omitempty"`
	Attitude Attitude      `json:"attitude"`
	Source   ProfileSource `json:"source"`
	Edited   bool          `json:"edited"`
	Rev      int64         `json:"rev"`
}
type RiskDefinition struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Category        string   `json:"category"`
	DefaultAttitude Attitude `json:"default_attitude"`
	Hint            string   `json:"hint"`
	Question        string   `json:"question"`
}
type ResolvedRisk struct {
	ProfileRisk
	Category string `json:"category"`
}
type GeneratedSpec struct {
	Name    string        `json:"name"`
	Options []string      `json:"options"`
	Values  []string      `json:"values"`
	Source  ProfileSource `json:"source"`
}
type GeneratedRequirement struct {
	Text   string        `json:"text"`
	Source ProfileSource `json:"source"`
}
type GeneratedRisk struct {
	ID       *string       `json:"id"`
	Name     *string       `json:"name"`
	Hint     *string       `json:"hint"`
	Question *string       `json:"question"`
	Attitude *Attitude     `json:"attitude"`
	Source   ProfileSource `json:"source"`
}

// 生成清单的预算用元，保存任务前由调用方换成整数分。
type GeneratedProfile struct {
	Summary      string                 `json:"summary"`
	Specs        []GeneratedSpec        `json:"specs"`
	Musts        []GeneratedRequirement `json:"musts"`
	Bonuses      []GeneratedRequirement `json:"bonuses"`
	Risks        []GeneratedRisk        `json:"risks"`
	AllowedKinds []Kind                 `json:"allowed_kinds"`
	BudgetMin    *float64               `json:"budget_min"`
	BudgetMax    *float64               `json:"budget_max"`
	Keyword      *string                `json:"keyword"`
	ExcludeWords []string               `json:"exclude_words"`
}
type ProfileSuggestions struct {
	BudgetMin    *float64 `json:"budget_min"`
	BudgetMax    *float64 `json:"budget_max"`
	Keyword      *string  `json:"keyword"`
	ExcludeWords []string `json:"exclude_words"`
}
type RegionCity struct {
	Province string `json:"province"`
	City     string `json:"city"`
}
type FirstLayerInput struct {
	Mode             Mode         `json:"mode"`
	Title            string       `json:"title"`
	Price            *int64       `json:"price"`
	Area             string       `json:"area"`
	PublishedAt      *int64       `json:"published_at"`
	FreeShipping     bool         `json:"free_shipping"`
	MinPrice         *int64       `json:"min_price"`
	MaxPrice         *int64       `json:"max_price"`
	FreeShippingOnly bool         `json:"free_shipping_only"`
	PublishDays      int64        `json:"publish_days"`
	Province         string       `json:"province"`
	City             string       `json:"city"`
	Matches          []string     `json:"matches"`
	Excludes         []string     `json:"excludes"`
	NowMS            int64        `json:"now_ms"`
	Cities           []RegionCity `json:"cities"`
}
type LayerResult struct {
	Rejected        bool             `json:"rejected"`
	RejectKind      RejectKind       `json:"reject_kind,omitempty"`
	Reason          *locales.Message `json:"reason"`
	MatchedKeywords []string         `json:"matched_keywords"`
	Kind            Kind             `json:"kind,omitempty"`
}
type SpecValue struct {
	Name  string  `json:"name"`
	Value *string `json:"value"`
}
type ItemSpecs struct {
	Kind      Kind        `json:"kind"`
	Specs     []SpecValue `json:"specs"`
	BrandNew  *bool       `json:"brand_new"`
	ConfigKey *string     `json:"config_key"`
	Excluded  bool        `json:"excluded"`
	Source    SpecSource  `json:"source"`
}
type TitleItem struct {
	No       int         `json:"no"`
	Kind     Kind        `json:"kind"`
	Specs    []SpecValue `json:"specs"`
	BrandNew *bool       `json:"brand_new"`
	Verdict  string      `json:"verdict"`
	Reason   string      `json:"reason"`
}
type TitleScreen struct {
	Items []TitleItem `json:"items"`
}
type TitleResult struct {
	Warnings []locales.Message `json:"warnings"`
	Item     TitleItem         `json:"item"`
	Specs    ItemSpecs         `json:"specs"`
	Result   LayerResult       `json:"result"`
}
type RequirementFact struct {
	ID       string            `json:"id"`
	Status   RequirementStatus `json:"status"`
	Source   *string           `json:"source"`
	Evidence *string           `json:"evidence"`
	Rev      int64             `json:"rev"`
}
type CheckFact struct {
	Key      string      `json:"key"`
	Status   CheckStatus `json:"status"`
	Source   *string     `json:"source"`
	Evidence *string     `json:"evidence"`
	Rev      int64       `json:"rev"`
}
type SellerSignal struct {
	Key      string `json:"key"`
	Evidence string `json:"evidence"`
}
type ExtraFinding struct {
	Name     string `json:"name"`
	Evidence string `json:"evidence"`
	Source   string `json:"source"`
}
type Adjustment struct {
	Value  int    `json:"value"`
	Reason string `json:"reason"`
}
type Facts struct {
	Specs         []SpecValue       `json:"specs"`
	BrandNew      *bool             `json:"brand_new"`
	Musts         []RequirementFact `json:"musts"`
	Bonuses       []RequirementFact `json:"bonuses"`
	Checks        []CheckFact       `json:"checks"`
	SellerSignals []SellerSignal    `json:"seller_signals"`
	PriceNote     *string           `json:"price_note"`
	ExtraFindings []ExtraFinding    `json:"extra_findings"`
	Adjust        Adjustment        `json:"adjust"`
	Summary       string            `json:"summary"`
}
type EvidenceOriginals struct {
	Title              string   `json:"title"`
	Description        string   `json:"description"`
	SellerIntroduction string   `json:"seller_introduction"`
	RecentRateTexts    []string `json:"recent_rate_texts"`
}
type SellerItem struct {
	Title  string `json:"title"`
	OnSale bool   `json:"on_sale"`
}
type SellerRate struct {
	AsSeller bool `json:"as_seller"`
	Negative bool `json:"negative"`
}
type SellerInput struct {
	Available bool `json:"available"`
	// 已取得评价时才为 true，空列表也算取得。
	RatesAvailable   bool         `json:"rates_available"`
	RegistrationDays *int         `json:"registration_days"`
	CreditExcellent  *bool        `json:"credit_excellent"`
	RecentItems      []SellerItem `json:"recent_items"`
	RecentRates      []SellerRate `json:"recent_rates"`
}
type SellerType string

const (
	SellerPersonal      SellerType = "personal"
	SellerDealerSuspect SellerType = "dealer_suspect"
	SellerUnknown       SellerType = "unknown"
)

type EffectiveCheck struct {
	Key      string         `json:"key"`
	Status   CheckStatus    `json:"status"`
	Source   EvidenceOrigin `json:"source"`
	Evidence *string        `json:"evidence"`
}
type SellerResult struct {
	Type          SellerType        `json:"type"`
	Checks        []EffectiveCheck  `json:"checks"`
	BonusEligible bool              `json:"bonus_eligible"`
	Reasons       []locales.Message `json:"reasons"`
}
type RiskOverride struct {
	Status CheckStatus `json:"status"`
	Rev    int64       `json:"rev"`
}
type UserOverrides struct {
	Risks map[string]RiskOverride `json:"risks"`
}
type ScoreInput struct {
	Profile   Profile       `json:"profile"`
	Facts     Facts         `json:"facts"`
	Item      ItemSpecs     `json:"item"`
	Price     *int64        `json:"price"`
	Fair      *FairPrice    `json:"fair"`
	Seller    SellerResult  `json:"seller"`
	Overrides UserOverrides `json:"overrides"`
}
type ScoreBase struct {
	Score     float64         `json:"score"`
	D         float64         `json:"d"`
	FairPrice int64           `json:"fair_price"`
	Sample    int             `json:"sample"`
	Basis     locales.Message `json:"basis"`
}
type ScoreItem struct {
	ID     string          `json:"id"`
	Name   string          `json:"name"`
	Value  int             `json:"value"`
	Source string          `json:"source"`
	Reason locales.Message `json:"reason"`
}
type ScoreCap struct {
	Limit  int             `json:"limit"`
	Reason locales.Message `json:"reason"`
}
type ScoreBreakdown struct {
	Base  *ScoreBase        `json:"base"`
	Items []ScoreItem       `json:"items"`
	AI    Adjustment        `json:"ai"`
	Caps  []ScoreCap        `json:"caps"`
	Gates []locales.Message `json:"gates"`
}
type ContactQuestion struct {
	Key      string          `json:"key"`
	Question locales.Message `json:"question"`
}
type ScoreResult struct {
	Decision   Decision          `json:"decision"`
	RejectKind RejectKind        `json:"reject_kind,omitempty"`
	Reason     locales.Message   `json:"reason"`
	Score      *int              `json:"score"`
	Grade      *Grade            `json:"grade"`
	Excluded   bool              `json:"excluded"`
	Breakdown  ScoreBreakdown    `json:"breakdown"`
	Checks     []EffectiveCheck  `json:"checks"`
	Questions  []ContactQuestion `json:"questions"`
}
type FairPrice struct {
	Cents       *int64          `json:"price"`
	Sample      int             `json:"sample"`
	Basis       locales.Message `json:"basis"`
	Days        int             `json:"days"`
	DroppedSpec *string         `json:"dropped_spec"`
	ItemIDs     []string        `json:"item_ids"`
}
type MarketObservation struct {
	ItemID     string      `json:"item_id"`
	SellerID   string      `json:"seller_id"`
	Title      string      `json:"title"`
	Day        string      `json:"day"`
	Price      *int64      `json:"price"`
	LastSeenAt int64       `json:"last_seen_at"`
	Specs      []SpecValue `json:"specs"`
	BrandNew   *bool       `json:"brand_new"`
	Kind       Kind        `json:"kind"`
	Excluded   bool        `json:"excluded"`
	Dismissed  bool        `json:"dismissed"`
}
type MarketInput struct {
	Mode             Mode                `json:"mode"`
	Profile          Profile             `json:"profile"`
	ExcludeKeywords  []string            `json:"exclude_keywords"`
	Observations     []MarketObservation `json:"observations"`
	LatestRunItemIDs []string            `json:"latest_run_item_ids"`
	NowMS            int64               `json:"now_ms"`
	TimeZone         string              `json:"timezone"`
}
type MarketSample struct {
	ItemID     string `json:"item_id"`
	SellerID   string `json:"seller_id"`
	Price      int64  `json:"price"`
	Day        string `json:"day"`
	LastSeenAt int64  `json:"last_seen_at"`
}
type PriceBand struct {
	P25 float64 `json:"p25"`
	P75 float64 `json:"p75"`
}
type DailyPrice struct {
	Day    string   `json:"day"`
	Median *float64 `json:"median"`
	Sample int      `json:"sample"`
}
type PriceTrend struct {
	FirstDay    string   `json:"first_day"`
	LastDay     string   `json:"last_day"`
	FirstMedian float64  `json:"first_median"`
	LastMedian  float64  `json:"last_median"`
	Change      float64  `json:"change"`
	Percent     *float64 `json:"percent"`
}
type MarketGroup struct {
	ConfigKey string         `json:"config_key"`
	Specs     []SpecValue    `json:"specs"`
	BrandNew  bool           `json:"brand_new"`
	Selected  bool           `json:"selected"`
	Fair      FairPrice      `json:"fair"`
	Samples   []MarketSample `json:"samples"`
	PriceBand *PriceBand     `json:"price_band"`
	Daily     []DailyPrice   `json:"daily"`
	Trend     *PriceTrend    `json:"trend"`
	Cheapest  *MarketSample  `json:"cheapest"`
}
type MarketResult struct {
	Groups     []MarketGroup     `json:"groups"`
	TaskTrend  []DailyPrice      `json:"task_trend"`
	SnapshotAt *int64            `json:"snapshot_at"`
	Warnings   []locales.Message `json:"warnings"`
}
type AdjacentPrice struct {
	SpecName       string `json:"spec_name"`
	From           string `json:"from"`
	To             string `json:"to"`
	LowerConfigKey string `json:"lower_config_key"`
	ItemPremium    int64  `json:"item_premium"`
	TypicalPremium int64  `json:"typical_premium"`
}
type RecentScoredFinding struct {
	IsBaseline   bool        `json:"is_baseline"`
	DecidedLayer int         `json:"decided_layer"`
	AnalyzedAt   int64       `json:"analyzed_at"`
	Result       ScoreResult `json:"result"`
}
type ReasonCount struct {
	Reason locales.Message `json:"reason"`
	Count  int             `json:"count"`
}

type Error struct {
	Message locales.Message `json:"message"`
}

func (e *Error) Error() string { return e.Message.Code }
func message(code string, args map[string]any) locales.Message {
	if args == nil {
		args = map[string]any{}
	}
	return locales.Message{Code: code, Args: args}
}
func ruleError(code string, args map[string]any) error { return &Error{Message: message(code, args)} }
func ptr[T any](v T) *T                                { return &v }
func clonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	return ptr(*p)
}
func validKind(k Kind) bool {
	switch k {
	case KindSale, KindWanted, KindAccessory, KindRecycle, KindRental, KindRepair, KindBox, KindMulti, KindBroken, KindOther:
		return true
	}
	return false
}
func validAttitude(a Attitude) bool { return a == Confirm || a == Reject || a == Mind || a == Ignore }
func validCheck(s CheckStatus) bool {
	return s == CheckOK || s == CheckRisk || s == CheckSuspect || s == CheckUnknown
}
func configText(s string) string { return strings.TrimSpace(norm.NFKC.String(s)) }

// RiskNameKey 用同一套文字规整规则检查清单外发现是否重名。
func RiskNameKey(s string) string { return evidenceText(s) }

func evidenceText(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || strings.ContainsRune("，。、！？；：,.!?;:\"'“”‘’「」（）()", r) {
			return -1
		}
		return r
	}, norm.NFKC.String(s))
}
func evidencePresent(original, quote string) bool {
	quote = evidenceText(quote)
	return len([]rune(quote)) >= 2 && strings.Contains(evidenceText(original), quote)
}

// 旧清单没有版本时按导入规则补成初始版本，事实和覆盖不能借此补确认。
func profileRevision(rev int64) int64 {
	if rev == 0 {
		return 1
	}
	return rev
}
func revisionMatches(saved, current int64) bool {
	return saved > 0 && saved == profileRevision(current)
}
