package service

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/analysis/client"
	"github.com/Seaony/Kakatta/internal/analysis/images"
	"github.com/Seaony/Kakatta/internal/analysis/prompts"
	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/store"
	"github.com/Seaony/Kakatta/internal/xianyu"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

func detailTestRun(t *testing.T, count int) (*Service, *runExecution, int64) {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "detail.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	now := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	config, err := appsettings.New(t.Context(), db.DB(), settings.Environment{TZ: "UTC"}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{db: db.DB(), settings: config, now: func() time.Time { return now }}
	detailExec(t, s.db, `INSERT INTO tasks(id,name,keyword,max_pages,interval_minutes,judge_mode,notify_policy,sort_index) VALUES(1,'相机','相机',1,30,'keyword','recommended',0)`)
	run := &runExecution{Ref: RunRef{RunID: 1, TaskID: 1, SearchGeneration: 1}, Config: settings.Values{"max_details_per_run": json.RawMessage(`5`)}}
	for index := 1; index <= count; index++ {
		id := int64(index)
		itemID := fmt.Sprint(id)
		detailExec(t, s.db, `INSERT INTO listings(item_id,title,desktop_url,first_seen_at,last_seen_at) VALUES(?,'相机',?,1,1)`, itemID, "https://www.goofish.com/item?id="+itemID)
		detailExec(t, s.db, `INSERT INTO findings(id,task_id,item_id,search_generation,processing_revision,found_at,is_baseline,decision,stage,is_read) VALUES(?,1,?,1,1,1,0,'pending','detail',0)`, id, itemID)
		run.P = append(run.P, WorkRef{RunRef: run.Ref, FindingID: id, ProcessingRevision: 1})
	}
	return s, run, now.UnixMilli()
}

type detailModelsFunc func(context.Context, client.Request) (client.Result, error)

func (f detailModelsFunc) Complete(ctx context.Context, request client.Request) (client.Result, error) {
	return f(ctx, request)
}

func detailAI(t *testing.T, s *Service, run *runExecution, now int64) {
	t.Helper()
	profile := analysis.Profile{Musts: []analysis.Requirement{{ID: "m1", Text: "国行", Rev: 1}}}
	raw, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	detailExec(t, s.db, `UPDATE tasks SET judge_mode='ai',profile_json=? WHERE id=1`, string(raw))
	detailExec(t, s.db, `UPDATE listings SET detail_fetched_at=?,description='国行，成色好',seller_registration_days=600,zhima_level='信用原文'`, now)
	run.CachedOnly = true
	run.Prompts, err = prompts.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
}

const detailFactsJSON = `{"checks":[],"musts":[{"id":"m1","status":"met","source":"text","evidence":"国行"}],"adjust":{"value":0,"reason":""},"summary":"符合要求"}`

func TestRunDetailModelUsesRequestProfileAndPartialSeller(t *testing.T) {
	s, run, now := detailTestRun(t, 1)
	detailAI(t, s, run, now)
	calls := 0
	s.models = detailModelsFunc(func(ctx context.Context, request client.Request) (client.Result, error) {
		calls++
		if request.Kind != client.Analysis || len(request.Images) != 0 || request.TaskID == nil || *request.TaskID != 1 {
			t.Fatalf("模型请求错误：%+v", request)
		}
		if !bytes.Contains([]byte(request.User), []byte(`"registration"`)) || !bytes.Contains([]byte(request.User), []byte("信用原文")) {
			t.Fatal("没有主页缓存时丢了详情中的卖家资料", request.User)
		}
		if err := request.Validate(detailFactsJSON); err != nil {
			t.Fatal(err)
		}
		profile := analysis.Profile{Musts: []analysis.Requirement{{ID: "m1", Text: "日版", Rev: 2}}}
		raw, _ := json.Marshal(profile)
		detailExec(t, s.db, `UPDATE tasks SET profile_json=? WHERE id=1`, string(raw))
		return client.Result{Text: detailFactsJSON, ProviderID: 7, Model: "local"}, nil
	})
	if err := s.runDetail(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	got := detailFinding(t, s, 1)
	if calls != 1 || got.Stage != StageDone || got.Analysis == nil || got.Analysis.Musts[0].Rev != 1 || got.Attempts != 1 || run.DetailCount != 0 || run.SellerPageCount != 0 {
		t.Fatalf("请求版本被晚保存清单替换：%+v，calls=%d", got, calls)
	}
}

func TestRunDetailPersistsOnlyRealFactParseWarnings(t *testing.T) {
	s, run, now := detailTestRun(t, 1)
	detailAI(t, s, run, now)
	response := `{"checks":[{"key":"unknown_check","status":"risk"}],"musts":[{"id":"m1","status":"met","source":"text","evidence":"原文没有这句话"}],"adjust":{"value":0},"summary":"已分析","parse_warnings":[{"code":"model.injected_warning","args":{}}],"raw_marker":"不要保存原始响应"}`
	s.models = detailModelsFunc(func(ctx context.Context, request client.Request) (client.Result, error) {
		if err := request.Validate(response); err != nil {
			t.Fatal(err)
		}
		return client.Result{Text: response}, nil
	})
	if err := s.runDetail(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	view, err := s.GetFinding(t.Context(), 1, FindingQuery{View: "all"})
	if err != nil {
		t.Fatal(err)
	}
	codes := map[string]bool{}
	for _, warning := range view.AnalysisWarnings {
		codes[warning.Code] = true
	}
	if len(view.AnalysisWarnings) != 2 || !codes["analysis.unknown_field"] || !codes["analysis.evidence_missing"] {
		t.Fatalf("发现没有保留真实解析警告：%+v", view.AnalysisWarnings)
	}
	var stored string
	if err := s.db.QueryRow(`SELECT analysis_json FROM findings WHERE id=1`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains([]byte(stored), []byte("model.injected_warning")) || bytes.Contains([]byte(stored), []byte("不要保存原始响应")) {
		t.Fatal("持久化信任了模型元数据或保存了原始响应", stored)
	}
}

func TestRunDetailModelUnavailableLeavesAllRemainingWaiting(t *testing.T) {
	for _, missing := range []error{client.ErrNoProvider, client.ErrDailyLimit} {
		t.Run(missing.Error(), func(t *testing.T) {
			s, run, now := detailTestRun(t, 2)
			detailAI(t, s, run, now)
			calls := 0
			s.models = detailModelsFunc(func(context.Context, client.Request) (client.Result, error) {
				calls++
				return client.Result{}, missing
			})
			if err := s.runDetail(t.Context(), run); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("模型不可用后仍继续分析：%d", calls)
			}
			for _, id := range []int64{1, 2} {
				got := detailFinding(t, s, id)
				if got.Decision != DecisionPending || got.Stage != StageDetail || got.Attempts != 0 || got.Reason == nil || got.Reason.Code != "await_ai" {
					t.Fatalf("等待分析消耗了重试或丢失阶段：%+v", got)
				}
			}
		})
	}
}

func TestRunDetailModelFailuresRespectInfrastructureBoundary(t *testing.T) {
	ordinary := &client.Failure{Last: locales.Message{Code: "run.detail_failed", Args: map[string]any{}}, Retryable: true}
	for _, failure := range []error{ordinary, client.ErrInfrastructure, client.ErrConfigChanged} {
		t.Run(failure.Error(), func(t *testing.T) {
			s, run, now := detailTestRun(t, 2)
			detailAI(t, s, run, now)
			calls := 0
			s.models = detailModelsFunc(func(context.Context, client.Request) (client.Result, error) {
				calls++
				if calls == 1 {
					return client.Result{}, failure
				}
				return client.Result{Text: detailFactsJSON}, nil
			})
			err := s.runDetail(t.Context(), run)
			got := detailFinding(t, s, 1)
			if failure == ordinary {
				if err != nil || calls != 2 || got.Decision != DecisionFailed || got.Attempts != 1 || !got.Retryable {
					t.Fatalf("普通模型错误未局限到当前商品：%+v，%v", got, err)
				}
			} else if !errors.Is(err, failure) || calls != 1 || got.Attempts != 0 || got.Decision != DecisionPending {
				t.Fatalf("模型基础设施错误被当普通失败：%+v，%v", got, err)
			}
		})
	}
}

func TestSellerCacheDistinguishesMissingAndKnownEmpty(t *testing.T) {
	s, run, now := detailTestRun(t, 1)
	detailExec(t, s.db, `UPDATE listings SET seller_id='seller' WHERE item_id='1'`)
	if profile := sellerPageProfile("seller", web.SellerPage{Head: &xianyu.SellerHead{}, Items: &xianyu.SellerItems{}, Ratings: &xianyu.SellerRatings{}}, now); profile != nil {
		t.Fatalf("未取得的列表被缓存成成功空列表：%+v", profile)
	}
	profile := sellerPageProfile("seller", web.SellerPage{
		Head:  &xianyu.SellerHead{ItemCountKnown: true, RateCountKnown: true},
		Items: &xianyu.SellerItems{ListKnown: true}, Ratings: &xianyu.SellerRatings{ListKnown: true},
	}, now)
	if profile == nil || profile.ItemCount == nil || *profile.ItemCount != 0 || profile.RecentItems == nil || profile.RecentRates == nil {
		t.Fatalf("真实空列表没有保留已知0：%+v", profile)
	}
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := saveSellerTx(t.Context(), tx, run.P[0], *profile); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var itemCount, sellerCount, buyerCount sql.NullInt64
	var items, rates sql.NullString
	if err := s.db.QueryRow(`SELECT item_count,seller_rate_count,buyer_rate_count,recent_items_json,recent_rates_json FROM seller_profiles WHERE seller_id='seller'`).Scan(&itemCount, &sellerCount, &buyerCount, &items, &rates); err != nil {
		t.Fatal(err)
	}
	if !itemCount.Valid || itemCount.Int64 != 0 || !sellerCount.Valid || sellerCount.Int64 != 0 || !buyerCount.Valid || buyerCount.Int64 != 0 || items.String != "[]" || rates.String != "[]" {
		t.Fatalf("空列表落盘变成未知：%+v %+v %+v %q %q", itemCount, sellerCount, buyerCount, items.String, rates.String)
	}
	tx, err = s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	older := *profile
	older.FetchedAt, older.Nick = now-1, detailText("旧昵称")
	if err := saveSellerTx(t.Context(), tx, run.P[0], older); err != nil {
		t.Fatal(err)
	}
	got, err := loadSellerTx(t.Context(), tx, "seller")
	if err != nil || got == nil || got.FetchedAt != now || got.Nick != nil {
		t.Fatalf("晚到旧缓存覆盖新资料：%+v，%v", got, err)
	}
}

func detailPNG(t *testing.T) []byte {
	t.Helper()
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestDetailImagesUseFixedHTTPProxyAndNeverFallback(t *testing.T) {
	pngData := detailPNG(t)
	var proxyCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		proxyCalls.Add(1)
		if request.URL.Host != "image.invalid" || request.Header.Get("Cookie") != "" {
			t.Error("目标未保留域名，或带了Cookie", request.URL.Host)
		}
		user, password, ok := request.BasicAuth()
		if ok || user != "" || password != "" {
			t.Error("目标请求带了认证头")
		}
		if request.Header.Get("Proxy-Authorization") == "" {
			t.Error("固定HTTP代理没有收到认证")
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngData)
	}))
	defer upstream.Close()
	fixed, _ := url.Parse(upstream.URL)
	fixed.User = url.UserPassword("proxy-user", "proxy-password")
	transport, err := detailImageTransport(fixed)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	preparer, err := images.New(images.Dependencies{HTTP: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := preparer.Prepare(t.Context(), []string{"http://image.invalid/image.png"}, 1)
	if err != nil || len(result.Images) != 1 || proxyCalls.Load() != 1 {
		t.Fatalf("固定代理图片准备失败：%+v，%v，calls=%d", result, err, proxyCalls.Load())
	}
	upstream.Close()
	var directCalls atomic.Int64
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		directCalls.Add(1)
		_, _ = w.Write(pngData)
	}))
	defer direct.Close()
	result, err = preparer.Prepare(t.Context(), []string{direct.URL + "/image.png"}, 1)
	if err != nil || len(result.Images) != 0 || len(result.Skipped) != 1 || directCalls.Load() != 0 {
		t.Fatalf("坏代理回退直连：%+v，%v，direct=%d", result, err, directCalls.Load())
	}
}

func TestRunDetailSellerFailureBoundariesAndPartialCache(t *testing.T) {
	for _, fetchErr := range []error{nil, web.ErrQuota, errors.New("卖家页面失败"), web.ErrDenied, findingChangedError()} {
		name := "partial"
		if fetchErr != nil {
			name = fetchErr.Error()
		}
		t.Run(name, func(t *testing.T) {
			s, run, now := detailTestRun(t, 1)
			detailAI(t, s, run, now)
			run.CachedOnly = false
			detailExec(t, s.db, `UPDATE listings SET seller_id='seller' WHERE item_id='1'`)
			input, current, err := s.detailInput(t.Context(), run.P[0])
			if err != nil || !current {
				t.Fatal(err)
			}
			got, err := s.detailSellerWith(t.Context(), run, run.P[0], input, 5, func(context.Context, string) (web.SellerPage, error) {
				return web.SellerPage{Opened: !errors.Is(fetchErr, web.ErrQuota), Head: &xianyu.SellerHead{Nick: "卖家", ItemCountKnown: true}}, fetchErr
			})
			if fetchErr == nil {
				if err != nil || got == nil || got.RecentRates != nil || got.ItemCount == nil || *got.ItemCount != 0 || run.SellerPageCount != 1 {
					t.Fatalf("部分资料被当完整评价：%+v，%v", got, err)
				}
				return
			}
			if web.IsStop(fetchErr) || fetchErr.Error() == findingChangedError().Error() {
				if !errors.Is(err, fetchErr) {
					t.Fatalf("卖家风控或闸门错误被吞：%v", err)
				}
			} else if err != nil || got != nil || len(run.Warnings) == 0 {
				t.Fatalf("卖家普通错误没有留空继续：%+v，%v", got, err)
			}
		})
	}
}

func TestRunDetailCachedOnlyDoesNotRefreshSeller(t *testing.T) {
	s, run, now := detailTestRun(t, 1)
	detailAI(t, s, run, now)
	detailExec(t, s.db, `UPDATE listings SET seller_id='seller' WHERE item_id='1'`)
	input, _, err := s.detailInput(t.Context(), run.P[0])
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	seller, err := s.detailSellerWith(t.Context(), run, run.P[0], input, 5, func(context.Context, string) (web.SellerPage, error) {
		calls++
		return web.SellerPage{}, nil
	})
	if err != nil || seller != nil || calls != 0 || run.SellerPageCount != 0 {
		t.Fatalf("缓存即时重分析打开卖家页面：%+v，calls=%d，err=%v", seller, calls, err)
	}
}

func TestDetailImagesSOCKSProxyReceivesDomainAndAuthentication(t *testing.T) {
	pngData := detailPNG(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer connection.Close()
		_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
		reader := bufio.NewReader(connection)
		read := func(count int) ([]byte, error) {
			value := make([]byte, count)
			_, err := io.ReadFull(reader, value)
			return value, err
		}
		header, err := read(2)
		if err != nil {
			done <- err
			return
		}
		if _, err = read(int(header[1])); err != nil {
			done <- err
			return
		}
		_, _ = connection.Write([]byte{5, 2})
		auth, err := read(2)
		if err != nil {
			done <- err
			return
		}
		username, err := read(int(auth[1]))
		if err != nil {
			done <- err
			return
		}
		length, err := read(1)
		if err != nil {
			done <- err
			return
		}
		password, err := read(int(length[0]))
		if err != nil || string(username) != "local-user" || string(password) != "local-password" {
			done <- errors.New("SOCKS认证不匹配")
			return
		}
		_, _ = connection.Write([]byte{1, 0})
		command, err := read(5)
		if err != nil || command[3] != 3 {
			done <- errors.New("SOCKS没有使用域名地址")
			return
		}
		domain, err := read(int(command[4]))
		if err != nil {
			done <- err
			return
		}
		port, err := read(2)
		if err != nil || string(domain) != "image.invalid" || binary.BigEndian.Uint16(port) != 80 {
			done <- errors.New("SOCKS目标地址不匹配")
			return
		}
		_, _ = connection.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 1})
		request, err := http.ReadRequest(reader)
		if err != nil {
			done <- err
			return
		}
		request.Body.Close()
		if request.Header.Get("Cookie") != "" || request.Header.Get("Authorization") != "" {
			done <- errors.New("图片请求带了认证信息")
			return
		}
		_, err = fmt.Fprintf(connection, "HTTP/1.1 200 OK\r\nContent-Type: image/png\r\nContent-Length: %d\r\nConnection: close\r\n\r\n", len(pngData))
		if err == nil {
			_, err = connection.Write(pngData)
		}
		done <- err
	}()
	fixed, _ := url.Parse("socks5://local-user:local-password@" + listener.Addr().String())
	transport, err := detailImageTransport(fixed)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	preparer, err := images.New(images.Dependencies{HTTP: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := preparer.Prepare(t.Context(), []string{"http://image.invalid/image.png"}, 1)
	if err != nil || len(result.Images) != 1 {
		t.Fatalf("SOCKS图片请求失败：%+v，%v", result, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestDetailImageTransportDoesNotUseEnvironmentProxy(t *testing.T) {
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY"} {
		t.Setenv(key, "http://127.0.0.1:1")
	}
	transport, err := detailImageTransport(nil)
	if err != nil || transport.Proxy != nil || transport.DialContext == nil {
		t.Fatalf("直连传输继承了环境代理：%v", err)
	}
	defer transport.CloseIdleConnections()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }))
	defer server.Close()
	response, err := (&http.Client{Transport: transport}).Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
}

func detailExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal(query, err)
	}
}

func detailFinding(t *testing.T, s *Service, id int64) Finding {
	t.Helper()
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	finding, err := loadFindingTx(t.Context(), tx, id)
	if err != nil {
		t.Fatal(err)
	}
	return finding
}

func TestRunDetailReusesFreshCacheWithoutResource(t *testing.T) {
	s, run, now := detailTestRun(t, 1)
	detailExec(t, s.db, `UPDATE listings SET detail_fetched_at=?,description='成色好' WHERE item_id='1'`, now-int64(time.Hour/time.Millisecond))
	if err := s.runDetail(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	got := detailFinding(t, s, 1)
	if got.Decision != analysis.DecisionMatched || got.Stage != StageDone || got.Attempts != 1 || run.DetailCount != 0 || run.AnalyzedCount != 1 || !reflect.DeepEqual(run.FirstConclusionIDs, []int64{1}) {
		t.Fatalf("缓存未直接判断：finding=%+v，run=%+v", got, run)
	}
}

func TestRunDetailKeywordDoesNotReadUnusedSellerCache(t *testing.T) {
	s, run, now := detailTestRun(t, 1)
	detailExec(t, s.db, `UPDATE listings SET detail_fetched_at=?,seller_id='seller' WHERE item_id='1'`, now)
	detailExec(t, s.db, `INSERT INTO seller_profiles(seller_id,recent_items_json,fetched_at) VALUES('seller','broken',?)`, now)
	if err := s.runDetail(t.Context(), run); err != nil {
		t.Fatalf("关键词流程读取了不需要的卖家缓存：%v", err)
	}
	if got := detailFinding(t, s, 1); got.Decision != analysis.DecisionMatched {
		t.Fatalf("关键词未完成：%+v", got)
	}
}

func TestRunDetailCacheExpiresAtTwentyFourHours(t *testing.T) {
	for _, age := range []time.Duration{24*time.Hour - time.Millisecond, 24 * time.Hour} {
		t.Run(age.String(), func(t *testing.T) {
			s, run, now := detailTestRun(t, 1)
			detailExec(t, s.db, `UPDATE listings SET detail_fetched_at=? WHERE item_id='1'`, now-int64(age/time.Millisecond))
			calls := 0
			err := s.runDetailWith(t.Context(), run, func(context.Context, string) (xianyu.Detail, error) {
				calls++
				return xianyu.Detail{Description: "成色好"}, nil
			})
			want := 0
			if age == 24*time.Hour {
				want = 1
			}
			if err != nil || calls != want || run.DetailCount != int64(want) {
				t.Fatalf("缓存边界错误：calls=%d，want=%d，err=%v", calls, want, err)
			}
		})
	}
}

func TestRunDetailQuotaAndPageLimitKeepPending(t *testing.T) {
	for _, quota := range []bool{false, true} {
		t.Run(fmt.Sprint(quota), func(t *testing.T) {
			s, run, _ := detailTestRun(t, 2)
			if !quota {
				run.Config["max_details_per_run"] = json.RawMessage(`1`)
				run.SellerPageCount = 1
			}
			calls := 0
			err := s.runDetailWith(t.Context(), run, func(context.Context, string) (xianyu.Detail, error) {
				calls++
				return xianyu.Detail{}, web.ErrQuota
			})
			if err != nil || run.DetailCount != 0 || calls != map[bool]int{false: 0, true: 1}[quota] {
				t.Fatalf("额度或页上限未停止：calls=%d，err=%v", calls, err)
			}
			for _, id := range []int64{1, 2} {
				finding := detailFinding(t, s, id)
				if finding.Decision != DecisionPending || finding.Attempts != 0 {
					t.Fatalf("未处理的商品被改写：%+v", finding)
				}
			}
		})
	}
}

func TestRunDetailOrdinaryFailureContinuesAndThirdFailureStopsRetry(t *testing.T) {
	s, run, _ := detailTestRun(t, 2)
	calls := 0
	err := s.runDetailWith(t.Context(), run, func(context.Context, string) (xianyu.Detail, error) {
		calls++
		if calls == 1 {
			return xianyu.Detail{}, context.DeadlineExceeded
		}
		return xianyu.Detail{Description: "成色好"}, nil
	})
	first, second := detailFinding(t, s, 1), detailFinding(t, s, 2)
	if err != nil || calls != 2 || first.Decision != DecisionFailed || !first.Retryable || first.Attempts != 1 || second.Decision != analysis.DecisionMatched {
		t.Fatalf("普通详情超时被当整次取消：%+v，%+v，%v", first, second, err)
	}
	run.P = run.P[:1]
	for range 2 {
		if err := s.runDetailWith(t.Context(), run, func(context.Context, string) (xianyu.Detail, error) {
			return xianyu.Detail{}, errors.New("页面解析失败")
		}); err != nil {
			t.Fatal(err)
		}
	}
	first = detailFinding(t, s, 1)
	if first.Attempts != 3 || first.Retryable || first.Stage != StageDetail {
		t.Fatalf("三次失败后仍可自动重试：%+v", first)
	}
}

func TestRunDetailStopErrorsNeverBecomeFindingFailures(t *testing.T) {
	for _, stop := range []error{web.ErrNeedVerify, web.ErrDenied, web.ErrEnvBlocked, web.ErrLoginExpired, context.Canceled, storageFailure(errors.New("写入失败")), findingChangedError()} {
		t.Run(stop.Error(), func(t *testing.T) {
			s, run, _ := detailTestRun(t, 2)
			calls := 0
			err := s.runDetailWith(t.Context(), run, func(context.Context, string) (xianyu.Detail, error) {
				calls++
				return xianyu.Detail{}, stop
			})
			if !errors.Is(err, stop) || calls != 1 {
				t.Fatalf("停止错误被吞：calls=%d，err=%v", calls, err)
			}
			for _, id := range []int64{1, 2} {
				finding := detailFinding(t, s, id)
				if finding.Attempts != 0 || finding.Decision != DecisionPending {
					t.Fatalf("风控或存储失败写成了普通失败：%+v", finding)
				}
			}
		})
	}
}

func TestRunDetailRecognizedRiskWinsConcurrentCancellation(t *testing.T) {
	s, run, _ := detailTestRun(t, 2)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	err := s.runDetailWith(ctx, run, func(context.Context, string) (xianyu.Detail, error) {
		cancel()
		return xianyu.Detail{}, web.ErrDenied
	})
	if !errors.Is(err, web.ErrDenied) {
		t.Fatalf("已识别的拒绝被取消吞掉：%v", err)
	}
}

func TestRunDetailCachedOnlyNeverOpensExpiredPage(t *testing.T) {
	s, run, now := detailTestRun(t, 1)
	run.CachedOnly = true
	detailExec(t, s.db, `UPDATE listings SET detail_fetched_at=? WHERE item_id='1'`, now-int64(24*time.Hour/time.Millisecond))
	calls := 0
	err := s.runDetailWith(t.Context(), run, func(context.Context, string) (xianyu.Detail, error) {
		calls++
		return xianyu.Detail{}, nil
	})
	if err != nil || calls != 0 {
		t.Fatalf("即时重分析在缓存刚过期时打开了页面：calls=%d，err=%v", calls, err)
	}
	if got := detailFinding(t, s, 1); got.Decision != DecisionPending || got.Attempts != 0 {
		t.Fatalf("缓存过期的即时重分析没有留队：%+v", got)
	}
}

func TestRunDetailRechecksBlockedSellerAndCurrentExcludes(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		t.Run(fmt.Sprint(blocked), func(t *testing.T) {
			s, run, _ := detailTestRun(t, 1)
			err := s.runDetailWith(t.Context(), run, func(context.Context, string) (xianyu.Detail, error) {
				if blocked {
					detailExec(t, s.db, `INSERT INTO blocked_sellers(seller_id) VALUES('seller')`)
				} else {
					detailExec(t, s.db, `UPDATE tasks SET exclude_keywords_json='["坏点"]' WHERE id=1`)
				}
				return xianyu.Detail{SellerID: "seller", Description: "有坏点"}, nil
			})
			got := detailFinding(t, s, 1)
			want := analysis.DecisionRejected
			if blocked {
				want = DecisionSkipped
			}
			if err != nil || got.Decision != want || got.Stage != StageDone {
				t.Fatalf("新屏蔽或新排除未生效：%+v，%v", got, err)
			}
			if !blocked && run.SearchSaved.Layer1Rejected != 1 {
				t.Fatalf("详情排除未计入第一层拒绝数：%d", run.SearchSaved.Layer1Rejected)
			}
		})
	}
}

func TestRunDetailLateWorkCannotReplaceNewRevisionOrGeneration(t *testing.T) {
	for _, generation := range []bool{false, true} {
		t.Run(fmt.Sprint(generation), func(t *testing.T) {
			s, run, _ := detailTestRun(t, 1)
			err := s.runDetailWith(t.Context(), run, func(context.Context, string) (xianyu.Detail, error) {
				if generation {
					detailExec(t, s.db, `UPDATE tasks SET search_generation=2 WHERE id=1`)
				} else {
					detailExec(t, s.db, `UPDATE findings SET processing_revision=2 WHERE id=1`)
				}
				return xianyu.Detail{Description: "旧结果"}, nil
			})
			if generation && !errors.Is(err, errSearchChanged) || !generation && err != nil {
				t.Fatalf("旧工作判别错误：%v", err)
			}
			got := detailFinding(t, s, 1)
			if got.Decision != DecisionPending || got.Attempts != 0 || len(run.FirstConclusionIDs) != 0 {
				t.Fatalf("晚结果覆盖新请求：%+v", got)
			}
		})
	}
}

func TestRunDetailStorageFailureStopsBeforeNextItem(t *testing.T) {
	s, run, _ := detailTestRun(t, 2)
	calls := 0
	err := s.runDetailWith(t.Context(), run, func(context.Context, string) (xianyu.Detail, error) {
		calls++
		detailExec(t, s.db, `CREATE TRIGGER fail_detail BEFORE UPDATE ON listings BEGIN SELECT RAISE(FAIL,'blocked'); END`)
		return xianyu.Detail{Description: "成色好"}, nil
	})
	var failure *ServiceError
	if !errors.As(err, &failure) || failure.Kind != ErrorUnavailable || calls != 1 {
		t.Fatalf("详情保存失败后继续访问：calls=%d，err=%v", calls, err)
	}
	for _, id := range []int64{1, 2} {
		got := detailFinding(t, s, id)
		if got.Decision != DecisionPending || got.Attempts != 0 {
			t.Fatalf("存储错误被改写为单件错误：%+v", got)
		}
	}
}
