package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/Seaony/Kakatta/internal/analysis"
)

func TestFavoriteListingRemainsReadableAfterTaskDeletionAndPricesAreNotTruncated(t *testing.T) {
	s, db := queryFixture(t)
	taskID := queryTask(t, db, nil)
	queryListing(t, db, "favorite", "favorite camera", queryPtr(int64(10000)))
	queryFinding(t, db, taskID, "favorite", analysis.DecisionMatched, nil, nil, 100)
	queryExec(t, db, `UPDATE listings SET is_favorite=1,favorited_at=123 WHERE item_id='favorite'`)
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 305; index++ {
		if _, err := tx.ExecContext(t.Context(), `INSERT INTO price_points(item_id,price,observed_at) VALUES('favorite',?,?)`, 10000-index, index/2); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	queryExec(t, db, `DELETE FROM tasks WHERE id=?`, taskID)
	view, err := s.GetListing(t.Context(), "favorite")
	if err != nil {
		t.Fatal(err)
	}
	if !view.IsFavorite || view.Findings == nil || len(view.Findings) != 0 || len(view.Prices) != 305 {
		t.Fatalf("删除任务破坏收藏或静默截断价格：favorite=%v findings=%v prices=%d", view.IsFavorite, view.Findings, len(view.Prices))
	}
	page, err := s.ListListings(t.Context(), ListingQuery{Favorite: queryPtr(true)})
	if err != nil || page.Total != 1 || page.Items[0].ItemID != "favorite" {
		t.Fatalf("删除任务后收藏列表不再可读：%+v %v", page, err)
	}
	q := ListQuery{Limit: 200}
	points := []PricePoint{}
	for {
		page, err := s.ListPrices(t.Context(), "favorite", q)
		if err != nil {
			t.Fatal(err)
		}
		if page.Total != 305 {
			t.Fatalf("价格分页total错误：%d", page.Total)
		}
		points = append(points, page.Items...)
		if page.NextCursor == nil {
			break
		}
		q.Cursor = *page.NextCursor
		q.Limit = 50
	}
	if !reflect.DeepEqual(points, view.Prices) {
		t.Fatalf("同时间价格点翻页不完整：got=%d want=%d", len(points), len(view.Prices))
	}
}

func TestListingPagingHasStableNullableFavoriteOrderAndLiteralSearch(t *testing.T) {
	s, db := queryFixture(t)
	for index, name := range []string{"a", "b", "c", "d", "e"} {
		queryListing(t, db, name, "camera "+name, nil)
		queryExec(t, db, `UPDATE listings SET last_seen_at=? WHERE item_id=?`, index+100, name)
	}
	queryExec(t, db, `UPDATE listings SET is_favorite=1,favorited_at=500,last_seen_at=200 WHERE item_id IN ('a','b')`)
	queryExec(t, db, `UPDATE listings SET is_favorite=1,favorited_at=400 WHERE item_id='c'`)
	queryExec(t, db, `UPDATE listings SET title='camera 50%_off',description='English Needle' WHERE item_id='a'`)
	q := ListingQuery{ListQuery: ListQuery{Limit: 1}}
	ids := []string{}
	var cursor string
	for {
		page, err := s.ListListings(t.Context(), q)
		if err != nil {
			t.Fatal(err)
		}
		if page.Total != 5 {
			t.Fatalf("商品总数错误：%d", page.Total)
		}
		for _, item := range page.Items {
			ids = append(ids, item.ItemID)
		}
		if page.NextCursor == nil {
			break
		}
		q.Cursor = *page.NextCursor
		if cursor == "" {
			cursor = q.Cursor
		}
	}
	if !reflect.DeepEqual(ids, []string{"a", "b", "c", "e", "d"}) {
		t.Fatalf("收藏时间/空时间/ID并列分页错序：%v", ids)
	}
	_, err := s.ListListings(t.Context(), ListingQuery{Favorite: queryPtr(true), ListQuery: ListQuery{Cursor: cursor}})
	assertQueryError(t, err)
	_, err = s.ListListings(t.Context(), ListingQuery{Q: string([]byte{0xff})})
	assertQueryError(t, err)
	for _, needle := range []string{"%_", "needle"} {
		page, err := s.ListListings(t.Context(), ListingQuery{Q: needle})
		if err != nil || page.Total != 1 || page.Items[0].ItemID != "a" {
			t.Fatalf("字面标题/描述搜索错误：%q %+v %v", needle, page, err)
		}
	}
}

func TestPriceCursorCannotCrossItemsAndUnknownListingIsNotAnEmptyHistory(t *testing.T) {
	s, db := queryFixture(t)
	for _, name := range []string{"one", "two"} {
		queryListing(t, db, name, name, nil)
		for index := 0; index < 3; index++ {
			queryExec(t, db, `INSERT INTO price_points(item_id,price,observed_at) VALUES(?,?,?)`, name, index+100, index)
		}
	}
	page, err := s.ListPrices(t.Context(), "one", ListQuery{Limit: 1})
	if err != nil || page.NextCursor == nil {
		t.Fatalf("未得到价格游标：%+v %v", page, err)
	}
	_, err = s.ListPrices(t.Context(), "two", ListQuery{Cursor: *page.NextCursor})
	assertQueryError(t, err)
	_, err = s.ListPrices(t.Context(), "absent", ListQuery{})
	var failure *ServiceError
	if !errors.As(err, &failure) || failure.Kind != ErrorNotFound {
		t.Fatalf("不存在商品被当成空历史：%v", err)
	}
	_, err = s.GetListing(t.Context(), "absent")
	if !errors.As(err, &failure) || failure.Kind != ErrorNotFound {
		t.Fatalf("不存在商品没有返回404语义：%v", err)
	}
}

func TestSellerCachePreservesUnknownVersusKnownEmptyAndRejectsDamage(t *testing.T) {
	s, db := queryFixture(t)
	queryListing(t, db, "seller-item", "camera", nil)
	queryExec(t, db, `UPDATE listings SET seller_id='seller' WHERE item_id='seller-item'`)
	view, err := s.GetListing(t.Context(), "seller-item")
	if err != nil || view.Seller != nil {
		t.Fatalf("未取得的卖家被捏成有资料：%+v %v", view.Seller, err)
	}
	queryExec(t, db, `INSERT INTO seller_profiles(seller_id,fetched_at) VALUES('seller',100)`)
	view, err = s.GetListing(t.Context(), "seller-item")
	if err != nil || view.Seller == nil || view.Seller.RecentRates != nil || view.Seller.SellerGoodCount != nil || view.Seller.BuyerRateCount != nil {
		t.Fatalf("未取到评价没有保留未知：%+v %v", view.Seller, err)
	}
	queryExec(t, db, `UPDATE seller_profiles SET recent_rates_json='[]',recent_items_json='[]',
		seller_good_rate=99.9,seller_good_count=3,seller_rate_count=4,buyer_good_count=0,buyer_rate_count=0 WHERE seller_id='seller'`)
	view, err = s.GetListing(t.Context(), "seller-item")
	if err != nil || view.Seller.RecentRates == nil || view.Seller.RecentItems == nil {
		t.Fatalf("成功取得的空列表没有保留：%+v %v", view.Seller, err)
	}
	if view.Seller.SellerGoodCount == nil || *view.Seller.SellerGoodCount != 3 || view.Seller.SellerRateCount == nil || *view.Seller.SellerRateCount != 4 ||
		view.Seller.BuyerGoodCount == nil || *view.Seller.BuyerGoodCount != 0 || view.Seller.BuyerRateCount == nil || *view.Seller.BuyerRateCount != 0 {
		t.Fatalf("评价原始计数丢失或被比例反推：%+v", view.Seller)
	}
	queryExec(t, db, `UPDATE seller_profiles SET recent_rates_json='{"broken":true}' WHERE seller_id='seller'`)
	_, err = s.GetListing(t.Context(), "seller-item")
	var failure *ServiceError
	if !errors.As(err, &failure) || failure.Kind != ErrorUnavailable {
		t.Fatalf("损坏的缓存被吞成无资料：%v", err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if item, err := loadSellerTx(t.Context(), tx, ""); err != nil || item != nil {
		t.Fatalf("空卖家编号不是无资料：%+v %v", item, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := loadSellerTx(ctx, tx, "seller"); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消被当成无卖家：%v", err)
	}
}

func TestListingViewsUseEmptyArraysForKnownCollections(t *testing.T) {
	s, db := queryFixture(t)
	queryListing(t, db, "empty", "empty", nil)
	view, err := s.GetListing(t.Context(), "empty")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"image_urls", "tags", "findings", "prices"} {
		if string(object[key]) != "[]" {
			t.Fatalf("%s没有保持空数组：%s", key, object[key])
		}
	}
	page, err := s.ListListings(t.Context(), ListingQuery{Q: fmt.Sprintf("missing-%d", 1)})
	if err != nil || page.Items == nil || page.Total != 0 || page.NextCursor != nil {
		t.Fatalf("空列表页不一致：%+v %v", page, err)
	}
}
