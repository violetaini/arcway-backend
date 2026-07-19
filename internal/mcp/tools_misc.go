package mcp

import (
	"context"
	"net/http"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// registerMiscTools 杂项工具(Xray 辅助:生成 x25519 / 列协议组合示例)。
func registerMiscTools(s *server.MCPServer, b *bridge) {
	s.AddTool(writeTool("xray_generate_x25519", "生成一对 reality x25519 公私钥(用于创建 reality 入站)。idempotent,可重复调用。", false),
		func(ctx context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return b.send(ctx, http.MethodPost, "/api/admin/xray/generate-x25519", nil)
		})

	s.AddTool(readTool("xray_examples", "返回主控内置的 Xray 协议组合参考(各种 protocol × transport × security 的可选项)。"),
		func(ctx context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return b.get(ctx, "/api/admin/xray-examples")
		})
}
