// proxypool_fetch.go：从公开源自动抓取代理列表，验证后并入池。
// 主要函数/类型：
//   - proxySource：一个代理清单来源（URL + 默认协议）。
//   - builtinProxySources：内置的公开清单源。
//   - fetchSource：抓单个源并解析成 URL 列表。
//   - proxyPoolFetch：HTTP 入口——抓取 → 去重 → 验证 → 入库。
//
// 为什么需要这个：手动粘贴代理只适合「买了几条固定 IP」的场景。短效代理池
// （几分钱一个 IP，有效期几十分钟）必须能自动补充，否则一个池几小时后就全是
// 死节点。这是 ProxyCat 的核心能力，也是实际使用中最需要的一环。
//
// 设计上的三个取舍：
//  1. 必须验证后才入库。公开清单里能用的通常不到 10%，不验证直接入池只会让
//     每一条扫描请求都先超时一次。验证并发跑，并在响应里如实报告通过率。
//  2. 按 URL 去重。同一批清单在多个源里重复出现是常态，不去重会把权重搞乱。
//  3. 单源失败只跳过。这些是第三方公开清单，某几个源挂掉或被墙是常态，
//     不该让整次抓取失败。

package server

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"artex/traffic"
)

// proxySource 是一个代理清单来源。默认协议用于清单里只有 ip:port 的情况。
type proxySource struct {
	URL      string `json:"url"`
	Protocol string `json:"protocol"` // http | socks5
}

// builtinProxySources 是内置的公开清单源。
//
// 只收录「纯文本、一行一个 ip:port」的源：那种返回 JSON/HTML 的源解析规则
// 随站点改版随时失效，维护成本远高于收益。这五个源的清单格式多年未变。
func builtinProxySources() []proxySource {
	return []proxySource{
		{URL: "https://raw.githubusercontent.com/TheSpeedX/PROXY-List/master/http.txt", Protocol: "http"},
		{URL: "https://raw.githubusercontent.com/TheSpeedX/PROXY-List/master/socks5.txt", Protocol: "socks5"},
		{URL: "https://raw.githubusercontent.com/clarketm/proxy-list/master/proxy-list-raw.txt", Protocol: "http"},
		{URL: "https://raw.githubusercontent.com/monosans/proxy-list/main/proxies/http.txt", Protocol: "http"},
		{URL: "https://raw.githubusercontent.com/monosans/proxy-list/main/proxies/socks5.txt", Protocol: "socks5"},
	}
}

// maxSourceBytes 限制单个源的响应体。清单通常几十 KB，给 8MB 足够宽裕，
// 同时防止某个源突然返回一个巨大文件把内存吃穿。
const maxSourceBytes = 8 << 20

// fetchSource 抓一个源并解析成代理 URL 列表。
//
// 解析规则刻意宽松：只认「一行一个地址」，空行与 # 注释跳过，带不出 host:port
// 的行直接丢弃。清单格式微调（比如末尾多一列国家代码）时，宁可少收几条，
// 也不要因为格式校验过严而整源作废。
func fetchSource(ctx context.Context, client *http.Client, src proxySource) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "artex-proxypool/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var out []string
	sc := bufio.NewScanner(io.LimitReader(resp.Body, maxSourceBytes))
	// 某些清单的行很长（带额外列），把扫描缓冲调大避免 ErrTooLong。
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		if u := normalizeProxyLine(line, src.Protocol); u != "" {
			out = append(out, u)
		}
	}
	// 扫描错误不算失败：可能只是最后一行超长，前面已解析到的仍然有用。
	return out, nil
}

// normalizeProxyLine 把清单里的一行规范成完整代理 URL；无法识别时返回空串。
func normalizeProxyLine(line, defProto string) string {
	// 只取第一段（有些清单是 ip:port 后面还跟国家/匿名度等列）
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ""
	}
	raw := fields[0]

	if strings.Contains(raw, "://") {
		if _, err := traffic.ValidateProxyURL(raw); err != nil {
			return ""
		}
		return raw
	}
	// 裸 ip:port
	if strings.Count(raw, ":") != 1 {
		return ""
	}
	full := defProto + "://" + raw
	if _, err := traffic.ValidateProxyURL(full); err != nil {
		return ""
	}
	return full
}

// proxyPoolFetch POST /api/proxypool/fetch
//
// 抓取 → 去重 → 并发验证 → 把通过的并入池。验证是必须的：公开清单可用率通常
// 低于 10%，不验证直接入池会让每条扫描请求都先超时一次才回退。
func (s *Server) proxyPoolFetch(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var req struct {
		Sources    []proxySource `json:"sources"`      // 空 = 用内置源
		Verify     *bool         `json:"verify"`       // 默认 true；false = 抓到就入池（快但脏）
		Max        int           `json:"max"`          // 最多入库多少条（验证通过后），默认 100
		Concurrency int          `json:"concurrency"`  // 验证并发数，默认 64
		Target     string        `json:"target"`       // 验证目标，空 = 用设置里的
	}
	if err := decodeJSONBounded(w, r, &req, maxMediumJSONBodyBytes); err != nil && err != io.EOF {
		writeJSONBodyError(w, err)
		return
	}
	sources := req.Sources
	if len(sources) == 0 {
		sources = builtinProxySources()
	}
	verify := true
	if req.Verify != nil {
		verify = *req.Verify
	}
	maxIn := req.Max
	if maxIn <= 0 {
		maxIn = 100
	}
	conc := req.Concurrency
	if conc <= 0 {
		conc = 64
	} else if conc > 256 {
		conc = 256
	}
	target := strings.TrimSpace(req.Target)
	if target == "" {
		target = settingOr(pg, settingProxyCheckTarget, defaultProxyCheckTarget)
	}

	// 抓取阶段的客户端走全局出口（这些清单源在墙外很常见）。
	fetchClient := &http.Client{Timeout: 25 * time.Second}
	if gp := s.m.GlobalProxy(); gp != "" {
		if pu, err := traffic.ValidateProxyURL(gp); err == nil {
			fetchClient.Transport = &http.Transport{Proxy: http.ProxyURL(pu)}
		}
	}

	// 并发抓所有源；单源失败记进 errors 但不中断。
	type srcResult struct {
		URL   string
		Count int
		Err   string
		list  []string
	}
	srcResults := make([]srcResult, len(sources))
	var wg sync.WaitGroup
	for i, src := range sources {
		wg.Add(1)
		go func(idx int, one proxySource) {
			defer wg.Done()
			list, err := fetchSource(r.Context(), fetchClient, one)
			srcResults[idx] = srcResult{URL: one.URL, Count: len(list)}
			if err != nil {
				srcResults[idx].Err = err.Error()
				return
			}
			srcResults[idx].list = list
		}(i, src)
	}
	wg.Wait()

	// 汇总去重，保持发现顺序（先出现的优先，便于「稳定源优先」的直觉）。
	seen := map[string]bool{}
	var candidates []string
	for _, sr := range srcResults {
		for _, u := range sr.list {
			if seen[u] {
				continue
			}
			seen[u] = true
			candidates = append(candidates, u)
		}
	}

	// 验证：并发探测，只保留真的能连通的。
	alive := candidates
	if verify && len(candidates) > 0 {
		alive = verifyProxies(r.Context(), candidates, target, conc, maxIn)
	}

	// 入库：与已有条目按 URL 合并，保留用户手工配置的条目与权重。
	st := s.loadProxyPool()
	existing := map[string]bool{}
	for _, e := range st.Entries {
		existing[e.URL] = true
	}
	added := 0
	for i, u := range alive {
		if added >= maxIn {
			break
		}
		if existing[u] {
			continue
		}
		name := proxyShortName(u, i)
		st.Entries = append(st.Entries, traffic.PoolEntry{
			URL: u, Name: name, Enabled: true, Weight: 1,
		})
		existing[u] = true
		added++
	}
	if err := s.saveProxyPool(*st); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if t := s.m.traffic; t != nil {
		if err := t.SetProxyPool(st.Entries, st.Strategy); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
	}

	// 源明细按成功数排序，前端能直接看到「哪个源还在工作」。
	sort.Slice(srcResults, func(i, j int) bool { return srcResults[i].Count > srcResults[j].Count })
	srcOut := make([]map[string]any, 0, len(srcResults))
	for _, sr := range srcResults {
		item := map[string]any{"url": sr.URL, "count": sr.Count}
		if sr.Err != "" {
			item["error"] = sr.Err
		}
		srcOut = append(srcOut, item)
	}

	writeJSON(w, 200, map[string]any{
		"ok":         true,
		"fetched":    len(candidates),
		"alive":      len(alive),
		"added":      added,
		"total":      len(st.Entries),
		"verified":   verify,
		"target":     target,
		"sources":    srcOut,
	})
}

// verifyProxies 并发验证候选代理，返回按原顺序排列的可用项（最多 keep 个）。
//
// keep 到位就取消剩余任务：验证一个几万条的公开清单要跑很久，而用户只想要
// 几十上百个能用的，没必要把所有候选都试完。
func verifyProxies(ctx context.Context, candidates []string, target string, conc, keep int) []string {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	type verdict struct {
		idx  int
		url  string
		ok   bool
	}
	sem := make(chan struct{}, conc)
	results := make(chan verdict, len(candidates))
	var wg sync.WaitGroup

	for i, u := range candidates {
		select {
		case <-ctx.Done():
			goto collect
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(idx int, raw string) {
			defer wg.Done()
			defer func() { <-sem }()
			// checkOneProxy 已带超时；这里只关心成功与否。
			res := checkOneProxy(ctx, idx, maskProxy(raw), raw, target, 6*time.Second)
			results <- verdict{idx: idx, url: raw, ok: res.OK}
		}(i, u)
	}

collect:
	go func() {
		wg.Wait()
		close(results)
	}()

	var ok []verdict
	for v := range results {
		if v.ok {
			ok = append(ok, v)
			if len(ok) >= keep {
				cancel() // 够数了，剩下的不必再等
			}
		}
	}
	sort.Slice(ok, func(i, j int) bool { return ok[i].idx < ok[j].idx })
	out := make([]string, len(ok))
	for i, v := range ok {
		out[i] = v.url
	}
	return out
}

// proxyShortName 生成一个易读的展示名：优先用 host:port，避免列表里全是长 URL。
func proxyShortName(raw string, idx int) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Sprintf("auto-%d", idx+1)
	}
	return u.Host
}
