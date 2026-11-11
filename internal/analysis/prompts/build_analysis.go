package prompts

import (
	"fmt"
	"math"
	"strings"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/analysis/client"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/xianyu"
)

const analysisNoImages = "补充说明：本次没有提供商品图片，请只根据文字信息判断，不要推测图片内容。"

// BuildAnalysis 使用同一份来源构造模型输入和证据原文，不把程序内部字段发给模型。
func BuildAnalysis(s Snapshot, taskID *int64, p analysis.Profile, item ProductSource, fair *analysis.FairPrice, dataURLs []string) (client.Request, analysis.EvidenceOriginals, error) {
	system, schema, err := requestParts(s, client.Analysis)
	if err != nil {
		return client.Request{}, analysis.EvidenceOriginals{}, err
	}
	p = cloneProfile(p)
	profile, err := analysisProfile(p, fair)
	if err != nil {
		return client.Request{}, analysis.EvidenceOriginals{}, err
	}
	product, originals, err := analysisProduct(item)
	if err != nil {
		return client.Request{}, analysis.EvidenceOriginals{}, err
	}
	profileJSON, err := userJSON(profile)
	if err != nil {
		return client.Request{}, analysis.EvidenceOriginals{}, err
	}
	productJSON, err := userJSON(product)
	if err != nil {
		return client.Request{}, analysis.EvidenceOriginals{}, err
	}
	user := "## 需求清单\n```json\n" + profileJSON + "\n```\n\n## 商品信息\n```json\n" + productJSON + "\n```"
	if len(dataURLs) == 0 {
		user += "\n\n" + analysisNoImages
	}
	imageCount := len(dataURLs)
	originals.ImageCount = &imageCount
	validationOriginals := originals
	validationOriginals.RecentRateTexts = append([]string(nil), originals.RecentRateTexts...)
	validationImageCount := imageCount
	validationOriginals.ImageCount = &validationImageCount
	return client.Request{
		Kind: client.Analysis, TaskID: copyTaskID(taskID), System: system, Schema: schema,
		User: user, Images: append([]string(nil), dataURLs...),
		Validate: func(text string) error {
			_, _, err := analysis.ParseFacts([]byte(text), p, validationOriginals)
			return err
		},
	}, originals, nil
}

func analysisProfile(p analysis.Profile, fair *analysis.FairPrice) (map[string]any, error) {
	specs := make([]map[string]any, 0, len(p.Specs))
	for _, spec := range p.Specs {
		specs = append(specs, map[string]any{"name": spec.Name, "options": append([]string{}, spec.Options...), "values": append([]string{}, spec.Values...)})
	}
	musts := make([]map[string]any, 0, len(p.Musts))
	for _, must := range p.Musts {
		musts = append(musts, map[string]any{"id": must.ID, "text": must.Text})
	}
	bonuses := make([]map[string]any, 0, len(p.Bonuses))
	for _, bonus := range p.Bonuses {
		bonuses = append(bonuses, map[string]any{"id": bonus.ID, "text": bonus.Text})
	}
	checks := make([]map[string]any, 0, len(p.Risks))
	for _, risk := range analysis.ResolveRisks(p) {
		if !strings.HasPrefix(risk.ID, "seller_") {
			checks = append(checks, map[string]any{"key": risk.ID, "name": risk.Name, "hint": risk.Hint})
		}
	}
	var fairPrice any
	if fair != nil && fair.Cents != nil {
		basis, err := locales.Format(locales.ZhCN, fair.Basis)
		if err != nil {
			return nil, inputError("fair_price.basis")
		}
		fairPrice = map[string]any{"price": centsNumber(*fair.Cents), "sample": fair.Sample, "basis": basis}
	}
	return map[string]any{"specs": specs, "musts": musts, "bonuses": bonuses, "checks": checks, "fair_price": fairPrice}, nil
}

func analysisProduct(item ProductSource) (map[string]any, analysis.EvidenceOriginals, error) {
	product := map[string]any{"title": item.Title}
	originals := analysis.EvidenceOriginals{Title: item.Title}
	analysisPutPrice(product, "price", item.PriceCents)
	analysisPutPrice(product, "original_price", item.OriginalPriceCents)
	analysisPut(product, "description", item.Description)
	analysisPut(product, "published_at", item.PublishedAtMS)
	analysisPut(product, "area", item.Area)
	analysisPut(product, "want_count", item.WantCount)
	analysisPut(product, "browse_count", item.BrowseCount)
	analysisPut(product, "url", item.URL)
	if item.Tags != nil {
		product["tags"] = append([]string{}, item.Tags...)
	}
	if item.Description != nil {
		originals.Description = *item.Description
	}
	if item.Seller != nil {
		seller, rates, err := analysisSeller(*item.Seller)
		if err != nil {
			return nil, analysis.EvidenceOriginals{}, err
		}
		product["seller"] = seller
		originals.RecentRateTexts = rates
		if item.Seller.Introduction != nil {
			originals.SellerIntroduction = *item.Seller.Introduction
		}
	}
	return product, originals, nil
}

func analysisSeller(source SellerSource) (map[string]any, []string, error) {
	seller := map[string]any{}
	analysisPut(seller, "nickname", source.Nickname)
	analysisPut(seller, "introduction", source.Introduction)
	analysisPut(seller, "zhima_level", source.ZhimaLevel)
	analysisPut(seller, "seller_credit", source.SellerCredit)
	analysisPut(seller, "buyer_credit", source.BuyerCredit)
	analysisPut(seller, "item_count", source.ItemCount)
	analysisPut(seller, "on_sale_count", source.OnSaleCount)
	analysisPut(seller, "sold_count", source.SoldCount)
	analysisPut(seller, "rate_count", source.RateCount)
	if source.DetailMetrics != nil {
		seller["detail_metrics"] = *source.DetailMetrics
	}
	if source.RegistrationDays != nil {
		if years, months, ok := xianyu.RegDuration(*source.RegistrationDays); ok {
			seller["registration"] = fmt.Sprintf("来闲鱼 %d 年 %d 个月", years, months)
		}
	}
	for key, rate := range map[string]*float64{"seller_good_rate": source.SellerGoodRate, "buyer_good_rate": source.BuyerGoodRate} {
		if rate != nil {
			if math.IsNaN(*rate) || math.IsInf(*rate, 0) || *rate < 0 || *rate > 1 {
				return nil, nil, inputError("seller." + key)
			}
			seller[key] = *rate
		}
	}
	var texts []string
	if source.RecentRates != nil {
		rates := make([]map[string]any, 0, min(10, len(source.RecentRates)))
		texts = []string{}
		for _, source := range source.RecentRates[:min(10, len(source.RecentRates))] {
			rate := map[string]any{}
			if source.Feedback != nil {
				text := analysisSummary(*source.Feedback)
				rate["feedback"] = text
				texts = append(texts, text)
			}
			analysisPut(rate, "role", source.Role)
			analysisPut(rate, "kind", source.Kind)
			analysisPut(rate, "published_at", source.PublishedAtMS)
			rates = append(rates, rate)
		}
		seller["recent_rates"] = rates
	}
	if source.RecentItems != nil {
		items := make([]map[string]any, 0, min(20, len(source.RecentItems)))
		for _, source := range source.RecentItems[:min(20, len(source.RecentItems))] {
			item := map[string]any{"title": source.Title}
			analysisPutPrice(item, "price", source.PriceCents)
			analysisPut(item, "status", source.Status)
			items = append(items, item)
		}
		seller["recent_items"] = items
	}
	return seller, texts, nil
}

func analysisPut[T any](values map[string]any, key string, value *T) {
	if value != nil {
		values[key] = *value
	}
}

func analysisPutPrice(values map[string]any, key string, value *int64) {
	if value != nil {
		values[key] = centsNumber(*value)
	}
}

func analysisSummary(text string) string {
	count := 0
	for pos := range text {
		if count == 60 {
			return text[:pos]
		}
		count++
	}
	return text
}
