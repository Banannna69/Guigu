// loginlimiter_test.go：登录失败限流与来源 IP 解析的单元测试。
// 主要函数/类型：
//   - TestLoginLimiterLocksByUserAndIP：按账号/按 IP 双维度达阈值即锁定。
//   - TestLoginLimiterResetsOnSuccess：成功登录清零计数。
//   - TestLoginLimiterDisabled：策略关闭时永不锁定。
//   - TestClientIPFromTrustsProxyOnlyWhenConfigured：只对可信代理采信 XFF。

package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func testPolicy() loginPolicy {
	return loginPolicy{
		enabled:       true,
		userThreshold: 3,
		ipThreshold:   5,
		window:        time.Minute,
		cooldown:      time.Minute,
	}
}

// TestLoginLimiterLocksByUserAndIP 覆盖两个维度的意义：只按账号锁可被用来锁死
// 合法用户，只按 IP 锁可被换来源绕过；两桶都得生效。
func TestLoginLimiterLocksByUserAndIP(t *testing.T) {
	pol := testPolicy()

	t.Run("账号维度", func(t *testing.T) {
		l := newLoginLimiter()
		// 每次换 IP，确保是账号桶先到阈值。
		for i := 0; i < pol.userThreshold; i++ {
			ip := "10.0.0." + string(rune('1'+i))
			if locked, _ := l.locked(ip, AdminUsername); locked {
				t.Fatalf("第 %d 次失败前不应已锁定", i+1)
			}
			l.fail(ip, AdminUsername, pol)
		}
		locked, retry := l.locked("10.9.9.9", AdminUsername)
		if !locked {
			t.Fatal("账号连续失败达阈值后应锁定，即使换了来源 IP")
		}
		if retry <= 0 {
			t.Fatalf("锁定时应返回剩余冷却时长，得到 %v", retry)
		}
	})

	t.Run("IP维度", func(t *testing.T) {
		l := newLoginLimiter()
		// 每次换用户名，确保是 IP 桶先到阈值。
		for i := 0; i < pol.ipThreshold; i++ {
			l.fail("1.2.3.4", "user"+string(rune('a'+i)), pol)
		}
		locked, _ := l.locked("1.2.3.4", "another")
		if !locked {
			t.Fatal("单一来源横扫多个用户名应被按 IP 锁定")
		}
	})

	t.Run("未达阈值不锁", func(t *testing.T) {
		l := newLoginLimiter()
		l.fail("5.6.7.8", AdminUsername, pol)
		if locked, _ := l.locked("5.6.7.8", AdminUsername); locked {
			t.Fatal("一次失败不应锁定")
		}
	})
}

// TestLoginLimiterResetsOnSuccess 保证登录成功清零，否则用户改对密码后仍被旧计数锁住。
func TestLoginLimiterResetsOnSuccess(t *testing.T) {
	pol := testPolicy()
	l := newLoginLimiter()
	for i := 0; i < pol.userThreshold-1; i++ {
		l.fail("9.9.9.9", AdminUsername, pol)
	}
	l.reset("9.9.9.9", AdminUsername)
	// 清零后应重新获得完整的阈值次数。
	for i := 0; i < pol.userThreshold-1; i++ {
		l.fail("9.9.9.9", AdminUsername, pol)
	}
	if locked, _ := l.locked("9.9.9.9", AdminUsername); locked {
		t.Fatal("成功登录后计数应清零，不应提前锁定")
	}
}

// TestLoginLimiterDisabled 保证策略关闭时完全不记状态。
func TestLoginLimiterDisabled(t *testing.T) {
	pol := testPolicy()
	pol.enabled = false
	l := newLoginLimiter()
	for i := 0; i < 100; i++ {
		l.fail("1.1.1.1", AdminUsername, pol)
	}
	if locked, _ := l.locked("1.1.1.1", AdminUsername); locked {
		t.Fatal("策略关闭时不应锁定")
	}
}

// TestClientIPFromTrustsProxyOnlyWhenConfigured 是安全要点：X-Forwarded-For 由
// 客户端任意伪造，未配置可信代理时采信它等于换个头就绕过按 IP 限流。
func TestClientIPFromTrustsProxyOnlyWhenConfigured(t *testing.T) {
	mk := func(remote, xff string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
		r.RemoteAddr = remote
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		return r
	}

	t.Run("默认不采信", func(t *testing.T) {
		t.Setenv("ARTEX_TRUSTED_PROXIES", "")
		if got := clientIPFrom(mk("203.0.113.5:1234", "1.2.3.4")); got != "203.0.113.5" {
			t.Fatalf("未配置可信代理时应使用直连地址，得到 %q", got)
		}
	})

	t.Run("可信代理时采信最左一跳", func(t *testing.T) {
		t.Setenv("ARTEX_TRUSTED_PROXIES", "127.0.0.1")
		if got := clientIPFrom(mk("127.0.0.1:1234", "1.2.3.4, 10.0.0.1")); got != "1.2.3.4" {
			t.Fatalf("应取 XFF 最左一跳，得到 %q", got)
		}
	})

	t.Run("CIDR 形式", func(t *testing.T) {
		t.Setenv("ARTEX_TRUSTED_PROXIES", "10.0.0.0/8")
		if got := clientIPFrom(mk("10.1.2.3:1234", "198.51.100.7")); got != "198.51.100.7" {
			t.Fatalf("CIDR 内的代理应被信任，得到 %q", got)
		}
	})

	t.Run("伪造头不得影响直连", func(t *testing.T) {
		t.Setenv("ARTEX_TRUSTED_PROXIES", "10.0.0.0/8")
		if got := clientIPFrom(mk("203.0.113.5:1234", "1.2.3.4")); got != "203.0.113.5" {
			t.Fatalf("非可信来源伪造 XFF 必须被忽略，得到 %q", got)
		}
	})
}
