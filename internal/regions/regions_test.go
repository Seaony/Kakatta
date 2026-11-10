package regions

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func value(s string) *string { return &s }

func child(t *testing.T, parent, name string) Node {
	t.Helper()
	items, err := List(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Name == name {
			return item
		}
	}
	t.Fatalf("%s 下面缺少 %s", parent, name)
	return Node{}
}

func TestUpstreamSnapshotIsCompleteAndOrdered(t *testing.T) {
	digest := sha256.Sum256(regionJSON)
	if hex.EncodeToString(digest[:]) != "10ea7178fe6ba5007943934b61cef94ae65a2a9eec267b4f423d0d100b062fea" {
		t.Fatal("地区快照与注明的上游版本不符")
	}
	provinces, err := List("")
	if err != nil || len(provinces) != 36 || provinces[0].Name != "全国" || provinces[1].Name != "北京" || provinces[35].Name != "海外" {
		t.Fatalf("省级选项或原顺序缺失：%v %v", provinces, err)
	}
	nation := child(t, "", "全国")
	groups, err := List(nation.ID)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, group := range groups {
		names = append(names, group.Name)
	}
	if !reflect.DeepEqual(names, []string{"全国", "珠三角", "江浙沪", "京津冀", "东三省"}) {
		t.Fatalf("全国特殊选项或原顺序被改动：%v", names)
	}
	seen := map[string]bool{}
	cityCount, districtCount := 0, 0
	for _, province := range provinces {
		if province.ParentID != nil || province.Level != "province" || seen[province.ID] {
			t.Fatalf("省级节点无效：%+v", province)
		}
		seen[province.ID] = true
		if err := Validate(value(province.Name), nil, nil); err != nil {
			t.Fatal(err)
		}
		cities, err := List(province.ID)
		if err != nil || cities == nil {
			t.Fatalf("读取市级选项失败：%s %v", province.Name, err)
		}
		cityCount += len(cities)
		for _, city := range cities {
			if city.ParentID == nil || *city.ParentID != province.ID || city.Level != "city" || seen[city.ID] {
				t.Fatalf("市级节点无效：%+v", city)
			}
			seen[city.ID] = true
			if err := Validate(value(province.Name), value(city.Name), nil); err != nil {
				t.Fatal(err)
			}
			districts, err := List(city.ID)
			if err != nil || districts == nil {
				t.Fatalf("读取区级选项失败：%s/%s %v", province.Name, city.Name, err)
			}
			districtCount += len(districts)
			for _, district := range districts {
				if district.ParentID == nil || *district.ParentID != city.ID || district.Level != "district" || seen[district.ID] {
					t.Fatalf("区级节点无效：%+v", district)
				}
				seen[district.ID] = true
				if err := Validate(value(province.Name), value(city.Name), value(district.Name)); err != nil {
					t.Fatal(err)
				}
				leaf, err := List(district.ID)
				if err != nil || leaf == nil || len(leaf) != 0 {
					t.Fatal("有效叶子必须返回空数组", leaf, err)
				}
			}
		}
	}
	if cityCount != 511 || districtCount != 3505 || len(seen) != 4052 {
		t.Fatalf("上游选项被截断：市=%d 区=%d 全部=%d", cityCount, districtCount, len(seen))
	}
}

func TestValidateRequiresAnExistingWholePath(t *testing.T) {
	for _, test := range []struct {
		name, province, city, district string
	}{
		{"普通省市区", "广东", "深圳", "南山区"},
		{"省全选", "广东", "全广东", ""},
		{"市全选", "广东", "深圳", "全深圳"},
		{"直辖市第二列是区", "北京", "朝阳区", ""},
		{"无第三级的市", "广东", "东莞", ""},
		{"城市群", "全国", "江浙沪", ""},
		{"全国", "全国", "全国", ""},
		{"海外", "海外", "全海外", ""},
		{"台湾", "台湾", "台北", "中正区"},
		{"香港", "香港", "香港", "中西区"},
		{"澳门", "澳门", "澳门", "大堂区"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var province, city, district *string
			if test.province != "" {
				province = value(test.province)
			}
			if test.city != "" {
				city = value(test.city)
			}
			if test.district != "" {
				district = value(test.district)
			}
			if err := Validate(province, city, district); err != nil {
				t.Fatal(err)
			}
		})
	}
	if err := Validate(nil, nil, nil); err != nil {
		t.Fatal("未设置地区应该有效", err)
	}
	for _, test := range []struct {
		province, city, district *string
		field                    string
	}{
		{nil, value("深圳"), nil, "region_province"},
		{value("广东"), nil, value("南山区"), "region_city"},
		{nil, nil, value("南山区"), "region_province"},
		{value(""), nil, nil, "region_province"},
		{value(" 广东"), nil, nil, "region_province"},
		{value("广东省"), nil, nil, "region_province"},
		{value("广东"), value(""), nil, "region_city"},
		{value("广东"), value("深圳市"), nil, "region_city"},
		{value("广东"), value("深圳"), value(""), "region_district"},
		{value("广东"), value("广州"), value("南山区"), "region_district"},
		{value("北京"), value("北京"), nil, "region_city"},
		{value("北京"), value("朝阳区"), value("南山区"), "region_district"},
		{value("全国"), value("江浙沪"), value("上海"), "region_district"},
		{value("海外"), value("深圳"), nil, "region_city"},
	} {
		var invalid *ValidationError
		err := Validate(test.province, test.city, test.district)
		if !errors.As(err, &invalid) || !errors.Is(err, ErrInvalidRegion) || invalid.Field != test.field {
			t.Fatalf("无效层级没有返回准确字段：%+v %v", test, err)
		}
	}
	if items, err := List("province/不存在"); !errors.Is(err, ErrUnknownParent) || items != nil {
		t.Fatal("不存在的父节点不能当成有效空列表", items, err)
	}
}

func TestReturnedNodesAndCitiesCannotChangeNextRead(t *testing.T) {
	province := child(t, "", "广东")
	cities, _ := List(province.ID)
	want := cities[0]
	wantParent := *want.ParentID
	cities[0].ID, cities[0].Name, cities[0].Level = "changed", "changed", "changed"
	*cities[0].ParentID = "changed"
	read, _ := List(province.ID)
	if read[0].ID != want.ID || read[0].Name != want.Name || read[0].Level != want.Level || *read[0].ParentID != wantParent {
		t.Fatal("返回值污染了内部目录", read[0])
	}
	lookup := Cities()
	wantCity := lookup[0]
	lookup[0].Province, lookup[0].City = "changed", "changed"
	if Cities()[0] != wantCity {
		t.Fatal("城市反查数据被调用方修改")
	}
}

func TestCitiesOnlyContainRealCityIdentities(t *testing.T) {
	lookup := Cities()
	byCity := map[string]string{}
	for _, entry := range lookup {
		if entry.Province == "全国" || entry.Province == "海外" || strings.HasPrefix(entry.City, "全") {
			t.Fatalf("特殊筛选项不能当作市名反查：%+v", entry)
		}
		byCity[entry.City] = entry.Province
	}
	for city, province := range map[string]string{"深圳": "广东", "杭州": "浙江", "北京": "北京", "天津": "天津", "上海": "上海", "重庆": "重庆", "台北": "台湾", "香港": "香港", "澳门": "澳门"} {
		if byCity[city] != province {
			t.Fatalf("市名反查缺失：%s → %s，实际%s", city, province, byCity[city])
		}
	}
	if _, exists := byCity["朝阳区"]; exists {
		t.Fatal("直辖市的区不能冒充唯一市名，导致同名区误配")
	}
}

func TestCatalogReadsAreSafeInParallel(t *testing.T) {
	province := child(t, "", "广东")
	var group sync.WaitGroup
	for range 16 {
		group.Go(func() {
			for range 20 {
				items, err := List(province.ID)
				if err != nil || len(items) == 0 || Validate(value("广东"), value("深圳"), value("南山区")) != nil || len(Cities()) == 0 {
					t.Error("并发读取目录失败", err)
				}
				if len(items) > 0 {
					items[0].Name = "局部改动"
				}
			}
		})
	}
	group.Wait()
}
