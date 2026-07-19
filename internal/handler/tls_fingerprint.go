package handler

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	xtls "github.com/xtls/xray-core/transport/internet/tls"
)

// fetchPeerCertSha256 对 address:port 做 TLS handshake,返回第一张 peer cert 的 SHA256(hex)。
//
// 双栈策略:
//  1. 首选 Go 标准库 crypto/tls.Dial(开销最小,大多数 TLS 服务能直接拿到证书)
//  2. 失败时回退到 xray 自带 uTLS(模拟 Chrome ClientHelloID)— 应对 reality / WAF 等会
//     按 ClientHello fingerprint 拒绝标准库 TLS 的目标;算法 sha256(cert.Raw) 与 `xray tls ping` 输出一致
//
// 参数:
//   - InsecureSkipVerify=true:我们就是要任意证书的 sha256,不要被 chain 校验失败挡掉
//   - ServerName 取 sni;为空则用 address(避免空 SNI 命中默认证书)
//   - alpn 用 "," 分隔(跟 xray tlsSettings.alpn 同源),空则不设
//   - ctx 超时优先;无超时时 fallback 10s 不让线程卡死
func fetchPeerCertSha256(ctx context.Context, address string, port int, sni, alpn string) (string, error) {
	addr := strings.TrimSpace(address)
	if addr == "" {
		return "", errors.New("address required")
	}
	if port <= 0 || port > 65535 {
		return "", fmt.Errorf("invalid port: %d", port)
	}
	servername := strings.TrimSpace(sni)
	if servername == "" {
		servername = addr
	}

	cfg := buildTLSConfig(servername, alpn)

	// 路径 1:Go 标准库
	if sha, err := fetchViaStdLib(ctx, addr, port, cfg); err == nil {
		return sha, nil
	} else {
		// 标准库失败 → 尝试 xray uTLS(Chrome fingerprint)
		if sha2, err2 := fetchViaXrayUTLS(ctx, addr, port, cfg); err2 == nil {
			return sha2, nil
		} else {
			return "", fmt.Errorf("std-tls failed: %v; xray-utls failed: %v", err, err2)
		}
	}
}

// buildTLSConfig 构造 InsecureSkipVerify + SNI + ALPN 的标准 tls.Config。
// xray 的 uTLS 包内部会把它复制成 utls.Config(见 xray-core/transport/internet/tls.copyConfig)。
func buildTLSConfig(servername, alpn string) *tls.Config {
	cfg := &tls.Config{
		InsecureSkipVerify: true, // #nosec G402 -- 目的就是无校验拿任意 peer cert sha256
		ServerName:         servername,
	}
	if a := strings.TrimSpace(alpn); a != "" {
		parts := strings.Split(a, ",")
		for i, p := range parts {
			parts[i] = strings.TrimSpace(p)
		}
		cfg.NextProtos = parts
	}
	return cfg
}

// dialTimeout 把 ctx deadline 转成 dialer timeout,默认 10s。
func dialTimeout(ctx context.Context, fallback time.Duration) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		if rem := time.Until(deadline); rem > 0 && rem < fallback {
			return rem
		}
	}
	return fallback
}

// fetchViaStdLib 用 crypto/tls.Dial 拿 peer cert sha256
func fetchViaStdLib(ctx context.Context, addr string, port int, cfg *tls.Config) (string, error) {
	dialer := &net.Dialer{Timeout: dialTimeout(ctx, 10*time.Second)}
	conn, err := tls.DialWithDialer(dialer, "tcp",
		net.JoinHostPort(addr, strconv.Itoa(port)), cfg)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return "", errors.New("no peer certificate received (stdlib)")
	}
	sum := sha256.Sum256(certs[0].Raw)
	return hex.EncodeToString(sum[:]), nil
}

// fetchViaXrayUTLS 用 xray 自带的 uTLS(Chrome ClientHelloID)路径,跟 `xray tls ping` 等价。
// 关键 import:GeneraticUClient(github.com/xtls/xray-core/transport/internet/tls)。
// 复刻 xray ping.go 里"with SNI"分支:tcp.Dial → UClient → Handshake → cert.Raw
func fetchViaXrayUTLS(ctx context.Context, addr string, port int, cfg *tls.Config) (string, error) {
	timeout := dialTimeout(ctx, 10*time.Second)
	rawConn, err := (&net.Dialer{Timeout: timeout}).DialContext(ctx, "tcp", net.JoinHostPort(addr, strconv.Itoa(port)))
	if err != nil {
		return "", err
	}
	defer rawConn.Close()
	if tcp, ok := rawConn.(*net.TCPConn); ok {
		_ = tcp.SetDeadline(time.Now().Add(timeout))
	}
	uConn := xtls.GeneraticUClient(rawConn, cfg)
	if err := uConn.HandshakeContext(ctx); err != nil {
		return "", err
	}
	certs := uConn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return "", errors.New("no peer certificate received (xray-utls)")
	}
	// 跟 xray tls ping 输出的 "Cert's leaf SHA256" 用同一算法(GenerateCertHashHex)
	return xtls.GenerateCertHashHex(certs[0].Raw), nil
}
