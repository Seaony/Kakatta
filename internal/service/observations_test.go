package service

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"
)

func observationTestTask(t *testing.T, tx *sql.Tx, name string) int64 {
	t.Helper()
	result, err := tx.ExecContext(t.Context(), `INSERT INTO tasks(name,keyword,max_pages,interval_minutes,judge_mode,notify_policy,sort_index)
		VALUES(?,?,1,30,'keyword','recommended',0)`, name, name)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func observationTestPrices(t *testing.T, tx *sql.Tx, itemID string) []int64 {
	t.Helper()
	rows, err := tx.QueryContext(t.Context(), `SELECT price FROM price_points WHERE item_id=? ORDER BY observed_at,id`, itemID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	prices := []int64{}
	for rows.Next() {
		var price int64
		if err := rows.Scan(&price); err != nil {
			t.Fatal(err)
		}
		prices = append(prices, price)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return prices
}

func TestObservedPriceUsesLatestPointAcrossTasksAndUnknownPrices(t *testing.T) {
	db := listingTestDB(t)
	tx := listingTestTx(t, db)
	firstTask, otherTask := observationTestTask(t, tx, "相机"), observationTestTask(t, tx, "镜头")
	for index, input := range []struct {
		price     *int64
		changed   bool
		previous  *int64
		drop      *int64
		dropAt    *int64
		wantCount int
	}{
		{listingTestPtr(int64(10000)), true, nil, nil, nil, 1},
		{listingTestPtr(int64(9000)), true, listingTestPtr(int64(10000)), listingTestPtr(int64(1000)), listingTestPtr(int64(101)), 2},
		{listingTestPtr(int64(9000)), false, nil, listingTestPtr(int64(1000)), listingTestPtr(int64(101)), 2},
		{nil, false, nil, listingTestPtr(int64(1000)), listingTestPtr(int64(101)), 2},
		{listingTestPtr(int64(9500)), true, listingTestPtr(int64(9000)), nil, nil, 3},
		{listingTestPtr(int64(0)), true, listingTestPtr(int64(9500)), listingTestPtr(int64(9500)), listingTestPtr(int64(105)), 4},
	} {
		observedAt := int64(100 + index)
		item, change, err := upsertSearchListingTx(t.Context(), tx, SearchObservation{
			ItemID: "camera", Title: "相机", Price: input.price, ObservedAt: observedAt,
		})
		if err != nil || (change != nil) != input.changed {
			t.Fatalf("第%d次价格记录错误：%+v，%v", index+1, change, err)
		}
		if change != nil && (!reflect.DeepEqual(change.Previous, input.previous) || change.Current != *input.price || change.ObservedAt != observedAt) {
			t.Fatalf("价格变化未根据最近价格点比较：%+v", change)
		}
		if !reflect.DeepEqual(item.Price, input.price) || !reflect.DeepEqual(item.LastDropAmount, input.drop) || !reflect.DeepEqual(item.LastDropAt, input.dropAt) || item.FirstSeenAt != 100 || item.LastSeenAt != observedAt {
			t.Fatalf("当前价格或降价标签不符：%+v", item)
		}
		if prices := observationTestPrices(t, tx, "camera"); len(prices) != input.wantCount {
			t.Fatalf("重复价格或未知价格追加了点：%v", prices)
		}
		for _, taskID := range []int64{firstTask, otherTask} {
			if err := recordSightingTx(t.Context(), tx, taskID, "camera", input.price, observedAt, "UTC"); err != nil {
				t.Fatal(err)
			}
		}
		// 另一个任务观察同价仍只能共用全局价格点。
		if _, duplicate, err := upsertSearchListingTx(t.Context(), tx, SearchObservation{ItemID: "camera", Price: input.price, ObservedAt: observedAt}); err != nil || duplicate != nil {
			t.Fatalf("跨任务同价重复追加：%+v，%v", duplicate, err)
		}
	}
	if prices := observationTestPrices(t, tx, "camera"); !reflect.DeepEqual(prices, []int64{10000, 9000, 9500, 0}) {
		t.Fatalf("价格历史不准确：%v", prices)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal("商品写入提前结束外层事务", err)
	}
}

func TestLateSearchDoesNotMoveListingOrPriceHistoryBackwards(t *testing.T) {
	db := listingTestDB(t)
	tx := listingTestTx(t, db)
	if _, _, err := upsertSearchListingTx(t.Context(), tx, SearchObservation{ItemID: "camera", Title: "旧标题", Price: listingTestPtr(int64(10000)), ObservedAt: 100}); err != nil {
		t.Fatal(err)
	}
	current, _, err := upsertSearchListingTx(t.Context(), tx, SearchObservation{ItemID: "camera", Title: "新标题", Price: listingTestPtr(int64(9000)), ObservedAt: 300})
	if err != nil {
		t.Fatal(err)
	}
	for _, price := range []*int64{listingTestPtr(int64(5000)), nil} {
		late, change, err := upsertSearchListingTx(t.Context(), tx, SearchObservation{ItemID: "camera", Title: "晚到旧标题", Price: price, ObservedAt: 200})
		if err != nil || change != nil || !reflect.DeepEqual(current, late) {
			t.Fatalf("晚到搜索倒退了当前数据：%+v，%+v，%v", late, change, err)
		}
	}
	if prices := observationTestPrices(t, tx, "camera"); !reflect.DeepEqual(prices, []int64{10000, 9000}) {
		t.Fatalf("晚到搜索补了旧价格点：%v", prices)
	}
	// 同一毫秒仍按提交顺序接纳，价格点用id打破相同时间的并列。
	if _, _, err := upsertSearchListingTx(t.Context(), tx, SearchObservation{ItemID: "camera", Price: listingTestPtr(int64(8000)), ObservedAt: 300}); err != nil {
		t.Fatal(err)
	}
	item, change, err := upsertSearchListingTx(t.Context(), tx, SearchObservation{ItemID: "camera", Price: listingTestPtr(int64(8500)), ObservedAt: 301})
	if err != nil || change == nil || change.Previous == nil || *change.Previous != 8000 || item.LastDropAmount != nil {
		t.Fatalf("相同毫秒价格点顺序错误：%+v，%+v，%v", item, change, err)
	}
}

func TestSparseSearchPreservesDetailAndLocalFlags(t *testing.T) {
	db := listingTestDB(t)
	tx := listingTestTx(t, db)
	if _, _, err := upsertSearchListingTx(t.Context(), tx, SearchObservation{
		ItemID: "camera", Title: "原始标题", DesktopURL: "https://example.invalid/item", Price: listingTestPtr(int64(100)), PriceText: listingTestPtr("¥1"),
		Tags: []string{"包邮"}, Area: listingTestPtr("上海"), WantCount: listingTestPtr(int64(12)), ObservedAt: 100,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(t.Context(), `UPDATE listings SET image_urls_json='["detail-image"]',description='详情正文',seller_id='seller',
		browse_count=42,detail_fetched_at=120,is_favorite=1,favorited_at=130,is_dismissed=1 WHERE item_id='camera'`); err != nil {
		t.Fatal(err)
	}
	item, _, err := upsertSearchListingTx(t.Context(), tx, SearchObservation{ItemID: "camera", ObservedAt: 200})
	if err != nil || item.Price != nil || item.PriceText != nil || item.Title != "原始标题" || item.DesktopURL != "https://example.invalid/item" ||
		item.Area == nil || *item.Area != "上海" || item.WantCount == nil || *item.WantCount != 12 || !reflect.DeepEqual(item.Tags, []string{"包邮"}) ||
		!reflect.DeepEqual(item.ImageURLs, []string{"detail-image"}) || item.Description == nil || *item.Description != "详情正文" || item.SellerID == nil || *item.SellerID != "seller" ||
		item.BrowseCount == nil || *item.BrowseCount != 42 || item.DetailFetchedAt == nil || *item.DetailFetchedAt != 120 || !item.IsFavorite || !item.IsDismissed || item.FavoritedAt == nil || *item.FavoritedAt != 130 {
		t.Fatalf("稀疏搜索清掉了缓存或本地标记：%+v，%v", item, err)
	}
	item, _, err = upsertSearchListingTx(t.Context(), tx, SearchObservation{ItemID: "camera", Price: listingTestPtr(int64(0)), Area: listingTestPtr(""), WantCount: listingTestPtr(int64(0)), Tags: []string{}, ObservedAt: 201})
	if err != nil || item.Price == nil || *item.Price != 0 || item.Area == nil || *item.Area != "" || item.WantCount == nil || *item.WantCount != 0 || item.Tags == nil || len(item.Tags) != 0 {
		t.Fatalf("明确空值或已知零未保存：%+v，%v", item, err)
	}
}

func TestSightingUsesObservedLocalDateAndLatestNullablePrice(t *testing.T) {
	db := listingTestDB(t)
	tx := listingTestTx(t, db)
	taskID := observationTestTask(t, tx, "相机")
	before := time.Date(2026, 11, 10, 15, 59, 0, 0, time.UTC).UnixMilli()
	after := before + int64(2*time.Minute/time.Millisecond)
	for _, input := range []struct {
		at    int64
		price *int64
	}{
		{before, listingTestPtr(int64(10000))},
		{after, listingTestPtr(int64(9000))},
		{after + 20, nil},
		{after + 10, listingTestPtr(int64(9500))},
	} {
		if err := recordSightingTx(t.Context(), tx, taskID, "camera", input.price, input.at, "Asia/Shanghai"); err != nil {
			t.Fatal(err)
		}
	}
	var price sql.NullInt64
	var at int64
	if err := tx.QueryRowContext(t.Context(), `SELECT price,last_seen_at FROM task_sightings WHERE task_id=? AND day='2026-11-11' AND item_id='camera'`, taskID).Scan(&price, &at); err != nil || price.Valid || at != after+20 {
		t.Fatalf("当日未知价格被旧观察或前一天补回：%+v，%d，%v", price, at, err)
	}
	if err := tx.QueryRowContext(t.Context(), `SELECT price FROM task_sightings WHERE task_id=? AND day='2026-11-10' AND item_id='camera'`, taskID).Scan(&price); err != nil || !price.Valid || price.Int64 != 10000 {
		t.Fatalf("跨零点的前一天记录丢失：%+v，%v", price, err)
	}
	for _, zone := range []string{"", "Local", "invalid/zone"} {
		if err := recordSightingTx(t.Context(), tx, taskID, "camera", listingTestPtr(int64(5000)), after+30, zone); err == nil {
			t.Fatal("接受了无效或本机隐式时区", zone)
		}
	}
}

func TestObservationFailureRollsBackListingPointsAndSighting(t *testing.T) {
	db := listingTestDB(t)
	tx := listingTestTx(t, db)
	taskID := observationTestTask(t, tx, "相机")
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `CREATE TRIGGER reject_price_point BEFORE INSERT ON price_points BEGIN SELECT RAISE(ABORT,'test-point-write'); END`); err != nil {
		t.Fatal(err)
	}
	tx = listingTestTx(t, db)
	if _, _, err := upsertSearchListingTx(t.Context(), tx, SearchObservation{ItemID: "camera", Price: listingTestPtr(int64(100)), ObservedAt: 100}); err == nil {
		t.Fatal("价格点写失败被吞掉")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM listings`).Scan(&count); err != nil || count != 0 {
		t.Fatal("失败后留下了半条商品", err)
	}
	if _, err := db.ExecContext(t.Context(), `DROP TRIGGER reject_price_point`); err != nil {
		t.Fatal(err)
	}
	tx = listingTestTx(t, db)
	if _, _, err := upsertSearchListingTx(t.Context(), tx, SearchObservation{ItemID: "camera", Price: listingTestPtr(int64(100)), ObservedAt: 100}); err != nil {
		t.Fatal(err)
	}
	if err := recordSightingTx(t.Context(), tx, taskID, "camera", listingTestPtr(int64(100)), 100, "UTC"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"listings", "price_points", "task_sightings"} {
		if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM `+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("外层回滚未覆盖%s：%d，%v", table, count, err)
		}
	}
	tx = listingTestTx(t, db)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := upsertSearchListingTx(ctx, tx, SearchObservation{ItemID: "camera", ObservedAt: 100}); !errors.Is(err, context.Canceled) {
		t.Fatalf("商品写入吞掉取消：%v", err)
	}
}

func TestDetailWriteUsesCurrentWorkAndPreservesUnknownFields(t *testing.T) {
	db := listingTestDB(t)
	tx := listingTestTx(t, db)
	taskID := observationTestTask(t, tx, "相机")
	listingTestSeed(t, tx, "camera")
	result, err := tx.ExecContext(t.Context(), `INSERT INTO findings(task_id,item_id,search_generation,found_at,is_baseline,decision,stage,is_read)
		VALUES(?,'camera',1,10,0,'pending','detail',0)`, taskID)
	if err != nil {
		t.Fatal(err)
	}
	findingID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	work := WorkRef{RunRef: RunRef{TaskID: taskID, SearchGeneration: 1}, FindingID: findingID, ProcessingRevision: 1}
	first := DetailObservation{ItemID: "camera", ImageURLs: []string{"detail-image"}, Description: listingTestPtr("详情正文"),
		WantCount: listingTestPtr(int64(0)), BrowseCount: listingTestPtr(int64(100)), SellerID: listingTestPtr("seller"),
		SellerRegistrationDays: listingTestPtr(int64(365)), ZhimaLevel: listingTestPtr("信用极好"), SaleStatus: listingTestPtr("on_sale"), FetchedAt: 200}
	if err := saveDetailTx(t.Context(), tx, work, first); err != nil {
		t.Fatal(err)
	}
	if err := saveDetailTx(t.Context(), tx, work, DetailObservation{ItemID: "camera", FetchedAt: 300}); err != nil {
		t.Fatal(err)
	}
	item, err := loadListingTx(t.Context(), tx, "camera")
	if err != nil || item.Description == nil || *item.Description != "详情正文" || !reflect.DeepEqual(item.ImageURLs, []string{"detail-image"}) ||
		item.WantCount == nil || *item.WantCount != 0 || item.BrowseCount == nil || *item.BrowseCount != 100 || item.DetailFetchedAt == nil || *item.DetailFetchedAt != 300 || item.LastSeenAt != 10 {
		t.Fatalf("缺失详情字段擦掉缓存或改了搜索时间：%+v，%v", item, err)
	}
	if err := saveDetailTx(t.Context(), tx, work, DetailObservation{ItemID: "camera", Description: listingTestPtr("晚到旧详情"), FetchedAt: 250}); err != nil {
		t.Fatal(err)
	}
	late, err := loadListingTx(t.Context(), tx, "camera")
	if err != nil || !reflect.DeepEqual(item, late) {
		t.Fatalf("详情缓存倒退：%+v，%v", late, err)
	}
	if err := saveDetailTx(t.Context(), tx, work, DetailObservation{ItemID: "other", FetchedAt: 400}); err == nil {
		t.Fatal("详情可以写到另一个商品")
	}
	if err := saveDetailTx(t.Context(), tx, work, DetailObservation{ItemID: "camera", ImageURLs: []string{}, Description: listingTestPtr(""), FetchedAt: 400}); err != nil {
		t.Fatal(err)
	}
	item, err = loadListingTx(t.Context(), tx, "camera")
	if err != nil || item.Description == nil || *item.Description != "" || item.ImageURLs == nil || len(item.ImageURLs) != 0 {
		t.Fatalf("明确空详情未清理旧值：%+v，%v", item, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []string{
		`UPDATE findings SET processing_revision=2 WHERE id=?`,
		`UPDATE findings SET stage='title' WHERE id=?`,
		`DELETE FROM findings WHERE id=?`,
	} {
		tx = listingTestTx(t, db)
		if _, err := tx.ExecContext(t.Context(), mutation, findingID); err != nil {
			t.Fatal(err)
		}
		if err := saveDetailTx(t.Context(), tx, work, first); err == nil {
			t.Fatal("过期或删除的处理结果仍可写回", mutation)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
	}
	tx = listingTestTx(t, db)
	if _, err := tx.ExecContext(t.Context(), `UPDATE tasks SET search_generation=2 WHERE id=?`, taskID); err != nil {
		t.Fatal(err)
	}
	if err := saveDetailTx(t.Context(), tx, work, first); err == nil {
		t.Fatal("旧搜索代次的详情仍可写回")
	}
}
