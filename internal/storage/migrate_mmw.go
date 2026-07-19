package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// MmwImportReport 每张表迁移结果。
type MmwImportReport struct {
	Users           int      `json:"users"`
	UserTokens      int      `json:"user_tokens"`
	Nodes           int      `json:"nodes"`
	SubscribeFiles  int      `json:"subscribe_files"`
	UserSubs        int      `json:"user_subscriptions"`
	UserSettings    int      `json:"user_settings"`
	Templates       int      `json:"templates"`
	CustomRules     int      `json:"custom_rules"`
	OverrideScripts int      `json:"override_scripts"`
	ExtSubs         int      `json:"external_subscriptions"`
	Warnings        []string `json:"warnings,omitempty"`
}

// ImportFromMmw 从一个 mmw.db 文件读取核心数据,合并写入当前 mmwx 数据库。
//
// 策略:
//   - 用 ATTACH DATABASE 在同一连接上挂载 mmw.db 为 src
//   - 每张表用 INSERT OR IGNORE INTO main.X(cols...) SELECT cols... FROM src.X
//     列名只取 mmw 表已有的列(两边交集);mmwx 多出来的列走默认值
//   - 整体在事务里执行,失败回滚
//   - 已存在(同 PRIMARY KEY / UNIQUE)的行用 IGNORE 跳过 — 不覆盖现有 mmwx 数据
//
// 调用方应在调用前确认目标 mmwx 数据库是空的(否则 IGNORE 跳过会让用户困惑)。
func (r *TrafficRepository) ImportFromMmw(ctx context.Context, mmwDBPath string) (*MmwImportReport, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("traffic repository not initialized")
	}
	if _, err := os.Stat(mmwDBPath); err != nil {
		return nil, fmt.Errorf("打开 mmw.db 失败: %w", err)
	}

	conn, err := r.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("get conn: %w", err)
	}
	defer conn.Close()

	// 用 quoted single quotes 防路径注入(虽然来源是 admin,但保守一些)
	safePath := strings.ReplaceAll(mmwDBPath, "'", "''")
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("ATTACH DATABASE '%s' AS src", safePath)); err != nil {
		return nil, fmt.Errorf("ATTACH mmw.db: %w", err)
	}
	defer conn.ExecContext(context.Background(), "DETACH DATABASE src")

	report := &MmwImportReport{}

	// 每张表的 (target_cols, src_cols, into_table_alias) 定义。
	// src_cols 列在 mmw 上必须全部存在(我们已经从样本 mmw.db 验证过 schema);
	// target_cols 列在 mmwx 上必须全部存在。两边一致时直接用同一个列表。
	// 单独写明列名而不是 `SELECT *`,这样 mmwx 后续加新列不会让 INSERT 报错。
	type tableSpec struct {
		dest   string
		target *int     // report 字段指针
		cols   []string // 两边都有的列
	}
	specs := []tableSpec{
		{
			dest:   "users",
			target: &report.Users,
			cols: []string{
				"username", "password_hash", "email", "nickname", "avatar_url",
				"role", "is_active", "remark",
				"totp_secret", "totp_enabled", "recovery_codes",
				"created_at", "updated_at",
			},
		},
		{
			// 用户短码 & 登录 token。username 是 PK,所以 INSERT OR IGNORE 会保留
			// mmwx 已有的 admin token,只为 mmw 独有的用户带过来 user_short_code /
			// custom_user_short_code,让分发出去的订阅短链(/x/<code>)继续可用。
			dest:   "user_tokens",
			target: &report.UserTokens,
			cols: []string{
				"username", "token", "user_short_code",
				"custom_user_short_code", "updated_at",
			},
		},
		{
			dest:   "nodes",
			target: &report.Nodes,
			cols: []string{
				"id", "username", "raw_url", "node_name", "protocol",
				"parsed_config", "clash_config", "enabled", "tag",
				"original_server", "chain_proxy_node_id",
				"created_at", "updated_at",
			},
		},
		{
			dest:   "subscribe_files",
			target: &report.SubscribeFiles,
			cols: []string{
				"id", "name", "description", "url", "type", "filename",
				"file_short_code", "custom_short_code", "auto_sync_custom_rules",
				"expire_at", "template_filename", "selected_tags",
				"raw_output", "sort_order", "traffic_limit",
				"stats_server_ids", "selected_custom_rule_ids", "selected_override_script_ids",
				"created_at", "updated_at",
			},
		},
		{
			dest:   "user_subscriptions",
			target: &report.UserSubs,
			cols:   []string{"username", "subscription_id", "created_at"},
		},
		{
			dest:   "user_settings",
			target: &report.UserSettings,
			cols: []string{
				"username", "force_sync_external", "match_rule", "sync_scope",
				"keep_node_name", "cache_expire_minutes", "sync_traffic",
				"node_name_filter", "append_sub_info", "enable_short_link",
				"use_new_template_system", "enable_proxy_provider",
				"created_at", "updated_at",
			},
		},
		{
			dest:   "templates",
			target: &report.Templates,
			cols: []string{
				"id", "name", "category", "template_url", "rule_source",
				"use_proxy", "enable_include_all", "created_at", "updated_at",
			},
		},
		{
			dest:   "custom_rules",
			target: &report.CustomRules,
			cols: []string{
				"id", "name", "type", "mode", "content", "enabled",
				"created_at", "updated_at",
			},
		},
		{
			dest:   "override_scripts",
			target: &report.OverrideScripts,
			cols: []string{
				"id", "username", "name", "hook", "content", "enabled",
				"sort_order", "created_at", "updated_at",
			},
		},
		{
			dest:   "external_subscriptions",
			target: &report.ExtSubs,
			cols: []string{
				"id", "username", "name", "url", "node_count", "last_sync_at",
				"upload", "download", "total", "expire", "user_agent", "traffic_mode",
				"created_at", "updated_at",
			},
		},
	}

	// 用单事务整体跑;任何一张表失败 → rollback
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	rollback := func() { _ = tx.Rollback() }

	for _, spec := range specs {
		// 检查 src 是否真有这些列;少的列对应 INSERT 给默认值(NULL/0/...)。
		availCols, err := txTableColumns(ctx, tx, "src", spec.dest)
		if err != nil {
			report.Warnings = append(report.Warnings, fmt.Sprintf("跳过 %s: %v", spec.dest, err))
			continue
		}
		availSet := make(map[string]bool, len(availCols))
		for _, c := range availCols {
			availSet[c] = true
		}
		var useCols []string
		var missing []string
		for _, c := range spec.cols {
			if availSet[c] {
				useCols = append(useCols, c)
			} else {
				missing = append(missing, c)
			}
		}
		if len(useCols) == 0 {
			report.Warnings = append(report.Warnings, fmt.Sprintf("跳过 %s: mmw 库无任何兼容列", spec.dest))
			continue
		}
		if len(missing) > 0 {
			report.Warnings = append(report.Warnings, fmt.Sprintf("%s: mmw 库缺少列 %v,这些列将取 mmwx 默认值", spec.dest, missing))
		}

		colList := joinCols(useCols)
		stmt := fmt.Sprintf(
			"INSERT OR IGNORE INTO main.%s (%s) SELECT %s FROM src.%s",
			spec.dest, colList, colList, spec.dest,
		)
		res, err := tx.ExecContext(ctx, stmt)
		if err != nil {
			rollback()
			return nil, fmt.Errorf("import %s: %w", spec.dest, err)
		}
		if n, err := res.RowsAffected(); err == nil {
			*spec.target = int(n)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return report, nil
}

// txTableColumns 在事务上下文中用 PRAGMA table_info 取某张表的列名。
// schema 是 attach 别名(如 "src" 或 "main")。
func txTableColumns(ctx context.Context, tx *sql.Tx, schema, table string) ([]string, error) {
	safeSchema := strings.ReplaceAll(schema, "'", "''")
	safeTable := strings.ReplaceAll(table, "'", "''")
	query := fmt.Sprintf("PRAGMA %s.table_info('%s')", safeSchema, safeTable)
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s.%s 表不存在或无列", schema, table)
	}
	return out, nil
}

// joinCols 把列名列表用逗号拼成 SQL column list 片段。
func joinCols(cols []string) string {
	return strings.Join(cols, ", ")
}

// DistinctNodeServer 表示一组 clash 节点指向同一个服务器地址的汇总。
type DistinctNodeServer struct {
	Address          string
	NodeCount        int
	Ports            []int
	Protocols        []string
	ExistingServer   bool // mmwx 已有同名 remote_server 或同 IP / 同域名
	ExistingServerID int64
	SampleNodeName   string
}

// ListDistinctNodeServers 遍历所有"外部节点"(无 original_server / inbound_tag 关联)
// 的 clash_config,提取去重的 server 地址 + 端口 + 协议 + 节点数。
// 用于迁移自检页显示"待添加为远程服务器"的清单。
func (r *TrafficRepository) ListDistinctNodeServers(ctx context.Context) ([]DistinctNodeServer, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("traffic repository not initialized")
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, node_name, protocol, clash_config
		FROM nodes
		WHERE (original_server IS NULL OR original_server = '')
		  AND (inbound_tag IS NULL OR inbound_tag = '')
		  AND COALESCE(node_type, 'physical') = 'physical'
	`)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	defer rows.Close()

	type bucket struct {
		count      int
		ports      map[int]struct{}
		protocols  map[string]struct{}
		sampleName string
	}
	groups := map[string]*bucket{}
	for rows.Next() {
		var id int64
		var name, protocol, clashJSON string
		if err := rows.Scan(&id, &name, &protocol, &clashJSON); err != nil {
			return nil, err
		}
		addr, port := extractServerPort(clashJSON)
		if addr == "" {
			continue
		}
		g, ok := groups[addr]
		if !ok {
			g = &bucket{ports: map[int]struct{}{}, protocols: map[string]struct{}{}}
			groups[addr] = g
		}
		g.count++
		if port > 0 {
			g.ports[port] = struct{}{}
		}
		if protocol != "" {
			g.protocols[protocol] = struct{}{}
		}
		if g.sampleName == "" {
			g.sampleName = name
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// 标注哪些地址已经在 remote_servers 里存在(按 ip_address / domain 模糊比对)
	existingByAddr := map[string]int64{}
	exrows, err := r.db.QueryContext(ctx,
		`SELECT id, COALESCE(ip_address,''), COALESCE(domain,''), COALESCE(pull_address,'') FROM remote_servers`)
	if err == nil {
		defer exrows.Close()
		for exrows.Next() {
			var sid int64
			var ip, dom, pull string
			if err := exrows.Scan(&sid, &ip, &dom, &pull); err != nil {
				continue
			}
			for _, a := range []string{ip, dom, pull} {
				if a != "" {
					existingByAddr[a] = sid
				}
			}
		}
	}

	out := make([]DistinctNodeServer, 0, len(groups))
	for addr, g := range groups {
		entry := DistinctNodeServer{
			Address:        addr,
			NodeCount:      g.count,
			SampleNodeName: g.sampleName,
		}
		for p := range g.ports {
			entry.Ports = append(entry.Ports, p)
		}
		for p := range g.protocols {
			entry.Protocols = append(entry.Protocols, p)
		}
		if sid, ok := existingByAddr[addr]; ok {
			entry.ExistingServer = true
			entry.ExistingServerID = sid
		}
		out = append(out, entry)
	}
	return out, nil
}

// extractServerPort 从 clash_config JSON 字符串里取 server 和 port。
// 对各种协议 (vless/vmess/trojan/ss/...) 通用,因为 clash 格式顶层一定有 server 和 port。
func extractServerPort(clashJSON string) (string, int) {
	type c struct {
		Server string `json:"server"`
		Port   int    `json:"port"`
	}
	var v c
	if clashJSON == "" {
		return "", 0
	}
	_ = jsonUnmarshalSafe(clashJSON, &v)
	return strings.TrimSpace(v.Server), v.Port
}

// jsonUnmarshalSafe 包装 json.Unmarshal:解析失败时调用方拿到的是零值结构体。
func jsonUnmarshalSafe(s string, out any) error {
	return json.Unmarshal([]byte(s), out)
}

// AssignOwnershipForMmwImported 把 mmw 导入数据中 created_by 字段为空的行
// (subscribe_files / templates) 赋值给给定的管理员用户名。
// mmw 数据库没有 created_by 概念,导入时落到 mmwx 是空字符串,会导致这些行
// 在「我的订阅 / 我的模板」列表里没有归属。把它们绑给系统第一个 admin,
// 之后管理员能在 UI 上看到并管理。
//
// 注意:只更新 created_by = ” 的行(导入前已有数据 / 普通用户创建的不动)。
func (r *TrafficRepository) AssignOwnershipForMmwImported(ctx context.Context, adminUsername string) error {
	if r == nil || r.db == nil {
		return errors.New("traffic repository not initialized")
	}
	if strings.TrimSpace(adminUsername) == "" {
		return errors.New("admin username 必填")
	}
	// 只更新 created_by 为空的行,不覆盖已有归属
	if _, err := r.db.ExecContext(ctx,
		`UPDATE subscribe_files SET created_by = ? WHERE created_by IS NULL OR created_by = ''`,
		adminUsername,
	); err != nil {
		return fmt.Errorf("update subscribe_files.created_by: %w", err)
	}
	if _, err := r.db.ExecContext(ctx,
		`UPDATE templates SET created_by = ? WHERE created_by IS NULL OR created_by = ''`,
		adminUsername,
	); err != nil {
		return fmt.Errorf("update templates.created_by: %w", err)
	}
	return nil
}

// DemoteExtraAdmins 把除"第一个用户"外的所有管理员降级为普通用户。
// 妙妙屋迁移用 INSERT OR IGNORE 把 mmw 的用户(含它自己的 admin)原样带入,
// 会让 mmwx 出现多个 role='admin' 的管理员。这里只保留本实例最早创建的那个用户
// (按 rowid 升序取第一个 —— 导入的用户带原始 created_at,可能早于本机 admin,
// 只有 rowid/插入顺序能可靠定位本实例最初创建的管理员),其余 admin 一律改普通用户。
// 返回被降级的用户数。
func (r *TrafficRepository) DemoteExtraAdmins(ctx context.Context) (int, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("traffic repository not initialized")
	}
	res, err := r.db.ExecContext(ctx,
		`UPDATE users SET role = ?, updated_at = CURRENT_TIMESTAMP
		   WHERE role = ?
		     AND rowid <> (SELECT rowid FROM users ORDER BY rowid ASC LIMIT 1)`,
		RoleUser, RoleAdmin,
	)
	if err != nil {
		return 0, fmt.Errorf("demote extra admins: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
