package traffic

import (
	"sync"
	"time"
)

// XrayRestartDetector 用 agent 上报的 xray_boot_time 作为权威信号,判断 server 上的 xray
// 进程在两次 collector tick 之间是否真的重启过 — 区别于 inbound client 增删导致的
// "user 维度 stats counter reset"(后者是历史 bug 来源,把 client 重加误判为 xray 重启)。
//
// 工作模式:in-memory cache,key=serverID,value=上次见到的 xray_boot_time。
// 每次 collector ProcessRemoteMetrics 入口调一次,把结果传给下游 Upsert*。
type XrayRestartDetector struct {
	mu    sync.Mutex
	cache map[int64]time.Time
}

func NewXrayRestartDetector() *XrayRestartDetector {
	return &XrayRestartDetector{cache: make(map[int64]time.Time)}
}

// CheckAndUpdate 返回本次 tick 是否需要把该 server 的 stats 视为"xray 真重启后"处理。
//
//   - bootTime == nil(老 agent 没上报 / db 字段为空) → false,走"非重启"路径
//     保守地不污染 total。极端情况下漏统计真重启那一刻,可接受。
//   - 首次见到 server → 记入 cache,返回 false 避免冷启动误触发(此时 last_*=0,
//     正常 delta 算法对首条记录有"!exists 分支"兜底,不需要重启逻辑介入)。
//   - cache 中 bootTime 跟入参不同 → 真重启,返回 true,更新 cache。
//   - cache 中 bootTime 相同 → 返回 false。
func (d *XrayRestartDetector) CheckAndUpdate(serverID int64, bootTime *time.Time) bool {
	if bootTime == nil || bootTime.IsZero() {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	prev, seen := d.cache[serverID]
	d.cache[serverID] = *bootTime
	if !seen {
		// 冷启动:不强制触发重启逻辑,让 Upsert 的 !exists 分支 / 正常 delta 处理
		return false
	}
	return !prev.Equal(*bootTime)
}
