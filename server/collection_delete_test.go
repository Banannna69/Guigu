// collection_delete_test.go：删除资产合集时对既有关联的保护。
// 主要函数/类型：
//   - TestDeleteCollectionKeepsTaskAttributionFromOtherSources：
//     资产同时属于企业与合集时，删合集不得把任务归属一并删掉。

package server

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"artex/db"
)

// TestDeleteCollectionKeepsTaskAttributionFromOtherSources 复现并钉住一个数据丢失
// 缺陷：task_asset_links 的主键是 (task_id, asset_id)，source 是单值；同一资产
// 既被企业引入又被合集引入时，后跑的 insertTaskCollections 会用其来源覆盖企业的。
// 此时删除合集若按「source='collection'」去清理，就会把这个资产从任务里抹掉——
// 即使它本来是通过企业关联进来的。
//
// 正确语义：只要该资产仍能通过其他方式（企业范围、显式范围、agent 发现等）进入
// 任务，删除合集就不能动它的任务归属。
func TestDeleteCollectionKeepsTaskAttributionFromOtherSources(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer m.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	td := t.TempDir()
	s := New(ctx, m, td, td, td)
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = s.Stop(stopCtx)
	})

	stamp := time.Now().UnixNano()
	// 唯一 IP，避免与其他用例或历史数据碰撞。
	ip := fmt.Sprintf("198.18.%d.%d", (stamp/250)%250+1, stamp%250+1)

	// 企业 + 该企业范围内的一个资产。
	companyID, _, _, _, _, err := m.pg.Companies().CreateCompanyWithScope(
		"合集删除测试企业-"+fmt.Sprint(stamp), "",
		[]db.ScopeInput{{Kind: "ip", Value: ip}}, "test")
	if err != nil {
		t.Skipf("企业写入不可用: %v", err)
	}
	assetID, err := m.pg.Assets().UpsertIP(db.UpsertIPReq{IP: ip})
	if err != nil {
		t.Fatal(err)
	}
	// 让资产真正归属到该企业（scope 规则匹配后需要一次归属刷新）。
	if _, err := m.pg.Exec(`UPDATE assets SET company_id=$1, company_source='scope' WHERE id=$2`, companyID, assetID); err != nil {
		t.Fatal(err)
	}

	// 合集，也包含同一个资产。
	cid, err := m.pg.UpsertCollection("合集删除测试-"+fmt.Sprint(stamp), "ip="+ip, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.pg.AddCollectionMembers(cid, []int64{assetID}); err != nil {
		t.Fatal(err)
	}

	// 建任务：同时关联企业与合集。
	task, err := m.CreateTaskWithOptions("合集删除测试任务", "目标", db.TaskCreateOptions{
		CompanyIDs:    []int64{companyID},
		CollectionIDs: []int64{cid},
	})
	if err != nil {
		t.Fatal(err)
	}
	taskID := mustTaskID(t, task.ID)

	// 前置确认：资产已进入任务。
	var inTask bool
	if err := m.pg.QueryRow(`SELECT $1 = ANY(task_ids) FROM assets WHERE id=$2`, taskID, assetID).Scan(&inTask); err != nil {
		t.Fatal(err)
	}
	if !inTask {
		t.Fatal("前置条件失败：资产未进入任务")
	}

	// 删除合集。
	if err := m.pg.DeleteCollection(cid); err != nil {
		t.Fatal(err)
	}

	// 核心断言：资产仍通过企业关联留在任务里。
	var stillInTask bool
	if err := m.pg.QueryRow(`SELECT $1 = ANY(task_ids) FROM assets WHERE id=$2`, taskID, assetID).Scan(&stillInTask); err != nil {
		t.Fatal(err)
	}
	if !stillInTask {
		t.Fatal("删除合集错误地移除了任务归属——该资产仍应通过企业关联留在任务中")
	}

	// 清理。
	_, _ = m.pg.Exec(`DELETE FROM tasks WHERE id=$1`, taskID)
	_, _ = m.pg.Exec(`DELETE FROM assets WHERE id=$1`, assetID)
	_, _ = m.pg.Exec(`DELETE FROM companies WHERE id=$1`, companyID)
}
// TestDeleteCollectionKeepsMembershipFromOtherCollection 覆盖多合集共享资产的场景：
// 同一资产同时在合集 A 和 B 里，删 A 之后它仍应通过 B 留在任务中。
func TestDeleteCollectionKeepsMembershipFromOtherCollection(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer m.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	td := t.TempDir()
	s := New(ctx, m, td, td, td)
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = s.Stop(stopCtx)
	})

	stamp := time.Now().UnixNano()
	ip := fmt.Sprintf("198.19.%d.%d", (stamp/250)%250+1, stamp%250+1)
	assetID, err := m.pg.Assets().UpsertIP(db.UpsertIPReq{IP: ip})
	if err != nil {
		t.Fatal(err)
	}
	cidA, err := m.pg.UpsertCollection("合集A-"+fmt.Sprint(stamp), "ip="+ip, "test")
	if err != nil {
		t.Fatal(err)
	}
	cidB, err := m.pg.UpsertCollection("合集B-"+fmt.Sprint(stamp), "ip="+ip, "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, cid := range []int64{cidA, cidB} {
		if err := m.pg.AddCollectionMembers(cid, []int64{assetID}); err != nil {
			t.Fatal(err)
		}
	}

	task, err := m.CreateTaskWithOptions("多合集任务", "目标", db.TaskCreateOptions{
		CollectionIDs: []int64{cidA, cidB},
	})
	if err != nil {
		t.Fatal(err)
	}
	taskID := mustTaskID(t, task.ID)

	// 删掉 A。
	if err := m.pg.DeleteCollection(cidA); err != nil {
		t.Fatal(err)
	}

	// 资产应仍通过 B 留在任务里。
	var stillInTask bool
	if err := m.pg.QueryRow(`SELECT $1 = ANY(task_ids) FROM assets WHERE id=$2`, taskID, assetID).Scan(&stillInTask); err != nil {
		t.Fatal(err)
	}
	if !stillInTask {
		t.Fatal("删除合集 A 移除了资产，但它仍属于合集 B 并应留在任务中")
	}

	// 再删 B —— 此时已无来源，应当移出。
	if err := m.pg.DeleteCollection(cidB); err != nil {
		t.Fatal(err)
	}
	if err := m.pg.QueryRow(`SELECT $1 = ANY(task_ids) FROM assets WHERE id=$2`, taskID, assetID).Scan(&stillInTask); err != nil {
		t.Fatal(err)
	}
	if stillInTask {
		t.Fatal("两个合集都删除后，资产应已移出任务")
	}

	_, _ = m.pg.Exec(`DELETE FROM tasks WHERE id=$1`, taskID)
	_, _ = m.pg.Exec(`DELETE FROM assets WHERE id=$1`, assetID)
}

// TestCollectionScopeMatchUsesHashedSubplan 是性能回归守卫。
//
// 合集匹配若写成 IN (SELECT ... WHERE m.collection_id = ts.collection_id)，优化器
// 会把它下推成 Join Filter 里的未哈希子查询，每扫一行资产重跑一次（实测 1001 行
// 时 38.9ms）。EXISTS 形式改用 hashed SubPlan，同条件 0.36ms。
//
// 这里不比对绝对耗时（CI 机器差异大），而是查计划里有没有出现未哈希的子查询。
func TestCollectionScopeMatchUsesHashedSubplan(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer m.Close()

	// 造一个带合集的 task_scope 行，确保合集分支真的会被求值。
	stamp := time.Now().UnixNano()
	var taskID int64
	if err := m.pg.QueryRow(`SELECT id FROM tasks WHERE deleted_at IS NULL LIMIT 1`).Scan(&taskID); err != nil {
		t.Skip("库里没有可用任务，跳过计划检查")
	}
	ip := fmt.Sprintf("198.20.%d.%d", (stamp/250)%250+1, stamp%250+1)
	assetID, err := m.pg.Assets().UpsertIP(db.UpsertIPReq{IP: ip})
	if err != nil {
		t.Fatal(err)
	}
	cid, err := m.pg.UpsertCollection("计划检查-"+fmt.Sprint(stamp), "ip="+ip, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.pg.AddCollectionMembers(cid, []int64{assetID}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.pg.Exec(`INSERT INTO task_scope(task_id, kind, collection_id) VALUES($1,'collection',$2)`, taskID, cid); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = m.pg.Exec(`DELETE FROM task_scope WHERE task_id=$1 AND collection_id=$2`, taskID, cid)
		_, _ = m.pg.Exec(`DELETE FROM asset_collections WHERE id=$1`, cid)
		_, _ = m.pg.Exec(`DELETE FROM assets WHERE id=$1`, assetID)
	})
	_, _ = m.pg.Exec(`ANALYZE assets`)
	_, _ = m.pg.Exec(`ANALYZE task_scope`)
	_, _ = m.pg.Exec(`ANALYZE asset_collection_members`)

	// 用与应用相同的谓词构造一条查询并取计划。
	var plan string
	err = m.pg.QueryRow(`
EXPLAIN (COSTS OFF)
SELECT a.id FROM assets a
JOIN task_scope ts ON `+db.TaskScopeAssetMatchSQLForTest()+`
WHERE ts.task_id=$1`, taskID).Scan(&plan)
	if err != nil {
		t.Fatalf("取执行计划失败: %v", err)
	}
	// hashed SubPlan 是我们要的形态；裸 SubPlan 意味着每行重算。
	if strings.Contains(plan, "SubPlan") && !strings.Contains(plan, "hashed SubPlan") {
		t.Fatalf("合集匹配退化成了未哈希子查询（每行资产重算一次）:\n%s", plan)
	}
}
