package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"miaomiaowux/internal/child"
	"miaomiaowux/internal/storage"
	"miaomiaowux/internal/version"
)

// ChildAPIHandler 处理来自主服务器的 API 请求（对于pull模式）
type ChildAPIHandler struct {
	client      *child.Client
	configToken string // 用于身份验证的令牌
}

// 创建一个新的子 API 处理程序
func NewChildAPIHandler(client *child.Client, configToken string) *ChildAPIHandler {
	return &ChildAPIHandler{
		client:      client,
		configToken: configToken,
	}
}

// 处理流量数据的 HTTP 请求
func (h *ChildAPIHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 只允许 GET 方法
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 验证请求
	if !h.authenticate(r) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "Unauthorized",
		})
		return
	}

	// 获取流量统计
	stats, err := h.client.GetStats()
	if err != nil {
		log.Printf("[Child API] Failed to get stats: %v", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "Failed to collect stats",
		})
		return
	}

	// 返回统计数据
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"stats":   stats,
	})
}

// 处理速度数据的 HTTP 请求
func (h *ChildAPIHandler) ServeSpeedHTTP(w http.ResponseWriter, r *http.Request) {
	// 只允许 GET 方法
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 验证请求
	if !h.authenticate(r) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "Unauthorized",
		})
		return
	}

	// 获取速度数据
	uploadSpeed, downloadSpeed := h.client.GetSpeed()

	// 返回速度数据
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":        true,
		"upload_speed":   uploadSpeed,
		"download_speed": downloadSpeed,
	})
}

// 验证检查请求是否被授权
func (h *ChildAPIHandler) authenticate(r *http.Request) bool {
	if h.configToken == "" {
		// 如果未配置令牌，则允许所有请求（不建议用于生产）
		return true
	}

	// 检查授权标头
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return false
	}

	// 支持“Bearer <token>”格式
	if strings.HasPrefix(auth, "Bearer ") {
		token := strings.TrimPrefix(auth, "Bearer ")
		return token == h.configToken
	}

	// 还支持普通令牌
	return auth == h.configToken
}

// RemoteHeartbeatRequest代表来自远程服务器的心跳请求
type RemoteHeartbeatRequest struct {
	BootTime     int64 `json:"boot_time"`      // MMWX进程启动时间（Unix时间戳）
	XrayBootTime int64 `json:"xray_boot_time"` // Xray 进程开始时间（Unix 时间戳）
	XrayPID      int   `json:"xray_pid"`       // 当前 X 射线进程 ID
	ListenPort   int   `json:"listen_port"`    // 代理HTTP监听端口
	LocalTime    int64 `json:"local_time"`     // agent 本地 Unix 时间戳，用于时钟偏差检测
	// PublicIPv4/v6 由 agent 端 ipProbeLoop 缓存后随心跳上报(WS auth/heartbeat 同款字段)。
	// master 优先用上报值写 db,fallback 才用 r.RemoteAddr 并强校验类型(避免 v6 写 v4 字段)。
	// 老 agent 不发 → 字段为空 → 走 fallback 路径,行为退化为现状。
	PublicIPv4 string `json:"public_ipv4,omitempty"`
	PublicIPv6 string `json:"public_ipv6,omitempty"`
}

// RemoteHeartbeatResponse 表示心跳响应
type RemoteHeartbeatResponse struct {
	Success          bool   `json:"success"`
	Message          string `json:"message"`
	MmwxRestarted    bool   `json:"mmwx_restarted,omitempty"`     // 检测到 MMWX 重启
	XrayRestarted    bool   `json:"xray_restarted,omitempty"`     // 检测到 X 射线重新启动
	TokenExpiresSoon bool   `json:"token_expires_soon,omitempty"` // 令牌将在 24 小时内过期
	TokenExpiresAt   int64  `json:"token_expires_at,omitempty"`   // 令牌过期时间戳
	ServerTime       int64  `json:"server_time"`                  // 当前服务器时间
}

// RemoteHeartbeat 处理来自远程服务器的心跳请求
// 该端点不需要管理员身份验证，只需要远程令牌验证
func (h *XrayServerHandler) RemoteHeartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if r.Header.Get("User-Agent") != version.AgentUserAgent {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(RemoteHeartbeatResponse{
			Success:    false,
			Message:    "Forbidden",
			ServerTime: time.Now().Unix(),
		})
		return
	}

	// 加密中间件处理
	crypto, cryptoErr := handleHTTPCrypto(r, w, h.crypto)
	if crypto == nil {
		return
	}
	_ = cryptoErr

	token := crypto.Token
	if token == "" {
		token = r.Header.Get("MM-Remote-Token")
	}
	if token == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(RemoteHeartbeatResponse{
			Success:    false,
			Message:    "缺少认证Token",
			ServerTime: time.Now().Unix(),
		})
		return
	}

	// 解析请求体
	var req RemoteHeartbeatRequest
	json.Unmarshal(crypto.Body, &req)

	// 获取客户端IP — X-Forwarded-For > X-Real-IP > r.RemoteAddr
	rawIP := r.RemoteAddr
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		// 从逗号分隔列表中获取第一个 IP
		rawIP = strings.Split(forwarded, ",")[0]
		rawIP = strings.TrimSpace(rawIP)
	} else if realIP := r.Header.Get("X-Real-IP"); realIP != "" {
		rawIP = realIP
	}
	// 用 stripPort 正确处理 v4 / [v6]:port / 裸 v6 三种形式。
	// 老代码用 strings.LastIndex(":") 截断,对裸 v6 会把最后一段误删,留下半截 v6 字符串塞进 db.ip_address。
	clientIP := stripPort(rawIP)
	clientParsed := net.ParseIP(clientIP)
	clientIsV4 := clientParsed != nil && clientParsed.To4() != nil

	// 严格选 v4 / v6 字段(同 WS handleHeartbeat 模式):
	//   1) 优先用 agent 上报的 public_ipv4/public_ipv6(经 ipProbeLoop 校验过的本机出口 IP)
	//   2) fallback 用 clientIP,但**只在类型匹配时**才写对应字段 — 避免 agent v6 拨号 master →
	//      master 把 clientIP(v6) 当 v4 塞进 ip_address → IPv4-only master 反向请求全部失败
	v4 := ""
	if reported := strings.TrimSpace(req.PublicIPv4); reported != "" {
		if p := net.ParseIP(reported); p != nil && p.To4() != nil {
			v4 = reported
		}
	}
	if v4 == "" && clientIsV4 {
		v4 = clientIP
	}

	v6 := ""
	if reported := strings.TrimSpace(req.PublicIPv6); reported != "" {
		if p := net.ParseIP(reported); p != nil && p.To4() == nil {
			v6 = reported
		}
	}
	if v6 == "" && clientParsed != nil && !clientIsV4 {
		v6 = clientIP
	}

	ctx := r.Context()

	// 构建心跳更新 — v4/v6 字段空字符串走 storage 层 COALESCE/NULLIF 保留 db 旧值
	update := storage.HeartbeatUpdate{
		Token:       token,
		IPAddress:   v4,
		IPAddressV6: v6,
		ListenPort:  req.ListenPort,
	}

	// 从 Unix 时间戳转换启动时间
	if req.BootTime > 0 {
		bootTime := time.Unix(req.BootTime, 0)
		update.BootTime = &bootTime
	}
	if req.XrayBootTime > 0 {
		xrayBootTime := time.Unix(req.XrayBootTime, 0)
		update.XrayBootTime = &xrayBootTime
	}
	if req.LocalTime > 0 {
		offset := req.LocalTime - time.Now().Unix()
		update.TimeOffsetSeconds = &offset
	}

	// 通过重启检测更新心跳
	result, err := h.repo.UpdateRemoteServerHeartbeatWithRestart(ctx, update)
	if err != nil {
		if err == storage.ErrRemoteServerNotFound {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(RemoteHeartbeatResponse{
				Success:    false,
				Message:    "无效的Token",
				ServerTime: time.Now().Unix(),
			})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(RemoteHeartbeatResponse{
			Success:    false,
			Message:    fmt.Sprintf("更新心跳失败: %s", err.Error()),
			ServerTime: time.Now().Unix(),
		})
		return
	}

	// 记录重启事件
	if result.MmwxRestarted {
		log.Printf("[RemoteHeartbeat] Detected MMWX restart for token %s... (boot count: %d)", token[:8], result.BootCount)
	}
	if result.XrayRestarted {
		log.Printf("[RemoteHeartbeat] Detected Xray restart for token %s... (xray boot count: %d)", token[:8], result.XrayBootCount)
	}

	if result.PreviousStatus != "connected" {
		SendServerOnlineNotification(ctx, result.ServerName, clientIP)
	}

	// agent IP 漂移 → 同步刷新已存在节点的 clash_config.server,避免节点继续指向旧 IP
	if result.IPChanged && result.Server != nil {
		if newHost := chooseClashServerHost(result.Server); newHost != "" {
			if n, e := h.repo.RefreshNodesServerAddress(ctx, result.Server.Name, newHost); e != nil {
				log.Printf("[RemoteHeartbeat] refresh nodes server address for %s failed: %v", result.Server.Name, e)
			} else if n > 0 {
				log.Printf("[RemoteHeartbeat] refreshed %d node(s) clash.server → %s for %s", n, newHost, result.Server.Name)
			}
		}
		// v6 节点单独刷成新的 IPv6 地址(RefreshNodesServerAddress 只动 v4/域名节点)
		if v6 := strings.TrimSpace(result.Server.IPAddressV6); v6 != "" {
			if n, e := h.repo.RefreshNodesServerAddressV6(ctx, result.Server.Name, v6); e != nil {
				log.Printf("[RemoteHeartbeat] refresh v6 nodes for %s failed: %v", result.Server.Name, e)
			} else if n > 0 {
				log.Printf("[RemoteHeartbeat] refreshed %d v6 node(s) clash.server → %s for %s", n, v6, result.Server.Name)
			}
		}
		// DDNS:把新 IP 同步到 pull_address 域名的 A/AAAA 记录
		if h.ddnsManager != nil && result.Server.DDNSEnabled {
			go h.ddnsManager.Trigger(context.Background(), result.Server)
		}
	}

	// 首次连接或 Xray 重启时推送限速配置（非 WebSocket 模式的补偿）
	if result.ServerID > 0 && h.limiterPusher != nil {
		if result.PreviousStatus != "connected" || result.XrayRestarted {
			go h.limiterPusher.PushToServer(context.Background(), result.ServerID)
		}
	}

	// 重置成功心跳时的推送失败计数（连接正常）
	if result.ServerID > 0 {
		if err := h.repo.ResetRemoteServerPushFailCount(ctx, result.ServerID); err != nil {
			log.Printf("[RemoteHeartbeat] Failed to reset push fail count for server %d: %v", result.ServerID, err)
		}
	}

	resp := RemoteHeartbeatResponse{
		Success:          true,
		Message:          "心跳成功",
		MmwxRestarted:    result.MmwxRestarted,
		XrayRestarted:    result.XrayRestarted,
		TokenExpiresSoon: result.TokenExpiresSoon,
		ServerTime:       time.Now().Unix(),
	}

	if result.TokenExpiresAt != nil {
		resp.TokenExpiresAt = result.TokenExpiresAt.Unix()
	}

	respData, _ := json.Marshal(resp)
	writeHTTPCryptoResponse(w, crypto.Session, respData)
}

// RefreshRemoteTokenResponse 是令牌刷新端点的响应
type RefreshRemoteTokenResponse struct {
	Success   bool   `json:"success"`
	Message   string `json:"message"`
	NewToken  string `json:"new_token,omitempty"`
	ExpiresAt int64  `json:"expires_at,omitempty"` // Unix时间戳
}

// 处理远程服务器的令牌刷新
func (h *XrayServerHandler) RefreshRemoteToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if r.Header.Get("User-Agent") != version.AgentUserAgent {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(RefreshRemoteTokenResponse{
			Success: false,
			Message: "Forbidden",
		})
		return
	}

	// 从标头获取令牌
	token := r.Header.Get("MM-Remote-Token")
	if token == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(RefreshRemoteTokenResponse{
			Success: false,
			Message: "Missing MM-Remote-Token header",
		})
		return
	}

	// 尝试刷新令牌
	ctx := r.Context()
	server, lookupErr := h.repo.GetRemoteServerByToken(ctx, token)
	if lookupErr != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(RefreshRemoteTokenResponse{Success: false, Message: "Invalid token"})
		return
	}
	newToken, expiresAt, err := h.repo.RefreshRemoteServerToken(ctx, token)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")

		// 检查具体错误
		if err.Error() == "token can only be refreshed within 24 hours of expiration" {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(RefreshRemoteTokenResponse{
				Success: false,
				Message: err.Error(),
			})
			return
		}

		if errors.Is(err, storage.ErrRemoteServerNotFound) {
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(RefreshRemoteTokenResponse{
				Success: false,
				Message: "Invalid token",
			})
			return
		}

		log.Printf("[Remote] Failed to refresh token: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(RefreshRemoteTokenResponse{
			Success: false,
			Message: "Failed to refresh token",
		})
		return
	}
	if err := syncRemoteExpiryGuardAgentToken(ctx, h.repo, server.ID, newToken); err != nil {
		log.Printf("[Remote] Failed to synchronize refreshed token to expiry guard for server %d: %v", server.ID, err)
	}

	log.Printf("[Remote] Token refreshed successfully, new expiration: %s", expiresAt.Format(time.RFC3339))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(RefreshRemoteTokenResponse{
		Success:   true,
		Message:   "Token refreshed successfully",
		NewToken:  newToken,
		ExpiresAt: expiresAt.Unix(),
	})
}

func (h *XrayServerHandler) getMasterPort() string {
	if port := os.Getenv("PORT"); port != "" {
		return port
	}
	return "12889"
}

func (h *XrayServerHandler) masterPublicKeyBase64() string {
	if h.crypto != nil && h.crypto.Identity != nil {
		return h.crypto.Identity.PublicKeyBase64()
	}
	return ""
}

// 返回远程服务器的安装脚本
func (h *XrayServerHandler) GetRemoteInstallScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	authorization := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(authorization, "Bearer ") {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	token := strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer "))
	server, err := h.repo.GetRemoteServerByToken(r.Context(), token)
	if err != nil || (server.TokenExpiresAt != nil && !server.TokenExpiresAt.After(time.Now())) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	guardSecret, err := h.repo.GetOrCreateRemoteServerGuardSecret(r.Context(), server.ID)
	if err != nil {
		http.Error(w, "Unable to initialize expiry guard", http.StatusInternalServerError)
		return
	}
	stealSelf := r.URL.Query().Get("steal_self") == "1"
	xrayMode := r.URL.Query().Get("xray_mode")
	if xrayMode != "embedded" {
		xrayMode = "external"
	}
	// 自定义 Agent 监听端口(由主控创建服务器时透传过来),非法/缺省值用 agent 内置默认 23889
	listenPortParam := strings.TrimSpace(r.URL.Query().Get("listen_port"))
	if p, perr := strconv.Atoi(listenPortParam); perr != nil || p < 1024 || p > 65535 {
		listenPortParam = ""
	}
	frontService := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("front_service")))
	if frontService != "xray" && frontService != "nginx" {
		frontService = "xray"
	}
	// nginx 前置暂未支持，先固定为 xray
	if frontService == "nginx" {
		frontService = "xray"
	}

	// 计算 install 脚本里写入的 SERVER:
	// 优先用系统设置 master_url 里的 host(用户配置的对外可达域名),
	// 这是 agent 真正访问主控的地址。仅在 master_url 未配置时回退到 r.Host(可能是 nginx upstream 名,如 miaomiaowu_web,不可对外访问)。
	// 若 master_url 已显式配置,EXPLICIT_MASTER=1 在脚本里禁用"同机部署"自动覆盖
	// (避免在主控本机上安装 agent 时把 master_url 改写成 127.0.0.1)。
	scriptServer := strings.TrimSpace(r.Host)
	// nginx 默认 `proxy_set_header Host $host` 不带端口,导致 cf:8443 → nginx → mmwx 时 r.Host 只有域名,
	// agent 安装命令缺端口连不上主控。这里如果检测到 X-Forwarded-Host(带端口最优)或 X-Forwarded-Port
	// 且端口不是 80/443,主动把 :port 拼回去,方便用户不需要必须先去配 master_url 就能拿到正确安装命令。
	if !strings.Contains(scriptServer, ":") {
		if xfh := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); xfh != "" && strings.Contains(xfh, ":") {
			scriptServer = xfh
		} else if xfp := strings.TrimSpace(r.Header.Get("X-Forwarded-Port")); xfp != "" && xfp != "80" && xfp != "443" {
			scriptServer = scriptServer + ":" + xfp
		}
	}
	scriptProtocol := ""
	// nginx 反代下大概率有 X-Forwarded-Proto,带这个就别走脚本里 "host 有 : 就当 http" 的启发,直接显式 https
	if xfproto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); xfproto == "https" || xfproto == "http" {
		scriptProtocol = xfproto
	}
	explicitMaster := "0"
	if mu, err := h.repo.GetSystemSetting(r.Context(), "master_url"); err == nil {
		mu = strings.TrimSpace(mu)
		if mu != "" {
			explicitMaster = "1"
			s := strings.TrimRight(mu, "/")
			if strings.HasPrefix(s, "https://") {
				scriptProtocol = "https"
				s = strings.TrimPrefix(s, "https://")
			} else if strings.HasPrefix(s, "http://") {
				scriptProtocol = "http"
				s = strings.TrimPrefix(s, "http://")
			}
			if i := strings.Index(s, "/"); i >= 0 {
				s = s[:i]
			}
			if s != "" {
				scriptServer = s
			}
		}
	}

	// 返回安装脚本内容
	script := `#!/bin/bash
# MMWX Remote Server Installation Script
# This script installs MMWX from GitHub and configures it as a remote server

set -e
umask 077

DOWNLOAD_DIR=$(mktemp -d /tmp/arcway-install.XXXXXX)
chmod 0700 "$DOWNLOAD_DIR"
trap 'rm -rf "$DOWNLOAD_DIR"' EXIT HUP INT TERM
AGENT_DOWNLOAD="$DOWNLOAD_DIR/mmw-agent"
GUARD_DOWNLOAD="$DOWNLOAD_DIR/arcway-expiry-guard"

TOKEN="` + token + `"
GUARD_SECRET="` + guardSecret + `"
SERVER="` + scriptServer + `"
SCRIPT_PROTOCOL="` + scriptProtocol + `"
EXPLICIT_MASTER="` + explicitMaster + `"
AUTO_STEAL_SELF="` + map[bool]string{true: "1", false: "0"}[stealSelf] + `"
FRONT_SERVICE="` + frontService + `"
XRAY_MODE="` + xrayMode + `"
MASTER_PUBLIC_KEY="` + h.masterPublicKeyBase64() + `"
MASTER_PORT="` + h.getMasterPort() + `"
LISTEN_PORT="` + listenPortParam + `"

# 协议:优先用主控注入的 SCRIPT_PROTOCOL(来自系统设置 master_url 的 scheme),
# 否则按 SERVER 是否带端口启发判断(开发场景常见 http)。
if [ -n "$SCRIPT_PROTOCOL" ]; then
    PROTOCOL="$SCRIPT_PROTOCOL"
elif [[ "$SERVER" == *":"* ]]; then
    PROTOCOL="http"
else
    PROTOCOL="https"
fi

# 允许通过环境变量强制覆盖协议
if [ -n "$MMWX_PROTOCOL" ]; then
    PROTOCOL="$MMWX_PROTOCOL"
fi

MASTER_URL="${PROTOCOL}://${SERVER}"

# 同机部署检测:只有在主控"没有显式配置 master_url"时才允许把 master_url 自动改成 127.0.0.1;
# 用户配置了对外域名(EXPLICIT_MASTER=1)就必须用用户的域名,不让自动改写。
if [ "$EXPLICIT_MASTER" != "1" ] && curl -sf "http://127.0.0.1:${MASTER_PORT}/api/setup/status" >/dev/null 2>&1; then
    MASTER_URL="http://127.0.0.1:${MASTER_PORT}"
    echo "Detected same-machine deployment, using ${MASTER_URL}"
fi

echo "=========================================="
echo "  MMWX Remote Server Installation"
echo "=========================================="
echo ""
echo "Master Server: $MASTER_URL"
echo ""

# 检测 init 系统:OpenRC(Alpine 首选)/ systemd(主流)/ 兜底用 nohup + rc.local。
# - Alpine 优先用 OpenRC:Alpine 主流就是 OpenRC,即便镜像里塞了 systemd 也不用它
# - Alpine 极简镜像/LXC 可能没装 openrc 包 → 自动 apk add 装上,再走 OpenRC 路径
# - 大部分 LXC 容器没有 systemd,老脚本直接 systemctl 失败"systemctl: command not found"
HAS_SYSTEMD=0
HAS_OPENRC=0
IS_ALPINE=0
if [ -f /etc/alpine-release ]; then
    IS_ALPINE=1
elif [ -f /etc/os-release ] && grep -qE '^ID=alpine' /etc/os-release 2>/dev/null; then
    IS_ALPINE=1
fi
# Alpine 上 openrc 缺失就尝试自动装,失败不致命(下面还有 nohup 兜底)
if [ "$IS_ALPINE" = "1" ] && ! command -v rc-service >/dev/null 2>&1; then
    echo "[Init] Alpine detected without OpenRC, installing openrc..."
    if command -v apk >/dev/null 2>&1; then
        apk add --no-cache openrc 2>/dev/null || echo "[Init] apk add openrc failed, will fall back to nohup"
    fi
fi
# Alpine 优先 OpenRC;非 Alpine 仍然先看 systemd(主流发行版默认)
if [ "$IS_ALPINE" = "1" ]; then
    if command -v rc-service >/dev/null 2>&1; then HAS_OPENRC=1; fi
fi
if [ "$HAS_OPENRC" = "0" ] && command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    HAS_SYSTEMD=1
fi
if [ "$HAS_SYSTEMD" = "0" ] && [ "$HAS_OPENRC" = "0" ] && command -v rc-service >/dev/null 2>&1; then
    HAS_OPENRC=1
fi
echo "Init system: $([ "$HAS_OPENRC" = 1 ] && echo openrc || ([ "$HAS_SYSTEMD" = 1 ] && echo systemd || echo none))$([ "$IS_ALPINE" = 1 ] && echo " (Alpine)")"

# Step 1: Stop existing service if running
echo "[1/6] Stopping existing service (if any)..."
if [ "$HAS_SYSTEMD" = "1" ]; then
    systemctl stop arcway-expiry-guard 2>/dev/null || true
    systemctl disable arcway-expiry-guard 2>/dev/null || true
    systemctl stop mmw-agent 2>/dev/null || true
    systemctl disable mmw-agent 2>/dev/null || true
elif [ "$HAS_OPENRC" = "1" ]; then
    rc-service arcway-expiry-guard stop 2>/dev/null || true
    rc-update del arcway-expiry-guard 2>/dev/null || true
    rc-service mmw-agent stop 2>/dev/null || true
    rc-update del mmw-agent 2>/dev/null || true
else
    # nohup 兜底:杀掉现有 agent / guard 进程及 supervisor
    pkill -f /usr/local/bin/arcway-expiry-guard 2>/dev/null || true
    pkill -f /usr/local/bin/mmw-agent 2>/dev/null || true
    sleep 1
fi

# Step 2: Create config directory first
echo ""
echo "[2/6] Creating configuration..."
mkdir -p /etc/mmw-agent
mkdir -p /var/lib/mmw-agent
mkdir -p /var/lib/arcway-expiry-guard
chmod 0700 /var/lib/arcway-expiry-guard

# 端口探测:Agent 与 expiry guard 必须占用连续两个端口。每次同时检查 P 和 P+1,
# 避免只挑到一个空闲端口后 guard 启动失败,导致过期客户端无法按时撤销。
port_is_listening() {
    CHECK_PORT="$1"
    if command -v ss >/dev/null 2>&1; then
        ss -H -ltn 2>/dev/null | awk '{print $4}' | grep -qE "[:.]${CHECK_PORT}\$"
        return $?
    fi
    if command -v netstat >/dev/null 2>&1; then
        netstat -ltn 2>/dev/null | awk 'NR > 2 {print $4}' | grep -qE "[:.]${CHECK_PORT}\$"
        return $?
    fi
    CHECK_PORT_HEX=$(printf '%04X' "$CHECK_PORT")
    awk -v suffix=":${CHECK_PORT_HEX}" '$2 ~ suffix "$" && $4 == "0A" { found=1 } END { exit !found }' /proc/net/tcp /proc/net/tcp6 2>/dev/null
}

REQUESTED_PORT="${LISTEN_PORT:-23889}"
ACTUAL_PORT=""
GUARD_PORT=""
for i in $(seq 0 19); do
    TRY_PORT=$((REQUESTED_PORT + i))
    TRY_GUARD_PORT=$((TRY_PORT + 1))
    if [ "$TRY_GUARD_PORT" -gt 65535 ]; then
        break
    fi
    if port_is_listening "$TRY_PORT" || port_is_listening "$TRY_GUARD_PORT"; then
        echo "  端口对 ${TRY_PORT}/${TRY_GUARD_PORT} 不可用,尝试下一个..."
        continue
    fi
    ACTUAL_PORT="$TRY_PORT"
    GUARD_PORT="$TRY_GUARD_PORT"
    break
done
if [ -z "$ACTUAL_PORT" ]; then
    echo "ERROR: 从 ${REQUESTED_PORT} 起找不到连续两个可用端口,安装中止" >&2
    exit 1
fi
if [ "$ACTUAL_PORT" != "$REQUESTED_PORT" ]; then
    echo "⚠ 请求端口对不可用,Agent 自动改用 ${ACTUAL_PORT},guard 使用 ${GUARD_PORT}"
fi
LISTEN_PORT="$ACTUAL_PORT"

cat > /etc/mmw-agent/config.yaml << EOF
# MMWX Remote Server Configuration
# Generated by install script

mode: remote
master_url: ${MASTER_URL}
token: ${TOKEN}
connection_mode: websocket
xray_mode: ${XRAY_MODE}
steal_mode: $([ "$AUTO_STEAL_SELF" = "1" ] && echo "tunnel" || echo "")
master_public_key: ${MASTER_PUBLIC_KEY}
listen_port: "${LISTEN_PORT}"
hide_port_on_ws: false
EOF

echo "Configuration saved to /etc/mmw-agent/config.yaml"

cat > /etc/arcway-expiry-guard.env << EOF
ARCWAY_GUARD_LISTEN=[::]:${GUARD_PORT}
ARCWAY_AGENT_URL=http://127.0.0.1:${LISTEN_PORT}
ARCWAY_GUARD_STATE=/var/lib/arcway-expiry-guard/state.json
ARCWAY_GUARD_SECRET=${GUARD_SECRET}
ARCWAY_AGENT_TOKEN=${TOKEN}
EOF
chmod 0600 /etc/arcway-expiry-guard.env
if [ ! -e /var/lib/arcway-expiry-guard/state.json ]; then
    printf '%s\n' '{"version":1,"entries":[]}' > /var/lib/arcway-expiry-guard/state.json
fi
chmod 0600 /var/lib/arcway-expiry-guard/state.json

# guard API 需要主控直连。仅当 UFW 已启用时增加端口规则；失败必须明确告警,
# 避免出现本机 healthz 正常、主控却永远无法下发到期计划的隐蔽故障。
# 某些 IPv4-only 主机的 ufw status 会因 ip6tables 损坏直接报错，因此同时读取 ufw.conf。
UFW_ACTIVE=0
if command -v ufw >/dev/null 2>&1; then
    if ufw status 2>/dev/null | grep -q '^Status: active' || grep -q '^ENABLED=yes' /etc/ufw/ufw.conf 2>/dev/null; then
        UFW_ACTIVE=1
    fi
fi
if [ "$UFW_ACTIVE" = "1" ]; then
    echo "Active UFW detected, allowing expiry guard TCP port ${GUARD_PORT}..."
    if ufw allow "${GUARD_PORT}/tcp"; then
        echo "UFW rule added for ${GUARD_PORT}/tcp"
    else
        # ufw may have installed the IPv4 rule before failing on ip6tables. If
        # not, install it in ufw-user-input for the current boot and flag that
        # the operator still needs to repair UFW persistence.
        IPV4_RULE_READY=0
        if command -v iptables >/dev/null 2>&1; then
            if iptables -C ufw-user-input -p tcp --dport "$GUARD_PORT" -j ACCEPT 2>/dev/null; then
                IPV4_RULE_READY=1
            elif iptables -I ufw-user-input 1 -p tcp --dport "$GUARD_PORT" -j ACCEPT 2>/dev/null; then
                IPV4_RULE_READY=1
                echo "WARNING: installed a temporary IPv4 guard rule; repair UFW/ip6tables so it survives firewall reload." >&2
            fi
        fi
        if [ "$IPV4_RULE_READY" != "1" ]; then
            echo "WARNING: UFW failed to allow ${GUARD_PORT}/tcp (including a possible ip6tables error)." >&2
            echo "WARNING: expiry enforcement will fail until the panel can reach this TCP port." >&2
        fi
    fi
fi

# Step 3: 创建 service 文件 — 按检测到的 init 系统选不同写法
echo ""
echo "[3/6] Creating service..."

if [ "$HAS_SYSTEMD" = "1" ]; then
    cat > /etc/systemd/system/mmw-agent.service << EOF
[Unit]
Description=MMW Agent Remote Server
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/mmw-agent -c /etc/mmw-agent/config.yaml
Restart=always
RestartSec=5
WorkingDirectory=/var/lib/mmw-agent

[Install]
WantedBy=multi-user.target
EOF
    cat > /etc/systemd/system/arcway-expiry-guard.service << EOF
[Unit]
Description=Arcway Managed Client Expiry Guard
After=network-online.target mmw-agent.service
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=/etc/arcway-expiry-guard.env
ExecStart=/usr/local/bin/arcway-expiry-guard
Restart=always
RestartSec=5
WorkingDirectory=/var/lib/arcway-expiry-guard
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ReadWritePaths=/var/lib/arcway-expiry-guard

[Install]
WantedBy=multi-user.target
EOF
    systemctl daemon-reload
elif [ "$HAS_OPENRC" = "1" ]; then
    cat > /etc/init.d/mmw-agent << 'EOF'
#!/sbin/openrc-run
name="mmw-agent"
description="MMW Agent Remote Server"
command="/usr/local/bin/mmw-agent"
command_args="-c /etc/mmw-agent/config.yaml"
command_background="yes"
pidfile="/run/mmw-agent.pid"
# 日志由 agent 自身写文件并轮转(/var/log/mmw-agent/mmw-agent.log),不再用 output_log 重复落地(避免无轮转爆盘)
depend() { need net; }
EOF
    chmod +x /etc/init.d/mmw-agent
    cat > /etc/init.d/arcway-expiry-guard << 'EOF'
#!/sbin/openrc-run
name="arcway-expiry-guard"
description="Arcway Managed Client Expiry Guard"
command="/usr/local/bin/arcway-expiry-guard"
command_background="yes"
pidfile="/run/arcway-expiry-guard.pid"
start_pre() {
    set -a
    . /etc/arcway-expiry-guard.env
    set +a
}
depend() { need net; after mmw-agent; }
EOF
    chmod +x /etc/init.d/arcway-expiry-guard
else
    # 无 init 系统(典型 LXC 容器):写一个 supervisor 脚本,失败自动重启,放后台跑;同时塞进 rc.local 以便重启
    cat > /usr/local/bin/mmw-agent-supervisor.sh << 'EOF'
#!/bin/sh
while true; do
    # 日志由 agent 自身写文件并轮转(/var/log/mmw-agent/mmw-agent.log);这里输出走 stdout(由 rc.local 的 nohup 接管)
    /usr/local/bin/mmw-agent -c /etc/mmw-agent/config.yaml
    echo "[supervisor] mmw-agent exited, restarting in 5s..."
    sleep 5
done
EOF
    chmod +x /usr/local/bin/mmw-agent-supervisor.sh

    cat > /usr/local/bin/arcway-expiry-guard-supervisor.sh << 'EOF'
#!/bin/sh
set -a
. /etc/arcway-expiry-guard.env
set +a
while true; do
    /usr/local/bin/arcway-expiry-guard
    echo "[supervisor] arcway-expiry-guard exited, restarting in 5s..."
    sleep 5
done
EOF
    chmod +x /usr/local/bin/arcway-expiry-guard-supervisor.sh

    # 写入 rc.local 实现重启自启动(若文件不存在就建一个)
    if [ ! -f /etc/rc.local ]; then
        echo "#!/bin/sh" > /etc/rc.local
        echo "exit 0" >> /etc/rc.local
        chmod +x /etc/rc.local
    fi
    if ! grep -q "mmw-agent-supervisor.sh" /etc/rc.local; then
        sed -i '/^exit 0/i nohup /usr/local/bin/mmw-agent-supervisor.sh >/dev/null 2>&1 \&' /etc/rc.local
    fi
    if ! grep -q "arcway-expiry-guard-supervisor.sh" /etc/rc.local; then
        sed -i '/^exit 0/i nohup /usr/local/bin/arcway-expiry-guard-supervisor.sh >/dev/null 2>&1 \&' /etc/rc.local
    fi
fi

# Step 4: Download and install binary only (without starting)
echo ""
echo "[4/6] Downloading MMWX binary..."

# Detect architecture
ARCH=$(uname -m)
case $ARCH in
    x86_64)
        ARCH_NAME="amd64"
        AGENT_SHA256="6ce2faac96f82a501ab86b1817c332bf05239ba10e36b5be0dd11995a5a1bf2f"
        ;;
    aarch64|arm64)
        ARCH_NAME="arm64"
        AGENT_SHA256="04ba897947923592846d3e57282d5ac80c213892b125c1575a8664abb770168f"
        ;;
    *)
        echo "Unsupported architecture: $ARCH"
        exit 1
        ;;
esac

AGENT_VERSION="v0.3.7"
AGENT_URL="https://github.com/iluobei/mmw-agent/releases/download/${AGENT_VERSION}/mmw-agent-linux-${ARCH_NAME}"

# Download binary — 优先用 curl(更普遍),没有就用 wget;两者都没就按发行版包管理器装一个,
# 杜绝 "wget: command not found" 噪声 / "ERROR: 都没装" 卡死。
ensure_downloader() {
    if command -v curl >/dev/null 2>&1; then return 0; fi
    if command -v wget >/dev/null 2>&1; then return 0; fi
    echo "未检测到 curl/wget,尝试自动安装 curl..."
    if command -v apt-get >/dev/null 2>&1; then
        apt-get update -qq >/dev/null 2>&1 || true
        DEBIAN_FRONTEND=noninteractive apt-get install -y curl
    elif command -v dnf >/dev/null 2>&1; then
        dnf install -y curl
    elif command -v yum >/dev/null 2>&1; then
        yum install -y curl
    elif command -v apk >/dev/null 2>&1; then
        apk add --no-cache curl
    elif command -v pacman >/dev/null 2>&1; then
        pacman -Sy --noconfirm curl
    elif command -v zypper >/dev/null 2>&1; then
        zypper -n install curl
    else
        echo "ERROR: 无法识别系统包管理器,请手动安装 curl 或 wget 后重试" >&2
        return 1
    fi
}
ensure_downloader || exit 1
echo "Downloading verified mmw-agent ${AGENT_VERSION} from GitHub..."
if command -v curl >/dev/null 2>&1; then
    curl -fsSL --connect-timeout 10 --max-time 180 -o "$AGENT_DOWNLOAD" "$AGENT_URL" || {
        echo "ERROR: 无法从 GitHub 下载 mmw-agent" >&2
        exit 1
    }
else
    wget -q --connect-timeout=10 --read-timeout=180 -O "$AGENT_DOWNLOAD" "$AGENT_URL" || {
        echo "ERROR: 无法从 GitHub 下载 mmw-agent" >&2
        exit 1
    }
fi
if [ "$(sha256sum "$AGENT_DOWNLOAD" | awk '{ print $1 }')" != "$AGENT_SHA256" ]; then
    echo "ERROR: mmw-agent SHA-256 校验失败,安装中止" >&2
    exit 1
fi

# expiry guard 由当前主控按远程服务器 token 鉴权分发,不依赖私有 GitHub Release。
GUARD_URL="${MASTER_URL}/api/remote/expiry-guard?arch=${ARCH_NAME}"
echo "Downloading Arcway expiry guard from master..."
if command -v curl >/dev/null 2>&1; then
	    curl -fsSL --connect-timeout 10 --max-time 180 \
	        -H "Authorization: Bearer ${TOKEN}" \
	        -o "$GUARD_DOWNLOAD" "$GUARD_URL"
else
	    wget -q --connect-timeout=10 --read-timeout=180 \
	        --header="Authorization: Bearer ${TOKEN}" \
	        -O "$GUARD_DOWNLOAD" "$GUARD_URL"
	fi
	if [ ! -s "$GUARD_DOWNLOAD" ]; then
    echo "ERROR: expiry guard 下载结果为空,安装中止" >&2
    exit 1
fi

# 拒绝明显的代理错误页/空文件，再以原子 rename 安装。
validate_elf() {
    [ -s "$1" ] || return 1
    [ "$(od -An -tx1 -N4 "$1" 2>/dev/null | tr -d ' \n')" = "7f454c46" ]
}
if ! validate_elf "$AGENT_DOWNLOAD" || ! validate_elf "$GUARD_DOWNLOAD"; then
    echo "ERROR: 下载结果不是有效 ELF 可执行文件,安装中止" >&2
    exit 1
fi

# Install binaries
install -m 0755 "$AGENT_DOWNLOAD" /usr/local/bin/.mmw-agent.new
mv -f /usr/local/bin/.mmw-agent.new /usr/local/bin/mmw-agent
install -m 0755 "$GUARD_DOWNLOAD" /usr/local/bin/.arcway-expiry-guard.new
mv -f /usr/local/bin/.arcway-expiry-guard.new /usr/local/bin/arcway-expiry-guard

echo "Binaries installed to /usr/local/bin/mmw-agent and /usr/local/bin/arcway-expiry-guard"

# Step 5: 启用并启动 service
echo ""
echo "[5/6] Starting service..."
if [ "$HAS_SYSTEMD" = "1" ]; then
    systemctl enable mmw-agent
    systemctl start mmw-agent
    systemctl enable arcway-expiry-guard
    systemctl start arcway-expiry-guard
elif [ "$HAS_OPENRC" = "1" ]; then
    # rc-update 在 LXC 容器里没初始化 runlevel 时会报错,失败不致命(set -e 兜底)
    rc-update add mmw-agent default 2>/dev/null || echo "  ⚠ rc-update add 失败(常见于 LXC 容器,不影响当前会话启动)"
    rc-service mmw-agent start
    rc-update add arcway-expiry-guard default 2>/dev/null || echo "  ⚠ guard rc-update add 失败,不影响当前会话启动"
    rc-service arcway-expiry-guard start
else
    nohup /usr/local/bin/mmw-agent-supervisor.sh >/dev/null 2>&1 &
    nohup /usr/local/bin/arcway-expiry-guard-supervisor.sh >/dev/null 2>&1 &
    echo "Started via nohup (PID=$!); 安装重启后通过 /etc/rc.local 自启动"
fi

# Wait a moment for service to start
sleep 3

# Step 6: Verify installation
echo ""
echo "[6/6] Verifying installation..."

echo ""
echo "=========================================="
echo "  Installation Complete!"
echo "=========================================="
echo ""
echo "Service status:"
if [ "$HAS_SYSTEMD" = "1" ]; then
    systemctl status mmw-agent --no-pager -l 2>/dev/null | head -15 || echo "Service started"
    systemctl status arcway-expiry-guard --no-pager -l 2>/dev/null | head -15 || echo "Guard service started"
elif [ "$HAS_OPENRC" = "1" ]; then
    rc-service mmw-agent status 2>/dev/null || echo "Service started"
    rc-service arcway-expiry-guard status 2>/dev/null || echo "Guard service started"
else
    pgrep -af /usr/local/bin/mmw-agent | head -5 || echo "Process not found in pgrep, check /var/log/mmw-agent.log"
    pgrep -af /usr/local/bin/arcway-expiry-guard | head -5 || echo "Guard process not found"
fi
guard_healthy=0
if command -v curl >/dev/null 2>&1; then
    curl -fsS --max-time 5 "http://127.0.0.1:${GUARD_PORT}/healthz" >/dev/null 2>&1 && guard_healthy=1
else
    wget -q --timeout=5 -O /dev/null "http://127.0.0.1:${GUARD_PORT}/healthz" >/dev/null 2>&1 && guard_healthy=1
fi
if [ "$guard_healthy" != "1" ]; then
    echo "ERROR: expiry guard health check failed on port ${GUARD_PORT}" >&2
    exit 1
fi
echo ""
echo "To check status:"
if [ "$HAS_SYSTEMD" = "1" ]; then
    echo "  systemctl status mmw-agent"
    echo "  systemctl status arcway-expiry-guard"
elif [ "$HAS_OPENRC" = "1" ]; then
    echo "  rc-service mmw-agent status"
    echo "  rc-service arcway-expiry-guard status"
else
    echo "  tail -f /var/log/mmw-agent.log  # 或: pgrep -af mmw-agent"
fi
echo ""
echo "To view logs:"
echo "  journalctl -u mmw-agent -f"
echo ""

# Auto-install Xray (unless embedded mode)
if [ "$XRAY_MODE" != "embedded" ]; then
    XRAY_INSTALLED=0
    if command -v xray >/dev/null 2>&1 || [ -x /usr/local/bin/xray ] || [ -x /usr/bin/xray ] || [ -x /opt/xray/xray ]; then
        XRAY_INSTALLED=1
    fi

    if [ "$XRAY_INSTALLED" = "1" ]; then
        echo "[Auto] Xray already installed, skip."
    else
        echo "[Auto] Installing Xray..."
        bash -c "$(curl -L https://github.com/XTLS/Xray-install/raw/main/install-release.sh)" @ install
    fi
fi

if [ "$AUTO_STEAL_SELF" = "1" ]; then
    echo "=========================================="
    echo "  Auto Install: Nginx"
    echo "=========================================="
    echo ""

    NGINX_INSTALLED=0
    if command -v nginx >/dev/null 2>&1 || [ -x /usr/local/nginx/sbin/nginx ]; then
        NGINX_INSTALLED=1
    fi

    if [ "$NGINX_INSTALLED" = "1" ]; then
        echo "[Auto] Nginx already installed, skip."
    else
        echo "[Auto] Installing Nginx..."
        curl -fsSL https://raw.githubusercontent.com/violetaini/arcway-backend/main/install-nginx.sh | bash
    fi
    echo ""
    echo "Auto install complete (front service: ${FRONT_SERVICE}, xray mode: ${XRAY_MODE})"
fi
echo ""
`

	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Content-Disposition", "attachment; filename=install.sh")
	w.Write([]byte(script))
}
