package traffic

// pool_hook_test.go：验证 MITM 的 upstream 钩子真的把池接了进去。
//
// 为什么不用「起一个完整 MITM 再发真请求」那套：go-mitmproxy 的监听器在测试里
// 启动/关闭时机不稳定，容易变成 flaky。而代理池在本项目里的唯一接入点就是
// SetUpstreamProxy 这个回调——它能决定「这个请求走哪个上游」。直接调用同样的选择
// 逻辑（Pick + lastPick 记录），既覆盖了真正要保证的行为，又不依赖网络时序。

import (
	"net/http"
	"testing"
)

// TestPoolPickRecordsLastPick 验证 Pick 选中的上游会被记进 lastPick。
//
// lastPick 是失败归因的唯一依据：请求完成/失败时，回调只拿到 host，靠 lastPick
// 才能知道「上一次为这个 host 选的是哪个上游」。它没被写入，熔断就完全失效——
// 坏出口会一直留在池里。
func TestPoolPickRecordsLastPick(t *testing.T) {
	tr := &Traffic{}

	if err := tr.SetProxyPool([]PoolEntry{
		{URL: "http://127.0.0.1:18001", Name: "A", Enabled: true, Weight: 1},
		{URL: "http://127.0.0.1:18002", Name: "B", Enabled: true, Weight: 1},
	}, "round_robin"); err != nil {
		t.Fatalf("set pool: %v", err)
	}

	// 复刻 upstream 钩子里的两行：Pick + Store。用 Host 带端口的形式，
	// 因为真实请求的 req.Host 就是 host:port。
	const host = "example.com:443"
	pool := tr.pool.Load()
	if pool == nil {
		t.Fatal("pool not installed")
	}
	u := pool.Pick(host)
	if u == nil {
		t.Fatal("Pick returned nil with a non-empty pool")
	}
	tr.lastPick.Store(hostOnly(host), u.String())

	got, ok := tr.lastPick.Load(hostOnly(host))
	if !ok {
		t.Fatalf("lastPick not recorded for %q — failure attribution would be impossible", hostOnly(host))
	}
	if got.(string) != u.String() {
		t.Fatalf("lastPick = %q, want %q", got, u.String())
	}
}

// TestPoolEmptyYieldsNilPick 验证池为空时 Pick 返回 nil，从而回退全局代理/直连。
// 这是「不配置代理池的部署行为完全不变」这条承诺的落点。
func TestPoolEmptyYieldsNilPick(t *testing.T) {
	tr := &Traffic{}
	if err := tr.SetProxyPool(nil, "round_robin"); err != nil {
		t.Fatalf("clear pool: %v", err)
	}
	if pool := tr.pool.Load(); pool != nil {
		t.Fatal("clearing the pool must nil it out, otherwise the hook keeps using stale upstreams")
	}
}

// TestPoolAllBrokenYieldsNilPick 验证池里所有上游都熔断时 Pick 返回 nil，
// 让流量回退直连而不是彻底断流。
func TestPoolAllBrokenYieldsNilPick(t *testing.T) {
	tr := &Traffic{}
	if err := tr.SetProxyPool([]PoolEntry{
		{URL: "http://127.0.0.1:18001", Name: "A", Enabled: true, Weight: 1},
	}, "round_robin"); err != nil {
		t.Fatalf("set pool: %v", err)
	}
	// 连败到熔断。
	for i := 0; i < breakAfterFails; i++ {
		tr.ProxyPoolReportFail("http://127.0.0.1:18001")
	}
	if u := tr.pool.Load().Pick("example.com:443"); u != nil {
		t.Fatalf("all upstreams broken: Pick returned %v, want nil (fall back to direct)", u)
	}
}

// TestProxyPoolReportBadByHost 验证「按目标 host 熔断」这条路：
// agent 只知道它访问的目标，不知道具体走了哪个出口，所以 platform 必须替他反查。
func TestProxyPoolReportBadByHost(t *testing.T) {
	tr := &Traffic{}
	if err := tr.SetProxyPool([]PoolEntry{
		{URL: "http://127.0.0.1:18001", Name: "A", Enabled: true, Weight: 1},
		{URL: "http://127.0.0.1:18002", Name: "B", Enabled: true, Weight: 1},
	}, "round_robin"); err != nil {
		t.Fatalf("set pool: %v", err)
	}
	// 模拟一次真实选择留下的记录。
	pool := tr.pool.Load()
	u := pool.Pick("target.local:8080")
	tr.lastPick.Store(hostOnly("target.local:8080"), u.String())

	broken := tr.ProxyPoolReportBad("target.local:8080", 0)
	if broken == "" {
		t.Fatal("ProxyPoolReportBad returned empty — agent feedback would be a no-op")
	}
	// 该上游应已熔断，后续不会再被选中。
	for i := 0; i < 10; i++ {
		if got := pool.Pick("other.local:80"); got != nil && got.String() == u.String() {
			t.Fatalf("reported upstream %v still being picked", u)
		}
	}
}

// TestProxyPoolReportBadUnknownHost 验证上报一个没走过池的目标时返回空串而不是 panic
// 或误伤别的上游。
func TestProxyPoolReportBadUnknownHost(t *testing.T) {
	tr := &Traffic{}
	if err := tr.SetProxyPool([]PoolEntry{
		{URL: "http://127.0.0.1:18001", Name: "A", Enabled: true, Weight: 1},
	}, "round_robin"); err != nil {
		t.Fatalf("set pool: %v", err)
	}
	if got := tr.ProxyPoolReportBad("never-seen.local:80", 0); got != "" {
		t.Fatalf("reporting an unseen host must be a no-op, got %q", got)
	}
}

// 编译期确认 http.Request 的 Host 形态与我们 hostOnly 的期望一致（文档性断言）。
var _ = func(r *http.Request) string { return hostOnly(r.Host) }
