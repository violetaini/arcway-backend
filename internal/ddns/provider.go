// Package ddns 把 agent 上报的 IPv4/IPv6 同步到 DNS provider 的 A/AAAA 记录。
// 跟 internal/acme 解耦:acme 只用 TXT 做 DNS-01 验证,DDNS 是常驻 + 并发 + 不同 record type,
// 直接复用 acme/dns_providers.go 里的凭据 JSON key 命名(CF_DNS_API_TOKEN 等)即可,
// 不要复用 SetDNSCredentialEnv(env var 模式不是线程安全的)。
package ddns

import (
	"context"
	"errors"
	"fmt"
)

// Provider 定义一个 DNS 服务商的 A/AAAA 记录 upsert 能力。
// "upsert" 语义:同 fqdn+recordType 已存在 → 更新 content;不存在 → 创建。
// 实现里要内部 lookup zone 并切分 sub(参考 zone.go 的 splitZone)。
type Provider interface {
	// UpsertRecord 把 fqdn 的 recordType 记录指到 content。
	// recordType ∈ {"A", "AAAA"};content 是 IP 字符串。
	// ttl=0 表示让 provider 用默认值(通常是 auto / 120-300s)。
	UpsertRecord(ctx context.Context, fqdn string, recordType string, content string, ttl int) error
}

// ProviderType 跟 dns_providers.provider_type 列、acme/dns_providers.go DNSProviderEnvKeys 的 key 一致。
const (
	ProviderTypeCloudflare   = "cloudflare"
	ProviderTypeAlidns       = "alidns"
	ProviderTypeTencentCloud = "tencentcloud"
	ProviderTypeDNSPod       = "dnspod"
	ProviderTypeNamesilo     = "namesilo"
	ProviderTypeGoDaddy      = "godaddy"
)

// ErrUnsupportedProvider — providerType 不在已实现列表里
var ErrUnsupportedProvider = errors.New("unsupported DNS provider type for DDNS")

// NewProvider 工厂:按 providerType 派发到具体实现。
// credentials 的 key 沿用 acme/dns_providers.go 里的环境变量名(如 CF_DNS_API_TOKEN / ALICLOUD_ACCESS_KEY),
// UI 复用现有 DNS provider 表单,不需要为 DDNS 单独存一份凭据。
func NewProvider(providerType string, credentials map[string]string) (Provider, error) {
	switch providerType {
	case ProviderTypeCloudflare:
		return newCloudflareProvider(credentials)
	case ProviderTypeAlidns:
		return newAlidnsProvider(credentials)
	case ProviderTypeTencentCloud:
		return newTencentProvider(credentials)
	case ProviderTypeDNSPod:
		return newDNSPodProvider(credentials)
	case ProviderTypeNamesilo:
		return newNamesiloProvider(credentials)
	case ProviderTypeGoDaddy:
		return newGoDaddyProvider(credentials)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedProvider, providerType)
	}
}
