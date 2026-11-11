package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/settings"
)

func TestRunLogStepsRetainAllFailuresAfterFirstHundred(t *testing.T) {
	s, now := logTestService(t)
	taskID := logTestTask(t, s, "步骤")
	tx := logTestTx(t, s)
	run, err := beginRunTx(t.Context(), tx, logTestRecord(taskID, "步骤", now.UnixMilli()))
	if err != nil {
		t.Fatal(err)
	}
	at := now.UnixMilli()
	for i := range 130 {
		status := StepOK
		if i >= 120 {
			status = StepFail
		} else if i >= 100 {
			status = StepWarn
		}
		recorded, err := appendRunStepTx(t.Context(), tx, RunStep{
			RunID: run.ID, Seq: 9999, At: &at, Status: &status,
			Message: &locales.Message{Code: "run.step", Args: map[string]any{"index": i}},
			Detail:  &locales.Message{Code: "run.duration", Args: map[string]any{"milliseconds": 100}},
		})
		if err != nil || recorded != (i < 100 || i >= 120) {
			t.Fatalf("第%d步保留规则错误：%v %v", i, recorded, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	detail, err := s.GetRun(t.Context(), run.ID)
	if err != nil || detail.Steps.Total != 110 || len(detail.Steps.Items) != 50 || detail.Steps.NextCursor == nil {
		t.Fatalf("详情没有复用可翻页的步骤读取：%+v %v", detail, err)
	}
	var all []RunStep
	q := ListQuery{Limit: 17}
	for {
		page, err := s.ListRunSteps(t.Context(), run.ID, q)
		if err != nil || page.Total != 110 {
			t.Fatal("读取步骤失败", page, err)
		}
		all = append(all, page.Items...)
		if page.NextCursor == nil {
			break
		}
		q.Cursor = *page.NextCursor
	}
	if len(all) != 110 {
		t.Fatal("失败步骤被硬截到100条", len(all))
	}
	for i, step := range all {
		if step.Seq != int64(i+1) || step.At == nil || step.Message == nil || step.Detail == nil ||
			i >= 100 && (step.Status == nil || *step.Status != StepFail) {
			t.Fatalf("步骤序号/失败保留/细节错误：%d %+v", i, step)
		}
	}
	other := logTestFinished(t, s, taskID, "另一次运行", now.Add(time.Second).UnixMilli(), 10, RunSuccess)
	page, err := s.ListRunSteps(t.Context(), run.ID, ListQuery{Limit: 1})
	if err != nil || page.NextCursor == nil {
		t.Fatal(err)
	}
	_, err = s.ListRunSteps(t.Context(), other.ID, ListQuery{Cursor: *page.NextCursor})
	logTestErrorKind(t, err, ErrorBadRequest)
	if _, err := s.db.ExecContext(t.Context(), "DELETE FROM run_records WHERE id=?", run.ID); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := s.db.QueryRowContext(t.Context(), "SELECT count(*) FROM run_steps WHERE run_id=?", run.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("运行删除后步骤没有联动清理", count, err)
	}
}

func TestRunLogStepsRollbackAndRejectBadInput(t *testing.T) {
	s, now := logTestService(t)
	taskID := logTestTask(t, s, "步骤错误")
	run := logTestFinished(t, s, taskID, "步骤错误", now.UnixMilli(), 10, RunSuccess)
	tx := logTestTx(t, s)
	step := RunStep{RunID: run.ID, At: logTestPointer(now.UnixMilli()), Status: logTestPointer(StepOK),
		Message: &locales.Message{Code: "run.step"}}
	if recorded, err := appendRunStepTx(t.Context(), tx, step); err != nil || !recorded {
		t.Fatal("合法步骤没有写入", recorded, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal("步骤入口擅自结束事务", err)
	}
	page, err := s.ListRunSteps(t.Context(), run.ID, ListQuery{})
	if err != nil || page.Total != 0 || page.Items == nil {
		t.Fatal("步骤没有随事务回滚", page, err)
	}
	tx = logTestTx(t, s)
	for _, bad := range []RunStep{
		{}, {RunID: run.ID, At: step.At, Status: logTestPointer(StepStatus("pending")), Message: step.Message},
		{RunID: run.ID, At: step.At, Status: step.Status},
	} {
		if recorded, err := appendRunStepTx(t.Context(), tx, bad); err == nil || recorded {
			t.Fatal("无效步骤被写入", bad, recorded, err)
		}
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if recorded, err := appendRunStepTx(cancelled, tx, step); !errors.Is(err, context.Canceled) || recorded {
		t.Fatal("步骤取消没有保留", recorded, err)
	}
	if recorded, err := appendRunStepTx(t.Context(), tx, step); err != nil || !recorded {
		t.Fatal("失败入口结束了外层事务", recorded, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), "UPDATE run_steps SET detail='broken' WHERE run_id=?", run.ID); err != nil {
		t.Fatal(err)
	}
	_, err = s.ListRunSteps(t.Context(), run.ID, ListQuery{})
	logTestErrorKind(t, err, ErrorUnavailable)
	_, err = s.ListRunSteps(t.Context(), run.ID+1, ListQuery{})
	logTestErrorKind(t, err, ErrorNotFound)
}

func TestRunLogStepsPreserveNullableLegacyFields(t *testing.T) {
	s, now := logTestService(t)
	taskID := logTestTask(t, s, "旧步骤")
	run := logTestFinished(t, s, taskID, "旧步骤", now.UnixMilli(), 10, RunSuccess)
	if _, err := s.db.ExecContext(t.Context(), "INSERT INTO run_steps(run_id,seq) VALUES(?,1)", run.ID); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListRunSteps(t.Context(), run.ID, ListQuery{})
	if err != nil || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	raw, err := json.Marshal(page.Items[0])
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || string(object["at"]) != "null" ||
		string(object["status"]) != "null" || string(object["message"]) != "null" || string(object["detail"]) != "null" {
		t.Fatalf("旧的未知字段被伪造成默认值：%s %v", raw, err)
	}
}

func TestRunLogStepsApplyCurrentRetentionBeforeCleanup(t *testing.T) {
	s, now := logTestService(t)
	taskID := logTestTask(t, s, "步骤保留期")
	cutoff := now.Add(-24 * time.Hour).UnixMilli()
	run := logTestFinished(t, s, taskID, "步骤保留期", cutoff-1, 10, RunSuccess)
	if _, err := s.db.ExecContext(t.Context(), `INSERT INTO run_steps(run_id,seq,at) VALUES
		(?,1,?), (?,2,?), (?,3,?), (?,4,NULL)`, run.ID, cutoff-1, run.ID, cutoff, run.ID, cutoff+1, run.ID); err != nil {
		t.Fatal(err)
	}
	initial, err := s.ListRunSteps(t.Context(), run.ID, ListQuery{Limit: 1})
	if err != nil || initial.Total != 4 || initial.NextCursor == nil || initial.Items[0].Seq != 1 {
		t.Fatalf("默认保留期的第一页：%+v %v", initial, err)
	}
	if _, err := s.settings.Apply(t.Context(), settings.Values{"retention.run_steps_days": json.RawMessage(`1`)}); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(-2 * time.Hour)
	detail, err := s.GetRun(t.Context(), run.ID)
	if err != nil || detail.Run.ID != run.ID || detail.Steps.Total != 3 || len(detail.Steps.Items) != 3 || detail.Steps.Items[0].Seq != 2 || detail.Steps.Items[2].At != nil {
		t.Fatalf("缩短保留期后只显示有效步骤和未知时间的旧步骤：%+v %v", detail, err)
	}
	page, err := s.ListRunSteps(t.Context(), run.ID, ListQuery{Limit: 1})
	if err != nil || page.Total != 3 || len(page.Items) != 1 || page.Items[0].Seq != 2 || page.NextCursor == nil {
		t.Fatalf("首屏数量、边界或游标错误：%+v %v", page, err)
	}
	continued, err := s.ListRunSteps(t.Context(), run.ID, ListQuery{Limit: 2, Cursor: *initial.NextCursor})
	if err != nil || continued.Total != 3 || len(continued.Items) != 2 || continued.Items[0].Seq != 2 || continued.Items[1].Seq != 3 || continued.NextCursor == nil {
		t.Fatalf("调整前的游标没有继续执行新保留期：%+v %v", continued, err)
	}
	var count int
	if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM run_steps WHERE run_id=?`, run.ID).Scan(&count); err != nil || count != 4 {
		t.Fatalf("只读查询物理删除了步骤：%d %v", count, err)
	}
}
