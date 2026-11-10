package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Seaony/Kakatta/internal/locales"
)

func assertTaskMessageNumber(t *testing.T, message *locales.Message, want string) {
	t.Helper()
	if message == nil || message.Code != "historical.task.error" {
		t.Fatalf("历史错误消息丢失：%+v", message)
	}
	value, ok := message.Args["value"].(json.Number)
	if !ok || value.String() != want {
		t.Fatalf("历史数字被舍入或改类型：%T %v，期望%s", message.Args["value"], message.Args["value"], want)
	}
	nested, ok := message.Args["nested"].(map[string]any)
	if !ok {
		t.Fatalf("嵌套参数变形：%+v", message.Args)
	}
	values, ok := nested["values"].([]any)
	if !ok || len(values) != 2 || values[1] != nil {
		t.Fatalf("参数数组或null变形：%+v", nested)
	}
	number, ok := values[0].(json.Number)
	if !ok || number.String() != want {
		t.Fatalf("嵌套数字被舍入或改类型：%T %v", values[0], values[0])
	}
	raw, err := json.Marshal(message)
	if err != nil || strings.Count(string(raw), want) != 2 {
		t.Fatalf("消息无法无损编码：%s %v", raw, err)
	}
}

func TestTaskLastErrorPreservesOpaqueNumbersInRealQueries(t *testing.T) {
	for _, value := range []string{"9007199254740993", "9007199254740993123456789", "1e1000"} {
		t.Run(value, func(t *testing.T) {
			h := newTaskHarness(t)
			task := h.create(nil)
			raw := `{"code":"historical.task.error","args":{"value":` + value + `,"nested":{"values":[` + value + `,null]}}}`
			h.exec(`UPDATE tasks SET last_error=? WHERE id=?`, raw, task.ID)
			view, err := h.s.GetTask(h.ctx, task.ID)
			if err != nil {
				t.Fatalf("真实GetTask无法读取合法历史参数：%v", err)
			}
			assertTaskMessageNumber(t, view.LastError, value)
			tx, err := h.s.db.BeginTx(h.ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			loaded, err := loadTaskTx(h.ctx, tx, task.ID)
			if err != nil {
				t.Fatalf("读取事务无法载入合法历史参数：%v", err)
			}
			assertTaskMessageNumber(t, loaded.LastError, value)
		})
	}
}

func TestTaskLastErrorRetainsSharedMessageValidation(t *testing.T) {
	h := newTaskHarness(t)
	task := h.create(nil)
	view, err := h.s.GetTask(h.ctx, task.ID)
	if err != nil || view.LastError != nil {
		t.Fatalf("数据库NULL没有保留：%+v %v", view.LastError, err)
	}
	h.exec(`UPDATE tasks SET last_error='{"code":"historical.task.error"}' WHERE id=?`, task.ID)
	view, err = h.s.GetTask(h.ctx, task.ID)
	if err != nil || view.LastError == nil || view.LastError.Args == nil {
		t.Fatalf("缺省参数未沿共用消息规则归一化：%+v %v", view.LastError, err)
	}
	for _, raw := range []string{`{"args":{}}`, `{"code":"historical.task.error","args":{}}{}`, `{"code":"historical.task.error","args":`} {
		h.exec(`UPDATE tasks SET last_error=? WHERE id=?`, raw, task.ID)
		if _, err = h.s.GetTask(h.ctx, task.ID); err == nil {
			t.Fatalf("损坏错误消息被接受：%s", raw)
		}
	}
}
