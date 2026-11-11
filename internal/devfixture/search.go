package devfixture

import "github.com/Seaony/Kakatta/internal/xianyu/fake"

func ComparisonPages() ([]fake.SearchItem, []fake.SearchItem) {
	first := []fake.SearchItem{{ID: "101", Title: "MacBook Pro 自用一", Price: "10000"}, {ID: "102", Title: "MacBook Pro 自用二", Price: "10000"}, {ID: "103", Title: "MacBook Pro 自用三", Price: "10000"}}
	second := []fake.SearchItem{{ID: "103", Title: "MacBook Pro 自用三", Price: "10000"}, {ID: "104", Title: "MacBook Pro 自用四", Price: "10000"}, {ID: "105", Title: "MacBook Pro 自用五", Price: "10000"}}
	for _, items := range [][]fake.SearchItem{first, second} {
		for i := range items {
			items[i].SellerID = "seller" + items[i].ID
		}
	}
	return first, second
}
