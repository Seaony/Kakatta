package service

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/browser"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/remote"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/store"
	"github.com/Seaony/Kakatta/internal/xianyu"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

func searchPageResponse(ids ...string) *browser.Response {
	body := `{"ret":["SUCCESS"],"data":{"resultList":[`
	for i, id := range ids {
		if i > 0 {
			body += ","
		}
		body += fmt.Sprintf(`{"data":{"item":{"main":{"exContent":{"itemId":%q,"title":%q},"targetUrl":"fleamarket://item?id=%s"}}}}`, id, "商品"+id, id)
	}
	return &browser.Response{Body: []byte(body + "]}}")}
}

func TestSearchPagesKeepCompletedPagesOnOrdinaryFailure(t *testing.T) {
	for _, laterErr := range []error{web.ErrQuota, errors.New("翻页失败"), context.DeadlineExceeded} {
		t.Run(laterErr.Error(), func(t *testing.T) {
			var got []string
			var warned []error
			calls := 0
			err := collectSearchPages(t.Context(), searchPageResponse("1"), 4,
				func(context.Context) (*browser.Response, bool, error) {
					calls++
					if calls == 1 {
						return searchPageResponse("2"), true, nil
					}
					return nil, false, laterErr
				},
				func(_ int64, items []xianyu.SearchItem) {
					for _, item := range items {
						got = append(got, item.ID)
					}
				},
				func(_ int64, err error) { warned = append(warned, err) })
			if err != nil || !reflect.DeepEqual(got, []string{"1", "2"}) || calls != 2 {
				t.Fatalf("完整页没有保留或继续翻页：items=%v calls=%d err=%v", got, calls, err)
			}
			wantWarnings := 1
			if errors.Is(laterErr, web.ErrQuota) {
				wantWarnings = 0
			}
			if len(warned) != wantWarnings || len(warned) > 0 && !errors.Is(warned[0], laterErr) {
				t.Fatalf("翻页结果提示不符：%v", warned)
			}
		})
	}
}

func TestSearchPagesPropagateStopAndCancellation(t *testing.T) {
	for _, stop := range []error{web.ErrNeedVerify, web.ErrDenied, web.ErrEnvBlocked, web.ErrLoginExpired, context.Canceled,
		&ServiceError{Kind: ErrorUnavailable, Reason: locales.Message{Code: "service.storage_failure"}},
		&ServiceError{Kind: ErrorConflict, Reason: locales.Message{Code: "service.account_denied"}},
	} {
		t.Run(stop.Error(), func(t *testing.T) {
			calls, warnings := 0, 0
			err := collectSearchPages(t.Context(), searchPageResponse("1"), 4,
				func(context.Context) (*browser.Response, bool, error) {
					calls++
					return nil, false, fmt.Errorf("停止：%w", stop)
				}, func(int64, []xianyu.SearchItem) {}, func(int64, error) { warnings++ })
			if !errors.Is(err, stop) || calls != 1 || warnings != 0 {
				t.Fatalf("停止信号被吞掉：calls=%d warnings=%d err=%v", calls, warnings, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	calls := 0
	err := collectSearchPages(ctx, searchPageResponse("1"), 4,
		func(context.Context) (*browser.Response, bool, error) {
			calls++
			cancel()
			return searchPageResponse("2"), true, nil
		}, func(int64, []xianyu.SearchItem) {}, func(int64, error) { t.Fatal("取消不应变为警告") })
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("收到响应时取消没有停止：calls=%d err=%v", calls, err)
	}
	ctx, cancel = context.WithCancel(t.Context())
	err = collectSearchPages(ctx, searchPageResponse("1"), 4,
		func(context.Context) (*browser.Response, bool, error) {
			cancel()
			return nil, false, web.ErrDenied
		}, func(int64, []xianyu.SearchItem) {}, func(int64, error) { t.Fatal("拒绝不应变为警告") })
	if !errors.Is(err, web.ErrDenied) {
		t.Fatalf("同时到达的取消吞掉了直接拒绝：%v", err)
	}
}

func TestSearchPagesStopAtEmptyMissingNextAndLimit(t *testing.T) {
	cases := []struct {
		name   string
		first  *browser.Response
		max    int64
		next   *browser.Response
		nextOK bool
		calls  int
		pages  int
	}{
		{"第一页为空", searchPageResponse(), 5, searchPageResponse("later"), true, 0, 1},
		{"后页为空", searchPageResponse("1"), 5, searchPageResponse(), true, 1, 2},
		{"没有下一页", searchPageResponse("1"), 5, nil, false, 1, 1},
		{"达到页数上限", searchPageResponse("1"), 1, searchPageResponse("later"), true, 0, 1},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			calls, pages := 0, 0
			err := collectSearchPages(t.Context(), test.first, test.max,
				func(context.Context) (*browser.Response, bool, error) {
					calls++
					return test.next, test.nextOK, nil
				}, func(int64, []xianyu.SearchItem) { pages++ }, func(int64, error) { t.Fatal("正常停止不应警告") })
			if err != nil || calls != test.calls || pages != test.pages {
				t.Fatalf("停止边界不符：calls=%d pages=%d err=%v", calls, pages, err)
			}
		})
	}
}

func TestSearchPagesRejectIncompleteFirstPage(t *testing.T) {
	for _, first := range []*browser.Response{nil, {Body: []byte(`{"ret":["SUCCESS"],"data":{}}`)}, {Body: []byte(`not json`)}} {
		accepted := 0
		err := collectSearchPages(t.Context(), first, 3,
			func(context.Context) (*browser.Response, bool, error) {
				t.Fatal("无完整第一页不能继续翻页")
				return nil, false, nil
			},
			func(int64, []xianyu.SearchItem) { accepted++ },
			func(int64, error) { t.Fatal("第一页错误不能变为成功警告") })
		if err == nil || accepted != 0 {
			t.Fatalf("不完整第一页被接纳：accepted=%d err=%v", accepted, err)
		}
	}
	var warned error
	accepted := 0
	err := collectSearchPages(t.Context(), searchPageResponse("1"), 3,
		func(context.Context) (*browser.Response, bool, error) {
			return &browser.Response{Body: []byte(`{"data":{}}`)}, true, nil
		},
		func(int64, []xianyu.SearchItem) { accepted++ },
		func(_ int64, err error) { warned = err })
	if err != nil || accepted != 1 || !errors.Is(warned, xianyu.ErrSearchStructure) {
		t.Fatalf("后页结构变化未保留先前完整页或丢失结构类型：accepted=%d warning=%v err=%v", accepted, warned, err)
	}
}

func TestSearchObservationsPreserveKnownZeroAndUnknown(t *testing.T) {
	items := []xianyu.SearchItem{
		{ID: "known", Title: "商品", WebURL: "链接", PriceText: "¥0", PriceKnown: true,
			OriPriceText: "原价未知", PublishedKnown: true, PublishedAt: time.UnixMilli(123),
			WantKnown: true, FreeShip: true, Inspected: true, Image: "主图", Area: "地区", SellerNick: "卖家"},
		{ID: "unknown", PriceCents: 100, WantNum: 7},
		{ID: "large", PriceKnown: true, PriceCents: 9007199254740993},
	}
	got := searchObservations(items, 456)
	if len(got) != 3 || got[0].Price == nil || *got[0].Price != 0 || got[0].WantCount == nil || *got[0].WantCount != 0 ||
		got[0].PublishedAt == nil || *got[0].PublishedAt != 123 || got[0].ObservedAt != 456 ||
		got[0].OriginalPriceText == nil || *got[0].OriginalPriceText != "原价未知" ||
		!reflect.DeepEqual(got[0].Tags, []string{"包邮", "验货宝"}) {
		t.Fatalf("已知零值或原文丢失：%+v", got)
	}
	if got[1].Price != nil || got[1].WantCount != nil || got[1].PublishedAt != nil || got[1].PriceText != nil || got[1].Tags == nil || len(got[1].Tags) != 0 {
		t.Fatalf("未知值被伪造：%+v", got[1])
	}
	if got[2].Price == nil || *got[2].Price != 9007199254740993 {
		t.Fatal("金额映射损失整数精度")
	}
	items[0].PriceCents, items[0].PriceText = 999, "已改"
	if *got[0].Price != 0 || *got[0].PriceText != "¥0" {
		t.Fatal("映射结果与输入共享了可变字段")
	}
}

type searchUnusableResource struct{ reads int }

func (r *searchUnusableResource) Runner() *web.Runner {
	r.reads++
	return &web.Runner{}
}
func (*searchUnusableResource) Chrome() *browser.Session { panic("不应读取浏览器") }
func (*searchUnusableResource) OpenRemote(context.Context) (*remote.Session, error) {
	panic("不应打开远程画面")
}
func (*searchUnusableResource) Close() error { return nil }

func TestRunSearchClockFailureRollsBackBacklogBeforeBrowsing(t *testing.T) {
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "search.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	initial := time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC)
	settingsStore, err := appsettings.New(t.Context(), db.DB(), settings.Environment{TZ: "UTC"}, func() time.Time { return initial })
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`INSERT INTO tasks(id,name,keyword,max_pages,interval_minutes,judge_mode,notify_policy,sort_index) VALUES(1,'任务','相机',1,30,'keyword','recommended',0)`,
		`INSERT INTO listings(item_id,title,desktop_url,first_seen_at,last_seen_at) VALUES('old','旧商品','',1,1)`,
		`INSERT INTO findings(id,task_id,item_id,search_generation,found_at,is_baseline,decision,stage,is_read) VALUES(1,1,'old',1,1,0,'pending','detail',0)`,
		`CREATE TRIGGER fail_search_clock BEFORE UPDATE ON meta WHEN NEW.key='quota.clock' BEGIN SELECT RAISE(ABORT,'clock blocked'); END`,
	} {
		if _, err := db.DB().ExecContext(t.Context(), query); err != nil {
			t.Fatal(query, err)
		}
	}
	var previousClock string
	if err := db.DB().QueryRowContext(t.Context(), `SELECT value FROM meta WHERE key='quota.clock'`).Scan(&previousClock); err != nil {
		t.Fatal(err)
	}
	resource := &searchUnusableResource{}
	s := &Service{db: db.DB(), settings: settingsStore, now: func() time.Time { return initial.Add(time.Hour) }}
	run := &runExecution{Ref: RunRef{TaskID: 1, RunID: 1, SearchGeneration: 1}, Resource: resource}
	err = s.runSearch(t.Context(), run)
	var failure *ServiceError
	if !errors.As(err, &failure) || failure.Kind != ErrorUnavailable || resource.reads != 1 || len(run.Backlog) != 0 {
		t.Fatalf("时钟提交失败后仍继续浏览或接纳队列：reads=%d backlog=%v err=%v", resource.reads, run.Backlog, err)
	}
	var decision, clock string
	if err := db.DB().QueryRowContext(t.Context(), `SELECT decision FROM findings WHERE id=1`).Scan(&decision); err != nil {
		t.Fatal(err)
	}
	if err := db.DB().QueryRowContext(t.Context(), `SELECT value FROM meta WHERE key='quota.clock'`).Scan(&clock); err != nil {
		t.Fatal(err)
	}
	if decision != "pending" {
		t.Fatal("时钟提交失败却保存了过期队列清理", decision)
	}
	if clock != previousClock {
		t.Fatal("失败事务仍推进了最高观察时间", clock)
	}
}
