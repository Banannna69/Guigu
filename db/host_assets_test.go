package db

import (
	"fmt"
	"reflect"
	"strconv"
	"testing"
)

func TestBuildAssetHostRowsAggregatesHostAndDeduplicatesFindings(t *testing.T) {
	companyID := int64(1)
	port := 443
	assets := []*Asset{
		{ID: 1, Type: "ip", IP: "192.0.2.10", CompanyID: &companyID},
		{ID: 2, Type: "subdomain", Domain: "www.example.test", RootDomain: "example.test", RecordValue: []string{"192.0.2.10"}, CompanyID: &companyID},
		{ID: 3, Type: "subdomain", Domain: "api.example.test", RootDomain: "example.test", RecordValue: []string{"www.example.test"}},
		{ID: 4, Type: "service", Domain: "api.example.test", RootDomain: "example.test", URL: "https://api.example.test", Port: &port, CompanyID: &companyID},
		{ID: 5, Type: "endpoint", Domain: "api.example.test", RootDomain: "example.test", URL: "https://api.example.test/users", Method: "GET", CompanyID: &companyID},
		{ID: 6, Type: "ip", IP: "192.0.2.20"},
	}
	hits := []hostFindingHit{
		{id: 100, severity: "critical", assetIDs: []int64{4, 5}},
		{id: 101, severity: "low", assetIDs: []int64{5}},
	}
	rows := buildAssetHostRows(assets, map[int64]string{1: "示例企业"}, hits)
	if len(rows) != 2 {
		t.Fatalf("want two hosts, got %+v", rows)
	}
	host := rows[0]
	if host.IP != "192.0.2.10" || host.CompanyName != "示例企业" || host.RootDomain != "example.test" {
		t.Fatalf("wrong host projection: %+v", host)
	}
	if !reflect.DeepEqual(host.Subdomains, []string{"api.example.test", "www.example.test"}) {
		t.Fatalf("subdomains not aggregated: %+v", host.Subdomains)
	}
	if len(host.Services) != 2 || host.FindingCount != 2 || host.Critical != 1 || host.Low != 1 {
		t.Fatalf("services/findings not aggregated or deduplicated: %+v", host)
	}
	if !reflect.DeepEqual(host.AssetIDs, []int64{1, 2, 3, 4, 5}) {
		t.Fatalf("wrong row scope: %+v", host.AssetIDs)
	}
	bare := rows[1]
	if bare.IP != "192.0.2.20" || bare.RootDomain != "" || bare.Subdomains == nil || len(bare.Subdomains) != 0 || bare.FindingCount != 0 {
		t.Fatalf("bare IP must keep domains empty: %+v", bare)
	}
}

func TestQueryHostsAndExactAssetFindingFilter(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	tk, err := d.CreateTask("宿主投影测试", "目标", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(tk.ID)
	taskIDs := "{" + strconv.FormatInt(tk.ID, 10) + "}"
	ipID := seedTreeAsset(t, d, "ip", map[string]any{"ip": "192.0.2.88", "task_ids": taskIDs})
	subID := seedTreeAsset(t, d, "subdomain", map[string]any{
		"domain": "api.host-projection.test", "root_domain": "host-projection.test",
		"record_value": []string{"192.0.2.88"}, "task_ids": taskIDs,
	})
	svcID := seedTreeAsset(t, d, "service", map[string]any{
		"domain": "api.host-projection.test", "root_domain": "host-projection.test",
		"url": "https://api.host-projection.test", "task_ids": taskIDs,
	})
	defer d.Assets().DeleteByIDs([]int64{ipID, subID, svcID}) //nolint:errcheck
	if _, err := d.AddFinding(tk.ID, 0, "Host", "host test", "high", "s", "e", "w", []int64{svcID}); err != nil {
		t.Fatal(err)
	}
	rows, total, err := d.Assets().QueryHosts(AssetHostFilter{TaskID: tk.ID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(rows) != 1 || rows[0].IP != "192.0.2.88" || rows[0].FindingCount != 1 {
		t.Fatalf("unexpected host query result: total=%d rows=%+v", total, rows)
	}
	items, findingTotal, err := d.ListFindingsPage(FindingFilter{TaskID: strconv.FormatInt(tk.ID, 10), AssetIDs: rows[0].AssetIDs}, 1, 20)
	if err != nil || findingTotal != 1 || len(items) != 1 {
		t.Fatalf("exact host asset finding filter failed: total=%d items=%d err=%v", findingTotal, len(items), err)
	}
}

// TestQueryHostsNoFilterReturnsAll 验证不加过滤时把所有 host 都返回。
//
// QueryHosts 的三维过滤刚从「每个 host 重扫全部 assets」改成按资产 id 预建索引表，
// 这类重构最容易出的错是「条件写反」导致本该返回的被滤掉。这里用一个无过滤的
// 查询做基线：插入 N 个 IP，返回的 host 数必须是 N。
func TestQueryHostsNoFilterReturnsAll(t *testing.T) {
	d, as, _ := testSetup(t)
	defer d.Close()

	const n = 25
	var ips []string
	for i := 0; i < n; i++ {
		ip := fmt.Sprintf("10.87.%d.%d", i/20, (i%20)+1)
		ips = append(ips, ip)
		if _, err := as.UpsertIP(UpsertIPReq{IP: ip}); err != nil {
			t.Fatalf("upsert %d: %v", i, err)
		}
	}
	defer func() {
		for _, ip := range ips {
			d.Exec(`DELETE FROM assets WHERE ip = $1`, ip)
		}
	}()

	rows, total, err := as.QueryHosts(AssetHostFilter{Limit: 1000})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if total != len(rows) {
		t.Fatalf("total=%d 与返回行数 %d 不一致", total, len(rows))
	}
	// 库里可能有别的测试留下的资产，所以只断言「我们插的都在」
	got := map[string]bool{}
	for _, r := range rows {
		got[r.IP] = true
	}
	for _, ip := range ips {
		if !got[ip] {
			t.Fatalf("无过滤查询漏掉了 %s（过滤条件写反了？）", ip)
		}
	}
}

// TestQueryHostsTaskFilterExcludesOthers 验证任务过滤真的生效：
// 指定一个不存在的 taskID 时，不该返回那些没关联到该任务的 host。
func TestQueryHostsTaskFilterExcludesOthers(t *testing.T) {
	d, as, _ := testSetup(t)
	defer d.Close()

	const n = 10
	var ips []string
	for i := 0; i < n; i++ {
		ip := fmt.Sprintf("10.86.0.%d", i+1)
		ips = append(ips, ip)
		if _, err := as.UpsertIP(UpsertIPReq{IP: ip}); err != nil {
			t.Fatalf("upsert %d: %v", i, err)
		}
	}
	defer func() {
		for _, ip := range ips {
			d.Exec(`DELETE FROM assets WHERE ip = $1`, ip)
		}
	}()

	// 一个几乎不可能存在的 taskID
	rows, total, err := as.QueryHosts(AssetHostFilter{TaskID: 999999999, Limit: 1000})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if total != 0 || len(rows) != 0 {
		t.Fatalf("任务过滤未生效：total=%d rows=%d，期望 0", total, len(rows))
	}
}
