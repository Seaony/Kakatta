package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/auth"
	"github.com/Seaony/Kakatta/internal/config"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/security"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/store"
)

func seedEnvironment(key string) (string, bool) {
	if key == "TZ" {
		return "Asia/Shanghai", true
	}
	return "", false
}

func TestSeedCommandCreatesOfflineDataAndPreservesInitialization(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sample")
	var output bytes.Buffer
	if err := run(t.Context(), []string{"--data", dir}, &output, seedEnvironment); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "2 个停用任务、9 件虚构商品、9 条结果") {
		t.Fatalf("样例计数输出不正确：%q", output.String())
	}
	paths, err := config.CreateDataDirectories(dir)
	if err != nil {
		t.Fatal(err)
	}
	key, err := config.LoadMasterKey(paths, seedEnvironment)
	if err != nil || len(key) != 32 {
		t.Fatalf("样例主密钥不可用：%v", err)
	}
	defer clear(key)
	database, err := store.Open(t.Context(), paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	db := database.DB()
	defaults, err := settings.Resolve(nil, settings.Environment{TZ: "Asia/Shanghai"})
	if err != nil || string(defaults.Values["general.onboarding_done"]) != "false" || countRows(t, db, `SELECT count(*) FROM settings WHERE key='general.onboarding_done'`) != 0 {
		t.Fatal("通用样例必须保留默认未完成的首次引导，不能使用假数据开发的完成状态", err)
	}
	for table, want := range map[string]int64{"tasks": 2, "listings": 9, "findings": 9, "seen_items": 9,
		"task_sightings": 10, "task_item_specs": 6, "price_points": 9, "run_records": 2,
		"accounts": 0, "proxies": 0, "ai_providers": 0, "channels": 0, "admin": 0,
		"auth_tokens": 0, "ai_calls": 0, "quota_events": 0, "deliveries": 0, "notification_intents": 0} {
		if got := countRows(t, db, "SELECT count(*) FROM "+table); got != want {
			t.Errorf("%s 数量 = %d，期望 %d", table, got, want)
		}
	}
	if countRows(t, db, `SELECT count(*) FROM tasks WHERE enabled<>0 OR notify_policy<>'none' OR account_id IS NOT NULL`) != 0 {
		t.Fatal("样例任务可能运行或推送")
	}
	if countRows(t, db, `SELECT count(*) FROM findings WHERE decision='matched'`) != 1 ||
		countRows(t, db, `SELECT count(*) FROM findings WHERE reject_kind='keyword_miss'`) != 1 ||
		countRows(t, db, `SELECT count(*) FROM findings WHERE reject_kind='excluded'`) != 1 ||
		countRows(t, db, `SELECT count(*) FROM findings WHERE decision='scored' AND score IS NOT NULL`) != 5 {
		t.Fatal("关键词和评分样例不完整")
	}
	var score, fair, sample int64
	if err := db.QueryRow(`SELECT score,fair_price,fair_sample FROM findings WHERE item_id='sample-laptop-deal'`).Scan(&score, &fair, &sample); err != nil || score != 90 || fair != 400000 || sample != 5 {
		t.Fatalf("价格或纯规则评分不正确：%d %d %d %v", score, fair, sample, err)
	}
	var price, want, browse, days, unknownScore, unknownGrade any
	if err := db.QueryRow(`SELECT l.price,l.want_count,l.browse_count,l.seller_registration_days,f.score,f.grade
		FROM listings l JOIN findings f ON f.item_id=l.item_id WHERE l.item_id='sample-laptop-unknown'`).Scan(&price, &want, &browse, &days, &unknownScore, &unknownGrade); err != nil {
		t.Fatal(err)
	}
	if price != nil || want != nil || browse != nil || days != nil || unknownScore != nil || unknownGrade != nil {
		t.Fatal("未知数值或分数被写成已知零")
	}
	for query, expected := range map[string]int64{
		`SELECT sum(scanned_count) FROM run_records`:                                   9,
		`SELECT sum(new_count) FROM run_records`:                                       8,
		`SELECT sum(analyzed_count) FROM run_records`:                                  7,
		`SELECT sum(recommended_count) FROM run_records`:                               2,
		`SELECT count(*) FROM listings WHERE is_favorite=1 AND last_drop_amount=20000`: 1,
		`SELECT count(*) FROM listings WHERE is_dismissed=1`:                           1,
		`SELECT count(*) FROM findings WHERE is_read=1`:                                2,
		`SELECT count(*) FROM findings f LEFT JOIN seen_items s ON s.task_id=f.task_id AND s.item_id=f.item_id WHERE s.item_id IS NULL`: 0,
		`SELECT count(*) FROM task_sightings s LEFT JOIN listings l ON l.item_id=s.item_id WHERE l.item_id IS NULL`:                     0,
	} {
		if got := countRows(t, db, query); got != expected {
			t.Errorf("合计或关联错误：%s = %d，期望 %d", query, got, expected)
		}
	}
	checkSampleJSON(t, db)
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	if rows.Next() || rows.Err() != nil {
		t.Fatal("存在外键错误")
	}
	rows.Close()
	cipher, err := security.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	authentication, bootstrap, err := auth.New(t.Context(), auth.Dependencies{DB: db, Cipher: cipher})
	if err != nil || bootstrap.Token == "" {
		t.Fatalf("正常首次初始化流程不可用：%v", err)
	}
	status, err := authentication.Setup(t.Context())
	if err != nil || status.Initialized {
		t.Fatalf("样例预先创建了管理员：%v", err)
	}
	if err := run(t.Context(), []string{"--data", dir}, io.Discard, seedEnvironment); err == nil {
		t.Fatal("重复执行没有拒绝已有数据")
	}
	again, err := config.LoadMasterKey(paths, seedEnvironment)
	if err != nil || !bytes.Equal(key, again) || countRows(t, db, "SELECT count(*) FROM findings") != 9 {
		t.Fatal("重复拒绝时改动了已有数据或密钥")
	}
	clear(again)
}

func checkSampleJSON(t *testing.T, db *sql.DB) {
	t.Helper()
	rows, err := db.Query(`SELECT desktop_url,image_urls_json FROM listings`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var link, images string
		if err := rows.Scan(&link, &images); err != nil {
			t.Fatal(err)
		}
		parsed, err := url.Parse(link)
		if err != nil || parsed.Hostname() != "example.invalid" || images != "[]" {
			t.Fatalf("样例含真实站点链接或外部图片：%q %q", link, images)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	for _, column := range []string{"reason", "fair_basis", "notify_skip_reason"} {
		rows, err := db.Query("SELECT " + column + " FROM findings WHERE " + column + " IS NOT NULL")
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var raw string
			var message locales.Message
			if err := rows.Scan(&raw); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(raw), &message); err != nil || message.Args == nil {
				t.Fatalf("原因没有使用正式 JSON：%q %v", raw, err)
			}
			for _, language := range []locales.Language{locales.ZhCN, locales.En, locales.Ja} {
				if _, err := locales.Format(language, message); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	var raw string
	if err := db.QueryRow(`SELECT analysis_json FROM findings WHERE item_id='sample-laptop-check'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var facts analysis.Facts
	if err := json.Unmarshal([]byte(raw), &facts); err != nil {
		t.Fatalf("待核风险未保留未知状态：%v", err)
	}
	found := false
	for _, check := range facts.Checks {
		found = found || check.Key == "repaired" && check.Status == analysis.CheckUnknown
	}
	if !found {
		t.Fatal("待核维修风险未保留未知状态")
	}
}

func TestSeedRequiresExplicitEmptyRealDirectory(t *testing.T) {
	for _, args := range [][]string{nil, {"--data", ""}, {"--data", t.TempDir(), "extra"}, {"--unknown"}} {
		if err := run(t.Context(), args, io.Discard, seedEnvironment); err == nil {
			t.Fatalf("接受了无效参数：%v", args)
		}
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "keep")
	if err := os.WriteFile(marker, []byte("不能覆盖"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(t.Context(), []string{"--data", dir}, io.Discard, seedEnvironment); err == nil {
		t.Fatal("接受了已有内容的目录")
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "不能覆盖" {
		t.Fatal("改动了已有文件")
	}
	outside := t.TempDir()
	linked := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(outside, linked); err != nil {
		t.Fatal(err)
	}
	if err := run(t.Context(), []string{"--data", linked}, io.Discard, seedEnvironment); err == nil {
		t.Fatal("接受了链接目录")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatal("跟随链接写入了外部目录")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	missing := filepath.Join(t.TempDir(), "cancelled")
	if err := run(ctx, []string{"--data", missing}, io.Discard, seedEnvironment); err == nil {
		t.Fatal("取消后仍创建样例")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("取消后创建了目录")
	}
}

func TestSeedKeyFailureDoesNotCreateDatabase(t *testing.T) {
	dir := t.TempDir()
	lookup := func(key string) (string, bool) {
		if key == "KAKATTA_SECRET_KEY_FILE" {
			return filepath.Join(dir, "missing-secret"), true
		}
		return seedEnvironment(key)
	}
	if err := run(t.Context(), []string{"--data", dir}, io.Discard, lookup); err == nil {
		t.Fatal("密钥失败后继续写库")
	}
	if _, err := os.Stat(filepath.Join(dir, "kakatta.db")); !os.IsNotExist(err) {
		t.Fatal("取得主密钥前创建了数据库")
	}
	lock, err := config.AcquireServerLock(dir)
	if err != nil {
		t.Fatal("失败退出后没有释放服务锁")
	}
	lock.Close()
}

func TestSeedWriteFailureRollsBackEverySample(t *testing.T) {
	database, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "kakatta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	db := database.DB()
	if _, err := db.Exec(`CREATE TRIGGER fail_sample BEFORE INSERT ON findings WHEN NEW.id=2 BEGIN SELECT RAISE(FAIL,'stop sample'); END`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 11, 14, 0, 0, 0, time.UTC)
	if _, err := populate(t.Context(), db, now, "Asia/Shanghai"); err == nil {
		t.Fatal("写入失败后报告成功")
	}
	for _, table := range []string{"tasks", "listings", "findings", "price_points", "seen_items", "task_sightings", "task_item_specs", "run_records"} {
		if countRows(t, db, "SELECT count(*) FROM "+table) != 0 {
			t.Fatalf("失败后留下了部分样例：%s", table)
		}
	}
	if _, err := db.Exec(`DROP TRIGGER fail_sample`); err != nil {
		t.Fatal(err)
	}
	if _, err := populate(t.Context(), db, now, "Asia/Shanghai"); err != nil {
		t.Fatalf("回滚后仍不能写入完整样例：%v", err)
	}
}

func countRows(t *testing.T, db *sql.DB, query string) int64 {
	t.Helper()
	var count int64
	if err := db.QueryRow(query).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestSampleAIProfilesContainValuationInputs(t *testing.T) {
	data, err := makeSamples(time.Date(2026, 11, 11, 14, 0, 0, 0, time.UTC), "Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range data.tasks {
		if task.JudgeMode == analysis.ModeAI && len(task.Profile.Specs) == 0 && len(task.Profile.Musts) == 0 {
			t.Fatalf("任务%d没有正式保存要求的规格或必须条件", task.ID)
		}
	}
	for _, finding := range data.findings {
		task, listing := data.tasks[finding.TaskID-1], data.listings[finding.ID-1]
		wantRecommended := finding.ItemID == "sample-camera-match" || finding.ItemID == "sample-laptop-deal"
		if sampleRecommended(task, finding, listing) != wantRecommended {
			t.Fatalf("样例%s的统计推荐资格错误", finding.ItemID)
		}
		if finding.Analysis == nil {
			continue
		}
		body, err := json.Marshal(finding.Analysis)
		if err != nil {
			t.Fatal(err)
		}
		facts, warnings, err := analysis.ParseFacts(body, task.Profile, analysis.EvidenceOriginals{Title: listing.Title, Description: *listing.Description})
		if err != nil || len(warnings) != 0 || !reflect.DeepEqual(&facts, finding.Analysis) {
			t.Fatalf("样例%s未通过正式事实核对：%+v %v", finding.ItemID, warnings, err)
		}
		if len(facts.Musts) != 1 || facts.Musts[0].ID != "m1" || facts.Musts[0].Status != analysis.ReqMet || facts.Musts[0].Rev != task.Profile.Musts[0].Rev {
			t.Fatalf("样例%s缺少可用于日常办公的已核事实", finding.ItemID)
		}
		withoutEvidence, warnings, err := analysis.ParseFacts(body, task.Profile, analysis.EvidenceOriginals{Title: listing.Title})
		if err != nil || len(warnings) == 0 || withoutEvidence.Musts[0].Status != analysis.ReqUnknown {
			t.Fatalf("样例%s的必须条件没有依赖真实原文证据", finding.ItemID)
		}
	}
}

func TestSampleRecommendationUsesStatisticsRule(t *testing.T) {
	for _, tc := range []struct {
		name       string
		mode       analysis.Mode
		decision   analysis.Decision
		score      *int64
		grade      *analysis.Grade
		price, max *int64
		hidden     bool
		want       bool
	}{
		{"80分", analysis.ModeAI, analysis.DecisionScored, samplePtr(int64(80)), samplePtr(analysis.GradeGood), samplePtr(int64(10000)), samplePtr(int64(10000)), false, true},
		{"79分", analysis.ModeAI, analysis.DecisionScored, samplePtr(int64(79)), samplePtr(analysis.GradeFair), samplePtr(int64(10000)), nil, false, false},
		{"无预算无法比价", analysis.ModeAI, analysis.DecisionScored, nil, nil, nil, nil, false, true},
		{"有预算未知价格", analysis.ModeAI, analysis.DecisionScored, nil, nil, nil, samplePtr(int64(10000)), false, false},
		{"无法比价但不建议", analysis.ModeAI, analysis.DecisionScored, nil, samplePtr(analysis.GradeAvoid), nil, nil, false, false},
		{"超预算", analysis.ModeAI, analysis.DecisionScored, samplePtr(int64(90)), samplePtr(analysis.GradeSteal), samplePtr(int64(10001)), samplePtr(int64(10000)), false, false},
		{"本地隐藏", analysis.ModeAI, analysis.DecisionScored, samplePtr(int64(90)), samplePtr(analysis.GradeSteal), samplePtr(int64(10000)), nil, true, false},
		{"关键词命中", analysis.ModeKeyword, analysis.DecisionMatched, nil, nil, samplePtr(int64(10000)), nil, false, true},
		{"关键词未命中", analysis.ModeKeyword, analysis.DecisionRejected, nil, nil, samplePtr(int64(10000)), nil, false, false},
		{"关键词隐藏", analysis.ModeKeyword, analysis.DecisionMatched, nil, nil, samplePtr(int64(10000)), nil, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policies := []string{"steal", "good", "fair", "all_new", "none"}
			if tc.mode == analysis.ModeKeyword {
				policies = []string{"recommended", "all_new", "none"}
			}
			for _, policy := range policies {
				task := service.Task{TaskFields: service.TaskFields{JudgeMode: tc.mode, BudgetMax: tc.max, NotifyPolicy: policy}}
				finding := service.Finding{Decision: tc.decision, Score: tc.score, Grade: tc.grade, NotifyKind: samplePtr("new")}
				listing := service.Listing{Price: tc.price, IsDismissed: tc.hidden}
				if got := sampleRecommended(task, finding, listing); got != tc.want {
					t.Fatalf("推送档位%s下统计推荐=%t，期望%t", policy, got, tc.want)
				}
			}
		})
	}
}
