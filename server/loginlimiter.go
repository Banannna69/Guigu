// loginlimiter.go：登录失败限流器（按来源 IP 与账号双维度）。
// 主要函数/类型：
//   - loginLimiter / loginPolicy：内存态滑动窗口计数与可调策略。
//   - locked / fail / reset / sweepLocked：锁定判定、记失败、成功清零与陈旧桶清理。
//   - clientIPFrom：来源 IP 解析（仅对可信代理采信 XFF/X-Real-IP）。
//
// 进程内存态（单实例部署够用，重启清零可接受），不做 DB 写——每次失败都落库会
// 反过来放大攻击面。双维度的意义：只按账号锁，攻击者可拿错误口令刷某账号把合法
// 用户锁死（DoS）；叠加按 IP 锁，单一来源横扫多个用户名也会被按 IP 挡下。两桶
// 任一命中即拒绝。

package server

import (
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"artex/db"
)

type loginBucket struct {
	count       int
	windowStart time.Time
	lockedUntil time.Time
}

type loginLimiter struct {
	mu      sync.Mutex
	buckets map[string]*loginBucket
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{buckets: map[string]*loginBucket{}}
}

// loginPolicy 是限流器的可调参数，从 settings 读取（未设则用安全默认）。
type loginPolicy struct {
	enabled       bool
	userThreshold int
	ipThreshold   int
	window        time.Duration
	cooldown      time.Duration
}

// loginPolicySnapshot 组装当前策略：各项可用 settings 覆盖，非法值回退默认。
func (s *Server) loginPolicySnapshot() loginPolicy {
	pol := loginPolicy{
		enabled:       true,
		userThreshold: 10,
		ipThreshold:   30,
		window:        15 * time.Minute,
		cooldown:      15 * time.Minute,
	}
	if s.m == nil || s.m.pg == nil {
		return pol
	}
	pg := s.m.pg
	pol.enabled = pg.GetBool("auth.lockout_enabled", true)
	if v := settingInt(pg, "auth.lockout_user_threshold"); v > 0 {
		pol.userThreshold = v
	}
	if v := settingInt(pg, "auth.lockout_ip_threshold"); v > 0 {
		pol.ipThreshold = v
	}
	if v := settingInt(pg, "auth.lockout_window_minutes"); v > 0 {
		pol.window = time.Duration(v) * time.Minute
	}
	if v := settingInt(pg, "auth.lockout_cooldown_minutes"); v > 0 {
		pol.cooldown = time.Duration(v) * time.Minute
	}
	return pol
}

// settingInt reads an integer setting, returning 0 when unset or unparseable so
// callers fall back to their defaults.
func settingInt(pg *db.DB, key string) int {
	raw, ok, err := pg.GetSetting(key)
	if err != nil || !ok {
		return 0
	}
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || v <= 0 {
		return 0
	}
	return v
}

func ipKey(ip string) string     { return "ip:" + ip }
func userKey(user string) string { return "u:" + user }

// locked 报告该 IP 或账号是否处于锁定冷却中，并返回剩余冷却时长（取两桶较大者）。
func (l *loginLimiter) locked(ip, user string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	var remain time.Duration
	for _, k := range []string{ipKey(ip), userKey(user)} {
		if b := l.buckets[k]; b != nil && now.Before(b.lockedUntil) {
			if r := time.Until(b.lockedUntil); r > remain {
				remain = r
			}
		}
	}
	return remain > 0, remain
}

// fail 记一次失败到 IP 桶与账号桶。返回 true 表示本次失败使某个桶【新进入】锁定
// 状态（供调用方写日志）。策略关闭时不记任何状态，locked 永远为假。
func (l *loginLimiter) fail(ip, user string, pol loginPolicy) bool {
	if !pol.enabled {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.sweepLocked(now, pol)
	newlyLocked := false
	if l.recordLocked(ipKey(ip), now, pol.window, pol.cooldown, pol.ipThreshold) {
		newlyLocked = true
	}
	if user != "" && l.recordLocked(userKey(user), now, pol.window, pol.cooldown, pol.userThreshold) {
		newlyLocked = true
	}
	return newlyLocked
}

// recordLocked 在一个桶上记一次失败，必要时滑动窗口，达阈值则设定冷却到期时间。
// 返回 true 表示本次恰好触发锁定。调用方持有 l.mu。
func (l *loginLimiter) recordLocked(key string, now time.Time, window, cooldown time.Duration, threshold int) bool {
	b := l.buckets[key]
	if b == nil {
		b = &loginBucket{windowStart: now}
		l.buckets[key] = b
	}
	if now.Sub(b.windowStart) > window {
		b.windowStart = now
		b.count = 0
	}
	b.count++
	if b.count >= threshold {
		b.lockedUntil = now.Add(cooldown)
		b.count = 0
		b.windowStart = now
		return true
	}
	return false
}

// reset 清掉该 IP 与账号的失败计数（登录成功时调用）。
func (l *loginLimiter) reset(ip, user string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.buckets, ipKey(ip))
	delete(l.buckets, userKey(user))
}

// sweepLocked 在桶数量偏大时清掉既不在锁定中、窗口也已过期的陈旧桶，防止海量不同
// 用户名/来源把 map 撑大成内存压力。调用方持有 l.mu。
func (l *loginLimiter) sweepLocked(now time.Time, pol loginPolicy) {
	if len(l.buckets) < 4096 {
		return
	}
	for k, b := range l.buckets {
		if now.After(b.lockedUntil) && now.Sub(b.windowStart) > pol.window {
			delete(l.buckets, k)
		}
	}
}

// clientIPFrom 取请求来源 IP。
//
// 只在请求来自可信代理时才认 X-Forwarded-For / X-Real-IP：这两个头客户端可任意
// 伪造，无条件采信等于换一个头就绕过按 IP 限流。反向代理部署时把代理地址填进
// ARTEX_TRUSTED_PROXIES（逗号分隔的 CIDR 或裸 IP）。
func clientIPFrom(r *http.Request) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		host = strings.TrimSpace(r.RemoteAddr)
	}
	if !trustedProxy(host) {
		return host
	}
	if v := strings.TrimSpace(r.Header.Get("X-Real-IP")); v != "" {
		if net.ParseIP(v) != nil {
			return v
		}
	}
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		// 取最左一跳（最初的客户端）；后续跳由代理自己追加。
		first := strings.TrimSpace(strings.Split(v, ",")[0])
		if net.ParseIP(first) != nil {
			return first
		}
	}
	return host
}

// trustedProxy 报告 addr 是否在 ARTEX_TRUSTED_PROXIES 里。
func trustedProxy(addr string) bool {
	raw := strings.TrimSpace(os.Getenv("ARTEX_TRUSTED_PROXIES"))
	if raw == "" {
		return false
	}
	ip := net.ParseIP(addr)
	if ip == nil {
		return false
	}
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if strings.Contains(entry, "/") {
			if _, netw, err := net.ParseCIDR(entry); err == nil && netw.Contains(ip) {
				return true
			}
			continue
		}
		if p := net.ParseIP(entry); p != nil && p.Equal(ip) {
			return true
		}
	}
	return false
}
