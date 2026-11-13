package traffic

import (
	"testing"
)

func TestSetPoolValidation(t *testing.T) {
	s := &selector{}
	if err := s.SetPool(nil, "bogus"); err == nil {
		t.Fatal("bogus strategy must be rejected")
	}
	if err := s.SetPool([]PoolEntry{
		{URL: "socks5://127.0.0.1:1080", Name: "a", Enabled: true},
		{URL: "not-a-proxy", Name: "bad", Enabled: true},
	}, "round_robin"); err != nil {
		t.Fatal(err)
	}
	// bad entry skipped, good one kept
	if len(s.entries) != 1 {
		t.Fatalf("entries=%d want 1", len(s.entries))
	}
}

func TestPickRoundRobinAndEmpty(t *testing.T) {
	s := &selector{}
	if got := s.Pick("example.com:443"); got != nil {
		t.Fatalf("empty pool must return nil, got %v", got)
	}
	_ = s.SetPool([]PoolEntry{
		{URL: "http://a:1", Name: "a", Enabled: true},
		{URL: "http://b:2", Name: "b", Enabled: true},
	}, "round_robin")
	seen := map[string]int{}
	for i := 0; i < 4; i++ {
		u := s.Pick("example.com:443")
		seen[u.String()]++
	}
	if len(seen) != 2 {
		t.Fatalf("round robin only used %v", seen)
	}
}

func TestPickStickyHost(t *testing.T) {
	s := &selector{}
	_ = s.SetPool([]PoolEntry{
		{URL: "http://a:1", Name: "a", Enabled: true},
		{URL: "http://b:2", Name: "b", Enabled: true},
	}, "sticky_host")
	first := s.Pick("target.test:443").String()
	for i := 0; i < 5; i++ {
		if got := s.Pick("target.test:443").String(); got != first {
			t.Fatalf("sticky broken: %s != %s", got, first)
		}
	}
}

func TestCircuitBreaker(t *testing.T) {
	s := &selector{}
	_ = s.SetPool([]PoolEntry{{URL: "http://a:1", Name: "a", Enabled: true}}, "round_robin")
	// 4 failures stay below threshold; the 5th opens the breaker.
	for i := 0; i < 4; i++ {
		s.ReportResult("http://a:1", false)
	}
	if got := s.Pick("x.test:443"); got == nil {
		t.Fatal("4 failures must not trip breaker")
	}
	s.ReportResult("http://a:1", false)
	if got := s.Pick("x.test:443"); got != nil {
		t.Fatal("5th failure must open breaker (Pick returns nil)")
	}
	// a success clears the counter — never reaches breaker
	s2 := &selector{}
	_ = s2.SetPool([]PoolEntry{{URL: "http://b:1", Name: "b", Enabled: true}}, "round_robin")
	for i := 0; i < 3; i++ {
		s2.ReportResult("http://b:1", false)
	}
	s2.ReportResult("http://b:1", true)
	s2.ReportResult("http://b:1", false)
	s2.ReportResult("http://b:1", false)
	if got := s2.Pick("x.test:443"); got == nil {
		t.Fatal("success must reset failure counter (4 < 5)")
	}
}

// feed 按给定的成功/失败序列喂结果。
func feed(sel *selector, url string, results []bool) {
	for _, ok := range results {
		sel.ReportResult(url, ok)
	}
}

// TestSuspectRatioCatchNonStreaking 验证「失败占比」抓得住连败计数抓不到的坏出口。
//
// 这是移植 ProxyCat 判定的真正理由。构造一个**永不连败 5 次**、但失败率很高的出口：
// 3 失败 + 1 成功 循环 —— 最长连败只有 3，连败计数永远熔断不了它；
// 而占比判定会把它标为「可疑」，从此不再优先派新请求。
func TestSuspectRatioCatchNonStreaking(t *testing.T) {
	sel := &selector{}
	if err := sel.SetPool([]PoolEntry{
		{URL: "http://127.0.0.1:1111", Name: "flaky", Enabled: true, Weight: 1},
		{URL: "http://127.0.0.1:2222", Name: "good", Enabled: true, Weight: 1},
	}, "round_robin"); err != nil {
		t.Fatal(err)
	}
	// 4 轮「失败 失败 失败 成功」= 16 个样本，失败率 75%，最长连败 3。
	pattern := []bool{false, false, false, true}
	var seq []bool
	for i := 0; i < 4; i++ {
		seq = append(seq, pattern...)
	}
	feed(sel, "http://127.0.0.1:1111", seq)

	var flaky PoolEntry
	for _, e := range sel.Status() {
		if e.Name == "flaky" {
			flaky = e
		}
	}
	if flaky.BreakRemaining != 0 {
		t.Fatalf("最长连败只有 3，不该被熔断（BreakRemaining=%d）", flaky.BreakRemaining)
	}
	if flaky.FailRatio < suspectRatio {
		t.Fatalf("75%% 失败的出口 FailRatio = %.2f，应 ≥ %.2f", flaky.FailRatio, suspectRatio)
	}
	// 关键断言：健康出口存在时，带权轮询不该再选中这个可疑出口。
	for i := 0; i < 20; i++ {
		if u := sel.Pick("example.com:443"); u != nil && u.Port() == "1111" {
			t.Fatalf("可疑出口 %v 仍被优先派发（健康出口存在时不该选中它）", u)
		}
	}
}

// TestSuspectIsDowngradeNotRemoval 验证可疑出口是「降级」而不是「剔除」。
//
// 若把可疑出口直接排除，当池里只剩这类出口时会彻底无出口可用——从「慢」变成
// 「完全不通」，比降级更糟。所以没有健康出口时必须仍能选出它。
func TestSuspectIsDowngradeNotRemoval(t *testing.T) {
	sel := &selector{}
	if err := sel.SetPool([]PoolEntry{
		{URL: "http://127.0.0.1:3333", Name: "only", Enabled: true, Weight: 1},
	}, "round_robin"); err != nil {
		t.Fatal(err)
	}
	// 同样的 3 失败 + 1 成功：占比高但不连败到熔断阈值。
	var seq []bool
	for i := 0; i < 4; i++ {
		seq = append(seq, false, false, false, true)
	}
	feed(sel, "http://127.0.0.1:3333", seq)

	if u := sel.Pick("example.com:443"); u == nil {
		t.Fatal("池里只有可疑出口时应当仍能选出（降级而不是断流）")
	}
}

// TestStreakBreaksEvenWithNoWindow 验证连败兜底仍然有效：
// 新出口样本还少时占比不稳，但连续 5 次失败已经足够说明此刻不可用。
func TestStreakBreaksEvenWithNoWindow(t *testing.T) {
	sel := &selector{}
	if err := sel.SetPool([]PoolEntry{
		{URL: "http://127.0.0.1:4444", Name: "dead", Enabled: true, Weight: 1},
	}, "round_robin"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < breakAfterFails; i++ {
		sel.ReportResult("http://127.0.0.1:4444", false)
	}
	st := sel.Status()
	if len(st) == 0 {
		t.Fatal("empty status")
	}
	if st[0].BreakRemaining <= 0 {
		t.Fatalf("连败 %d 次后应已熔断，BreakRemaining=%d", breakAfterFails, st[0].BreakRemaining)
	}
	// 熔断后样本窗口清空：冷却结束是「重新给一次机会」，不该带着旧账复出。
	if st[0].Samples != 0 || st[0].FailRatio != 0 {
		t.Fatalf("熔断后样本窗口应清空，得到 samples=%d ratio=%.2f", st[0].Samples, st[0].FailRatio)
	}
	if u := sel.Pick("example.com:443"); u != nil {
		t.Fatalf("唯一出口已熔断，Pick 应返回 nil（回退全局代理/直连），得到 %v", u)
	}
}
