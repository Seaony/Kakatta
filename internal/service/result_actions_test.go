package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/store"
)

func resultActionDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "actions.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db.DB()
}

func resultActionSeed(t *testing.T, db *sql.DB, taskID, findingID int64, itemID string, favorite bool) {
	t.Helper()
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`INSERT OR IGNORE INTO tasks(id,name,keyword,max_pages,interval_minutes,judge_mode,notify_policy,sort_index) VALUES(?,?,?,1,60,'keyword','recommended',?)`, []any{taskID, "测试任务", "相机", taskID}},
		{`INSERT OR IGNORE INTO listings(item_id,title,desktop_url,first_seen_at,last_seen_at,is_favorite) VALUES(?,?,?,1,2,?)`, []any{itemID, "相机", "https://example.invalid/item", favorite}},
		{`INSERT INTO findings(id,task_id,item_id,search_generation,found_at,is_baseline,decision,stage,is_read) VALUES(?,?,?,1,2,0,'matched','done',0)`, []any{findingID, taskID, itemID}},
		{`INSERT OR IGNORE INTO seen_items(task_id,item_id,first_seen_at) VALUES(?,?,1)`, []any{taskID, itemID}},
	} {
		if _, err := db.ExecContext(t.Context(), statement.sql, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
}

func resultActionCount(t *testing.T, db *sql.DB, query string, args ...any) int64 {
	t.Helper()
	var count int64
	if err := db.QueryRowContext(t.Context(), query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestSelectedFindingIDsValidateAndKeepOrder(t *testing.T) {
	input := []int64{3, 1, 3, 2, 1, math.MaxInt64}
	snapshot := slices.Clone(input)
	got, err := selectedFindingIDs(input)
	if err != nil || !reflect.DeepEqual(got, []int64{3, 1, 2, math.MaxInt64}) {
		t.Fatalf("稳定去重失败：%v，%v", got, err)
	}
	if !slices.Equal(input, snapshot) {
		t.Fatal("批量校验修改了调用方的选择")
	}
	for _, invalid := range [][]int64{nil, {}, {0}, {-1}, {1, 2, 0}} {
		_, err := selectedFindingIDs(invalid)
		var failure *ServiceError
		if !errors.As(err, &failure) || failure.Kind != ErrorInvalid || failure.Reason.Code != "findings.invalid_selection" {
			t.Fatalf("非法选择没有拒绝：%v，%v", invalid, err)
		}
	}
}

func TestSelectedFindingIDsLimitCountsUniqueRows(t *testing.T) {
	input := make([]int64, 200)
	for i := range input {
		input[i] = int64(i + 1)
	}
	input = append(input, input...)
	if got, err := selectedFindingIDs(input); err != nil || len(got) != 200 {
		t.Fatalf("重复选择被当作额外行：%d，%v", len(got), err)
	}
	if _, err := selectedFindingIDs(append(input, 201)); err == nil {
		t.Fatal("超出200个不同发现仍然通过")
	}
}

func TestSetReadReturnsEveryUniqueSelectionAndPublishesAfterCommit(t *testing.T) {
	db := resultActionDB(t)
	resultActionSeed(t, db, 1, 1, "a", false)
	resultActionSeed(t, db, 2, 2, "a", false)
	s := &Service{db: db}
	var events []string
	s.publish = func(kind string, _ *locales.Message, raw json.RawMessage) error {
		events = append(events, kind)
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		var marked int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM findings WHERE is_read=1`).Scan(&marked); err != nil || marked != 2 {
			t.Errorf("通知发布时事务未提交：%d，%v", marked, err)
		}
		var changed struct {
			IDs []int64 `json:"ids"`
		}
		if err := json.Unmarshal(raw, &changed); err != nil || !slices.Equal(changed.IDs, []int64{2, 1}) {
			t.Errorf("通知没有只包含实际变更：%s，%v", raw, err)
		}
		return errors.New("本地事件流已经关闭")
	}
	got, err := s.SetRead(t.Context(), []int64{2, 99, 1, 2}, true)
	if err != nil || got.Requested != 3 || got.Applied != 2 || got.Skipped != 1 || len(got.Items) != 3 {
		t.Fatalf("批量合计错误：%+v，%v", got, err)
	}
	if got.Applied+got.Skipped != got.Requested || got.Items[0].ID != 2 || got.Items[1].ID != 99 || got.Items[2].ID != 1 || got.Items[1].Status != BatchSkipped || got.Items[1].Reason == nil || got.Items[1].Reason.Code != "findings.not_found" {
		t.Fatalf("未知结果被假报成功或顺序变化：%+v", got)
	}
	if !slices.Equal(events, []string{"finding.changed"}) || resultActionCount(t, db, `SELECT COUNT(*) FROM seen_items`) != 2 {
		t.Fatal("事件重复或标已读修改了seen")
	}
	if got, err := s.SetRead(t.Context(), []int64{1, 2}, true); err != nil || got.Applied != 2 || len(events) != 1 {
		t.Fatalf("重复标记不能幂等或重复发事件：%+v，%v", got, err)
	}
}

func TestSetReadRollsBackWholeBatchOnStorageFailure(t *testing.T) {
	db := resultActionDB(t)
	resultActionSeed(t, db, 1, 1, "a", false)
	resultActionSeed(t, db, 1, 2, "b", false)
	if _, err := db.ExecContext(t.Context(), `CREATE TRIGGER stop_second_read BEFORE UPDATE ON findings WHEN NEW.id=2 BEGIN SELECT RAISE(ABORT,'本地写入失败'); END`); err != nil {
		t.Fatal(err)
	}
	s := &Service{db: db, publish: func(string, *locales.Message, json.RawMessage) error {
		t.Error("回滚的事务仍发布了事件")
		return nil
	}}
	_, err := s.SetRead(t.Context(), []int64{1, 2}, true)
	var failure *ServiceError
	if !errors.As(err, &failure) || failure.Kind != ErrorUnavailable || resultActionCount(t, db, `SELECT COUNT(*) FROM findings WHERE is_read=1`) != 0 {
		t.Fatalf("写入失败没有整体回滚：%v", err)
	}
	if _, err := s.SetRead(t.Context(), []int64{1, 0}, true); err == nil || resultActionCount(t, db, `SELECT COUNT(*) FROM findings WHERE is_read=1`) != 0 {
		t.Fatal("非法批量选择仍写入")
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.SetRead(cancelled, []int64{1}, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消错误丢失：%v", err)
	}
}

func TestBlockedSellerPagingUsesStableNullLastOrder(t *testing.T) {
	db := resultActionDB(t)
	for _, row := range []struct {
		id string
		at any
	}{{"z", nil}, {"c", int64(20)}, {"a", int64(20)}, {"b", int64(10)}, {"m", nil}} {
		if _, err := db.ExecContext(t.Context(), `INSERT INTO blocked_sellers(seller_id,created_at) VALUES(?,?)`, row.id, row.at); err != nil {
			t.Fatal(err)
		}
	}
	s := &Service{db: db}
	ids := []string{}
	cursor := ""
	for pageNumber := 0; pageNumber < 3; pageNumber++ {
		page, err := s.ListBlockedSellers(t.Context(), ListQuery{Limit: 2, Cursor: cursor})
		if err != nil || page.Total != 5 || page.Items == nil {
			t.Fatalf("屏蔽列表分页失败：%+v，%v", page, err)
		}
		for _, seller := range page.Items {
			ids = append(ids, seller.SellerID)
		}
		if pageNumber == 2 {
			if page.NextCursor != nil {
				t.Fatal("最后一页还有游标")
			}
			break
		}
		if page.NextCursor == nil {
			t.Fatal("中间页缺游标")
		}
		cursor = *page.NextCursor
	}
	if !slices.Equal(ids, []string{"a", "c", "b", "m", "z"}) {
		t.Fatalf("同时间或null排序丢失/重复：%v", ids)
	}
	for _, invalid := range []ListQuery{{Limit: -1}, {Limit: 201}, {Cursor: "not-a-cursor"}} {
		if _, err := s.ListBlockedSellers(t.Context(), invalid); err == nil {
			t.Fatalf("非法分页未拒绝：%+v", invalid)
		}
	}
}

func TestBlockedSellerListReturnsEmptyArray(t *testing.T) {
	db := resultActionDB(t)
	page, err := (&Service{db: db}).ListBlockedSellers(t.Context(), ListQuery{})
	if err != nil || page.Total != 0 || page.Items == nil || page.NextCursor != nil {
		t.Fatalf("空列表不准确：%+v，%v", page, err)
	}
}

func resultActionIntent(t *testing.T, db *sql.DB, findingID, taskID int64, state string) int64 {
	t.Helper()
	result, err := db.ExecContext(t.Context(), `INSERT INTO notification_intents(finding_id,task_id,kind,state,created_at,updated_at,price) VALUES(?,?,'recommended',?,1,1,10000)`, findingID, taskID, state)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestUpdateListingPatchKeepsPresenceAndFavoriteTime(t *testing.T) {
	db := resultActionDB(t)
	resultActionSeed(t, db, 1, 1, "a", false)
	now := int64(5000)
	s := &Service{db: db, now: func() time.Time { return time.UnixMilli(now) }}
	for _, patch := range []ListingPatch{
		{"title": json.RawMessage(`"标题"`)},
		{"is_favorite": json.RawMessage(`null`)},
		{"is_dismissed": json.RawMessage(`"false"`)},
		{"is_favorite": json.RawMessage(`1`)},
	} {
		_, err := s.UpdateListing(t.Context(), "a", patch)
		var failure *ServiceError
		if !errors.As(err, &failure) || failure.Kind != ErrorInvalid || len(failure.Fields) != 1 {
			t.Fatalf("非法商品字段仍被保存：%v，%v", patch, err)
		}
	}
	view, err := s.UpdateListing(t.Context(), "a", ListingPatch{"is_favorite": json.RawMessage(`true`)})
	if err != nil || !view.IsFavorite || view.FavoritedAt == nil || *view.FavoritedAt != 5000 || view.IsDismissed {
		t.Fatalf("收藏首次时点或缺省字段错误：%+v，%v", view, err)
	}
	now = 9000
	view, err = s.UpdateListing(t.Context(), "a", ListingPatch{"is_favorite": json.RawMessage(`true`)})
	if err != nil || view.FavoritedAt == nil || *view.FavoritedAt != 5000 {
		t.Fatalf("重复收藏改写原时间：%+v，%v", view, err)
	}
	view, err = s.UpdateListing(t.Context(), "a", ListingPatch{})
	if err != nil || !view.IsFavorite {
		t.Fatalf("空patch清空收藏：%+v，%v", view, err)
	}
	view, err = s.UpdateListing(t.Context(), "a", ListingPatch{"is_favorite": json.RawMessage(`false`)})
	if err != nil || view.IsFavorite || view.FavoritedAt != nil {
		t.Fatalf("显式false没有撤销收藏：%+v，%v", view, err)
	}
	if _, err := s.UpdateListing(t.Context(), "missing", ListingPatch{}); err == nil {
		t.Fatal("不存在商品仍假报保存成功")
	}
}

func TestDismissedIsGlobalAndUndoDoesNotReviveReminders(t *testing.T) {
	db := resultActionDB(t)
	resultActionSeed(t, db, 1, 1, "a", false)
	resultActionSeed(t, db, 2, 2, "a", false)
	resultActionSeed(t, db, 1, 3, "b", false)
	resultActionIntent(t, db, 1, 1, "pending")
	resultActionIntent(t, db, 2, 2, "pending")
	resultActionIntent(t, db, 3, 1, "pending")
	resultActionIntent(t, db, 1, 1, "sent")
	s := &Service{db: db, now: func() time.Time { return time.UnixMilli(5000) }}
	got, err := s.SetDismissed(t.Context(), []int64{1, 2, 99, 1}, true)
	if err != nil || got.Requested != 3 || got.Applied != 2 || got.Skipped != 1 || got.Items[2].Reason == nil {
		t.Fatalf("批量隐藏合计错误：%+v，%v", got, err)
	}
	if resultActionCount(t, db, `SELECT COUNT(*) FROM listings WHERE item_id='a' AND is_dismissed=1`) != 1 || resultActionCount(t, db, `SELECT COUNT(*) FROM notification_intents WHERE state='cancelled'`) != 2 || resultActionCount(t, db, `SELECT COUNT(*) FROM notification_intents WHERE state='sent'`) != 1 || resultActionCount(t, db, `SELECT COUNT(*) FROM notification_intents WHERE state='pending'`) != 1 {
		t.Fatal("隐藏没有取消同商品全部任务的待发提醒，或改了已送达历史")
	}
	if got, err := s.SetDismissed(t.Context(), []int64{2}, false); err != nil || got.Applied != 1 {
		t.Fatalf("撤销隐藏失败：%+v，%v", got, err)
	}
	if resultActionCount(t, db, `SELECT COUNT(*) FROM notification_intents WHERE state='cancelled'`) != 2 || resultActionCount(t, db, `SELECT COUNT(*) FROM findings WHERE decision='matched'`) != 3 || resultActionCount(t, db, `SELECT COUNT(*) FROM seen_items`) != 3 || resultActionCount(t, db, `SELECT COUNT(*) FROM listings WHERE is_dismissed=1`) != 0 {
		t.Fatal("撤销隐藏复活了旧提醒，或改了seen/结论")
	}
}

func TestDeleteResultsKeepsSeenFavoritesAndDeliveryHistory(t *testing.T) {
	db := resultActionDB(t)
	resultActionSeed(t, db, 1, 1, "favorite", true)
	resultActionSeed(t, db, 1, 2, "a", false)
	resultActionSeed(t, db, 2, 3, "a", false)
	resultActionIntent(t, db, 1, 1, "pending")
	resultActionIntent(t, db, 2, 1, "pending")
	resultActionIntent(t, db, 3, 2, "pending")
	sent := resultActionIntent(t, db, 1, 1, "sent")
	if _, err := db.ExecContext(t.Context(), `INSERT INTO deliveries(id,kind,ok) VALUES(1,'recommended',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO delivery_items(delivery_id,finding_id) VALUES(1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO delivery_intents(delivery_id,intent_id) VALUES(1,?)`, sent); err != nil {
		t.Fatal(err)
	}
	s := &Service{db: db, now: func() time.Time { return time.UnixMilli(5000) }}
	count, err := s.DeleteTaskFindings(t.Context(), 1)
	if err != nil || count != 1 || resultActionCount(t, db, `SELECT COUNT(*) FROM findings WHERE id IN(1,3)`) != 2 || resultActionCount(t, db, `SELECT COUNT(*) FROM notification_intents WHERE state='cancelled'`) != 1 {
		t.Fatalf("全部删除没有保护收藏或越过了任务：%d，%v", count, err)
	}
	got, err := s.DeleteFindings(t.Context(), []int64{1, 99, 1})
	if err != nil || got.Requested != 2 || got.Applied != 1 || got.Skipped != 1 || got.Items[1].Reason == nil {
		t.Fatalf("单个/批量删除错误：%+v，%v", got, err)
	}
	if resultActionCount(t, db, `SELECT COUNT(*) FROM listings WHERE item_id='favorite' AND is_favorite=1`) != 1 || resultActionCount(t, db, `SELECT COUNT(*) FROM seen_items`) != 3 || resultActionCount(t, db, `SELECT COUNT(*) FROM deliveries`) != 1 || resultActionCount(t, db, `SELECT COUNT(*) FROM delivery_items`) != 1 || resultActionCount(t, db, `SELECT COUNT(*) FROM delivery_intents`) != 1 || resultActionCount(t, db, `SELECT COUNT(*) FROM notification_intents WHERE state='sent'`) != 1 {
		t.Fatal("删除发现丢了收藏、seen或投递历史")
	}
	if _, err := s.DeleteTaskFindings(t.Context(), 99); err == nil {
		t.Fatal("不存在任务仍假报全部删除成功")
	}
}

func TestDeleteAllResultsDoesNotStopAtPageOrBatchLimit(t *testing.T) {
	db := resultActionDB(t)
	for id := int64(1); id <= 205; id++ {
		resultActionSeed(t, db, 1, id, "item-"+strconv.FormatInt(id, 10), false)
	}
	resultActionSeed(t, db, 1, 300, "favorite", true)
	s := &Service{db: db, now: time.Now}
	count, err := s.DeleteTaskFindings(t.Context(), 1)
	if err != nil || count != 205 || resultActionCount(t, db, `SELECT COUNT(*) FROM findings`) != 1 || resultActionCount(t, db, `SELECT COUNT(*) FROM seen_items`) != 206 {
		t.Fatalf("全部删除被页面/批量上限截断：%d，%v", count, err)
	}
}

func TestBlockedSellerIsIdempotentAndUndoPreservesFacts(t *testing.T) {
	db := resultActionDB(t)
	resultActionSeed(t, db, 1, 1, "a", false)
	if _, err := db.ExecContext(t.Context(), `UPDATE listings SET seller_id='seller' WHERE item_id='a'`); err != nil {
		t.Fatal(err)
	}
	resultActionIntent(t, db, 1, 1, "pending")
	nowMS := int64(5000)
	s := &Service{db: db, now: func() time.Time { return time.UnixMilli(nowMS) }}
	nick := "测试卖家"
	saved, err := s.BlockSeller(t.Context(), "seller", &nick)
	if err != nil || saved.SellerID != "seller" || saved.Nick == nil || *saved.Nick != nick || saved.CreatedAt == nil || *saved.CreatedAt != 5000 {
		t.Fatalf("没有返回实际保存的屏蔽记录：%+v，%v", saved, err)
	}
	nowMS = 9000
	saved, err = s.BlockSeller(t.Context(), "seller", nil)
	if err != nil || saved.Nick == nil || *saved.Nick != nick || saved.CreatedAt == nil || *saved.CreatedAt != 5000 {
		t.Fatalf("重复屏蔽没有返回原有记录：%+v，%v", saved, err)
	}
	page, err := s.ListBlockedSellers(t.Context(), ListQuery{})
	if err != nil || len(page.Items) != 1 || page.Items[0].Nick == nil || *page.Items[0].Nick != nick || page.Items[0].CreatedAt == nil || *page.Items[0].CreatedAt != 5000 {
		t.Fatalf("重复屏蔽清掉原昵称或改了创建时间：%+v，%v", page, err)
	}
	if err := s.UnblockSeller(t.Context(), "seller"); err != nil {
		t.Fatal(err)
	}
	if err := s.UnblockSeller(t.Context(), "seller"); err != nil {
		t.Fatal("重复撤销不是幂等操作", err)
	}
	if resultActionCount(t, db, `SELECT COUNT(*) FROM blocked_sellers`) != 0 || resultActionCount(t, db, `SELECT COUNT(*) FROM findings WHERE decision='matched'`) != 1 || resultActionCount(t, db, `SELECT COUNT(*) FROM seen_items`) != 1 || resultActionCount(t, db, `SELECT COUNT(*) FROM notification_intents WHERE state='cancelled'`) != 1 {
		t.Fatal("撤销屏蔽改了既有事实、seen，或复活了旧提醒")
	}
	for _, invalid := range []string{"", " \t", string([]byte{0xff})} {
		if _, err := s.BlockSeller(t.Context(), invalid, nil); err == nil {
			t.Fatalf("非法卖家编号仍保存：%q", invalid)
		}
	}
}

func TestLocalMutationsRollbackWhenReminderCancellationFails(t *testing.T) {
	for _, action := range []struct {
		name string
		run  func(context.Context, *Service) error
	}{
		{"dismiss", func(ctx context.Context, s *Service) error {
			_, err := s.SetDismissed(ctx, []int64{1}, true)
			return err
		}},
		{"flags", func(ctx context.Context, s *Service) error {
			_, err := s.UpdateListing(ctx, "a", ListingPatch{"is_favorite": json.RawMessage(`true`), "is_dismissed": json.RawMessage(`true`)})
			return err
		}},
		{"block", func(ctx context.Context, s *Service) error { _, err := s.BlockSeller(ctx, "seller", nil); return err }},
		{"delete", func(ctx context.Context, s *Service) error { _, err := s.DeleteFindings(ctx, []int64{1}); return err }},
		{"delete_all", func(ctx context.Context, s *Service) error { _, err := s.DeleteTaskFindings(ctx, 1); return err }},
	} {
		t.Run(action.name, func(t *testing.T) {
			db := resultActionDB(t)
			resultActionSeed(t, db, 1, 1, "a", false)
			resultActionIntent(t, db, 1, 1, "pending")
			if _, err := db.ExecContext(t.Context(), `UPDATE listings SET seller_id='seller' WHERE item_id='a'`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(t.Context(), `CREATE TRIGGER stop_cancel BEFORE UPDATE OF state ON notification_intents WHEN NEW.state='cancelled' BEGIN SELECT RAISE(ABORT,'本地取消落盘失败'); END`); err != nil {
				t.Fatal(err)
			}
			s := &Service{db: db, now: func() time.Time { return time.UnixMilli(5000) }, publish: func(string, *locales.Message, json.RawMessage) error {
				t.Error("回滚事务仍然发布事件")
				return nil
			}}
			var failure *ServiceError
			if err := action.run(t.Context(), s); !errors.As(err, &failure) || failure.Kind != ErrorUnavailable {
				t.Fatalf("取消失败被吞成保存成功：%v", err)
			}
			if resultActionCount(t, db, `SELECT COUNT(*) FROM findings`) != 1 || resultActionCount(t, db, `SELECT COUNT(*) FROM listings WHERE is_favorite=0 AND is_dismissed=0`) != 1 || resultActionCount(t, db, `SELECT COUNT(*) FROM blocked_sellers`) != 0 || resultActionCount(t, db, `SELECT COUNT(*) FROM notification_intents WHERE state='pending'`) != 1 {
				t.Fatal("取消失败没有回滚整个本地动作")
			}
		})
	}
}

func TestDeleteBatchRollsBackEarlierDeletesAndIntentCancellation(t *testing.T) {
	db := resultActionDB(t)
	resultActionSeed(t, db, 1, 1, "a", false)
	resultActionSeed(t, db, 1, 2, "b", false)
	resultActionIntent(t, db, 1, 1, "pending")
	resultActionIntent(t, db, 2, 1, "pending")
	if _, err := db.ExecContext(t.Context(), `CREATE TRIGGER stop_second_delete BEFORE DELETE ON findings WHEN OLD.id=2 BEGIN SELECT RAISE(ABORT,'本地删除失败'); END`); err != nil {
		t.Fatal(err)
	}
	s := &Service{db: db, now: time.Now}
	if _, err := s.DeleteFindings(t.Context(), []int64{1, 2}); err == nil {
		t.Fatal("后一个删除失败被吞掉")
	}
	if resultActionCount(t, db, `SELECT COUNT(*) FROM findings`) != 2 || resultActionCount(t, db, `SELECT COUNT(*) FROM notification_intents WHERE state='pending'`) != 2 || resultActionCount(t, db, `SELECT COUNT(*) FROM seen_items`) != 2 {
		t.Fatal("后一个删除失败没有回滚前面的删除或意图取消")
	}
}

func TestUnknownSelectionsAreSkippedWithoutEventsOrInventedRows(t *testing.T) {
	db := resultActionDB(t)
	s := &Service{db: db, now: time.Now, publish: func(string, *locales.Message, json.RawMessage) error {
		t.Error("没有存在的结果仍发布了修改事件")
		return nil
	}}
	for _, action := range []func(context.Context, []int64) (BatchResult, error){
		func(ctx context.Context, ids []int64) (BatchResult, error) { return s.SetRead(ctx, ids, true) },
		func(ctx context.Context, ids []int64) (BatchResult, error) { return s.SetDismissed(ctx, ids, true) },
		s.DeleteFindings,
	} {
		result, err := action(t.Context(), []int64{4, 3, 4})
		if err != nil || result.Requested != 2 || result.Applied != 0 || result.Skipped != 2 || len(result.Items) != 2 || result.Items[0].ID != 4 || result.Items[1].ID != 3 {
			t.Fatalf("不存在结果被假报成功或漏计：%+v，%v", result, err)
		}
	}
	if resultActionCount(t, db, `SELECT COUNT(*) FROM findings`) != 0 || resultActionCount(t, db, `SELECT COUNT(*) FROM listings`) != 0 || resultActionCount(t, db, `SELECT COUNT(*) FROM notification_intents`) != 0 {
		t.Fatal("本地标记复活了不存在的对象")
	}
}

func TestResultActionMessagesFormatInBothLanguages(t *testing.T) {
	for _, code := range []string{
		"findings.invalid_selection", "findings.not_found", "listings.invalid_patch", "listings.not_found", "tasks.invalid_id", "tasks.not_found",
		"blocked_sellers.invalid_id", "blocked_sellers.invalid_nick", "notify.cancelled_deleted", "notify.cancelled_dismissed", "notify.cancelled_blocked",
	} {
		for _, language := range []locales.Language{locales.ZhCN, locales.En} {
			if text, err := locales.Format(language, locales.Message{Code: code}); err != nil || text == "" {
				t.Fatalf("本地动作消息缺失或参数错误：%s，%s，%v", code, language, err)
			}
		}
	}
}

func TestLocalWritesRejectMaintenanceAndClosedService(t *testing.T) {
	db := resultActionDB(t)
	resultActionSeed(t, db, 1, 1, "a", false)
	if _, err := db.ExecContext(t.Context(), `INSERT INTO blocked_sellers(seller_id,created_at) VALUES('seller',1)`); err != nil {
		t.Fatal(err)
	}
	for _, action := range []struct {
		name string
		run  func(context.Context, *Service) error
	}{
		{"read", func(ctx context.Context, s *Service) error { _, err := s.SetRead(ctx, []int64{1}, true); return err }},
		{"flags", func(ctx context.Context, s *Service) error {
			_, err := s.UpdateListing(ctx, "a", ListingPatch{"is_favorite": json.RawMessage(`true`)})
			return err
		}},
		{"dismiss", func(ctx context.Context, s *Service) error {
			_, err := s.SetDismissed(ctx, []int64{1}, true)
			return err
		}},
		{"delete", func(ctx context.Context, s *Service) error { _, err := s.DeleteFindings(ctx, []int64{1}); return err }},
		{"delete_all", func(ctx context.Context, s *Service) error { _, err := s.DeleteTaskFindings(ctx, 1); return err }},
		{"block", func(ctx context.Context, s *Service) error {
			_, err := s.BlockSeller(ctx, "new-seller", nil)
			return err
		}},
		{"unblock", func(ctx context.Context, s *Service) error { return s.UnblockSeller(ctx, "seller") }},
	} {
		for _, state := range []string{"maintenance", "closed", "storage_fault"} {
			t.Run(action.name+"/"+state, func(t *testing.T) {
				s := &Service{db: db}
				s.initRuntime()
				switch state {
				case "maintenance":
					s.maintenance = 1
				case "closed":
					s.closing = true
				case "storage_fault":
					s.runtimeErr = errors.New("本地恢复失败")
				}
				var failure *ServiceError
				if err := action.run(t.Context(), s); !errors.As(err, &failure) || failure.Kind != ErrorUnavailable || failure.Reason.Code != "runtime.closed" {
					t.Fatalf("维护/关闭/存储失败时仍接收本地写动作：%v", err)
				}
				if resultActionCount(t, db, `SELECT COUNT(*) FROM findings WHERE is_read=0`) != 1 || resultActionCount(t, db, `SELECT COUNT(*) FROM listings WHERE is_favorite=0 AND is_dismissed=0`) != 1 || resultActionCount(t, db, `SELECT COUNT(*) FROM blocked_sellers WHERE seller_id='seller'`) != 1 || resultActionCount(t, db, `SELECT COUNT(*) FROM blocked_sellers WHERE seller_id='new-seller'`) != 0 || s.mutations != 0 {
					t.Fatal("被门禁拒绝的本地动作仍有写入或遗留占用")
				}
			})
		}
	}
}
