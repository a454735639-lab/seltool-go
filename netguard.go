package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// 出站请求防护。
//
// 背景：MoviePilot 的 /api/v1/site/icon/<id> 会返回一个 {"data":{"url": "..."}}，
// 旧实现拿这个 url 直接 GET，于是第三方（或中间人）只要返回
// http://169.254.169.254/... 就能让服务端去访问内网/云元数据，并把响应体
// 转成 data URI 存进图标表、再由面板渲染出来 —— 一个无需管理员点击的 SSRF
// （自动导入结束后会自动触发图标同步）。
//
// 这里提供两件事：
//  1. guardOutboundURL：在发请求前校验 scheme 与主机字面量；
//  2. guardedDialer：真正建连时再解析一次并校验 IP，防 DNS rebinding
//     （域名第一次解析到公网、第二次解析到 127.0.0.1 的绕过手法）。
//
// 注意：这里只拦「服务端替第三方去取」的场景。目标站点自动化访问 PT 站属于
// 正常业务，不走这里。

var (
	errBlockedScheme = errors.New("仅允许 http/https")
	errBlockedHost   = errors.New("目标地址位于内网/回环/链路本地，已拒绝")
)

// guardOutboundURL 做请求前的静态校验。
func guardOutboundURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("地址无法解析: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return errBlockedScheme
	}
	host := u.Hostname()
	if host == "" {
		return errors.New("地址缺少主机名")
	}
	// 主机字面量直接判断（IP 或 localhost 等）。
	if ip := net.ParseIP(host); ip != nil {
		if !isPublicIP(ip) {
			return errBlockedHost
		}
		return nil
	}
	lower := strings.ToLower(strings.TrimSuffix(host, "."))
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") ||
		strings.HasSuffix(lower, ".internal") || strings.HasSuffix(lower, ".local") {
		return errBlockedHost
	}
	return nil
}

// isPublicIP 判断是否可安全出站。私网、回环、链路本地（含 169.254.169.254
// 云元数据）、未指定、组播、以及 IPv6 唯一本地地址一律拒绝。
func isPublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return false
	}
	// IPv4 映射的 IPv6 再判一次，避免 ::ffff:127.0.0.1 这类写法。
	if v4 := ip.To4(); v4 != nil {
		if v4[0] == 0 || v4[0] == 127 || v4[0] == 10 {
			return false
		}
		if v4[0] == 192 && v4[1] == 168 {
			return false
		}
		if v4[0] == 172 && v4[1] >= 16 && v4[1] <= 31 {
			return false
		}
		if v4[0] == 169 && v4[1] == 254 {
			return false
		}
		if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 { // CGNAT 100.64/10
			return false
		}
		if v4[0] == 192 && v4[1] == 0 && v4[2] == 0 { // 192.0.0/24 IETF 保留
			return false
		}
		if v4[0] == 198 && (v4[1] == 18 || v4[1] == 19) { // 198.18/15 基准测试
			return false
		}
		if v4[0] >= 240 { // 240/4 保留 + 255.255.255.255 广播
			return false
		}
	}
	return true
}

// guardDialAddress 解析 host:port 并确认所有解析结果都是公网地址，
// 返回可以直接拨号的 IP:port（用已校验的地址，避免二次解析被换掉）。
func guardDialAddress(ctx context.Context, addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return "", err
	}
	if len(ips) == 0 {
		return "", fmt.Errorf("解析 %s 无结果", host)
	}
	for _, a := range ips {
		if !isPublicIP(a.IP) {
			return "", fmt.Errorf("%w（%s → %s）", errBlockedHost, host, a.IP)
		}
	}
	return net.JoinHostPort(ips[0].IP.String(), port), nil
}

// guardedDialer 在建立 TCP 连接前校验解析出的每一个 IP。
// 只要有一个落在内网就整体拒绝（不做「挑一个能连的」）。
func guardedDialer(timeout time.Duration) func(ctx context.Context, network, addr string) (net.Conn, error) {
	d := &net.Dialer{Timeout: timeout}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		target, err := guardDialAddress(ctx, addr)
		if err != nil {
			return nil, err
		}
		return d.DialContext(ctx, network, target)
	}
}

// newGuardedClient 返回一个禁止直连内网的 HTTP 客户端。
// 不使用环境代理（这类取图请求本就是服务端直连语义），并限制重定向跳数、
// 每一跳都重新校验目标。
func newGuardedClient(timeout time.Duration) *http.Client {
	dial := guardedDialer(timeout)
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           dial,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
		// 显式指定，避免继承环境里的 http_proxy 造成出口不一致。
		ForceAttemptHTTP2: true,
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("重定向次数过多")
			}
			return guardOutboundURL(req.URL.String())
		},
	}
}
