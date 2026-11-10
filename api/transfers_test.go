package api_test

import (
	"slices"
	"strings"
	"testing"
)

func TestTransferOperationsUseFrozenPathsAndMediaTypes(t *testing.T) {
	document := decoded(t)
	paths := object(t, document["paths"])
	for _, want := range []struct {
		method, path, id, requestMedia, responseMedia string
	}{
		{"get", "/api/v1/tasks/export", "exportTasks", "", "application/json"},
		{"post", "/api/v1/tasks/import", "importTasks", "multipart/form-data", "application/json"},
		{"get", "/api/v1/tasks/{task_id}/findings/export", "exportTaskFindings", "", "text/csv"},
		{"get", "/api/v1/runs/export", "exportRuns", "", "text/plain"},
		{"get", "/api/v1/system/update", "getUpdateStatus", "", "application/json"},
		{"post", "/api/v1/backup/export", "exportBackup", "application/json", "application/zip"},
		{"post", "/api/v1/backup/restore", "prepareBackupRestore", "multipart/form-data", "application/json"},
		{"post", "/api/v1/backup/confirm", "confirmBackupRestore", "application/json", "application/json"},
	} {
		operation := object(t, object(t, paths[want.path])[want.method])
		if operation["operationId"] != want.id {
			t.Fatalf("操作名不符：%s", want.path)
		}
		success := object(t, object(t, operation["responses"])["200"])
		if _, exists := object(t, success["content"])[want.responseMedia]; !exists {
			t.Fatalf("下载或报告媒体类型不符：%s", want.path)
		}
		if want.requestMedia == "" {
			if _, exists := operation["requestBody"]; exists {
				t.Fatalf("只读接口不应新增请求体：%s", want.path)
			}
		} else if _, exists := object(t, object(t, operation["requestBody"])["content"])[want.requestMedia]; !exists {
			t.Fatalf("上传媒体类型不符：%s", want.path)
		}
	}
	for _, old := range []string{"/api/v1/backups/export", "/api/v1/backups/restore/prepare", "/api/v1/backups/restore/commit", "/api/v1/updates", "/api/v1/runs/export.txt"} {
		if _, exists := paths[old]; exists {
			t.Fatalf("出现未冻结的旧建议路径：%s", old)
		}
	}
}

func TestTransferQueriesDoNotTruncateDownloads(t *testing.T) {
	document := decoded(t)
	paths := object(t, document["paths"])
	for path, want := range map[string][]string{
		"/api/v1/tasks/{task_id}/findings/export": {"decision", "over_budget", "price_drop", "q", "sort", "view"},
		"/api/v1/runs/export":                     {"failure_kind", "from_at", "outcome", "q", "task_id", "to_at"},
	} {
		operation := object(t, object(t, paths[path])["get"])
		got := []string{}
		for _, raw := range operation["parameters"].([]any) {
			parameter := object(t, raw)
			if parameter["in"] == "query" {
				got = append(got, parameter["name"].(string))
			}
		}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Fatalf("%s 没有保持完整筛选或混入分页参数：%v", path, got)
		}
	}
}

func TestTransferUploadsAndPreviewsHaveRealBoundaries(t *testing.T) {
	document := decoded(t)
	for _, name := range []string{"TaskImportUpload", "BackupRestoreUpload"} {
		definition := schema(t, document, name)
		file := object(t, object(t, definition["properties"])["file"])
		if file["type"] != "string" || file["format"] != "binary" || !slices.Contains(stringsIn(t, definition["required"]), "file") {
			t.Fatalf("%s 没有使用真实二进制文件输入", name)
		}
		if _, exists := file["maxLength"]; exists {
			t.Fatalf("%s 添加了未经冻结的文件上限", name)
		}
	}
	preview := object(t, schema(t, document, "BackupRestorePreview")["properties"])
	for _, forbidden := range []string{"path", "database_path", "master_key", "password", "owner", "generation"} {
		if _, exists := preview[forbidden]; exists {
			t.Fatalf("恢复预览公开了内部字段 %s", forbidden)
		}
	}
	report := schema(t, document, "TaskImportReport")
	description, _ := report["description"].(string)
	if !strings.Contains(description, "total=imported+skipped+failed+unprocessed") || !strings.Contains(description, "complete=false") {
		t.Fatal("导入报告未保留部分保存的计数与完成语义")
	}
	item := object(t, schema(t, document, "TaskImportItem")["properties"])
	for _, key := range []string{"corrections", "warnings", "fields"} {
		if object(t, item[key])["type"] != "array" {
			t.Fatalf("真实导入报告缺少 %s 数组", key)
		}
	}
	file := schema(t, document, "TaskExportFile")
	if object(t, object(t, file["properties"])["format"])["const"] != "kakatta.tasks" {
		t.Fatal("任务下载没有保持文件格式标识")
	}
	prices := object(t, schema(t, document, "TaskExportEntry")["properties"])
	for _, name := range []string{"min_price", "max_price", "budget_min", "budget_max"} {
		value := object(t, prices[name])
		if !allowsNull(value) || object(t, value["anyOf"].([]any)[0])["type"] != "number" {
			t.Fatalf("任务文件的元单位金额被误写成整数分：%s", name)
		}
	}
}
