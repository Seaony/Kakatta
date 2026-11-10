package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Seaony/Kakatta/internal/analysis"
)

func TestListRegionsUsesTheBundledHierarchy(t *testing.T) {
	s := &Service{}
	provinces, err := s.ListRegions(t.Context(), "")
	if err != nil || len(provinces) != 36 || provinces[0].Name != "全国" || provinces[35].Name != "海外" {
		t.Fatalf("地区服务没有使用完整随包选项：%v %v", provinces, err)
	}
	var guangdong RegionNode
	for _, province := range provinces {
		if province.Name == "广东" {
			guangdong = province
		}
	}
	cities, err := s.ListRegions(t.Context(), guangdong.ID)
	if err != nil || len(cities) == 0 || cities[0].Name != "全广东" {
		t.Fatal("市级选项未保留页面顺序", cities, err)
	}
	*cities[0].ParentID = "局部修改"
	again, err := s.ListRegions(t.Context(), guangdong.ID)
	if err != nil || *again[0].ParentID != guangdong.ID {
		t.Fatal("服务返回值影响了下一次读取", again, err)
	}
	var invalid *ServiceError
	if _, err := s.ListRegions(t.Context(), "不存在"); !errors.As(err, &invalid) || invalid.Kind != ErrorBadRequest {
		t.Fatal("未知父节点没有作为查询错误返回", err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.ListRegions(canceled, ""); !errors.Is(err, context.Canceled) {
		t.Fatal("地区查询吞掉了取消", err)
	}
}

func TestRegionCitiesFeedFirstLayerWithoutInventingProvince(t *testing.T) {
	for _, test := range []struct {
		province, area string
		rejected       bool
	}{
		{"浙江", "杭州", false},
		{"广东", "杭州", true},
		{"北京", "北京", false},
		{"江苏", "南京", false},
	} {
		result := analysis.FirstLayer(analysis.FirstLayerInput{
			Mode: analysis.ModeAI, Province: test.province, City: "全" + test.province,
			Area: test.area, Cities: regionCities(),
		})
		if result.Rejected != test.rejected {
			t.Fatalf("市名反查省名错误：%+v → %+v", test, result)
		}
	}
	first := regionCities()
	want := first[0]
	first[0].Province, first[0].City = "局部修改", "局部修改"
	if regionCities()[0] != want {
		t.Fatal("地区投影共享了可修改切片")
	}
}
