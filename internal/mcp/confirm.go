package mcp

import (
	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

// readTool 构造只读工具:统一打上 readOnly / 非 destructive 注解,便于 agent 判断安全性。
func readTool(name, desc string, extra ...mcpgo.ToolOption) mcpgo.Tool {
	opts := []mcpgo.ToolOption{
		mcpgo.WithDescription(desc),
		mcpgo.WithReadOnlyHintAnnotation(true),
		mcpgo.WithDestructiveHintAnnotation(false),
		mcpgo.WithOpenWorldHintAnnotation(false),
	}
	opts = append(opts, extra...)
	return mcpgo.NewTool(name, opts...)
}

// writeTool 构造写工具(标注非只读)。destructive=true 时标注 destructiveHint。
func writeTool(name, desc string, destructive bool, extra ...mcpgo.ToolOption) mcpgo.Tool {
	opts := []mcpgo.ToolOption{
		mcpgo.WithDescription(desc),
		mcpgo.WithReadOnlyHintAnnotation(false),
		mcpgo.WithDestructiveHintAnnotation(destructive),
	}
	opts = append(opts, extra...)
	return mcpgo.NewTool(name, opts...)
}

// argsBody 取工具入参作为请求体,去掉控制字段(confirm)与指定的路径参数。
func argsBody(req mcpgo.CallToolRequest, omit ...string) map[string]any {
	src := req.GetArguments()
	out := make(map[string]any, len(src))
	for k, v := range src {
		skip := k == "confirm"
		for _, o := range omit {
			if k == o {
				skip = true
			}
		}
		if !skip {
			out[k] = v
		}
	}
	return out
}

// confirmGate 高危写操作的二次确认:未传 confirm=true 时返回提示、不执行。
func confirmGate(req mcpgo.CallToolRequest, action string) (*mcpgo.CallToolResult, bool) {
	if c, _ := req.GetArguments()["confirm"].(bool); c {
		return nil, true
	}
	return mcpgo.NewToolResultError("⚠️ 「" + action + "」是高危操作,确认后请在参数中加 confirm=true 再次调用以执行。"), false
}
