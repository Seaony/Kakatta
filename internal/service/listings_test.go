package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/store"
)

func listingTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "listings.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if db.DB().Stats().MaxOpenConnections != 1 {
		t.Fatal("商品回归需要真实单连接数据库")
	}
	return db.DB()
}

func listingTestTx(t *testing.T, db *sql.DB) *sql.Tx {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tx.Rollback() })
	return tx
}

func listingTestPtr[T any](value T) *T { return &value }

func listingTestSeed(t *testing.T, tx *sql.Tx, itemID string) {
	t.Helper()
	if _, err := tx.ExecContext(t.Context(), `INSERT INTO listings(item_id,title,desktop_url,first_seen_at,last_seen_at) VALUES(?,'相机','',10,10)`, itemID); err != nil {
		t.Fatal(err)
	}
}

func TestLoadListingPreservesUnknownAndRejectsDamagedJSON(t *testing.T) {
	db := listingTestDB(t)
	tx := listingTestTx(t, db)
	listingTestSeed(t, tx, "camera")
	item, err := loadListingTx(t.Context(), tx, "camera")
	if err != nil || item.Price != nil || item.DetailFetchedAt != nil || item.SellerID != nil || item.ImageURLs == nil || item.Tags == nil {
		t.Fatalf("未知值或空数组发生变化：%+v，%v", item, err)
	}
	raw, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	var encoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &encoded); err != nil || string(encoded["price"]) != "null" || string(encoded["image_urls"]) != "[]" || string(encoded["tags"]) != "[]" {
		t.Fatalf("公开JSON没有保留null和[]：%s，%v", raw, err)
	}
	for _, column := range []string{"image_urls_json", "tags_json"} {
		if _, err := tx.ExecContext(t.Context(), `UPDATE listings SET `+column+`='{}' WHERE item_id='camera'`); err != nil {
			t.Fatal(err)
		}
		if _, err := loadListingTx(t.Context(), tx, "camera"); err == nil {
			t.Fatal("损坏的列表被当成空数组", column)
		}
		if _, err := tx.ExecContext(t.Context(), `UPDATE listings SET `+column+`='null' WHERE item_id='camera'`); err != nil {
			t.Fatal(err)
		}
	}
	item, err = loadListingTx(t.Context(), tx, "camera")
	if err != nil || item.ImageURLs == nil || item.Tags == nil {
		t.Fatal("旧null列表没有按公开空数组读取", err)
	}
	_, err = loadListingTx(t.Context(), tx, "missing")
	var missing *ServiceError
	if !errors.As(err, &missing) || missing.Kind != ErrorNotFound || missing.Reason.Code != "listings.not_found" {
		t.Fatalf("缺失商品没有返回明确不存在：%v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := loadListingTx(ctx, tx, "camera"); !errors.Is(err, context.Canceled) {
		t.Fatalf("读取吞掉取消：%v", err)
	}
}

func TestListingFlagsAreGlobalIdempotentAndTransactional(t *testing.T) {
	db := listingTestDB(t)
	tx := listingTestTx(t, db)
	listingTestSeed(t, tx, "camera")
	for _, input := range []struct {
		favorite, dismissed *bool
		at                  int64
		changed             bool
		wantFavorite        bool
		wantDismissed       bool
		favoriteAt          *int64
	}{
		{nil, nil, 20, false, false, false, nil},
		{listingTestPtr(true), nil, 30, true, true, false, listingTestPtr(int64(30))},
		{listingTestPtr(true), nil, 40, false, true, false, listingTestPtr(int64(30))},
		{nil, listingTestPtr(true), 50, true, true, true, listingTestPtr(int64(30))},
		{listingTestPtr(false), nil, 60, true, false, true, nil},
		{nil, listingTestPtr(false), 70, true, false, false, nil},
	} {
		changed, err := setListingFlagsTx(t.Context(), tx, "camera", input.favorite, input.dismissed, input.at)
		if err != nil || changed != input.changed {
			t.Fatalf("本地标记变化不符：%v %v", changed, err)
		}
		item, err := loadListingTx(t.Context(), tx, "camera")
		if err != nil || item.IsFavorite != input.wantFavorite || item.IsDismissed != input.wantDismissed ||
			(item.FavoritedAt == nil) != (input.favoriteAt == nil) || item.FavoritedAt != nil && *item.FavoritedAt != *input.favoriteAt || item.LastSeenAt != 10 {
			t.Fatalf("标记或首次收藏时间错误：%+v，%v", item, err)
		}
	}
	if _, err := setListingFlagsTx(t.Context(), tx, "missing", listingTestPtr(true), nil, 80); err == nil {
		t.Fatal("缺失商品被假报成功")
	}
	if err := tx.Commit(); err != nil {
		t.Fatal("标记函数擅自结束了调用者事务", err)
	}
	tx = listingTestTx(t, db)
	if _, err := setListingFlagsTx(t.Context(), tx, "camera", listingTestPtr(true), listingTestPtr(true), 90); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	tx = listingTestTx(t, db)
	item, err := loadListingTx(t.Context(), tx, "camera")
	if err != nil || item.IsFavorite || item.IsDismissed || item.FavoritedAt != nil {
		t.Fatalf("本地标记未随外层回滚：%+v，%v", item, err)
	}
}

func TestSellerBlockedLookupUsesCallerTransaction(t *testing.T) {
	db := listingTestDB(t)
	tx := listingTestTx(t, db)
	if blocked, err := isSellerBlockedTx(t.Context(), tx, "seller"); err != nil || blocked {
		t.Fatalf("不存在的卖家被屏蔽：%v，%v", blocked, err)
	}
	if _, err := tx.ExecContext(t.Context(), `INSERT INTO blocked_sellers(seller_id) VALUES('seller')`); err != nil {
		t.Fatal(err)
	}
	if blocked, err := isSellerBlockedTx(t.Context(), tx, "seller"); err != nil || !blocked {
		t.Fatalf("没有读取同一事务中的屏蔽：%v，%v", blocked, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	tx = listingTestTx(t, db)
	if blocked, err := isSellerBlockedTx(t.Context(), tx, "seller"); err != nil || blocked {
		t.Fatalf("屏蔽没有随事务回滚：%v，%v", blocked, err)
	}
}
