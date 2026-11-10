package prompts

import (
	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/analysis/client"
)

// BuildTitle 只发送标题整理需要的清单和搜索列表，本批序号从 1 开始。
func BuildTitle(s Snapshot, taskID *int64, p analysis.Profile, items []TitleSource) (client.Request, error) {
	count := len(items)
	if count < 1 || count > 20 {
		return client.Request{}, inputError("items")
	}
	system, schema, err := requestParts(s, client.TitleScreen)
	if err != nil {
		return client.Request{}, err
	}
	p = cloneProfile(p)
	specs := make([]map[string]any, 0, len(p.Specs))
	for _, spec := range p.Specs {
		specs = append(specs, map[string]any{
			"name": spec.Name, "options": append([]string{}, spec.Options...), "values": append([]string{}, spec.Values...),
		})
	}
	musts := make([]string, 0, len(p.Musts))
	for _, must := range p.Musts {
		musts = append(musts, must.Text)
	}
	rejects := []string{}
	for _, risk := range analysis.ResolveRisks(p) {
		if risk.Attitude == analysis.Confirm || risk.Attitude == analysis.Reject {
			rejects = append(rejects, risk.Name)
		}
	}
	allowed := append([]analysis.Kind{}, p.AllowedKinds...)
	profileJSON, err := userJSON(map[string]any{
		"specs": specs, "musts": musts, "reject_checks": rejects, "allowed_kinds": allowed,
	})
	if err != nil {
		return client.Request{}, err
	}
	batch := make([]map[string]any, 0, count)
	for i, item := range items {
		var price any
		if item.PriceCents != nil {
			price = centsNumber(*item.PriceCents)
		}
		batch = append(batch, map[string]any{
			"no": i + 1, "title": item.Title, "price": price, "tags": append([]string{}, item.Tags...),
		})
	}
	itemsJSON, err := userJSON(batch)
	if err != nil {
		return client.Request{}, err
	}
	return client.Request{
		Kind: client.TitleScreen, TaskID: copyTaskID(taskID), System: system, Schema: schema,
		User: "## 需求清单\n```json\n" + profileJSON + "\n```\n\n## 商品（搜索列表）\n```json\n" + itemsJSON + "\n```",
		Validate: func(text string) error {
			_, _, err := analysis.ParseTitleScreen([]byte(text), count)
			return err
		},
	}, nil
}
