// Package regions 提供随程序发布的地区选项，不访问外部地区服务。
package regions

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

//go:embed goofish_regions.json
var regionJSON []byte

var (
	ErrUnknownParent = errors.New("地区父节点不存在")
	ErrInvalidRegion = errors.New("地区选项无效")
)

type Node struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Level    string  `json:"level"`
	ParentID *string `json:"parent_id"`
}

type City struct {
	Province string `json:"province"`
	City     string `json:"city"`
}

type ValidationError struct{ Field string }

func (e *ValidationError) Error() string { return "地区选项无效：" + e.Field }
func (e *ValidationError) Unwrap() error { return ErrInvalidRegion }

type catalog struct {
	nodes    map[string]Node
	children map[string][]Node
	cities   []City
}

var options = loadCatalog()

// List 返回下一列的选项；空父节点表示省级列表，有效叶子返回空数组。
func List(parentID string) ([]Node, error) {
	if parentID != "" {
		if _, exists := options.nodes[parentID]; !exists {
			return nil, ErrUnknownParent
		}
	}
	out := make([]Node, 0, len(options.children[parentID]))
	for _, node := range options.children[parentID] {
		if node.ParentID != nil {
			parent := *node.ParentID
			node.ParentID = &parent
		}
		out = append(out, node)
	}
	return out, nil
}

// Validate 按页面原名逐级核对，不能把其他城市的同名区拼进来。
func Validate(province, city, district *string) error {
	if province == nil {
		if city != nil || district != nil {
			return &ValidationError{Field: "region_province"}
		}
		return nil
	}
	provinceID := "province/" + *province
	if node, ok := options.nodes[provinceID]; !ok || node.Level != "province" {
		return &ValidationError{Field: "region_province"}
	}
	if city == nil {
		if district != nil {
			return &ValidationError{Field: "region_city"}
		}
		return nil
	}
	cityID := "city/" + *province + "/" + *city
	if node, ok := options.nodes[cityID]; !ok || node.Level != "city" {
		return &ValidationError{Field: "region_city"}
	}
	if district != nil {
		districtID := "district/" + *province + "/" + *city + "/" + *district
		if node, ok := options.nodes[districtID]; !ok || node.Level != "district" {
			return &ValidationError{Field: "region_district"}
		}
	}
	return nil
}

// Cities 只供市名反查省名，不把全选、城市群或直辖市的区县当成市。
func Cities() []City { return append([]City{}, options.cities...) }

func loadCatalog() catalog {
	out := catalog{nodes: map[string]Node{}, children: map[string][]Node{}, cities: []City{}}
	decoder := json.NewDecoder(bytes.NewReader(regionJSON))
	fail := func(err error) { panic(fmt.Sprintf("随包地区数据损坏：%v", err)) }
	delimiter := func(want json.Delim) {
		token, err := decoder.Token()
		if err != nil || token != want {
			fail(errors.New("对象层级不符"))
		}
	}
	name := func() string {
		token, err := decoder.Token()
		text, ok := token.(string)
		if err != nil || !ok || text == "" || strings.Contains(text, "/") {
			fail(errors.New("地区名称无效"))
		}
		return text
	}
	add := func(id, text, level, parent string) {
		if _, duplicate := out.nodes[id]; duplicate || text == "" || strings.Contains(text, "/") {
			fail(errors.New("地区选项重复或无效"))
		}
		node := Node{ID: id, Name: text, Level: level}
		if parent != "" {
			node.ParentID = &parent
		}
		out.nodes[id] = node
		out.children[parent] = append(out.children[parent], node)
	}
	delimiter('{')
	for decoder.More() {
		province := name()
		provinceID := "province/" + province
		add(provinceID, province, "province", "")
		municipality := province == "北京" || province == "天津" || province == "上海" || province == "重庆"
		if municipality {
			out.cities = append(out.cities, City{Province: province, City: province})
		}
		delimiter('{')
		for decoder.More() {
			city := name()
			cityID := "city/" + province + "/" + city
			add(cityID, city, "city", provinceID)
			if !municipality && province != "全国" && province != "海外" && !strings.HasPrefix(city, "全") {
				out.cities = append(out.cities, City{Province: province, City: city})
			}
			var districts []string
			if err := decoder.Decode(&districts); err != nil || districts == nil {
				fail(errors.New("区级选项不是数组"))
			}
			for _, district := range districts {
				add("district/"+province+"/"+city+"/"+district, district, "district", cityID)
			}
		}
		delimiter('}')
	}
	delimiter('}')
	if decoder.Decode(new(any)) != io.EOF || len(out.nodes) == 0 {
		fail(errors.New("地区数据不完整"))
	}
	return out
}
