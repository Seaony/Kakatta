package service

import (
	"database/sql"
	"encoding/json"
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/limits"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/store"
	"github.com/Seaony/Kakatta/internal/xianyu"
)

func sellerDetailListing(t *testing.T, db *sql.DB, id string) Listing {
	t.Helper()
	tx := listingTestTx(t, db)
	item, err := loadListingTx(t.Context(), tx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	return item
}

func TestSearchSellerIDFlowsIntoStorageAndBlocksBeforeDetail(t *testing.T) {
	h := newFindingsHarness(t)
	ref := h.task(analysis.ModeKeyword, false)
	h.exec(`INSERT INTO blocked_sellers(seller_id,nick,created_at) VALUES('blocked','屏蔽卖家',1)`)
	observations := searchObservations([]xianyu.SearchItem{{ID: "blocked-item", Title: "camera", SellerID: "blocked"}}, h.now)
	if len(observations) != 1 || observations[0].SellerID == nil || *observations[0].SellerID != "blocked" {
		t.Fatal("搜索卖家编号没有进入正式观察")
	}
	tx := h.tx()
	saved, err := h.s.persistSearchTx(t.Context(), tx, ref, observations, h.now)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if len(saved.NewFindingIDs) != 1 {
		t.Fatal("屏蔽商品没有保留发现记录")
	}
	item := sellerDetailListing(t, h.s.db, "blocked-item")
	finding := h.read(saved.NewFindingIDs[0])
	if item.SellerID == nil || *item.SellerID != "blocked" || item.DetailFetchedAt != nil || finding.Decision != DecisionSkipped || finding.Stage != StageDone || finding.Reason == nil || finding.Reason.Code != "findings.blocked_seller" {
		t.Fatalf("搜索阶段没有在详情前拦下已知屏蔽卖家：商品=%+v 发现=%+v", item, finding)
	}
}

func TestSearchSellerIDPreservesUnknownAndIgnoresLateObservation(t *testing.T) {
	db := listingTestDB(t)
	tx := listingTestTx(t, db)
	for _, input := range []struct {
		at   int64
		id   *string
		want string
	}{
		{100, listingTestPtr("seller-one"), "seller-one"},
		{200, nil, "seller-one"},
		{201, listingTestPtr(""), "seller-one"},
		{202, listingTestPtr("   "), "seller-one"},
		{300, listingTestPtr("seller-two"), "seller-two"},
		{250, listingTestPtr("late-seller"), "seller-two"},
	} {
		item, _, err := upsertSearchListingTx(t.Context(), tx, SearchObservation{ItemID: "camera", Title: "camera", SellerID: input.id, ObservedAt: input.at})
		if err != nil || item.SellerID == nil || *item.SellerID != input.want {
			t.Fatalf("搜索卖家编号被空值或晚到数据覆盖：at=%d 商品=%+v err=%v", input.at, item, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := searchObservations([]xianyu.SearchItem{{ID: "unknown", Title: "camera"}}, 400); len(got) != 1 || got[0].SellerID != nil {
		t.Fatal("未知卖家编号没有保持空值")
	}
}

func TestSellerDetailMetricsSnapshotAndWorkVersionBoundaries(t *testing.T) {
	h := newFindingsHarness(t)
	work := h.finding(h.task(analysis.ModeKeyword, false), "item", StageDetail, DecisionPending, h.now)
	first := &xianyu.SellerDetailMetrics{ItemCount: listingTestPtr(int64(0)), HasSoldNumInteger: listingTestPtr(int64(3)), AvgReply30dLong: listingTestPtr(0.0), NewGoodRatioRate: listingTestPtr(" 100% "), LastVisitTime: listingTestPtr("刚刚来过"), IdleFishCreditLevel: listingTestPtr("L3"), ZhimaAuth: listingTestPtr(false)}
	write := func(at int64, metrics *xianyu.SellerDetailMetrics) {
		t.Helper()
		tx := h.tx()
		if err := saveDetailTx(t.Context(), tx, work, DetailObservation{ItemID: "item", SellerDetail: metrics, FetchedAt: at}); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	if item := sellerDetailListing(t, h.s.db, "item"); item.SellerDetail != nil {
		t.Fatal("旧商品指标被编成了零值")
	} else if raw, err := json.Marshal(item); err != nil || strings.Contains(string(raw), `"seller_detail"`) {
		t.Fatalf("旧商品公开JSON没有省略未知指标：%s %v", raw, err)
	}
	write(300, first)
	write(200, &xianyu.SellerDetailMetrics{ItemCount: listingTestPtr(int64(99)), ZhimaAuth: listingTestPtr(true)})
	write(400, nil)
	if item := sellerDetailListing(t, h.s.db, "item"); !reflect.DeepEqual(item.SellerDetail, first) || item.DetailFetchedAt == nil || *item.DetailFetchedAt != 400 {
		t.Fatalf("未知或晚到详情清掉原指标：%+v", item)
	}
	partial := &xianyu.SellerDetailMetrics{ItemCount: listingTestPtr(int64(7))}
	write(500, partial)
	if item := sellerDetailListing(t, h.s.db, "item"); !reflect.DeepEqual(item.SellerDetail, partial) {
		t.Fatalf("新快照缺失的字段被旧指标填回：%+v", item.SellerDetail)
	}
	h.exec(`UPDATE findings SET processing_revision=processing_revision+1 WHERE id=?`, work.FindingID)
	tx := h.tx()
	if err := saveDetailTx(t.Context(), tx, work, DetailObservation{ItemID: "item", SellerDetail: first, FetchedAt: 600}); err == nil {
		t.Fatal("旧工作版本覆盖了新指标")
	}
	tx.Rollback()
	if item := sellerDetailListing(t, h.s.db, "item"); !reflect.DeepEqual(item.SellerDetail, partial) || *item.DetailFetchedAt != 500 {
		t.Fatal("旧工作版本改了详情快照")
	}
}

func TestSellerDetailMetricsRejectInvalidObservationWithoutPartialWrite(t *testing.T) {
	h := newFindingsHarness(t)
	work := h.finding(h.task(analysis.ModeKeyword, false), "item", StageDetail, DecisionPending, h.now)
	for _, metrics := range []*xianyu.SellerDetailMetrics{
		{ItemCount: listingTestPtr(int64(-1))},
		{HasSoldNumInteger: listingTestPtr(limits.MaxSafeInteger + 1)},
		{AvgReply30dLong: listingTestPtr(math.NaN())},
		{LastVisitTime: listingTestPtr(" ")},
	} {
		tx := h.tx()
		if err := saveDetailTx(t.Context(), tx, work, DetailObservation{ItemID: "item", Description: listingTestPtr("不应写入"), SellerDetail: metrics, FetchedAt: 300}); err == nil {
			t.Fatal("非法指标观察没有被拒绝")
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if item := sellerDetailListing(t, h.s.db, "item"); item.Description != nil || item.SellerDetail != nil || item.DetailFetchedAt != nil {
			t.Fatal("非法指标观察留下了部分写入")
		}
	}
}

func TestSellerDetailMetricsSurviveReopenAndValidatedRestore(t *testing.T) {
	h, cipher, work := storedValidationFixture(t)
	h.exec(`UPDATE findings SET stage='detail',decision='pending' WHERE id=?`, work.FindingID)
	metrics := &xianyu.SellerDetailMetrics{ItemCount: listingTestPtr(int64(0)), HasSoldNumInteger: listingTestPtr(int64(3)), AvgReply30dLong: listingTestPtr(12.5), NewGoodRatioRate: listingTestPtr("100%"), LastVisitTime: listingTestPtr("刚刚来过"), IdleFishCreditLevel: listingTestPtr("L3"), ZhimaAuth: listingTestPtr(false)}
	tx := h.tx()
	if err := saveDetailTx(t.Context(), tx, work, DetailObservation{ItemID: "item", SellerDetail: metrics, FetchedAt: h.now}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var seq int
	var name, path string
	if err := h.s.db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	if err := h.s.db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if item := sellerDetailListing(t, reopened.DB(), "item"); !reflect.DeepEqual(item.SellerDetail, metrics) {
		t.Fatalf("重开丢失原值：%+v", item.SellerDetail)
	}
	backup := filepath.Join(t.TempDir(), "restored.db")
	if err := reopened.Backup(t.Context(), backup); err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateBackup(t.Context(), backup, store.SchemaVersion()); err != nil {
		t.Fatal(err)
	}
	restored, err := store.Open(t.Context(), backup)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if err := ValidateStored(t.Context(), restored.DB(), cipher, settings.Environment{TZ: "UTC"}); err != nil {
		t.Fatal(err)
	}
	item := sellerDetailListing(t, restored.DB(), "item")
	if !reflect.DeepEqual(item.SellerDetail, metrics) {
		t.Fatalf("已验收的恢复改了原指标：%+v", item.SellerDetail)
	}
	raw, err := json.Marshal(item)
	if err != nil || !strings.Contains(string(raw), `"item_count":0`) || !strings.Contains(string(raw), `"zhima_auth":false`) {
		t.Fatalf("已知零和false没有出现在公开JSON中：%s %v", raw, err)
	}
}

func TestSellerDetailMetricsRejectDamagedJSONInReadsAndRestore(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `0`, `{"extra":1}`, `{"item_count":"0"}`, `{"zhima_auth":0}`, `{"item_count":-1}`, `{"item_count":9007199254740992}`, `{"has_sold_num_integer":9007199254740992}`, `{"avg_reply_30d_long":-0.1}`, `{"last_visit_time":3}`, `{"item_count":0} {}`} {
		t.Run(raw, func(t *testing.T) {
			h, cipher, _ := storedValidationFixture(t)
			h.exec(`UPDATE listings SET seller_detail_json=? WHERE item_id='item'`, raw)
			tx := h.tx()
			if _, err := loadListingTx(t.Context(), tx, "item"); err == nil {
				t.Fatal("损坏的卖家指标被当成正常值")
			}
			tx.Rollback()
			if err := ValidateStored(t.Context(), h.s.db, cipher, settings.Environment{TZ: "UTC"}); err == nil || !strings.Contains(err.Error(), "seller_detail_json") {
				t.Fatalf("候选恢复漏掉坏指标或没有准确定位：%v", err)
			}
		})
	}
}
