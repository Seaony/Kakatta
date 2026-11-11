package prompts

import (
	"bytes"
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/analysis/client"
	"github.com/Seaony/Kakatta/internal/locales"
)

//go:embed assets/v1/*
var catalogAssets embed.FS

const catalogRoot = "assets/v1/"

type manifest struct {
	AssetVersion int                  `json:"asset_version"`
	Definitions  []manifestDefinition `json:"definitions"`
}

type manifestDefinition struct {
	Kind         client.Kind     `json:"kind"`
	Template     string          `json:"template"`
	RequiredKeys []string        `json:"required_keys"`
	Schema       *manifestSchema `json:"schema"`
}

type manifestSchema struct {
	Name string `json:"name"`
	File string `json:"file"`
}

func Definitions() ([]Definition, error) { return definitionsFrom(catalogAssets) }

func definitionsFrom(files fs.FS) ([]Definition, error) {
	data, err := fs.ReadFile(files, catalogRoot+"manifest.json")
	if err != nil {
		return nil, inputError("assets.manifest")
	}
	var index manifest
	if strictJSON(data, &index) != nil || index.AssetVersion != AssetVersion || len(index.Definitions) != 4 {
		return nil, inputError("assets.manifest")
	}
	seen := make(map[client.Kind]bool, 4)
	definitions := make([]Definition, 0, 4)
	for _, entry := range index.Definitions {
		schemaName, required, known := definitionRules(entry.Kind)
		if !known || seen[entry.Kind] || entry.Template != string(entry.Kind)+".txt" || entry.RequiredKeys == nil || !slices.Equal(entry.RequiredKeys, required) {
			return nil, inputError("assets.definitions")
		}
		seen[entry.Kind] = true
		data, err := fs.ReadFile(files, catalogRoot+entry.Template)
		if err != nil || !utf8.Valid(data) || len(bytes.TrimSpace(data)) == 0 {
			return nil, inputError("assets.template")
		}
		definition := Definition{Kind: entry.Kind, Default: string(data), RequiredKeys: slices.Clone(entry.RequiredKeys)}
		if err := validateTemplate(definition, definition.Default); err != nil {
			return nil, err
		}
		if schemaName == "" {
			if entry.Schema != nil {
				return nil, inputError("assets.schema")
			}
		} else {
			if entry.Schema == nil || entry.Schema.Name != schemaName || entry.Schema.File != schemaName+".json" {
				return nil, inputError("assets.schema")
			}
			data, err := fs.ReadFile(files, catalogRoot+entry.Schema.File)
			if err != nil || !validSchema(data) {
				return nil, inputError("assets.schema")
			}
			definition.Schema = &client.Schema{Name: schemaName, JSON: slices.Clone(data)}
		}
		definitions = append(definitions, definition)
	}
	return definitions, nil
}

func definitionRules(kind client.Kind) (string, []string, bool) {
	switch kind {
	case client.Analysis:
		return "listing_facts", []string{"checks", "adjust", "summary"}, true
	case client.TitleScreen:
		return "title_screen", []string{"items", "kind", "verdict"}, true
	case client.Profile:
		return "task_profile", []string{"specs", "risks"}, true
	case client.Market:
		return "", []string{}, true
	}
	return "", nil, false
}

func strictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return inputError("assets.json")
	}
	return nil
}

// 资产只采用当前三份 Schema 用到的结构，损坏或混入别的关键字就拒绝。
type schemaNode struct {
	Type                 json.RawMessage       `json:"type"`
	Properties           map[string]schemaNode `json:"properties"`
	Required             []string              `json:"required"`
	AdditionalProperties *bool                 `json:"additionalProperties"`
	Items                *schemaNode           `json:"items"`
	Enum                 []json.RawMessage     `json:"enum"`
	Defs                 map[string]schemaNode `json:"$defs"`
	Ref                  string                `json:"$ref"`
}

func validSchema(data []byte) bool {
	var root schemaNode
	if strictJSON(data, &root) != nil || string(root.Type) != `"object"` {
		return false
	}
	var validNode func(schemaNode) bool
	validNode = func(node schemaNode) bool {
		if node.Ref != "" {
			name, ok := strings.CutPrefix(node.Ref, "#/$defs/")
			_, found := root.Defs[name]
			return ok && found && len(node.Type) == 0 && node.Properties == nil && node.Required == nil && node.Items == nil && node.Enum == nil && node.Defs == nil && node.AdditionalProperties == nil
		}
		var types []string
		var single string
		if json.Unmarshal(node.Type, &single) == nil {
			types = []string{single}
		} else if json.Unmarshal(node.Type, &types) != nil || len(types) == 0 {
			return false
		}
		known := map[string]bool{}
		for _, kind := range types {
			if known[kind] || !slices.Contains([]string{"object", "array", "string", "integer", "number", "boolean", "null"}, kind) {
				return false
			}
			known[kind] = true
		}
		if known["object"] {
			if node.Properties == nil || node.Required == nil || node.AdditionalProperties == nil || *node.AdditionalProperties || len(node.Required) != len(node.Properties) {
				return false
			}
			required := map[string]bool{}
			for _, key := range node.Required {
				if _, exists := node.Properties[key]; !exists || required[key] || key == "rev" {
					return false
				}
				required[key] = true
			}
		} else if node.Properties != nil || node.Required != nil || node.AdditionalProperties != nil {
			return false
		}
		if known["array"] != (node.Items != nil) {
			return false
		}
		if node.Enum != nil && len(node.Enum) == 0 {
			return false
		}
		for _, child := range node.Properties {
			if !validNode(child) {
				return false
			}
		}
		for _, child := range node.Defs {
			if !validNode(child) {
				return false
			}
		}
		return node.Items == nil || validNode(*node.Items)
	}
	return validNode(root)
}

func Resolve(overrides map[client.Kind]string) (Snapshot, error) {
	definitions, err := Definitions()
	if err != nil {
		return Snapshot{}, err
	}
	contents := make(map[client.Kind]string, len(definitions))
	for _, definition := range definitions {
		contents[definition.Kind] = definition.Default
	}
	for kind, content := range overrides {
		if _, known := contents[kind]; !known {
			return Snapshot{}, inputError("kind")
		}
		contents[kind] = content
	}
	for _, definition := range definitions {
		if err := validateTemplate(definition, contents[definition.Kind]); err != nil {
			return Snapshot{}, err
		}
	}
	return Snapshot{AssetVersion: AssetVersion, Contents: contents}, nil
}

func PrepareSave(kind client.Kind, content string) (*string, error) {
	definitions, err := Definitions()
	if err != nil {
		return nil, err
	}
	for _, definition := range definitions {
		if definition.Kind != kind {
			continue
		}
		if err := validateTemplate(definition, content); err != nil {
			return nil, err
		}
		if content == definition.Default {
			return nil, nil
		}
		return &content, nil
	}
	return nil, inputError("kind")
}

func validateTemplate(definition Definition, content string) error {
	var missing []string
	for _, key := range definition.RequiredKeys {
		quoted := strconv.Quote(key)
		if !strings.Contains(content, quoted) {
			missing = append(missing, quoted)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return &analysis.Error{Message: locales.Message{Code: "analysis.prompts.missing_keys", Args: map[string]any{"kind": string(definition.Kind), "keys": strings.Join(missing, ", ")}}}
}

func requestParts(s Snapshot, kind client.Kind) (string, *client.Schema, error) {
	if s.AssetVersion != AssetVersion {
		return "", nil, inputError("asset_version")
	}
	definitions, err := Definitions()
	if err != nil {
		return "", nil, err
	}
	if len(s.Contents) != len(definitions) {
		return "", nil, inputError("snapshot.contents")
	}
	var system string
	var schema *client.Schema
	found := false
	for _, definition := range definitions {
		content, exists := s.Contents[definition.Kind]
		if !exists {
			return "", nil, inputError("snapshot.contents")
		}
		if err := validateTemplate(definition, content); err != nil {
			return "", nil, err
		}
		if definition.Kind == kind {
			found = true
			system = content
			if definition.Schema != nil {
				schema = &client.Schema{Name: definition.Schema.Name, JSON: slices.Clone(definition.Schema.JSON)}
			}
		}
	}
	if !found {
		return "", nil, inputError("kind")
	}
	return system + "\n\n" + SellerDataSystemSuffix, schema, nil
}
