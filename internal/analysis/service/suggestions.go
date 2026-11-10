package modelservice

import (
	"math"
	"math/big"
	"slices"
	"strconv"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/limits"
)

// ConvertSuggestions 只丢弃无效预算，诊断供安全日志使用，不作为清单警告返回。
func ConvertSuggestions(source analysis.ProfileSuggestions) (SuggestionsView, []string) {
	view := SuggestionsView{ExcludeWords: append([]string{}, source.ExcludeWords...)}
	if source.Keyword != nil {
		keyword := *source.Keyword
		view.Keyword = &keyword
	}
	var minDiscarded, maxDiscarded bool
	view.BudgetMin, minDiscarded = suggestionCents(source.BudgetMin)
	view.BudgetMax, maxDiscarded = suggestionCents(source.BudgetMax)
	if view.BudgetMin != nil && view.BudgetMax != nil && *source.BudgetMin > *source.BudgetMax {
		view.BudgetMin, view.BudgetMax = nil, nil
		minDiscarded, maxDiscarded = true, true
	}
	var discarded []string
	if minDiscarded || slices.Contains(source.DiscardedBudgets, "budget_min") {
		discarded = append(discarded, "budget_min")
	}
	if maxDiscarded || slices.Contains(source.DiscardedBudgets, "budget_max") {
		discarded = append(discarded, "budget_max")
	}
	return view, discarded
}

func suggestionCents(yuan *float64) (*int64, bool) {
	if yuan == nil {
		return nil, false
	}
	if math.IsNaN(*yuan) || math.IsInf(*yuan, 0) || *yuan < 0 {
		return nil, true
	}
	// 从最短十进制换算，避免 1.005 乘一百后的二进制误差改变四舍五入。
	decimal, ok := new(big.Rat).SetString(strconv.FormatFloat(*yuan, 'f', -1, 64))
	if !ok {
		return nil, true
	}
	numerator := new(big.Int).Mul(decimal.Num(), big.NewInt(100))
	whole, remainder := new(big.Int), new(big.Int)
	whole.QuoRem(numerator, decimal.Denom(), remainder)
	if remainder.Lsh(remainder, 1).Cmp(decimal.Denom()) >= 0 {
		whole.Add(whole, big.NewInt(1))
	}
	if whole.Cmp(big.NewInt(limits.MaxSafeInteger)) > 0 {
		return nil, true
	}
	cents := whole.Int64()
	return &cents, false
}
