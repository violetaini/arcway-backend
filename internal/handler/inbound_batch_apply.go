package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"miaomiaowux/internal/storage"
)

// collectInboundClientAddItem 从 master InboundCache 拿 protocol/settings,算好 cred,
// 返回 batch item。**不调 agent / 不写 DB**。
//
// 返回 (nil, false, err):缓存 miss、已有凭据需刷新 deadline、入站不存在等 → fallback
// 返回 (nil, false, err):缓存 miss / 入站不存在等 → 调用方应 fallback 到逐个 addUserToInbound
// 返回 (item, true, nil):成功,加入 batch 列表
func collectInboundClientAddItem(ctx context.Context, cache *InboundCache, repo *storage.TrafficRepository, user storage.User, serverID int64, inboundTag string) (*InboundClientAddItem, bool, error) {
	if cache == nil {
		return nil, false, fmt.Errorf("inbound cache not available")
	}
	ib, ok := cache.GetInbound(serverID, inboundTag)
	if !ok {
		return nil, false, fmt.Errorf("inbound cache miss for server=%d tag=%s", serverID, inboundTag)
	}

	// Existing credentials still need an idempotent Agent call: package renewal
	// or source coexistence may have changed their absolute deadline.
	existing, _ := repo.GetUserInboundConfig(ctx, user.Username, serverID, inboundTag)
	if existing != nil && existing.Protocol == ib.Protocol {
		return nil, false, fmt.Errorf("existing credential requires deadline refresh")
	}

	// DB 无记录 → 走 getOrCreateInboundCredential:全局锁内按 email 复用 agent 已有 client / 生成新凭据 + 立即写 DB。
	// 并发时两条路径拿到同一份 canonical 凭据(同 uuid),batch-apply / add-client 按 id 幂等,永不产生重复子账户。
	// flow(VLESS Reality)继承 + 写 DB 都在其内部完成。
	credential, credJSON, _, err := getOrCreateInboundCredential(ctx, repo, user, serverID, inboundTag, ib.Protocol, ib.Settings)
	if err != nil {
		return nil, false, fmt.Errorf("generate credential: %w", err)
	}
	// The legacy batch endpoint has no acknowledged deadline contract. Any
	// expiring source must use add-client so an old Agent cannot silently accept
	// the credential while ignoring not_after.
	now := time.Now().UTC()
	hasManaged, managedExpiry, err := repo.HasEffectiveUserInboundAccess(ctx, user.Username, serverID, inboundTag, 0, now)
	if err != nil {
		return nil, false, err
	}
	hasPackage, packageExpiry, err := hasLegacyPackageInboundAccess(ctx, repo, user.Username, serverID, inboundTag, now)
	if err != nil {
		return nil, false, err
	}
	_, notAfter := laterOptionalExpiry(hasManaged, managedExpiry, hasPackage, packageExpiry)
	if notAfter != nil {
		return nil, false, fmt.Errorf("expiring credential requires atomic add-client")
	}
	return &InboundClientAddItem{
		Username:       user.Username,
		ServerID:       serverID,
		InboundTag:     inboundTag,
		Protocol:       ib.Protocol,
		Credential:     credential,
		CredentialJSON: credJSON,
	}, true, nil
}

// applyInboundBatchOrFallback per-server 收集到的 inbound add-client items 一次 batch-apply 提交,
// 失败时降级到逐项 addUserToInbound(老 agent 不支持 batch-apply 也走这条)。
// 返回收集到的 warning(供前端 toast),空切片=全成功。
func applyInboundBatchOrFallback(ctx context.Context, rm *RemoteManageHandler, repo *storage.TrafficRepository, serverID int64, items []InboundClientAddItem, label string) []string {
	if len(items) == 0 {
		return nil
	}
	err := applyInboundClientsBatchToAgent(ctx, rm, repo, serverID, items)
	if err == nil {
		return nil
	}
	if err != ErrAgentBatchNotSupported {
		log.Printf("[%s] inbound batch-apply server=%d failed: %v — falling back to per-item", label, serverID, err)
	} else {
		log.Printf("[%s] agent server=%d 不支持 batch-apply,fallback per-item", label, serverID)
	}

	var warnings []string
	for _, it := range items {
		user := storage.User{Username: it.Username}
		if ferr := addUserToInbound(ctx, rm, repo, user, it.ServerID, it.InboundTag); ferr != nil {
			log.Printf("[%s] fallback addUserToInbound user=%s server=%d tag=%s: %v",
				label, it.Username, it.ServerID, it.InboundTag, ferr)
			warnings = append(warnings, fmt.Sprintf("节点 %s 添加用户 %s 失败", it.InboundTag, it.Username))
		}
	}
	return warnings
}

// InboundClientAddItem 描述一次 per-server batch 中的单条"加 client"操作。
// 调用方按 ServerID 聚合后一次性传给 applyInboundClientsBatchToAgent。
//
// 字段使用规则:
//   - InboundTag / Credential 给 agent batch-apply 用,生成 add-client 操作
//   - Username / ServerID / Protocol / CredentialJSON 给 master DB 写 user_inbound_configs 用
//
// 调用方必须保证 (Username, ServerID, InboundTag) 三元组在 master DB 中**不存在**
// (即:已过滤 GetUserInboundConfig 返回非 nil 的情况),否则会写入重复行。
type InboundClientAddItem struct {
	Username       string
	ServerID       int64
	InboundTag     string
	Protocol       string
	Credential     map[string]interface{}
	CredentialJSON string
}

// applyInboundClientsBatchToAgent 把同一 server 上多个用户加 client 的操作合并成 1 次
// POST /api/child/batch-apply,显著减少跨海外往返耗时:
//   - 现状:每个 (user, inbound) 一次 GET /api/child/inbounds + 一次 add-client → N 次 round-trip + agent inboundsMu 串行
//   - 改造:0 次 GET(cred 用 master InboundCache 算)+ 1 次 batch-apply → 整 server 1 次 round-trip
//
// 老 agent(无 batch-apply 端点)→ 返回 ErrAgentBatchNotSupported,caller 应 fallback 逐个 addUserToInbound。
// 全成功 → 批量 SaveUserInboundConfig 写 DB,返回 nil。
// agent 个别 item 报 err(如 inbound 不存在)→ 跳过该 item 的 DB 写入,其它仍写入,函数仍返回 nil。
func applyInboundClientsBatchToAgent(ctx context.Context, rm *RemoteManageHandler, repo *storage.TrafficRepository, serverID int64, items []InboundClientAddItem) error {
	if len(items) == 0 {
		return nil
	}
	usernames := make([]string, 0, len(items))
	for _, item := range items {
		usernames = append(usernames, item.Username)
	}
	return repo.WithUsersProvisioningLease(ctx, usernames, func() error {
		return applyInboundClientsBatchToAgentLocked(ctx, rm, serverID, items)
	})
}

func applyInboundClientsBatchToAgentLocked(ctx context.Context, rm *RemoteManageHandler, serverID int64, items []InboundClientAddItem) error {

	type batchInboundClient struct {
		Tag    string                 `json:"tag"`
		Client map[string]interface{} `json:"client"`
	}
	type batchReq struct {
		InboundClients []batchInboundClient `json:"inbound_clients"`
		// NoRestart=true:agent 端只 replaceRuntimeInbound 热更新,不重启 xray。
		// 加 client 是 HandlerService 热生效的场景,完全不需要 restart。
		NoRestart bool `json:"no_restart,omitempty"`
	}

	req := batchReq{NoRestart: true}
	for _, it := range items {
		req.InboundClients = append(req.InboundClients, batchInboundClient{
			Tag:    it.InboundTag,
			Client: it.Credential,
		})
	}
	body, _ := json.Marshal(req)

	raw, err := rm.forwardToRemoteServer(ctx, serverID, "POST", "/api/child/batch-apply", body)
	if err != nil {
		low := strings.ToLower(err.Error())
		if strings.Contains(low, "404") || strings.Contains(low, "not found") || strings.Contains(low, "method not allowed") {
			return ErrAgentBatchNotSupported
		}
		return fmt.Errorf("inbound batch-apply server=%d: %w", serverID, err)
	}

	var resp struct {
		Success         bool     `json:"success"`
		InboundResults  []string `json:"inbound_results"`
		RuntimeWarnings []string `json:"runtime_warnings"`
		Message         string   `json:"message"`
	}
	if jerr := json.Unmarshal(raw, &resp); jerr != nil {
		return fmt.Errorf("parse batch-apply response: %w", jerr)
	}
	if !resp.Success {
		return fmt.Errorf("batch-apply rejected: %s", resp.Message)
	}
	if len(resp.InboundResults) != len(items) {
		return fmt.Errorf("batch-apply returned incomplete item results")
	}
	needsRestart := len(resp.RuntimeWarnings) > 0
	for i, result := range resp.InboundResults {
		noOp, resultErr := validateAgentBatchItemResult(result)
		if resultErr != nil {
			return fmt.Errorf("batch-apply item %d failed: %w", i, resultErr)
		}
		needsRestart = needsRestart || noOp
	}
	if needsRestart {
		if err := rm.restartXrayWithRecovery(ctx, serverID, "InboundBatchApply"); err != nil {
			return fmt.Errorf("apply persisted inbound batch to Xray: %w", err)
		}
	}

	return nil
}

func validateAgentBatchItemResult(result string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(result)) {
	case "ok":
		return false, nil
	case "ok (no-op)":
		return true, nil
	default:
		return false, fmt.Errorf("Agent returned unacknowledged result %q", result)
	}
}
