package handler

import "net"

// IsLocalOrPrivateIP 判定 IP 是否落在 loopback / link-local / 私有网段 / unspecified。
//
// 用途:三个限流器(brute_force / login_rate / subscription_rate)在记账前调用,
// 命中即跳过 — 避免反代/docker 未正确转发 X-Forwarded-For 时,
// 主控 fallback 到 r.RemoteAddr 拿到的本机/网关/内网 IP 被封禁,
// 导致所有真实用户都连不上。
//
// 空串或非法 IP 一律视为"本地"安全降级 — 防"封禁空字符串"导致后续永远命中。
func IsLocalOrPrivateIP(ipStr string) bool {
	if ipStr == "" {
		return true
	}
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return true
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()
}
