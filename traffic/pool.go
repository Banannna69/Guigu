// pool.go：代理池——多个上游出口按策略为每个请求轮换，失败按占比熔断。
// 主要函数/类型：
//   - PoolEntry / poolUpstream：池条目（持久化视图）与上游运行态。
//   - selector / SetPool / Pick：策略选择器（round_robin / sticky_host / sticky_host_random / random）。
//   - ReportResult / MarkBad / BreakRemaining：结果上报、主动熔断与冷却查询。
//   - Status / maskURL：对外状态（URL 掩码凭据）。
//   - Traffic.SetProxyPool / ProxyPoolStatus：门面与状态导出。
//
// 接入点就是 MITM 的 per-request upstream 钩子：agent 的 bash 子进程统一走本地
// MITM 代理（见 agent/worker.go 的 proxyEnv），所以只要池非空，**所有扫描请求**
// （curl / nmap / python 脚本 / WebFetch）都会按策略自动轮换出口——不需要改任何
// agent 工具。这是「agent 自动扫描走代理池」的实现方式。
//
// 与 global_proxy 的关系：池非空时池优先（全局单代理被忽略），池空回退全局单代理
// 或直连——不使用代理池的部署行为完全不变。
//
// 淘汰策略取自 ProxyCat：判据是**最近窗口内的失败占比**，而不是连续失败次数。
// 连败计数会把一次抖动放大成熔断，又会漏掉「60% 失败但从不连败」的半死出口。

package traffic

import (
	"fmt"
	"hash/fnv"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// PoolEntry 是池中的一个上游代理。
type PoolEntry struct {
	URL      string `json:"url"`      // http/https/socks5,可含 user:pass(输出时掩码)
	Name     string `json:"name"`     // 展示名
	Enabled  bool   `json:"enabled"`  // 手动开关
	Weight   int    `json:"weight"`   // 轮询权重(round_robin 有效,≥1)
	Failures int    `json:"failures"` // 连败计数(熔断窗口)
	LastOK   string `json:"last_ok,omitempty"`
	LastFail string `json:"last_fail,omitempty"`
	// BreakRemaining 是该上游当前熔断的剩余秒数(0=未熔断)。主动健康检查与
	// 手动验证都会读写 last_ok/last_fail/break,页面据此筛选失效代理。
	BreakRemaining int `json:"break_remaining,omitempty"`
	// FailRatio 是最近窗口内的失败占比(0~1)，UI 用它区分「偶发抖动」与「半死出口」：
	// 连败计数为 0 不代表出口健康，一个 60% 失败率、但总在连败 4 次后成功一次的出口
	// 连败永远是 0，却会拖慢三分之二的请求。Samples 是该占比的样本数。
	FailRatio float64 `json:"fail_ratio,omitempty"`
	Samples   int     `json:"samples,omitempty"`
}

// selector 持池的内存态与策略。持久化在 server 侧(settings+json),这里只管选。
type selector struct {
	mu       sync.Mutex
	entries  []*poolUpstream // enabled 项
	strategy string          // round_robin / sticky_host / sticky_host_random / random
	rr       atomic.Uint64   // 轮询游标(round_robin/random 共用)
	// hostPick 记录 sticky_host_random 的「目标 → 已选定上游」绑定,让同一目标在
	// 一段会话里稳定走同一出口(像真实用户),同时不用给每个 host 重新掷骰子。
	// 存 URL 字符串而不是指针:池整体重载时条目指针会换,字符串仍能对上。
	hostPick sync.Map
}

type poolUpstream struct {
	raw       *url.URL
	name      string
	weight    int
	failures  atomic.Int64
	lastFail  atomic.Int64 // unix
	lastOK    atomic.Int64 // unix
	breakOpen atomic.Int64 // 熔断到期时间(unix;0=未熔断)

	// outcomes 是最近 outcomeWindow 次请求的结果环形窗口(1=成功/0=失败)。
	// 判定一个出口好不好，看的应该是「最近这批请求里失败占多少」而不是「连续失败几次」：
	// 连败计数会把一次网络抖动放大成熔断（抖动后成功一次就清零，但抖动期间那几次
	// 已经白白拖慢了请求），也会漏掉「成功率 60% 但从不连败」的半死出口。
	// 用占比能同时把这两种情况分开。照搬 ProxyCat 的做法。
	outcomes outcomeRing
}

// outcomeRing 是定长环形结果窗口。零值可用。
//
// 不用 deque + mutex：这里是每个请求都会写的热路径，加锁会成为吞吐瓶颈。
// 定长数组 + 原子游标足够——统计上偶尔读到正在被覆盖的槽位不影响结论，
// 比例只用来做「可疑/淘汰」的粗判定。
type outcomeRing struct {
	bits atomic.Uint64 // 最近 64 次结果，1=成功 0=失败，高位更新
	n    atomic.Int64  // 已填充样本数（上限 64）
}

// outcomeWindow 是参与判定的样本数。10 次是 ProxyCat 的取值：足够平滑一次抖动，
// 又能在一个出口刚坏掉时迅速反应（几分钟内就能凑满 10 个样本）。
const outcomeWindow = 10

// push 记录一次结果。
func (r *outcomeRing) push(ok bool) {
	prev := r.bits.Load()
	next := prev << 1
	if ok {
		next |= 1
	}
	// 只保留低 64 位；窗口判定只看最低的 outcomeWindow 位。
	r.bits.Store(next)
	if n := r.n.Load(); n < 64 {
		r.n.Add(1)
	}
}

// ratio 返回窗口内的失败占比（0~1）。样本不足 1 个时返回 0。
func (r *outcomeRing) ratio() float64 {
	n := r.n.Load()
	if n == 0 {
		return 0
	}
	if n > outcomeWindow {
		n = outcomeWindow
	}
	mask := uint64(1)<<uint(n) - 1
	window := r.bits.Load() & mask
	fails := n - bitsOnes(window)
	return float64(fails) / float64(n)
}

// reset 清空窗口。绝不整体赋值 outcomeRing{}——那会拷贝正在被原子读写的字段，
// 造成数据竞争且可能丢失清空效果。这里只做字段级写入。
func (r *outcomeRing) reset() {
	r.bits.Store(0)
	r.n.Store(0)
}

// bitsOnes 数 1 的位数（Go 1.13+ 有 math/bits，这里避免多引一个包到热路径上）。
func bitsOnes(x uint64) int64 {
	var c int64
	for x != 0 {
		x &= x - 1
		c++
	}
	return c
}

const (
	// breakAfterFails 是熔断阈值：连续 5 次失败就冷却。
	//
	// 为什么用连败而不是「占比 ≥ N 次」来熔断：失败占比 ≥ 90% 这个条件在窗口
	// 只有 10 个样本时**永远先被连败触发**（9 次失败里必然存在 5 次连败），
	// 所以它是死参数。占比真正的作用在 suspectRatio 那一层——把「80% 失败但
	// 每隔几次能成功一下、永远不会连败 5 次」的出口降级，而那些出口旧判定
	// 是抓不到的。
	breakAfterFails = 5
	// suspectRatio：失败占比超过它就标记为「可疑」，不再优先派新请求。
	// 0.5 意味着「一半以上都失败」——这种出口继续用只是把失败率分给用户。
	suspectRatio = 0.5
	// breakCooldown 是被动熔断的冷却时长。
	breakCooldown = 10 * time.Minute
)

// SetPool 原子替换整池(server 每次 CRUD 后全量推送)。
func (s *selector) SetPool(items []PoolEntry, strategy string) error {
	strategy = strings.ToLower(strings.TrimSpace(strategy))
	switch strategy {
	case "", "round_robin", "sticky_host", "sticky_host_random", "random":
	default:
		return fmt.Errorf("未知策略 %q(round_robin/sticky_host/sticky_host_random/random)", strategy)
	}
	ups := make([]*poolUpstream, 0, len(items))
	for _, it := range items {
		if !it.Enabled {
			continue
		}
		u, err := ValidateProxyURL(it.URL)
		if err != nil {
			continue // server 侧已校验;双保险跳过坏条目
		}
		w := it.Weight
		if w < 1 {
			w = 1
		}
		nu := &poolUpstream{raw: u, name: it.Name, weight: w}
		// 重推整池时继承旧条目的熔断状态(按 URL 匹配),避免每次保存都重置熔断。
		for _, prev := range s.entries {
			if prev.raw.String() == nu.raw.String() {
				nu.failures.Store(prev.failures.Load())
				nu.breakOpen.Store(prev.breakOpen.Load())
			}
		}
		ups = append(ups, nu)
	}
	s.mu.Lock()
	s.entries = ups
	s.strategy = strategy
	s.mu.Unlock()
	return nil
}

// Pick 为一个目标请求挑上游。池空/全熔断返回 nil(直连,回退全局逻辑)。
// host 是目标 "host:port",sticky_host 策略按它稳定散列。
func (s *selector) Pick(host string) *url.URL {
	s.mu.Lock()
	entries := s.entries
	strategy := s.strategy
	s.mu.Unlock()
	if len(entries) == 0 {
		return nil
	}
	now := time.Now().Unix()
	available := make([]*poolUpstream, 0, len(entries))
	healthy := make([]*poolUpstream, 0, len(entries))
	for _, e := range entries {
		if bo := e.breakOpen.Load(); bo != 0 && now < bo {
			continue // 熔断中
		}
		available = append(available, e)
		// 「可疑」的出口（失败占比 > suspectRatio）只在没有健康出口时兜底使用。
		// 直接排除它们会让池在市场里全是坏代理时彻底无出口可用，比降级更糟。
		if e.outcomes.ratio() <= suspectRatio {
			healthy = append(healthy, e)
		}
	}
	if len(healthy) > 0 {
		available = healthy
	}
	if len(available) == 0 {
		return nil
	}
	switch strategy {
	case "sticky_host":
		h := fnv.New32a()
		_, _ = h.Write([]byte(host))
		return available[h.Sum32()%uint32(len(available))].raw
	case "sticky_host_random":
		// 首次遇到该目标时随机钉住一个上游,之后固定 —— 兼得「同目标走同一出口」
		// 与「不同目标的出口分布随机」。钉住的那个被熔断时自动重钉。
		if raw, ok := s.hostPick.Load(host); ok {
			if u := findUpstream(available, raw.(string)); u != nil {
				return u.raw
			}
		}
		u := available[s.rr.Add(1)%uint64(len(available))]
		s.hostPick.Store(host, u.raw.String())
		return u.raw
	case "random":
		return available[s.rr.Add(1)%uint64(len(available))].raw
	default: // round_robin,按权重展开(权重大的出现更密)
		total := 0
		for _, e := range available {
			total += e.weight
		}
		n := s.rr.Add(1) - 1
		idx := n % uint64(total)
		for _, e := range available {
			if idx < uint64(e.weight) {
				return e.raw
			}
			idx -= uint64(e.weight)
		}
		return available[0].raw
	}
}

// findUpstream 从一段候选里按原始 URL 找回条目(钉住关系用 URL 串对账,池重载后仍有效)。
func findUpstream(list []*poolUpstream, raw string) *poolUpstream {
	for _, e := range list {
		if e.raw.String() == raw {
			return e
		}
	}
	return nil
}

// ReportResult 上报某上游代理的一次请求结果。
//
// 熔断看连败（breakAfterFails），降级看占比（suspectRatio，在 Pick 里生效）。
//
// 两者的分工：连败抓「已经彻底挂掉」的出口；占比抓「半死不活」的出口——
// 后者每隔几次就能成功一下，连败计数永远到不了阈值，却拖慢了大部分请求。
// 只看连败的话这类出口会一直留在池里，这是移植 ProxyCat 占比判定要解决的唯一问题。
func (s *selector) ReportResult(proxyRaw string, ok bool) {
	if proxyRaw == "" {
		return
	}
	s.mu.Lock()
	entries := s.entries
	s.mu.Unlock()
	for _, e := range entries {
		if e.raw.String() != proxyRaw {
			continue
		}
		now := time.Now().Unix()
		e.outcomes.push(ok)
		if ok {
			e.failures.Store(0)
			e.lastOK.Store(now)
			return
		}
		e.lastFail.Store(now)
		fails := e.failures.Add(1)
		if fails >= breakAfterFails {
			e.breakOpen.Store(time.Now().Add(breakCooldown).Unix())
			e.failures.Store(0)
			// 清空样本窗口：冷却结束后是「重新给一次机会」，不该带着旧账复出。
			//
			// 必须用 reset() 而不是 `e.outcomes = outcomeRing{}`：outcomeRing 内部
			// 是两个 atomic 值，整体赋值既拷贝了正在被 push()/ratio() 原子读写的内容，
			// 又让之后的读写指向不同的存储位置——go test -race 会直接报数据竞争，
			// 而且「清空」可能丢失，导致半死出口带着旧账复出、占比降级判定失效。
			e.outcomes.reset()
		}
		return
	}
}

// MarkBad 把某个上游立即熔断 cooldown(主动健康检查 / Agent 上报用)。
// 被动熔断要等连败 5 次,而「已知不通」的代理再试 5 次只是白白拖慢请求——
// 主动判定成立就立刻冷却。返回被熔断上游的掩码 URL,空串表示没找到。
func (s *selector) MarkBad(proxyRaw string, cooldown time.Duration) string {
	if proxyRaw == "" {
		return ""
	}
	if cooldown <= 0 {
		cooldown = breakCooldown
	}
	s.mu.Lock()
	entries := s.entries
	s.mu.Unlock()
	for _, e := range entries {
		if e.raw.String() != proxyRaw {
			continue
		}
		e.breakOpen.Store(time.Now().Add(cooldown).Unix())
		e.lastFail.Store(time.Now().Unix())
		e.failures.Store(0)
		// 钉住关系一并作废,sticky_host_random 下次 Pick 会换一个出口。
		s.hostPick.Range(func(k, v any) bool {
			if raw, _ := v.(string); raw == proxyRaw {
				s.hostPick.Delete(k)
			}
			return true
		})
		return maskURL(e.raw)
	}
	return ""
}

// BreakRemaining 返回该上游当前熔断的剩余秒数(0 = 未熔断 / 不在池里)。
func (s *selector) BreakRemaining(proxyRaw string) int {
	if proxyRaw == "" {
		return 0
	}
	s.mu.Lock()
	entries := s.entries
	s.mu.Unlock()
	for _, e := range entries {
		if e.raw.String() != proxyRaw {
			continue
		}
		rest := int(e.breakOpen.Load() - time.Now().Unix())
		if rest < 0 {
			return 0
		}
		return rest
	}
	return 0
}

// Status 导出池状态(UI 展示;URL 掩码凭据)。
func (s *selector) Status() []PoolEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().Unix()
	out := make([]PoolEntry, 0, len(s.entries))
	for _, e := range s.entries {
		item := PoolEntry{
			URL:       maskURL(e.raw),
			Name:      e.name,
			Enabled:   true,
			Weight:    e.weight,
			Failures:  int(e.failures.Load()),
			FailRatio: e.outcomes.ratio(),
			Samples:   int(min(e.outcomes.n.Load(), outcomeWindow)),
		}
		if bo := e.breakOpen.Load(); bo > now {
			item.BreakRemaining = int(bo - now)
		}
		if t := e.lastOK.Load(); t > 0 {
			item.LastOK = time.Unix(t, 0).Format(time.RFC3339)
		}
		if t := e.lastFail.Load(); t > 0 {
			item.LastFail = time.Unix(t, 0).Format(time.RFC3339)
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// maskURL 隐藏 user:pass(user 保留,pass → ****)。
func maskURL(u *url.URL) string {
	c := *u
	if c.User != nil {
		if _, hasPass := c.User.Password(); hasPass {
			c.User = url.UserPassword(c.User.Username(), "****")
		}
	}
	return c.String()
}

// SetProxyPool 原子替换代理池(Traffic 门面)。
func (t *Traffic) SetProxyPool(items []PoolEntry, strategy string) error {
	sel := &selector{}
	if err := sel.SetPool(items, strategy); err != nil {
		return err
	}
	if len(items) == 0 {
		t.pool.Store(nil) // 池清空 = 回退全局代理/直连
		return nil
	}
	t.pool.Store(sel)
	return nil
}

// ProxyPoolStatus 导出池内条目(掩码 URL + 熔断计数),供 UI 展示。
func (t *Traffic) ProxyPoolStatus() []PoolEntry {
	if pool := t.pool.Load(); pool != nil {
		return pool.Status()
	}
	return []PoolEntry{}
}

// ProxyPoolReportOK 上报一次成功(清零连败,记录时间)。
func (t *Traffic) ProxyPoolReportOK(proxyRaw string) {
	pool := t.pool.Load()
	if pool == nil {
		return
	}
	pool.ReportResult(proxyRaw, true)
}

// ProxyPoolReportFail 上报一次失败(只累计连败,不立刻熔断——被动路径的阈值是 5 次)。
func (t *Traffic) ProxyPoolReportFail(proxyRaw string) {
	pool := t.pool.Load()
	if pool == nil {
		return
	}
	pool.ReportResult(proxyRaw, false)
}

// ProxyPoolMarkBad 把某个上游立即熔断 cooldown(主动健康检查 / 手动验证失败时用)。
// 返回被熔断的掩码 URL,空串表示不在池里。
func (t *Traffic) ProxyPoolMarkBad(proxyRaw string, cooldown time.Duration) string {
	pool := t.pool.Load()
	if pool == nil {
		return ""
	}
	return pool.MarkBad(proxyRaw, cooldown)
}

// ProxyPoolReportBad 让调用方(Agent)把某个目标「当前正在用的出口」立即熔断。
// target 可以是完整 host:port —— MITM 侧按 host 记录了最近一次选中的上游,
// 所以不需要请求方知道它到底走了哪个代理。返回被熔断的掩码 URL,空串表示
// 池为空 / 该目标没走过池上游(例如直连或全局单代理)。
func (t *Traffic) ProxyPoolReportBad(target string, cooldown time.Duration) string {
	pool := t.pool.Load()
	if pool == nil {
		return ""
	}
	raw, ok := t.lastPick.Load(hostOnly(target))
	if !ok {
		return ""
	}
	u, _ := raw.(string)
	return pool.MarkBad(u, cooldown)
}
