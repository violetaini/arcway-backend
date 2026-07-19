package mcp

import (
	"context"
	"net/http"
	"strconv"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// registerSubscribeTools 订阅文件 + 流量统计域。
// 订阅文件:list / get / create / update / delete + 临时订阅 create。
// 流量:summary / user_detail / server_detail / snapshots。
func registerSubscribeTools(s *server.MCPServer, b *bridge) {
	// —— 订阅文件 ——
	s.AddTool(readTool("subscribe_file_list", "列出所有订阅文件(含短链、绑定模板等)。"),
		func(ctx context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return b.get(ctx, "/api/admin/subscribe-files")
		})

	s.AddTool(readTool("subscribe_file_get", "查询单个订阅文件详情(短码、绑定模板、最近一次访问时间等)。",
		mcpgo.WithNumber("id", mcpgo.Required(), mcpgo.Description("订阅文件 ID")),
	),
		func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			idF, err := req.RequireFloat("id")
			if err != nil {
				return mcpgo.NewToolResultError("id 必填"), nil
			}
			return b.get(ctx, "/api/admin/subscribe-files/"+strconv.FormatInt(int64(idF), 10))
		})

	s.AddTool(writeTool("subscribe_file_create", "为用户创建订阅文件(主控生成订阅链接给用户使用)。", false,
		mcpgo.WithString("username", mcpgo.Required(), mcpgo.Description("用户名")),
		mcpgo.WithString("filename", mcpgo.Description("自定义文件名(可选,默认按用户名)")),
		mcpgo.WithNumber("template_id", mcpgo.Description("绑定的 V3 模板 ID(可选)")),
		mcpgo.WithString("template_filename", mcpgo.Description("绑定的 V3 模板文件名(可选)")),
		mcpgo.WithString("custom_short_code", mcpgo.Description("自定义短码(可选,字母数字 _ -,长度 2-16)")),
		mcpgo.WithArray("selected_tags", mcpgo.Description("筛选标签(可选,V3 模板下生效)")),
		mcpgo.WithString("remark", mcpgo.Description("备注(可选)")),
	),
		func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return b.send(ctx, http.MethodPost, "/api/admin/subscribe-files", argsBody(req))
		})

	s.AddTool(writeTool("subscribe_file_update", "更新订阅文件(改备注、改绑模板、改筛选标签等)。覆盖式更新。", true,
		mcpgo.WithNumber("id", mcpgo.Required(), mcpgo.Description("订阅文件 ID")),
		mcpgo.WithString("filename", mcpgo.Description("新文件名")),
		mcpgo.WithNumber("template_id", mcpgo.Description("新模板 ID")),
		mcpgo.WithString("template_filename", mcpgo.Description("新模板文件名")),
		mcpgo.WithString("custom_short_code", mcpgo.Description("新短码")),
		mcpgo.WithArray("selected_tags", mcpgo.Description("新筛选标签")),
		mcpgo.WithString("remark", mcpgo.Description("新备注")),
	),
		func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			idF, err := req.RequireFloat("id")
			if err != nil {
				return mcpgo.NewToolResultError("id 必填"), nil
			}
			return b.send(ctx, http.MethodPut, "/api/admin/subscribe-files/"+strconv.FormatInt(int64(idF), 10), argsBody(req, "id"))
		})

	s.AddTool(writeTool("subscribe_file_delete", "删除订阅文件(用户将拿不到此订阅链接)。", true,
		mcpgo.WithNumber("id", mcpgo.Required(), mcpgo.Description("订阅文件 ID")),
		mcpgo.WithBoolean("confirm", mcpgo.Description("必须为 true 才执行")),
	),
		func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			idF, err := req.RequireFloat("id")
			if err != nil {
				return mcpgo.NewToolResultError("id 必填"), nil
			}
			if msg, ok := confirmGate(req, "删除订阅文件"); !ok {
				return msg, nil
			}
			return b.send(ctx, http.MethodDelete, "/api/admin/subscribe-files/"+strconv.FormatInt(int64(idF), 10), nil)
		})

	s.AddTool(writeTool("temp_subscription_create", "为节点生成临时订阅链接(限时/限流)。", false,
		mcpgo.WithNumber("expire_hours", mcpgo.Description("有效小时数")),
		mcpgo.WithNumber("traffic_limit_gb", mcpgo.Description("流量上限 GB")),
	),
		func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return b.send(ctx, http.MethodPost, "/api/admin/temp-subscription", argsBody(req))
		})

	// —— 流量统计 ——
	s.AddTool(readTool("traffic_summary", "获取流量汇总概览(跨服务器聚合的已用/限额等)。"),
		func(ctx context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return b.get(ctx, "/api/traffic/summary/aggregated")
		})

	s.AddTool(readTool("traffic_user_detail", "查询指定用户的详细流量统计。",
		mcpgo.WithString("username", mcpgo.Required(), mcpgo.Description("用户名")),
	),
		func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			u, err := req.RequireString("username")
			if err != nil {
				return mcpgo.NewToolResultError("username 必填"), nil
			}
			return b.get(ctx, "/api/admin/traffic/users/"+pathEscape(u))
		})

	s.AddTool(readTool("traffic_server_detail", "查询某远程服务器的流量明细(按 inbound tag / 出站 tag 拆分)。",
		mcpgo.WithNumber("server_id", mcpgo.Required(), mcpgo.Description("服务器 ID")),
	),
		func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			idF, err := req.RequireFloat("server_id")
			if err != nil {
				return mcpgo.NewToolResultError("server_id 必填"), nil
			}
			return b.get(ctx, "/api/admin/traffic/servers/"+strconv.FormatInt(int64(idF), 10))
		})

	s.AddTool(readTool("traffic_snapshots", "查询 xray 流量历史快照(按时段聚合的服务器/节点流量曲线)。",
		mcpgo.WithNumber("server_id", mcpgo.Description("过滤指定服务器 ID(可选)")),
		mcpgo.WithNumber("hours", mcpgo.Description("回溯多少小时(可选)")),
	),
		func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return b.getWithQuery(ctx, "/api/admin/xray-snapshots", argsBody(req))
		})
}
