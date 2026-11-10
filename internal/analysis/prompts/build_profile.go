package prompts

import (
	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/analysis/client"
)

func BuildProfile(s Snapshot, taskID *int64, keyword, requirement string, current analysis.Profile) (client.Request, error) {
	system, schema, err := requestParts(s, client.Profile)
	if err != nil {
		return client.Request{}, err
	}
	dictionary := analysis.RiskDictionary()
	reference := make([]map[string]any, 0, len(dictionary))
	for _, risk := range dictionary {
		reference = append(reference, map[string]any{
			"id": risk.ID, "name": risk.Name, "category": risk.Category,
			"default_attitude": risk.DefaultAttitude, "hint": risk.Hint,
		})
	}
	retained := []map[string]any{}
	for _, spec := range current.Specs {
		if spec.Edited {
			retained = append(retained, map[string]any{
				"name": spec.Name, "options": spec.Options, "values": spec.Values,
				"source": spec.Source, "edited": spec.Edited,
			})
		}
	}
	for _, requirements := range [][]analysis.Requirement{current.Musts, current.Bonuses} {
		for _, item := range requirements {
			if item.Edited {
				retained = append(retained, map[string]any{
					"id": item.ID, "text": item.Text, "source": item.Source, "edited": item.Edited,
				})
			}
		}
	}
	for _, risk := range current.Risks {
		if !risk.Edited {
			continue
		}
		item := map[string]any{"id": risk.ID, "attitude": risk.Attitude, "source": risk.Source, "edited": risk.Edited}
		for key, value := range map[string]string{"name": risk.Name, "hint": risk.Hint, "question": risk.Question} {
			if value != "" {
				item[key] = value
			}
		}
		retained = append(retained, item)
	}
	dictionaryJSON, err := userJSON(reference)
	if err != nil {
		return client.Request{}, err
	}
	retainedJSON, err := userJSON(retained)
	if err != nil {
		return client.Request{}, err
	}
	user := "我想买：" + keyword + "\n我的要求：" + requirement +
		"\n\n## 检查项字典\n```json\n" + dictionaryJSON +
		"\n```\n\n## 必须保留的条目\n```json\n" + retainedJSON + "\n```"
	return client.Request{
		Kind: client.Profile, TaskID: copyTaskID(taskID), System: system, User: user, Schema: schema,
		Validate: func(text string) error {
			_, _, err := analysis.ParseGeneratedProfile([]byte(text))
			return err
		},
	}, nil
}
