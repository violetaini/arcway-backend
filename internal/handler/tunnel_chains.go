package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"miaomiaowux/internal/storage"
)

// 链式端口转发编排:选有序 N 台服务器,建 N 条单跳 dokodemo tunnel 首尾相接。
//   A 监听 P1 → B:P2 ；B 监听 P2 → C:P3 ；… ；出口监听 Pn → 最终目标。
// 每跳 = 一个 protocol:"tunnel"(dokodemo)入站,target 烤进 settings.address/port,走默认 direct 出站。
// tag 命名 `tunnel-<label>-h<i>`(i 从 0),聚合视图按此分组成一条链;删除由前端逐跳走通用 inbound remove。
// agent/xray 无需改动。

type TunnelChainHandler struct {
	repo *storage.TrafficRepository
	rm   *RemoteManageHandler
}

func NewTunnelChainHandler(repo *storage.TrafficRepository, rm *RemoteManageHandler) *TunnelChainHandler {
	return &TunnelChainHandler{repo: repo, rm: rm}
}

type createChainReq struct {
	Label         string  `json:"label"`
	ServerIDs     []int64 `json:"server_ids"`
	EntryPort     int     `json:"entry_port"` // 0 = 随机
	TargetAddress string  `json:"target_address"`
	TargetPort    int     `json:"target_port"`
}

type chainHopResult struct {
	ServerID      int64  `json:"server_id"`
	ServerName    string `json:"server_name"`
	Tag           string `json:"tag"`
	ListenPort    int    `json:"listen_port"`
	TargetAddress string `json:"target_address"`
	TargetPort    int    `json:"target_port"`
}

func (h *TunnelChainHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, errors.New("only POST is supported"))
		return
	}
	h.create(w, r)
}

func (h *TunnelChainHandler) create(w http.ResponseWriter, r *http.Request) {
	var req createChainReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid request body"))
		return
	}
	label := slugify(req.Label)
	if label == "" {
		writeError(w, http.StatusBadRequest, errors.New("label 只能含字母数字和短横线,长度 2-32"))
		return
	}
	if len(req.ServerIDs) < 2 {
		writeError(w, http.StatusBadRequest, errors.New("链式转发至少需要 2 台服务器"))
		return
	}
	if strings.TrimSpace(req.TargetAddress) == "" || req.TargetPort <= 0 || req.TargetPort > 65535 {
		writeError(w, http.StatusBadRequest, errors.New("最终目标 address/port 无效"))
		return
	}
	if req.EntryPort < 0 || req.EntryPort > 65535 {
		writeError(w, http.StatusBadRequest, errors.New("入口端口无效"))
		return
	}
	ctx := r.Context()

	n := len(req.ServerIDs)
	servers := make([]*storage.RemoteServer, n)
	hosts := make([]string, n)
	used := make([]map[int]bool, n)
	for i, sid := range req.ServerIDs {
		s, err := h.repo.GetRemoteServer(ctx, sid)
		if err != nil || s == nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("服务器 %d 不存在", sid))
			return
		}
		servers[i] = s
		hosts[i] = serverEntryHost(s)
		if hosts[i] == "" {
			writeError(w, http.StatusBadRequest, fmt.Errorf("服务器 %s 缺少可达地址(ip/domain)", s.Name))
			return
		}
		used[i] = h.usedPorts(ctx, sid)
	}

	// 端口分配:入口=指定/随机;中间跳沿用上一跳实际端口;出口跳沿用入口端口;冲突则随机空闲。
	ports := make([]int, n)
	entryPort := req.EntryPort
	if entryPort == 0 {
		entryPort = randomFreePort(used[0])
	}
	ports[0] = pickPort(used[0], entryPort)
	used[0][ports[0]] = true
	for i := 1; i < n; i++ {
		want := ports[i-1] // 中间跳:沿用上一跳
		if i == n-1 {
			want = ports[0] // 出口跳:沿用入口
		}
		ports[i] = pickPort(used[i], want)
		used[i][ports[i]] = true
	}

	// 逐跳下发;记录已建以便回滚。
	type created struct {
		sid int64
		tag string
	}
	var done []created
	rollback := func() {
		for _, c := range done {
			body, _ := json.Marshal(map[string]any{"action": "remove", "tag": c.tag})
			rctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			_, _ = h.rm.ForwardToServer(rctx, c.sid, http.MethodPost, "/api/child/inbounds", body)
			cancel()
		}
	}

	hops := make([]chainHopResult, n)
	for i := 0; i < n; i++ {
		var tHost string
		var tPort int
		if i < n-1 {
			tHost, tPort = hosts[i+1], ports[i+1] // 转发到下一跳的实际监听端口
		} else {
			tHost, tPort = strings.TrimSpace(req.TargetAddress), req.TargetPort // 出口 → 最终目标
		}
		tag := fmt.Sprintf("tunnel-%s-h%d", label, i)
		inbound := map[string]any{
			"tag":      tag,
			"protocol": "tunnel",
			"port":     ports[i],
			// network 含 udp:TCP+UDP 都转;target 烤进 settings(followRedirect 默认 false)。
			"settings": map[string]any{"address": tHost, "port": tPort, "network": "tcp,udp"},
		}
		body, _ := json.Marshal(map[string]any{"action": "add", "inbound": inbound})
		hctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		_, err := h.rm.ForwardToServer(hctx, req.ServerIDs[i], http.MethodPost, "/api/child/inbounds", body)
		cancel()
		if err != nil {
			rollback()
			writeError(w, http.StatusBadGateway, fmt.Errorf("第 %d 跳(%s)下发失败,已回滚: %v", i+1, servers[i].Name, err))
			return
		}
		done = append(done, created{sid: req.ServerIDs[i], tag: tag})
		hops[i] = chainHopResult{
			ServerID: req.ServerIDs[i], ServerName: servers[i].Name, Tag: tag,
			ListenPort: ports[i], TargetAddress: tHost, TargetPort: tPort,
		}
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"success":    true,
		"label":      label,
		"entry_host": hosts[0],
		"entry_port": ports[0],
		"hops":       hops,
	})
}

// usedPorts 拉该服务器 xray config,收集所有 inbound 已用的 port。失败返回空集(退化为随机不冲突判断弱一点,靠下发失败兜底)。
func (h *TunnelChainHandler) usedPorts(ctx context.Context, serverID int64) map[int]bool {
	set := map[int]bool{}
	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	result, err := h.rm.ForwardToServer(cctx, serverID, http.MethodGet, "/api/child/xray/config", nil)
	if err != nil {
		return set
	}
	var envelope struct {
		Config string `json:"config"`
	}
	if json.Unmarshal(result, &envelope) != nil || envelope.Config == "" {
		return set
	}
	var cfg map[string]any
	if json.Unmarshal([]byte(envelope.Config), &cfg) != nil {
		return set
	}
	inbounds, _ := cfg["inbounds"].([]any)
	for _, ibAny := range inbounds {
		if ib, ok := ibAny.(map[string]any); ok {
			if p := toInt(ib["port"]); p > 0 {
				set[p] = true
			}
		}
	}
	return set
}

// serverEntryHost 取一台服务器对外可达地址(优先 IP,再 domain / pull_address)。
func serverEntryHost(s *storage.RemoteServer) string {
	for _, v := range []string{s.IPAddress, s.Domain, s.PullAddress} {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// pickPort:want 空闲就用 want,否则挑一个随机空闲端口。
func pickPort(used map[int]bool, want int) int {
	if want > 0 && want <= 65535 && !used[want] {
		return want
	}
	return randomFreePort(used)
}

// randomFreePort 在 [20000,60000) 随机挑一个不在 used 里的端口。
func randomFreePort(used map[int]bool) int {
	for i := 0; i < 200; i++ {
		p := 20000 + rand.Intn(40000)
		if !used[p] {
			return p
		}
	}
	// 极端兜底:线性找
	for p := 20000; p < 60000; p++ {
		if !used[p] {
			return p
		}
	}
	return 20000
}
