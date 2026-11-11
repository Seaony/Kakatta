package main

import (
	"testing"
	"time"
)

func TestControllerRepeatedViewingKeepsOriginalDeadline(t *testing.T) {
	c, err := newController(options{Data: t.TempDir(), Account: 449, Fixture: true}, testSettings())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	r := &resources{}
	c.res, c.state.Phase = r, "verification"
	c.arm(r, time.Now().Add(30*time.Minute))

	before := time.Now()
	c.viewing(r)
	after := time.Now()
	r.mu.Lock()
	deadline, timer := r.deadline, r.timer
	r.mu.Unlock()
	if deadline.Before(before.Add(10*time.Minute)) || deadline.After(after.Add(10*time.Minute)) || timer == nil {
		t.Fatalf("首次打开没有把保留时间缩短为十分钟：%s", deadline)
	}
	for range 3 {
		c.viewing(r)
		r.mu.Lock()
		gotDeadline, gotTimer := r.deadline, r.timer
		r.mu.Unlock()
		if !gotDeadline.Equal(deadline) || gotTimer != timer {
			t.Fatalf("重复打开延长或重新安排了验证期限：原来 %s，现在 %s", deadline, gotDeadline)
		}
	}
}
