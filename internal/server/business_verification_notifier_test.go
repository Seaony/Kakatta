package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/service"
)

type verificationBusinessNotifier struct {
	failedBusinessNotifier
	t         *testing.T
	db        *sql.DB
	now       func() int64
	accountID atomic.Int64
	taskID    atomic.Int64
	runID     atomic.Int64
	eventID   atomic.Int64
	failures  chan error
}

func (n *verificationBusinessNotifier) reportFailure(kind string, id int64, err error) {
	if err == nil {
		return
	}
	n.t.Logf("受控%s失败：id=%d，err=%v", kind, id, err)
	select {
	case n.failures <- err:
	default:
	}
}

func (n *verificationBusinessNotifier) FlushRun(ctx context.Context, ref service.RunRef, ids []int64) (result service.NotifyResult, err error) {
	defer func() { n.reportFailure("运行结算", ref.RunID, err) }()
	if ref.RunID < 1 || ref.TaskID != n.taskID.Load() || ref.SearchGeneration < 1 || len(ids) != 0 {
		return result, fmt.Errorf("非预期的零商品运行结算：ref=%+v first_conclusions=%v", ref, ids)
	}
	if previous := n.runID.Load(); previous != 0 && previous != ref.RunID {
		return result, fmt.Errorf("验证会话出现额外运行结算：ref=%+v expected_run=%d", ref, previous)
	}
	tx, err := n.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var runID, conclusions, pending int64
	err = tx.QueryRowContext(ctx, `SELECT r.id,
		(SELECT count(*) FROM findings f WHERE f.task_id=t.id AND f.search_generation=t.search_generation AND f.decision IN ('scored','matched','rejected')),
		(SELECT count(*) FROM notification_intents ni WHERE ni.task_id=t.id AND ni.state='pending')
		FROM run_records r JOIN tasks t ON t.id=r.task_id JOIN accounts a ON a.id=r.account_id
		WHERE r.id=? AND t.id=? AND r.account_id=? AND t.account_id=r.account_id
		AND t.search_generation=? AND r.ended_at IS NULL AND a.state='active'`,
		ref.RunID, ref.TaskID, n.accountID.Load(), ref.SearchGeneration).Scan(&runID, &conclusions, &pending)
	if err != nil {
		return result, fmt.Errorf("读取当前验证运行 %+v：%w", ref, err)
	}
	if conclusions != 0 || pending != 0 {
		return result, fmt.Errorf("验证运行存在商品通知工作：ref=%+v conclusions=%d pending=%d", ref, conclusions, pending)
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	if n.runID.CompareAndSwap(0, runID) {
		n.t.Logf("受控运行结算确认零投递：run=%d task=%d account=%d", runID, ref.TaskID, n.accountID.Load())
	}
	return result, nil
}

func TestBusinessVerificationNotifierSettlesOnlyEmptyCurrentRun(t *testing.T) {
	f := newServerFixture(t, true)
	notifier := &verificationBusinessNotifier{t: t, db: f.store.DB()}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := f.store.DB().ExecContext(t.Context(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO accounts(id,name,state) VALUES(1,'当前账号','active'),(2,'其他账号','active')`)
	exec(`INSERT INTO tasks(id,name,keyword,enabled,account_id,max_pages,interval_minutes,judge_mode,notify_policy,sort_index)
		VALUES(1,'当前任务','相机',0,1,1,60,'keyword','recommended',0),(2,'其他任务','相机',0,2,1,60,'keyword','recommended',1)`)
	exec(`INSERT INTO run_records(id,task_id,task_name,trigger,account_id,started_at,outcome)
		VALUES(1,1,'当前任务','manual',1,1,'failed'),(2,2,'其他任务','manual',2,1,'failed')`)
	notifier.accountID.Store(1)
	notifier.taskID.Store(1)
	ref := service.RunRef{RunID: 1, TaskID: 1, SearchGeneration: 1}
	reject := func(ref service.RunRef, ids []int64) {
		t.Helper()
		if _, err := notifier.FlushRun(t.Context(), ref, ids); err == nil || notifier.runID.Load() != 0 {
			t.Fatalf("非空或非当前运行不能成功结算：ref=%+v ids=%v err=%v", ref, ids, err)
		}
	}
	for _, invalid := range []service.RunRef{{}, {RunID: 99, TaskID: 1, SearchGeneration: 1}, {RunID: 2, TaskID: 2, SearchGeneration: 1}, {RunID: 1, TaskID: 1, SearchGeneration: 2}} {
		reject(invalid, nil)
	}
	reject(ref, []int64{1})
	for _, mutation := range []struct{ change, restore string }{
		{`UPDATE accounts SET state='needs_verification' WHERE id=1`, `UPDATE accounts SET state='active' WHERE id=1`},
		{`UPDATE run_records SET account_id=2 WHERE id=1`, `UPDATE run_records SET account_id=1 WHERE id=1`},
		{`UPDATE tasks SET account_id=2 WHERE id=1`, `UPDATE tasks SET account_id=1 WHERE id=1`},
		{`UPDATE run_records SET ended_at=2 WHERE id=1`, `UPDATE run_records SET ended_at=NULL WHERE id=1`},
	} {
		exec(mutation.change)
		reject(ref, nil)
		exec(mutation.restore)
	}
	exec(`INSERT INTO notification_intents(finding_id,task_id,kind,state,created_at,updated_at) VALUES(99,1,'recommended','pending',1,1)`)
	reject(ref, nil)
	exec(`UPDATE notification_intents SET state='cancelled'`)
	exec(`INSERT INTO listings(item_id,title,desktop_url,first_seen_at,last_seen_at) VALUES('商品','相机','http://localhost/item',1,1)`)
	exec(`INSERT INTO findings(task_id,item_id,search_generation,found_at,is_baseline,decision,stage,is_read) VALUES(1,'商品',1,1,0,'matched','done',0)`)
	reject(ref, nil)
	exec(`DELETE FROM findings`)
	result, err := notifier.FlushRun(t.Context(), ref, nil)
	if err != nil || result.NotifiedCount != 0 || len(result.Warnings) != 0 || notifier.runID.Load() != ref.RunID {
		t.Fatalf("真实无商品运行必须结算零投递：result=%+v run=%d err=%v", result, notifier.runID.Load(), err)
	}
	var deliveries int
	if err := f.store.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM deliveries`).Scan(&deliveries); err != nil || deliveries != 0 {
		t.Fatalf("受控结算不能制造投递：count=%d err=%v", deliveries, err)
	}
	exec(`INSERT INTO run_records(id,task_id,task_name,trigger,account_id,started_at,outcome) VALUES(3,1,'当前任务','manual',1,1,'failed')`)
	if _, err := notifier.FlushRun(t.Context(), service.RunRef{RunID: 3, TaskID: 1, SearchGeneration: 1}, nil); err == nil {
		t.Fatal("同一验证夹具不能接收另一次运行")
	}
	if _, err := (failedBusinessNotifier{}).FlushRun(t.Context(), ref, nil); err == nil {
		t.Fatal("默认业务夹具仍须拒绝未声明的结算")
	}
}

func (n *verificationBusinessNotifier) System(ctx context.Context, id int64) (err error) {
	defer func() { n.reportFailure("系统通知", id, err) }()
	tx, err := n.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var kind string
	var subjectID, resolvedAt sql.NullInt64
	if err = tx.QueryRowContext(ctx, `SELECT e.kind,e.subject_id,e.resolved_at FROM system_events e JOIN accounts a ON a.id=e.subject_id
		WHERE e.id=? AND a.state='needs_verification'`, id).Scan(&kind, &subjectID, &resolvedAt); err != nil {
		return fmt.Errorf("读取已提交的验证事件 %d：%w", id, err)
	}
	accountID := n.accountID.Load()
	if accountID < 1 || kind != "account_verify" || !subjectID.Valid || subjectID.Int64 != accountID || resolvedAt.Valid {
		return fmt.Errorf("非预期的待验证事件：event=%d kind=%s subject=%v resolved=%v expected_account=%d", id, kind, subjectID, resolvedAt, accountID)
	}
	if previous := n.eventID.Load(); previous != 0 && previous != id {
		return fmt.Errorf("验证会话出现额外事件：event=%d expected_event=%d", id, previous)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE system_events SET notified_at=coalesce(notified_at,?) WHERE id=?`, n.now(), id); err != nil {
		return fmt.Errorf("保存受控验证通知回执 %d：%w", id, err)
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if n.eventID.CompareAndSwap(0, id) {
		n.t.Logf("受控系统通知收到真实待验证事件：event=%d account=%d", id, accountID)
	}
	return nil
}

func newVerificationBusinessFixture(t *testing.T, factories ...service.ResourceFactory) (*businessFixture, *verificationBusinessNotifier) {
	t.Helper()
	f := prepareBusinessFixture(t)
	notifier := &verificationBusinessNotifier{t: t, db: f.store.DB(), now: f.clock.Load, failures: make(chan error, 1)}
	t.Cleanup(func() {
		select {
		case err := <-notifier.failures:
			t.Errorf("验证会话存在未预期的通知错误：%v", err)
		default:
		}
	})
	f.start(t, notifier, factories...)
	return f, notifier
}

func TestBusinessVerificationNotifierOnlyAcceptsExpectedPendingEvent(t *testing.T) {
	f := newServerFixture(t, true)
	notifier := &verificationBusinessNotifier{t: t, db: f.store.DB(), now: f.clock.Load}
	var accountID, otherID int64
	if err := f.store.DB().QueryRowContext(t.Context(), `INSERT INTO accounts(name,state) VALUES('预期验证账号','needs_verification') RETURNING id`).Scan(&accountID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.DB().QueryRowContext(t.Context(), `INSERT INTO accounts(name,state) VALUES('其他账号','needs_verification') RETURNING id`).Scan(&otherID); err != nil {
		t.Fatal(err)
	}
	notifier.accountID.Store(accountID)
	insert := func(kind string, subjectID int64, resolvedAt *int64) int64 {
		t.Helper()
		var id int64
		if err := f.store.DB().QueryRowContext(t.Context(), `INSERT INTO system_events(kind,subject_id,resolved_at) VALUES(?,?,?) RETURNING id`, kind, subjectID, resolvedAt).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	resolvedAt := f.clock.Load()
	for _, id := range []int64{
		0,
		insert("account_login", accountID, nil),
		insert("account_verify", otherID, nil),
		insert("account_verify", accountID, &resolvedAt),
	} {
		if err := notifier.System(t.Context(), id); err == nil || notifier.eventID.Load() != 0 {
			t.Fatalf("未预期或已解决事件不能成功：event=%d，err=%v", id, err)
		}
	}
	id := insert("account_verify", accountID, nil)
	if _, err := f.store.DB().ExecContext(t.Context(), `UPDATE accounts SET state='active' WHERE id=?`, accountID); err != nil {
		t.Fatal(err)
	}
	if err := notifier.System(t.Context(), id); err == nil || notifier.eventID.Load() != 0 {
		t.Fatalf("验证状态尚未保存时不能确认系统事件：%v", err)
	}
	if _, err := f.store.DB().ExecContext(t.Context(), `UPDATE accounts SET state='needs_verification' WHERE id=?`, accountID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.DB().ExecContext(t.Context(), `CREATE TRIGGER reject_verification_receipt BEFORE UPDATE OF notified_at ON system_events BEGIN SELECT RAISE(ABORT,'本地回执写入失败'); END`); err != nil {
		t.Fatal(err)
	}
	if err := notifier.System(t.Context(), id); err == nil || !strings.Contains(err.Error(), "本地回执写入失败") || notifier.eventID.Load() != 0 {
		t.Fatalf("保存回执失败不能报告系统通知成功，且必须保留原错：%v", err)
	}
	if _, err := f.store.DB().ExecContext(t.Context(), `DROP TRIGGER reject_verification_receipt`); err != nil {
		t.Fatal(err)
	}
	notifiedAt := f.clock.Load()
	for range 2 {
		if err := notifier.System(t.Context(), id); err != nil || notifier.eventID.Load() != id {
			t.Fatalf("同一真实未解决事件应受控成功：event=%d，err=%v", id, err)
		}
		var savedAt int64
		if err := f.store.DB().QueryRowContext(t.Context(), `SELECT notified_at FROM system_events WHERE id=?`, id).Scan(&savedAt); err != nil || savedAt != notifiedAt {
			t.Fatalf("受控回执必须保存首次成功时间：saved=%d want=%d err=%v", savedAt, notifiedAt, err)
		}
		f.advance(time.Minute)
	}
	var changedOthers int
	if err := f.store.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM system_events WHERE id<>? AND notified_at IS NOT NULL`, id).Scan(&changedOthers); err != nil || changedOthers != 0 {
		t.Fatalf("不能给其他系统事件写回执：count=%d err=%v", changedOthers, err)
	}
	if err := notifier.System(t.Context(), id+1000); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("通知查询的真实错误必须保留：%v", err)
	}
	if _, err := notifier.FlushRun(context.Background(), service.RunRef{}, nil); err == nil {
		t.Fatal("受控系统通知不能放行意外商品推送")
	}
	if err := (failedBusinessNotifier{}).System(t.Context(), id); err == nil {
		t.Fatal("其他业务夹具仍须拒绝意外通知")
	}
}
