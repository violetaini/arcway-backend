package mcp

import (
	"context"
	"net/http"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// registerNodeTools 节点域 MCP 工具(list / get / create / update / delete / batch_delete / speedtest / tcping / tunnel_list)。
func registerNodeTools(s *server.MCPServer, b *bridge) {
	// 只读
	s.AddTool(readTool("node_list", "列出所有代理节点(协议、名称、服务器地址、入站标签等)。"),
		func(ctx context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return b.get(ctx, "/api/admin/nodes")
		})

	s.AddTool(readTool("node_get", "查询单个节点详情(配置、所属服务器、节点类型、路由出站标签等)。",
		mcpgo.WithString("id", mcpgo.Required(), mcpgo.Description("节点 ID")),
	),
		func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			id, err := req.RequireString("id")
			if err != nil {
				return mcpgo.NewToolResultError("id 必填"), nil
			}
			return b.get(ctx, "/api/admin/nodes/"+pathEscape(id))
		})

	s.AddTool(readTool("tunnel_list", "列出所有 tunnel(dokodemo 转发)入站,跨所有远程/分享服务器。"),
		func(ctx context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return b.get(ctx, "/api/admin/tunnels")
		})

	s.AddTool(readTool("node_tcping", "TCP 连通性探测(从主控发起对目标 host:port 的 TCP 握手)。常用于诊断节点失联。",
		mcpgo.WithString("host", mcpgo.Required(), mcpgo.Description("目标主机/IP")),
		mcpgo.WithNumber("port", mcpgo.Required(), mcpgo.Description("目标端口")),
	),
		func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return b.send(ctx, http.MethodPost, "/api/admin/tcping", argsBody(req))
		})

	// 写
	s.AddTool(writeTool("node_create", "新建一个节点。手动节点请在管理 UI 配置;此工具主要给已知 server+protocol+inbound_tag 时由 agent 调用。", false,
		mcpgo.WithString("name", mcpgo.Required(), mcpgo.Description("节点名称")),
		mcpgo.WithNumber("server_id", mcpgo.Required(), mcpgo.Description("所属远程服务器 ID")),
		mcpgo.WithString("inbound_tag", mcpgo.Required(), mcpgo.Description("入站 tag")),
		mcpgo.WithString("protocol", mcpgo.Required(), mcpgo.Description("协议:vless/vmess/trojan/shadowsocks/hysteria2/anytls")),
		mcpgo.WithObject("clash_config", mcpgo.Description("Clash 节点配置 JSON(可选,缺省由主控生成)")),
		mcpgo.WithString("tag", mcpgo.Description("分组 tag,默认 手动输入")),
	),
		func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return b.send(ctx, http.MethodPost, "/api/admin/nodes", argsBody(req))
		})

	s.AddTool(writeTool("node_update", "更新节点信息(改名、改 tag、改归属服务器、改入站标签等)。覆盖式更新。", true,
		mcpgo.WithString("id", mcpgo.Required(), mcpgo.Description("节点 ID")),
		mcpgo.WithString("name", mcpgo.Description("新名称")),
		mcpgo.WithString("tag", mcpgo.Description("新分组 tag")),
		mcpgo.WithString("inbound_tag", mcpgo.Description("新入站 tag")),
		mcpgo.WithNumber("server_id", mcpgo.Description("新服务器 ID")),
		mcpgo.WithObject("clash_config", mcpgo.Description("新 Clash 配置")),
	),
		func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			id, err := req.RequireString("id")
			if err != nil {
				return mcpgo.NewToolResultError("id 必填"), nil
			}
			return b.send(ctx, http.MethodPut, "/api/admin/nodes/"+pathEscape(id), argsBody(req, "id"))
		})

	s.AddTool(writeTool("node_delete", "删除指定节点(会同步更新所有订阅)。", true,
		mcpgo.WithString("id", mcpgo.Required(), mcpgo.Description("节点 ID")),
		mcpgo.WithBoolean("confirm", mcpgo.Description("必须为 true 才执行")),
	),
		func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			id, err := req.RequireString("id")
			if err != nil {
				return mcpgo.NewToolResultError("id 必填"), nil
			}
			if msg, ok := confirmGate(req, "删除节点 "+id); !ok {
				return msg, nil
			}
			return b.send(ctx, http.MethodDelete, "/api/admin/nodes/"+pathEscape(id), nil)
		})

	s.AddTool(writeTool("node_batch_delete", "批量删除节点(半径较大,需 confirm=true)。", true,
		mcpgo.WithArray("ids", mcpgo.Required(), mcpgo.Description("节点 ID 数组")),
		mcpgo.WithBoolean("confirm", mcpgo.Description("必须为 true 才执行")),
	),
		func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			if msg, ok := confirmGate(req, "批量删除节点"); !ok {
				return msg, nil
			}
			return b.send(ctx, http.MethodPost, "/api/admin/nodes/batch-delete", argsBody(req))
		})

	s.AddTool(writeTool("node_speedtest", "对指定节点发起测速(异步,结果稍后可经 speedtest 结果查询)。", false,
		mcpgo.WithString("node_id", mcpgo.Required(), mcpgo.Description("节点 ID")),
		mcpgo.WithNumber("tester_id", mcpgo.Description("家用测速端 ID;省略则用主控本机")),
	),
		func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return b.send(ctx, http.MethodPost, "/api/admin/speedtest/run", argsBody(req))
		})
}
