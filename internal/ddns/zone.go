package ddns

import (
	"errors"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// ErrNotFQDN — 传入的不是合法可被 DDNS 同步的 FQDN(单段 / 含 IP / 含通配符等)
var ErrNotFQDN = errors.New("not a domain name suitable for DDNS")

// SplitFQDN 把 fqdn 切成 (zone, sub),根据公共后缀列表识别注册域:
//
//	"jp1.example.com"        → "example.com", "jp1"
//	"jp1.api.example.co.uk"  → "example.co.uk", "jp1.api"   (复合 TLD)
//	"example.com"            → "example.com", "@"           (根域,@ 是 DNS 行业惯例)
//	"1.2.3.4"                → "", "", ErrNotFQDN
//	"localhost"              → "", "", ErrNotFQDN
//
// 使用 golang.org/x/net/publicsuffix 处理 co.uk 这类复合后缀,避免错切。
func SplitFQDN(fqdn string) (zone, sub string, err error) {
	fqdn = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(fqdn, ".")))
	if fqdn == "" || !strings.Contains(fqdn, ".") {
		return "", "", ErrNotFQDN
	}
	root, icannErr := publicsuffix.EffectiveTLDPlusOne(fqdn)
	if icannErr != nil || root == "" {
		return "", "", ErrNotFQDN
	}
	if fqdn == root {
		return root, "@", nil
	}
	// 期望 fqdn 是 "sub.<root>" 形式
	if !strings.HasSuffix(fqdn, "."+root) {
		return "", "", ErrNotFQDN
	}
	sub = strings.TrimSuffix(fqdn, "."+root)
	return root, sub, nil
}
