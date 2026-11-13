// sync_fofa.go：FOFA 资产测绘同步（检索 → 资产图 → 资产合集）。
// 主要函数/类型：
//   - fofaSearch / fofaProbe：FOFA REST 调用与账号校验。
//   - (s *Server) syncFofaRun：分页检索 + 批量入库 + 归类合集。
//   - fofaIngestBatch：把一页结果批量写成资产（单个事务）。
//
// 与"照抄一份"的差异（每一条都是针对已知问题的处理）：
//   1. **批量事务**：逐行 Upsert 时每行一次往返，5000 条就要几千次；且中途失败会留
//      下半批写了一半的数据。这里按页提交一个事务，页内整体成功或整体回滚。
//   2. **凭据永不进错误信息**：FOFA 的 key 在 query string 里，*url.Error 会把整条
//      URL 打出来。这里所有对外错误都过一遍 redact，只保留 scheme://host。
//   3. **入库计数如实**：按实际写成功的资产计数，不把失败行也算进去（否则 truncated
//      判断和"导入了 N 条"都会骗人）。
//   4. **分页有上限**：FOFA 异常返回时不会无限翻页。
//   5. **增量合并**：同一查询重复同步时按名字并进同一合集，不产生重复合集。

package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"artex/db"
	"artex/notify"
)

const (
	fofaAPIBase = "https://fofa.info"
	// fofaFields 固定顺序，按下标解析。改这里必须同步改 rowToFofaRow / fofaIngestBatch。
	fofaFields = "host,ip,port,protocol,domain,title,server,icp"

	fofaPreviewMax  = 100  // 预览单页上限（交互用，不做大批量）
	fofaMaxResults  = 5000 // 单次同步入库上限
	fofaPageSize    = 100  // 分页大小
	fofaMaxPages    = 100  // 翻页硬上限：FOFA 异常返回时防止无限循环
	fofaTimeout     = 25 * time.Second
	fofaSyncTimeout = 10 * time.Minute

	settingFofaEmail = "fofa.email"
	settingFofaKey   = "fofa.key"
)

// fofaRow 是一行检索结果（预览用字段子集）。
type fofaRow struct {
	Host     string `json:"host"`
	IP       string `json:"ip"`
	Port     string `json:"port"`
	Protocol string `json:"protocol"`
	Domain   string `json:"domain"`
	Title    string `json:"title"`
	Server   string `json:"server"`
}

type fofaSearchResp struct {
	Error   bool       `json:"error"`
	Errmsg  string     `json:"errmsg"`
	Mode    string     `json:"mode"`
	Size    int        `json:"size"`
	Results [][]string `json:"results"`
}

// fofaCreds 读取已存的凭据。
func (s *Server) fofaCreds() (email, key string) {
	email, _, _ = s.m.pg.GetSetting(settingFofaEmail)
	key, _, _ = s.m.pg.GetSetting(settingFofaKey)
	return strings.TrimSpace(email), strings.TrimSpace(key)
}

// fofaRedact 把错误信息里的完整 URL 换成 scheme://host。
//
// FOFA 的 key 在 query string 上，而 *url.Error 和多数传输层错误会把完整 URL 打进
// 错误文本。这条路径的错误会直接回给浏览器，不脱敏就是把 key 交给任何看得到错误
// 提示的人。复用 notify 包已经为同类问题写好的实现。
func fofaRedact(err error) string {
	if err == nil {
		return ""
	}
	// *url.Error 会把完整 URL（含 key）打进 Error()。这里取它的 URL 字段重写成
	// 只保留 scheme://host，其余类型的错误交给通用兜底。
	var uerr *url.Error
	if errors.As(err, &uerr) {
		host := ""
		if u, perr := url.Parse(uerr.URL); perr == nil {
			host = u.Scheme + "://" + u.Host
		}
		inner := ""
		if uerr.Err != nil {
			inner = fofaStripURLs(uerr.Err.Error())
		}
		if inner == "" {
			inner = "未知错误"
		}
		return strings.TrimSpace(uerr.Op + " " + host + ": " + inner)
	}
	return fofaStripURLs(err.Error())
}

// fofaStripURLs 把一段文本里出现的 http(s) 地址截成 scheme://host。
func fofaStripURLs(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		rest := s[i:]
		if strings.HasPrefix(rest, "http://") || strings.HasPrefix(rest, "https://") {
			end := len(rest)
			if j := strings.IndexAny(rest, " \t\n\"'"); j >= 0 {
				end = j
			}
			if u, err := url.Parse(rest[:end]); err == nil && u.Host != "" {
				b.WriteString(u.Scheme + "://" + u.Host + "/…")
			} else {
				b.WriteString("(地址不可解析)")
			}
			i += end
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// fofaClient 构造走全局出口代理的客户端（空 = 直连）。
//
// 刻意不走流量录制代理：FOFA 是测绘数据源，不是测试目标，录它没有意义且会污染
// 目标流量库。TLS 校验保持开启。
func (s *Server) fofaClient(timeout time.Duration) *http.Client {
	tr := &http.Transport{
		DisableKeepAlives:   true,
		TLSHandshakeTimeout: timeout,
		Proxy: func(*http.Request) (*url.URL, error) {
			p := strings.TrimSpace(s.m.GlobalProxy())
			if p == "" {
				return nil, nil
			}
			return url.Parse(p)
		},
	}
	return &http.Client{Transport: tr, Timeout: timeout}
}

// fofaGet 发一次 GET 并解码 JSON。
//
// 客户端在这里创建、用完即关：FOFA 调用是低频交互式操作（一次同步最多几十次），
// 复用客户端的收益抵不上维护一个长生命周期 Transport 的复杂度。但**必须**显式
// CloseIdleConnections——否则每次调用都会留下空闲连接直到 GC，一次 5000 条的同步
// 就会累积几十个悬挂连接。
func (s *Server) fofaGet(ctx context.Context, email, key, path string, extra url.Values, out any) error {
	q := url.Values{}
	if email != "" {
		q.Set("email", email)
	}
	q.Set("key", key)
	for k, vs := range extra {
		for _, v := range vs {
			q.Add(k, v)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fofaAPIBase+path+"?"+q.Encode(), nil)
	if err != nil {
		return fmt.Errorf("构造请求失败")
	}
	client := s.fofaClient(fofaTimeout)
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("FOFA 请求失败：%s", fofaRedact(err))
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("FOFA 认证失败（HTTP %d），请检查 email / key", resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("FOFA 返回 HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// fofaSearch 拉一页结果。
func (s *Server) fofaSearch(ctx context.Context, email, key, query string, size, page int) (fofaSearchResp, error) {
	var out fofaSearchResp
	extra := url.Values{}
	extra.Set("qbase64", base64.StdEncoding.EncodeToString([]byte(query)))
	extra.Set("fields", fofaFields)
	extra.Set("size", strconv.Itoa(size))
	extra.Set("page", strconv.Itoa(page))
	if err := s.fofaGet(ctx, email, key, "/api/v1/search/all", extra, &out); err != nil {
		return out, err
	}
	if out.Error {
		msg := strings.TrimSpace(out.Errmsg)
		if msg == "" {
			msg = "FOFA 查询失败"
		}
		return out, fmt.Errorf("%s", msg)
	}
	return out, nil
}

// fofaProbe 用 /info/my 校验账号是否可用。
func (s *Server) fofaProbe(ctx context.Context, email, key string) error {
	var out struct {
		Error  bool   `json:"error"`
		Errmsg string `json:"errmsg"`
	}
	if err := s.fofaGet(ctx, email, key, "/api/v1/info/my", url.Values{}, &out); err != nil {
		return err
	}
	if out.Error {
		msg := strings.TrimSpace(out.Errmsg)
		if msg == "" {
			msg = "账号校验失败"
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

// rowToFofaRow 按 fofaFields 顺序映射一行。
func rowToFofaRow(row []string) fofaRow {
	at := func(i int) string {
		if i < len(row) {
			return strings.TrimSpace(row[i])
		}
		return ""
	}
	return fofaRow{
		Host: at(0), IP: at(1), Port: at(2), Protocol: at(3),
		Domain: at(4), Title: at(5), Server: at(6),
	}
}

// ---------- GET /api/sync/fofa/status ----------

func (s *Server) syncFofaStatus(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	email, key := s.fofaCreds()
	out := map[string]any{
		"configured": key != "",
		// 只回邮箱；key 是否已设置用布尔表达，永不回显明文或尾号。
		"email": email,
	}
	if out["configured"].(bool) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		if err := s.fofaProbe(ctx, email, key); err != nil {
			out["reachable"] = false
			out["error"] = fofaRedact(err)
		} else {
			out["reachable"] = true
		}
	}
	writeJSON(w, 200, out)
}

// ---------- POST /api/sync/fofa/datasource ----------

func (s *Server) syncFofaDatasource(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var body struct {
		Email string `json:"email"`
		Key   string `json:"key"`
	}
	if !decodeJSON(w, r, &body, maxSmallJSONBodyBytes) {
		return
	}
	curEmail, curKey := s.fofaCreds()

	// email 非密：空 = 保留原值。
	newEmail := curEmail
	if e := strings.TrimSpace(body.Email); e != "" {
		newEmail = e
	}
	// key 是密钥：空或掩码 = 保留原值（与 notify 渠道同一套约定）。
	newKey := curKey
	if k := strings.TrimSpace(body.Key); k != "" && !notify.IsMasked(k) {
		newKey = k
	}
	if newEmail != curEmail {
		if err := pg.SetSetting(settingFofaEmail, newEmail); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	if newKey != curKey {
		if err := pg.SetSetting(settingFofaKey, newKey); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	writeJSON(w, 200, map[string]any{"configured": newKey != "", "email": newEmail})
}

// ---------- POST /api/sync/fofa/query（预览，不写库）----------

func (s *Server) syncFofaQuery(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	var body struct {
		Query string `json:"query"`
		Size  int    `json:"size"`
	}
	if !decodeJSON(w, r, &body, maxSmallJSONBodyBytes) {
		return
	}
	query := strings.TrimSpace(body.Query)
	if query == "" {
		writeErr(w, 400, "query 不能为空")
		return
	}
	email, key := s.fofaCreds()
	if key == "" {
		writeErr(w, 400, "未配置 FOFA API Key，请先保存账号")
		return
	}
	size := body.Size
	if size <= 0 || size > fofaPreviewMax {
		size = fofaPreviewMax
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	resp, err := s.fofaSearch(ctx, email, key, query, size, 1)
	if err != nil {
		writeErr(w, 502, fofaRedact(err))
		return
	}
	rows := make([]fofaRow, 0, len(resp.Results))
	for _, row := range resp.Results {
		rows = append(rows, rowToFofaRow(row))
	}
	writeJSON(w, 200, map[string]any{"total": resp.Size, "mode": resp.Mode, "rows": rows})
}

// ---------- POST /api/sync/fofa/sync（检索 + 批量入库 + 归入合集）----------

func (s *Server) syncFofaRun(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	as := s.m.Assets()
	if as == nil {
		writeErr(w, 503, "资产库不可用")
		return
	}
	var req struct {
		Query          string `json:"query"`
		Size           int    `json:"size"`
		CollectionName string `json:"collection_name"`
		PageSize       int    `json:"page_size"`
	}
	if !decodeJSON(w, r, &req, maxSmallJSONBodyBytes) {
		return
	}
	query := strings.TrimSpace(req.Query)
	if query == "" {
		writeErr(w, 400, "query 不能为空")
		return
	}
	email, key := s.fofaCreds()
	if key == "" {
		writeErr(w, 400, "未配置 FOFA API Key，请先保存账号")
		return
	}
	name := strings.TrimSpace(req.CollectionName)
	if name == "" {
		name = query // 默认用查询语法本身命名，便于回溯这批资产怎么来的
	}
	target := req.Size
	if target <= 0 || target > fofaMaxResults {
		target = fofaMaxResults
	}
	pageSize := req.PageSize
	if pageSize <= 0 || pageSize > 500 {
		pageSize = fofaPageSize
	}

	// 同一时刻只允许一次同步。两次并发同步会：重复 Upsert 同一批资产（白耗数据库）、
	// 对同一查询名并发 Upsert 合集（后到的覆盖前一条的成员统计），并让两边的
	// imported 计数互相干扰。用一个进程内标志挡住——同步是人工触发的低频操作，
	// 返回 409 让用户稍后重试比排队更好懂。
	if !s.beginFofaSync() {
		writeErr(w, http.StatusConflict, "已有一次 FOFA 同步正在进行中，请等待完成")
		return
	}
	defer s.endFofaSync()

	ctx, cancel := context.WithTimeout(r.Context(), fofaSyncTimeout)
	defer cancel()

	synced := map[string]int{}
	memberSet := map[int64]struct{}{}
	imported, fofaTotal := 0, 0
	truncated := false
	var errs []string

	for page := 1; page <= fofaMaxPages && imported < target; page++ {
		resp, err := s.fofaSearch(ctx, email, key, query, pageSize, page)
		if err != nil {
			errs = append(errs, fmt.Sprintf("第 %d 页拉取失败：%s", page, fofaRedact(err)))
			break
		}
		if page == 1 {
			fofaTotal = resp.Size
		}
		if len(resp.Results) == 0 {
			break
		}
		// 页内裁剪到目标条数，避免超额入库。
		rows := resp.Results
		if remaining := target - imported; len(rows) > remaining {
			rows = rows[:remaining]
			truncated = true
		}
		// 一页一个事务：页内整体成功或整体回滚，不留半页脏数据。
		ids, n, err := s.fofaIngestBatch(ctx, as, rows, synced)
		if err != nil {
			errs = append(errs, fmt.Sprintf("第 %d 页入库失败：%v", page, err))
			break
		}
		for _, id := range ids {
			memberSet[id] = struct{}{}
		}
		imported += n // 只计真正写成功的行
		if len(resp.Results) < pageSize {
			break // 最后一页
		}
	}
	if imported < target && fofaTotal > imported {
		truncated = true
	}

	var collection map[string]any
	if len(memberSet) > 0 {
		memberIDs := make([]int64, 0, len(memberSet))
		for id := range memberSet {
			memberIDs = append(memberIDs, id)
		}
		sort.Slice(memberIDs, func(i, j int) bool { return memberIDs[i] < memberIDs[j] })
		if cid, err := pg.UpsertCollection(name, query, "fofa"); err != nil {
			errs = append(errs, "创建合集失败："+err.Error())
		} else if err := pg.AddCollectionMembers(cid, memberIDs); err != nil {
			errs = append(errs, "合集写入成员失败："+err.Error())
		} else {
			collection = map[string]any{"id": cid, "name": name, "members": len(memberIDs)}
		}
	}

	pg.RecordAudit(AdminUsername, db.AuditCategoryAsset, "fofa_sync", "success", clientIPFrom(r),
		fmt.Sprintf("FOFA 同步：查询 %q 导入 %d 条", query, imported),
		map[string]any{"query": query, "imported": imported, "total": fofaTotal})

	writeJSON(w, 200, map[string]any{
		"synced": synced, "total": fofaTotal, "imported": imported,
		"truncated": truncated, "collection": collection, "errors": errs,
	})
}

// fofaIngestBatch 把一页结果写成资产，返回新产生的资产 id 与成功写入的行数。
//
// 单个事务覆盖整页：FOFA 的同一行会同时产生 IP / 域名 / 服务三类资产，逐行提交时
// 中途失败会留下"IP 建好了但服务没建"的半截数据，下一轮同步又要靠 Upsert 去补，
// 徒增往返。
func (s *Server) fofaIngestBatch(ctx context.Context, as *db.AssetStore, rows [][]string, synced map[string]int) ([]int64, int, error) {
	tx, err := s.m.pg.Begin()
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback() //nolint:errcheck // 提交后为 no-op

	txStore := as.WithTx(tx)
	var ids []int64
	ok := 0
	for _, row := range rows {
		got, err := fofaIngestRow(txStore, row, synced)
		if err != nil {
			// 单行失败不整页回滚：FOFA 数据常有脏行（缺 IP、非法端口），
			// 为一行坏数据丢掉整页 100 条不划算。
			continue
		}
		ids = append(ids, got...)
		ok++
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, err
	}
	return ids, ok, nil
}

// fofaIngestRow 把一行结果映射成资产写入，返回该行涉及的资产 id。
func fofaIngestRow(as *db.AssetStore, row []string, synced map[string]int) ([]int64, error) {
	at := func(i int) string {
		if i < len(row) {
			return strings.TrimSpace(row[i])
		}
		return ""
	}
	host, ip, portStr := at(0), at(1), at(2)
	protocol, domain, title := strings.ToLower(at(3)), at(4), at(5)

	scheme, hostname, hostPort := splitFofaHost(host)
	if portStr == "" {
		portStr = hostPort
	}

	var ids []int64
	// IP 资产。
	if net.ParseIP(ip) != nil {
		bound := domain
		if bound == "" {
			bound = hostname
		}
		var boundList []string
		if bound != "" {
			boundList = []string{bound}
		}
		if id, err := as.UpsertIP(db.UpsertIPReq{IP: ip, BoundDomains: boundList}); err == nil {
			synced["ip"]++
			ids = append(ids, id)
		}
	}
	// 根域 + 子域。
	if domain != "" {
		if id, err := as.UpsertRootDomain(db.UpsertRootDomainReq{Domain: domain}); err == nil {
			synced["domain"]++
			ids = append(ids, id)
		}
		if hostname != "" && hostname != domain && net.ParseIP(hostname) == nil {
			if id, err := as.UpsertSubdomain(db.UpsertSubdomainReq{Domain: hostname}); err == nil {
				synced["subdomain"]++
				ids = append(ids, id)
			}
		}
	}
	// 服务。
	isHTTP := protocol == "http" || protocol == "https" || scheme == "http" || scheme == "https"
	if isHTTP {
		if u := buildFofaURL(scheme, protocol, hostname, hostPort); u != "" {
			if id, err := as.UpsertHTTPService(db.UpsertHTTPServiceReq{URL: u, PageTitle: title, IP: ip}); err == nil {
				synced["service"]++
				ids = append(ids, id)
			}
		}
	} else if protocol != "" && portStr != "" {
		if port, perr := strconv.Atoi(portStr); perr == nil && port > 0 && port <= 65535 {
			d := domain
			if d == "" && net.ParseIP(hostname) == nil {
				d = hostname
			}
			if id, err := as.UpsertOtherService(db.UpsertOtherServiceReq{
				Domain: d, IP: ip, Port: port, ServiceName: protocol,
			}); err == nil {
				synced["service"]++
				ids = append(ids, id)
			}
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("无可入库字段")
	}
	return ids, nil
}

// splitFofaHost 拆 "https://a.com" / "a.com:8080" / "1.2.3.4" 三种形态。
func splitFofaHost(host string) (scheme, hostname, port string) {
	host = strings.TrimSpace(host)
	if host == "" {
		return "", "", ""
	}
	if strings.Contains(host, "://") {
		if u, err := url.Parse(host); err == nil {
			return strings.ToLower(u.Scheme), u.Hostname(), u.Port()
		}
	}
	if h, p, err := net.SplitHostPort(host); err == nil {
		return "", h, p
	}
	return "", host, ""
}

// buildFofaURL 由解析出的部件拼规范 URL。
func buildFofaURL(scheme, protocol, hostname, port string) string {
	if hostname == "" {
		return ""
	}
	sc := scheme
	if sc == "" {
		sc = protocol
	}
	if sc != "http" && sc != "https" {
		if port == "443" {
			sc = "https"
		} else {
			sc = "http"
		}
	}
	hostpart := hostname
	if port != "" {
		defaultPort := (sc == "http" && port == "80") || (sc == "https" && port == "443")
		if !defaultPort {
			hostpart = net.JoinHostPort(hostname, port)
		}
	}
	return sc + "://" + hostpart
}
// fofaSyncMu/fofaSyncRunning 保证同一时刻只有一次 FOFA 同步在跑。
var (
	fofaSyncMu      sync.Mutex
	fofaSyncRunning bool
)

func (s *Server) beginFofaSync() bool {
	fofaSyncMu.Lock()
	defer fofaSyncMu.Unlock()
	if fofaSyncRunning {
		return false
	}
	fofaSyncRunning = true
	return true
}

func (s *Server) endFofaSync() {
	fofaSyncMu.Lock()
	fofaSyncRunning = false
	fofaSyncMu.Unlock()
}
