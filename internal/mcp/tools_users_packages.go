package mcp

import (
	"context"
	"net/http"
	"strconv"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// registerUserPackageTools 用户 + 套餐 域(list/get/create/update/delete + 状态/限速/邮箱/备注 + 套餐绑定)。
func registerUserPackageTools(s *server.MCPServer, b *bridge) {
	// —— 用户:只读 ——
	s.AddTool(readTool("user_list", "列出所有用户(状态、套餐、配额等)。"),
		func(ctx context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return b.get(ctx, "/api/admin/users")
		})

	s.AddTool(readTool("user_detail", "查询指定用户的订阅/配额信息。",
		mcpgo.WithString("username", mcpgo.Required(), mcpgo.Description("用户名")),
	),
		func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			u, err := req.RequireString("username")
			if err != nil {
				return mcpgo.NewToolResultError("username 必填"), nil
			}
			return b.get(ctx, "/api/admin/users/"+pathEscape(u))
		})

	// —— 用户:写 ——
	s.AddTool(writeTool("user_create", "创建新用户。", false,
		mcpgo.WithString("username", mcpgo.Required(), mcpgo.Description("用户名")),
		mcpgo.WithString("password", mcpgo.Required(), mcpgo.Description("密码")),
		mcpgo.WithString("email", mcpgo.Description("邮箱")),
		mcpgo.WithString("nickname", mcpgo.Description("昵称")),
	),
		func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return b.send(ctx, http.MethodPost, "/api/admin/users/create", argsBody(req))
		})

	s.AddTool(writeTool("user_set_status", "启用/禁用用户。", false,
		mcpgo.WithString("username", mcpgo.Required(), mcpgo.Description("用户名")),
		mcpgo.WithBoolean("is_active", mcpgo.Required(), mcpgo.Description("true 启用 / false 禁用")),
	),
		func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return b.send(ctx, http.MethodPost, "/api/admin/users/status", argsBody(req))
		})

	s.AddTool(writeTool("user_set_limits", "设置用户限速与设备数(per-user override)。", false,
		mcpgo.WithString("username", mcpgo.Required(), mcpgo.Description("用户名")),
		mcpgo.WithNumber("speed_limit_override", mcpgo.Description("限速 Mbps(浮点),0 不限,留空走套餐默认")),
		mcpgo.WithNumber("device_limit_override", mcpgo.Description("设备数,0 不限,留空走套餐默认")),
	),
		func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return b.send(ctx, http.MethodPost, "/api/admin/users/limits", argsBody(req))
		})

	s.AddTool(writeTool("user_set_email", "更新用户邮箱(用于通知/标识)。覆盖式更新。", true,
		mcpgo.WithString("username", mcpgo.Required(), mcpgo.Description("用户名")),
		mcpgo.WithString("email", mcpgo.Required(), mcpgo.Description("新邮箱")),
	),
		func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return b.send(ctx, http.MethodPost, "/api/admin/users/update-email", argsBody(req))
		})

	s.AddTool(writeTool("user_set_remark", "更新用户备注(管理员视角的标签/备注,不影响用户本身)。", true,
		mcpgo.WithString("username", mcpgo.Required(), mcpgo.Description("用户名")),
		mcpgo.WithString("remark", mcpgo.Required(), mcpgo.Description("新备注")),
	),
		func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return b.send(ctx, http.MethodPost, "/api/admin/users/remark", argsBody(req))
		})

	s.AddTool(writeTool("user_delete", "删除用户(会解绑套餐、清理入站凭据)。", true,
		mcpgo.WithString("username", mcpgo.Required(), mcpgo.Description("用户名")),
		mcpgo.WithBoolean("confirm", mcpgo.Description("必须为 true 才执行")),
	),
		func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			u, err := req.RequireString("username")
			if err != nil {
				return mcpgo.NewToolResultError("username 必填"), nil
			}
			if msg, ok := confirmGate(req, "删除用户 "+u); !ok {
				return msg, nil
			}
			return b.send(ctx, http.MethodPost, "/api/admin/users/delete", map[string]any{"username": u})
		})

	// —— 套餐:只读 ——
	s.AddTool(readTool("package_list", "列出所有套餐(流量/周期/节点/限速/设备数)。"),
		func(ctx context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return b.get(ctx, "/api/admin/packages")
		})

	// —— 套餐:写 ——
	s.AddTool(writeTool("package_create", "创建套餐。nodes 为节点 ID 数组,traffic_mode 为 oneway/twoway。", false,
		mcpgo.WithString("name", mcpgo.Required(), mcpgo.Description("套餐名")),
		mcpgo.WithNumber("traffic_limit_gb", mcpgo.Required(), mcpgo.Description("流量上限 GB")),
		mcpgo.WithNumber("cycle_days", mcpgo.Required(), mcpgo.Description("周期天数")),
		mcpgo.WithArray("nodes", mcpgo.Description("节点 ID 数组")),
		mcpgo.WithString("traffic_mode", mcpgo.Description("oneway / twoway")),
		mcpgo.WithNumber("speed_limit_mbps", mcpgo.Description("限速 Mbps")),
		mcpgo.WithNumber("device_limit", mcpgo.Description("设备数")),
	),
		func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return b.send(ctx, http.MethodPost, "/api/admin/packages/create", argsBody(req))
		})

	s.AddTool(writeTool("package_update", "更新套餐(改名、改限额、改节点列表、改限速等)。覆盖式更新。", true,
		mcpgo.WithNumber("id", mcpgo.Required(), mcpgo.Description("套餐 ID")),
		mcpgo.WithString("name", mcpgo.Description("新套餐名")),
		mcpgo.WithNumber("traffic_limit_gb", mcpgo.Description("新流量上限 GB")),
		mcpgo.WithNumber("cycle_days", mcpgo.Description("新周期天数")),
		mcpgo.WithArray("nodes", mcpgo.Description("新节点 ID 数组")),
		mcpgo.WithString("traffic_mode", mcpgo.Description("oneway / twoway")),
		mcpgo.WithNumber("speed_limit_mbps", mcpgo.Description("新限速 Mbps")),
		mcpgo.WithNumber("device_limit", mcpgo.Description("新设备数")),
	),
		func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return b.send(ctx, http.MethodPost, "/api/admin/packages/update", argsBody(req))
		})

	s.AddTool(writeTool("package_delete", "删除套餐(会自动解绑所有该套餐的用户,影响半径较大)。", true,
		mcpgo.WithNumber("id", mcpgo.Required(), mcpgo.Description("套餐 ID")),
		mcpgo.WithBoolean("confirm", mcpgo.Description("必须为 true 才执行")),
	),
		func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			idF, err := req.RequireFloat("id")
			if err != nil {
				return mcpgo.NewToolResultError("id 必填"), nil
			}
			if msg, ok := confirmGate(req, "删除套餐"); !ok {
				return msg, nil
			}
			return b.send(ctx, http.MethodDelete, "/api/admin/packages/"+strconv.FormatInt(int64(idF), 10), nil)
		})

	s.AddTool(writeTool("package_assign", "把用户绑定到套餐。", false,
		mcpgo.WithString("username", mcpgo.Required(), mcpgo.Description("用户名")),
		mcpgo.WithNumber("package_id", mcpgo.Required(), mcpgo.Description("套餐 ID")),
		mcpgo.WithString("start_date", mcpgo.Description("开始日期 YYYY-MM-DD")),
		mcpgo.WithString("expire_date", mcpgo.Description("到期日期 YYYY-MM-DD")),
	),
		func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return b.send(ctx, http.MethodPost, "/api/admin/packages/assign", argsBody(req))
		})

	s.AddTool(writeTool("package_unassign", "解绑用户的套餐。", false,
		mcpgo.WithString("username", mcpgo.Required(), mcpgo.Description("用户名")),
	),
		func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return b.send(ctx, http.MethodPost, "/api/admin/packages/unassign", argsBody(req))
		})
}
