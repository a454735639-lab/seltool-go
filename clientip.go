package main

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
)

// 可信反向代理白名单。
//
// 旧实现的门槛是「来源地址属于私网就采信 X-Forwarded-For」。这个前提太宽：
// 同一个 Docker bridge（172.x）、同一个局域网、宿主机上任何一个进程，源地址
// 都是私网。于是任意一台内网机器只要每次带一个不同的 X-Forwarded-For，就能
// 让限速表里永远出现新 key，20 次/10 分钟的登录硬锁被完全绕过；反过来也能
// 伪造受害者的地址，打 20 次失败把人锁在门外。
//
// 现在只有「显式配置为可信代理」的来源才采信转发头，默认只信任回环
// （即本机反代：nginx / caddy / traefik 直接跑在宿主机上）。
// 反代在容器里时，把它的地址或网段写进配置：
//
//	automation:
//	  trusted-proxies: ["172.17.0.0/16"]
//
// 或环境变量 SELTOOL_TRUSTED_PROXIES=172.17.0.0/16,10.0.0.5
var (
	trustedProxyMu   sync.RWMutex
	trustedProxyNets = parseTrustedProxies([]string{"127.0.0.1/32", "::1/128"})
	proxyHintOnce    atomic.Bool
)

// parseTrustedProxies 解析 CIDR 或单个 IP（补成 /32 或 /128）。
// 无法解析的条目直接丢弃：这里宁可少信任，也不能因为一条笔误退回旧行为。
func parseTrustedProxies(list []string) []*net.IPNet {
	var out []*net.IPNet
	for _, raw := range list {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if _, n, err := net.ParseCIDR(raw); err == nil {
			out = append(out, n)
			continue
		}
		if ip := net.ParseIP(raw); ip != nil {
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			out = append(out, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
		}
	}
	return out
}

// setTrustedProxies 覆盖可信代理列表（回环始终隐含在内）。
func setTrustedProxies(list []string) error {
	for _, raw := range list {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if _, _, err := net.ParseCIDR(raw); err != nil && net.ParseIP(raw) == nil {
			return fmt.Errorf("trusted-proxies 配置无效: %q（需要 CIDR 或 IP）", raw)
		}
	}
	nets := parseTrustedProxies(append([]string{"127.0.0.1/32", "::1/128"}, list...))
	trustedProxyMu.Lock()
	trustedProxyNets = nets
	trustedProxyMu.Unlock()
	return nil
}

func isTrustedProxy(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	trustedProxyMu.RLock()
	nets := trustedProxyNets
	trustedProxyMu.RUnlock()
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// peerIP 取 TCP 对端地址（不含端口与 IPv6 zone）。
func peerIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		host = strings.TrimSpace(r.RemoteAddr)
	}
	host = strings.Trim(host, "[]")
	if i := strings.IndexByte(host, '%'); i >= 0 {
		host = host[:i]
	}
	return host
}

// clientIP 取请求来源 IP。
//
// 只有当对端本身是可信代理时才看 X-Forwarded-For，并从右往左找第一个
// 「不是可信代理」的地址 —— 右边是最近一跳追加的，左边是客户端自己可以随便写的。
// 整条链都是可信代理时退回到最左边那个有效地址。
func clientIP(r *http.Request) string {
	peer := peerIP(r)
	if !isTrustedProxy(peer) {
		warnUntrustedForwardedFor(peer, r)
		return peer
	}
	var hops []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		for _, part := range strings.Split(v, ",") {
			if part = strings.TrimSpace(part); part != "" {
				hops = append(hops, part)
			}
		}
	}
	if len(hops) == 0 {
		return peer
	}
	for i := len(hops) - 1; i >= 0; i-- {
		if net.ParseIP(hops[i]) == nil {
			continue
		}
		if !isTrustedProxy(hops[i]) {
			return hops[i]
		}
		if i == 0 {
			return hops[0]
		}
	}
	return peer
}

// warnUntrustedForwardedFor 在「私网来源 + 带转发头 + 未列入白名单」时提示一次，
// 避免用户升级后反代配置没跟上，导致所有人被算成同一个 IP 而互相锁死。
func warnUntrustedForwardedFor(peer string, r *http.Request) {
	ip := net.ParseIP(peer)
	if ip == nil || !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()) {
		return
	}
	if len(r.Header.Values("X-Forwarded-For")) == 0 && r.Header.Get("X-Forwarded-Proto") == "" {
		return
	}
	if !proxyHintOnce.CompareAndSwap(false, true) {
		return
	}
	logf("检测到来自 %s 的转发头，但该地址不在 trusted-proxies 里，已忽略 X-Forwarded-*。"+
		"如果它是你的反向代理，请把地址或网段加入配置 automation.trusted-proxies（或环境变量 SELTOOL_TRUSTED_PROXIES），"+
		"否则所有客户端会共用同一个限速计数", peer)
}

// isHTTPS 判断本次请求是否走 HTTPS。反代终止 TLS 时 r.TLS 恒为 nil，
// 只靠它会让 Secure 永远为 false（会话 cookie 可被明文链路窃取）。
// 因此仅在来源是可信代理时才采信 X-Forwarded-Proto，避免外部客户端伪造该头。
func isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if !isTrustedProxy(peerIP(r)) {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https") {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Ssl")), "on")
}
