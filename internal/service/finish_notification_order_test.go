package service

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func TestFinishRunRecalculatesMarketAndScoreBeforeNotification(t *testing.T) {
	s, run, notifier, now := finishFixture(t)
	seedFinishMarket(t, s, now, 6, false)
	finishExec(t, s.db, `UPDATE tasks SET judge_mode='ai',notify_policy='good',
		profile_json='{"musts":[{"id":"m1","text":"国行","source":"user","rev":1}],"next_no":2}' WHERE id=1`)
	finishExec(t, s.db, `UPDATE listings SET price=75 WHERE item_id='item-0'`)
	finishExec(t, s.db, `UPDATE task_sightings SET price=75 WHERE item_id='item-0'`)
	finishExec(t, s.db, `INSERT INTO findings(id,task_id,item_id,search_generation,found_at,is_baseline,decision,stage,decided_layer,
		analysis_json,score,grade,scored_at,analyzed_at,is_read)
		VALUES(41,1,'item-0',1,?,0,'scored','done',3,
		'{"specs":[],"musts":[{"id":"m1","status":"met","source":"text","evidence":"国行","rev":1}],"checks":[],"adjust":{"value":0,"reason":""},"summary":"符合要求"}',NULL,NULL,?,?,0)`,
		now.UnixMilli()-1, now.UnixMilli()-1, now.UnixMilli()-1)
	notifier.flush = func(ctx context.Context, ref RunRef, ids []int64) (NotifyResult, error) {
		if ref != run.Ref || len(ids) != 1 || ids[0] != 41 {
			t.Fatalf("通知没有收到本次首次结论：%+v %v", ref, ids)
		}
		bounded, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		tx, err := s.db.BeginTx(bounded, nil)
		if err != nil {
			return NotifyResult{}, err
		}
		defer tx.Rollback()
		var score, fair, sample sql.NullInt64
		var grade sql.NullString
		var scoredAt int64
		if err := tx.QueryRowContext(bounded, `SELECT score,grade,fair_price,fair_sample,scored_at FROM findings WHERE id=41`).
			Scan(&score, &grade, &fair, &sample, &scoredAt); err != nil {
			return NotifyResult{}, err
		}
		if !score.Valid || score.Int64 != 88 || !grade.Valid || grade.String != "good" || !fair.Valid || fair.Int64 != 100 || !sample.Valid || sample.Int64 != 5 || scoredAt != now.UnixMilli() {
			t.Errorf("通知必须读到新行情重算后的划算结论：分数=%+v，档位=%+v，合理价=%+v，样本=%+v，重算时间=%d", score, grade, fair, sample, scoredAt)
		}
		if _, err := tx.ExecContext(bounded, `UPDATE tasks SET name=name WHERE id=1`); err != nil {
			return NotifyResult{}, err
		}
		return NotifyResult{}, tx.Commit()
	}
	if err := s.finishRun(t.Context(), run, nil); err != nil {
		t.Fatal(err)
	}
	if notifier.calls != 1 {
		t.Fatalf("首次结论应判定通知一次，实际%d次", notifier.calls)
	}
}
