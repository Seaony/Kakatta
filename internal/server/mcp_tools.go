package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/Seaony/Kakatta/api"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type mcpAPIDocument struct {
	Components struct {
		Schemas map[string]map[string]any `json:"schemas"`
	} `json:"components"`
	Paths map[string]map[string]struct {
		Parameters []struct {
			Name   string         `json:"name"`
			In     string         `json:"in"`
			Schema map[string]any `json:"schema"`
		} `json:"parameters"`
	} `json:"paths"`
}

func registerMCPTools(sdk *mcp.Server, business *service.Service) error {
	var document mcpAPIDocument
	if err := json.Unmarshal(api.JSON(), &document); err != nil {
		return errors.New("工具接口描述无效")
	}
	tasks := document.parameters("/api/v1/tasks", "get", "query")
	findings := document.parameters("/api/v1/findings", "get", "query")
	id := document.parameters("/api/v1/tasks/{task_id}/run", "post", "path")
	id["required"] = []string{"task_id"}
	for _, tool := range []struct {
		name, description, output string
		input                     map[string]any
		readOnly, idempotent      bool
		invoke                    func(context.Context, json.RawMessage) (any, error)
	}{
		{"list_tasks", "列出任务，支持筛选和游标分页。", "PageTaskView", tasks, true, true, func(ctx context.Context, raw json.RawMessage) (any, error) {
			var query service.TaskQuery
			if err := json.Unmarshal(raw, &query); err != nil {
				return nil, invalidParameter("arguments")
			}
			return business.ListTasks(ctx, query)
		}},
		{"query_findings", "查询商品判断结果，支持筛选、排序和游标分页。", "PageFindingView", findings, true, true, func(ctx context.Context, raw json.RawMessage) (any, error) {
			var query service.FindingQuery
			if err := json.Unmarshal(raw, &query); err != nil {
				return nil, invalidParameter("arguments")
			}
			return business.ListFindings(ctx, query)
		}},
		{"create_task", "新建任务；省略的字段采用当前设置，返回保存结果和风险提示。请勿自动重试。", "TaskSaveResult", document.Components.Schemas["TaskCreate"], false, false, func(ctx context.Context, raw json.RawMessage) (any, error) {
			var patch service.TaskPatch
			if err := json.Unmarshal(raw, &patch); err != nil {
				return nil, invalidParameter("arguments")
			}
			return business.CreateTask(ctx, patch)
		}},
		{"pause_task", "暂停任务的后续调度。", "TaskSaveResult", id, false, true, func(ctx context.Context, raw json.RawMessage) (any, error) {
			return mcpSetTaskEnabled(ctx, business, raw, false)
		}},
		{"resume_task", "恢复任务的后续调度，仍受账号状态、额度和前置检查约束。", "TaskSaveResult", id, false, true, func(ctx context.Context, raw json.RawMessage) (any, error) {
			return mcpSetTaskEnabled(ctx, business, raw, true)
		}},
		{"run_task_now", "请求立即运行任务，返回接纳或排队状态，不等待抓取完成。仍检查额度和账号状态，请勿自动重试。", "RunSubmission", id, false, false, func(ctx context.Context, raw json.RawMessage) (any, error) {
			var args struct {
				TaskID int64 `json:"task_id"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return nil, invalidParameter("task_id")
			}
			return business.RunNow(ctx, args.TaskID)
		}},
	} {
		input, err := document.schema(tool.input)
		if err != nil {
			return err
		}
		output, err := document.schema(map[string]any{"oneOf": []any{
			map[string]any{"$ref": "#/components/schemas/" + tool.output},
			map[string]any{"$ref": "#/components/schemas/ErrorEnvelope"},
		}})
		if err != nil {
			return err
		}
		open := !tool.readOnly && tool.name != "pause_task"
		mcp.AddTool[json.RawMessage, any](sdk, &mcp.Tool{
			Name: tool.name, Description: tool.description, InputSchema: input, OutputSchema: output,
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: tool.readOnly, IdempotentHint: tool.idempotent, OpenWorldHint: &open},
		}, func(ctx context.Context, request *mcp.CallToolRequest, raw json.RawMessage) (*mcp.CallToolResult, any, error) {
			value, err := tool.invoke(ctx, raw)
			if err != nil {
				return &mcp.CallToolResult{IsError: true}, mcpErrorEnvelope(ctx, err), nil
			}
			return nil, value, nil
		})
	}
	return nil
}

func mcpSetTaskEnabled(ctx context.Context, business *service.Service, raw json.RawMessage, enabled bool) (any, error) {
	var args struct {
		TaskID int64 `json:"task_id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, invalidParameter("task_id")
	}
	value := json.RawMessage("false")
	if enabled {
		value = json.RawMessage("true")
	}
	return business.UpdateTask(ctx, args.TaskID, service.TaskPatch{"enabled": value})
}

func (d mcpAPIDocument) parameters(path, method, location string) map[string]any {
	properties := map[string]any{}
	for _, parameter := range d.Paths[path][method].Parameters {
		if parameter.In == location {
			properties[parameter.Name] = parameter.Schema
		}
	}
	return map[string]any{"type": "object", "additionalProperties": false, "properties": properties}
}

// 只搬运接口描述中的引用；默认值必须留给业务服务按当前设置计算。
func (d mcpAPIDocument) schema(root map[string]any) (map[string]any, error) {
	if root == nil {
		return nil, errors.New("工具接口描述缺少结构")
	}
	definitions := map[string]any{}
	var copySchema func(any) (any, error)
	copySchema = func(value any) (any, error) {
		switch value := value.(type) {
		case map[string]any:
			result := make(map[string]any, len(value))
			for key, item := range value {
				if key == "default" {
					continue
				}
				if key == "$ref" {
					ref, ok := item.(string)
					if !ok || !strings.HasPrefix(ref, "#/components/schemas/") {
						return nil, errors.New("工具接口描述包含无效引用")
					}
					name := strings.TrimPrefix(ref, "#/components/schemas/")
					if _, exists := definitions[name]; !exists {
						definition, exists := d.Components.Schemas[name]
						if !exists {
							return nil, errors.New("工具接口描述缺少引用")
						}
						definitions[name] = nil
						cloned, err := copySchema(definition)
						if err != nil {
							return nil, err
						}
						definitions[name] = cloned
					}
					result[key] = "#/$defs/" + name
					continue
				}
				cloned, err := copySchema(item)
				if err != nil {
					return nil, err
				}
				result[key] = cloned
			}
			return result, nil
		case []any:
			result := make([]any, len(value))
			for i, item := range value {
				cloned, err := copySchema(item)
				if err != nil {
					return nil, err
				}
				result[i] = cloned
			}
			return result, nil
		default:
			return value, nil
		}
	}
	cloned, err := copySchema(root)
	if err != nil {
		return nil, err
	}
	result := cloned.(map[string]any)
	if len(definitions) != 0 {
		result["$defs"] = definitions
	}
	return result, nil
}
