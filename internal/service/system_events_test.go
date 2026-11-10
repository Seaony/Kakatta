package service

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Seaony/Kakatta/internal/appsettings"
)

func TestSystemEventsPreserveNullableFieldsAndStablePages(t *testing.T) {
	s, _ := statusTestService(t)
	for _, event := range []struct{ at, resolved any }{{100, nil}, {100, 101}, {100, nil}, {90, nil}, {nil, nil}, {nil, nil}} {
		if _, err := s.db.ExecContext(t.Context(), `INSERT INTO system_events(kind,subject_id,message,created_at,resolved_at) VALUES(NULL,NULL,NULL,?,?)`, event.at, event.resolved); err != nil {
			t.Fatal(err)
		}
	}
	var oldClock string
	if err := s.db.QueryRowContext(t.Context(), `SELECT value FROM meta WHERE key=?`, appsettings.ClockKey).Scan(&oldClock); err != nil {
		t.Fatal(err)
	}
	query := SystemEventQuery{ListQuery: ListQuery{Limit: 2}}
	ids := []int64{}
	for {
		page, err := s.ListSystemEvents(t.Context(), query)
		if err != nil {
			t.Fatal(err)
		}
		if page.Total != 6 {
			t.Fatalf("总数被分页游标截断：%d", page.Total)
		}
		for _, event := range page.Items {
			ids = append(ids, event.ID)
			if event.Kind != nil || event.SubjectID != nil || event.Message != nil || event.NotifiedAt != nil {
				t.Fatalf("数据库的未知字段被编造：%+v", event)
			}
		}
		if page.NextCursor == nil {
			break
		}
		query.Cursor = *page.NextCursor
	}
	if !reflect.DeepEqual(ids, []int64{3, 2, 1, 4, 6, 5}) {
		t.Fatalf("同时间或空时间分页丢失/重复：%v", ids)
	}
	var afterClock string
	if err := s.db.QueryRowContext(t.Context(), `SELECT value FROM meta WHERE key=?`, appsettings.ClockKey).Scan(&afterClock); err != nil || afterClock != oldClock {
		t.Fatal("事件只读列表改了额度时钟", err)
	}
}

func TestSystemEventsFilterAndCursorBinding(t *testing.T) {
	s, _ := statusTestService(t)
	for i := int64(1); i <= 5; i++ {
		var resolved any
		if i%2 == 0 {
			resolved = i + 1
		}
		if _, err := s.db.ExecContext(t.Context(), `INSERT INTO system_events(kind,subject_id,message,created_at,resolved_at) VALUES('ai_error',?,?,?,?)`, i, `{"code":"ai.provider_error","args":{"provider":"本地测试"}}`, i, resolved); err != nil {
			t.Fatal(err)
		}
	}
	unresolved := false
	query := SystemEventQuery{ListQuery: ListQuery{Limit: 1}, Resolved: &unresolved}
	first, err := s.ListSystemEvents(t.Context(), query)
	if err != nil || first.Total != 3 || len(first.Items) != 1 || first.Items[0].ID != 5 || first.NextCursor == nil {
		t.Fatalf("未解决筛选不准：%+v %v", first, err)
	}
	if first.Items[0].Message == nil || first.Items[0].Message.Code != "ai.provider_error" || first.Items[0].Message.Args["provider"] != "本地测试" {
		t.Fatal("原因代码/参数没有保留")
	}
	query.Cursor, query.Limit = *first.NextCursor, 2
	second, err := s.ListSystemEvents(t.Context(), query)
	if err != nil || len(second.Items) != 2 || second.Items[0].ID != 3 || second.Items[1].ID != 1 || second.NextCursor != nil {
		t.Fatalf("游标没有绑定规范筛选：%+v %v", second, err)
	}
	resolved := true
	query.Resolved = &resolved
	if _, err := s.ListSystemEvents(t.Context(), query); !statusBadQuery(err) {
		t.Fatalf("跨筛选游标被接受：%v", err)
	}
	for _, query := range []SystemEventQuery{{ListQuery: ListQuery{Limit: -1}}, {ListQuery: ListQuery{Limit: 201}}, {ListQuery: ListQuery{Cursor: "invalid"}}} {
		if _, err := s.ListSystemEvents(t.Context(), query); !statusBadQuery(err) {
			t.Fatalf("非法查询没有返回明确错误：%+v %v", query, err)
		}
	}
}

func TestSystemEventsRejectBrokenMessagesAndReturnEmptyArrays(t *testing.T) {
	s, _ := statusTestService(t)
	page, err := s.ListSystemEvents(t.Context(), SystemEventQuery{})
	if err != nil || page.Items == nil || len(page.Items) != 0 || page.Total != 0 || page.NextCursor != nil {
		t.Fatalf("空页不符合接口：%+v %v", page, err)
	}
	raw, _ := json.Marshal(page)
	if !strings.Contains(string(raw), `"items":[]`) {
		t.Fatal("空列表编码成null")
	}
	for _, raw := range []string{`{`, `null`, `[]`, `{"code":""}`, `{"code":"x","args":[]}`, `{"code":"x","unknown":true}`} {
		if _, err := s.db.ExecContext(t.Context(), `INSERT INTO system_events(kind,subject_id,message,created_at) VALUES('ai_error',1,?,1)`, raw); err != nil {
			t.Fatal(err)
		}
		_, err := s.ListSystemEvents(t.Context(), SystemEventQuery{})
		var failure *ServiceError
		if !errors.As(err, &failure) || failure.Kind != ErrorUnavailable || failure.Reason.Code != "service.storage_failure" {
			t.Fatalf("损坏原因被吞掉或回显：%s %v", raw, err)
		}
		if _, err := s.db.ExecContext(t.Context(), `DELETE FROM system_events`); err != nil {
			t.Fatal(err)
		}
	}
}

func statusBadQuery(err error) bool {
	var failure *ServiceError
	return errors.As(err, &failure) && failure.Kind == ErrorBadRequest
}
