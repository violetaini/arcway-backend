// Package probe 提供 UDP 协议节点的延迟探测,补 tcping(纯 TCP DialTimeout)对 hy2/hysteria/tuic 这类
// QUIC-based UDP 协议永远 fail 的缺口。
//
// 实现原理(RFC 9000 §6.2):客户端发 QUIC v1 Initial 包时若用未知 version(如 0x0a0a0a0a GREASE),
// 服务端**必须**回 Version Negotiation 包。任何 QUIC 服务端都遵守这一规范,所以无论 hy2 / tuic / hysteria
// 都能用同款探测路径触发响应。
//
// 任何 UDP 响应字节(VN 包 / Initial response / TLS alert 等)都算"server 活着",
// 不解析内容、不验证合法性,time.Since(send) = RTT。
package probe

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// udpProtocols 需要走 UDP 探测路径的协议集合(小写)。
// 未列入的协议(vless/vmess/trojan/shadowsocks/anytls 等)会被 tcping handler 走 TCP DialTimeout 路径,
// 因为它们要么本身是 TCP-based,要么在 TCP/443 也开了 TLS 握手端口可探测。
var udpProtocols = map[string]bool{
	"hysteria2": true,
	"hy2":       true,
	"hysteria":  true,
	"tuic":      true,
}

// IsUDPProtocol 判断 protocol 是否需要 UDP 探测路径替代 TCP tcping。
// 空字符串返回 false(向后兼容 — 前端不传 protocol 时走原 TCP 路径)。
func IsUDPProtocol(protocol string) bool {
	return udpProtocols[strings.ToLower(strings.TrimSpace(protocol))]
}

// UDPProbe 对 host:port 做协议感知 UDP 探测,返回 RTT 和 error。
// timeout 是总超时(socket read deadline)。
//
// 当前实现对所有 QUIC-based 协议(hysteria2/hy2/hysteria/tuic)走同款 QUIC VN trigger 路径;
// 其它 protocol 返回 error("udping: protocol %q not supported"),调用方应该走 TCP 或别的兜底。
func UDPProbe(ctx context.Context, host string, port int, protocol string, timeout time.Duration) (time.Duration, error) {
	proto := strings.ToLower(strings.TrimSpace(protocol))
	switch proto {
	case "hysteria2", "hy2", "hysteria", "tuic":
		return quicVNProbe(ctx, host, port, timeout)
	default:
		return 0, fmt.Errorf("udping: protocol %q not supported (only QUIC-based: hysteria/hysteria2/tuic)", protocol)
	}
}

// quicVNProbe 发一个 QUIC v1 Initial 包(version 用 GREASE 0x0a0a0a0a 触发 Version Negotiation),
// 等待 server 回任意 UDP 包。time.Since(send) = RTT。
//
// host 支持 IP 或域名;域名走系统 DNS 解析,失败立即返回 error。
// 不验证响应内容 — 只要收到 ≥1 字节就算 server 活着。
func quicVNProbe(ctx context.Context, host string, port int, timeout time.Duration) (time.Duration, error) {
	// 解析地址:支持 IP 直接用,域名走 DNS。
	addr := &net.UDPAddr{Port: port}
	if ip := net.ParseIP(host); ip != nil {
		addr.IP = ip
	} else {
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return 0, fmt.Errorf("resolve %s: %w", host, err)
		}
		if len(ips) == 0 {
			return 0, fmt.Errorf("resolve %s: no addresses", host)
		}
		addr.IP = ips[0].IP
	}

	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		return 0, fmt.Errorf("dial udp: %w", err)
	}
	defer conn.Close()

	pkt, err := buildQUICInitialGREASEVN()
	if err != nil {
		return 0, err
	}

	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return 0, fmt.Errorf("set read deadline: %w", err)
	}

	start := time.Now()
	if _, err := conn.Write(pkt); err != nil {
		return 0, fmt.Errorf("write: %w", err)
	}

	buf := make([]byte, 1500)
	n, err := conn.Read(buf)
	rtt := time.Since(start)
	if err != nil {
		return 0, fmt.Errorf("no response within %v: %w", timeout, err)
	}
	if n == 0 {
		return 0, errors.New("empty response")
	}
	return rtt, nil
}

// buildQUICInitialGREASEVN 构造 1200 字节的 QUIC v1 Initial 包,version=GREASE(0x0a0a0a0a)。
//
// 包结构(RFC 9000 §17.2):
//
//	flags (1)            = 0xc0  // Long Header + Fixed + Initial Type + reserved 0 + PN length 1
//	version (4)          = 0x0a0a0a0a  // GREASE → server 必须回 Version Negotiation
//	DCID len (1)         = 8
//	DCID (8)             = random
//	SCID len (1)         = 8
//	SCID (8)             = random
//	Token len varint (1) = 0
//	Length varint (2)    = pnLen + paddingLen
//	Packet Number (1)    = 0x00
//	PADDING frames       = 全 0x00 字节填到 1200 总长
//
// 1200 字节是 RFC 9000 §14.1 强制(防 amplification attack)— 小于 1200 的 Initial 包会被 server 丢弃。
func buildQUICInitialGREASEVN() ([]byte, error) {
	const total = 1200
	const headerWithoutLength = 24 // flags+version+dcidLen+dcid+scidLen+scid+tokenLen
	const lengthFieldBytes = 2
	const pnLen = 1
	paddingLen := total - headerWithoutLength - lengthFieldBytes - pnLen

	var dcid [8]byte
	if _, err := rand.Read(dcid[:]); err != nil {
		return nil, fmt.Errorf("rand dcid: %w", err)
	}
	var scid [8]byte
	if _, err := rand.Read(scid[:]); err != nil {
		return nil, fmt.Errorf("rand scid: %w", err)
	}

	buf := make([]byte, 0, total)
	buf = append(buf, 0xc0)
	buf = append(buf, 0x0a, 0x0a, 0x0a, 0x0a)
	buf = append(buf, 8)
	buf = append(buf, dcid[:]...)
	buf = append(buf, 8)
	buf = append(buf, scid[:]...)
	buf = append(buf, 0x00) // Token Length varint = 0(1 字节)

	// Length 字段:varint 2 字节编码,顶 2 bits = 0b10,低 14 bits = value
	lengthValue := pnLen + paddingLen
	if lengthValue > 16383 {
		return nil, fmt.Errorf("quic Length field overflow: %d > 16383", lengthValue)
	}
	lenBytes := make([]byte, 2)
	binary.BigEndian.PutUint16(lenBytes, uint16(lengthValue)|0x4000)
	buf = append(buf, lenBytes...)

	buf = append(buf, 0x00)                        // Packet Number 1 byte
	buf = append(buf, make([]byte, paddingLen)...) // PADDING frames (0x00 == PADDING frame)

	if len(buf) != total {
		return nil, fmt.Errorf("buildQUICInitialGREASEVN: got %d bytes, want %d", len(buf), total)
	}
	return buf, nil
}
