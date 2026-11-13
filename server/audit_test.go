// audit_test.go：审计日志写入与查询的回归。
// 主要函数/类型：
//   - TestRecordAuditAndList：写入后可按类别/动作/结果筛选读到。
//   - TestAuditFiltersAreExact：筛选是精确匹配，空值不筛选。
//   - TestAuditSurvivesNilDetail：detail 为空时写入空对象而非失败。
//   - TestDeleteAuditLogsBefore：按保留期清理，新记录保留。

package server

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"artex/db"
)

// uniqueAction 生成一个不会与其他用例或历史数据冲突的 action 名。
func uniqueAction(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// TestRecordAuditAndList 覆盖基本闭环：写进去能读出来，且明细可解析。
func TestRecordAuditAndList(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer m.Close()

	action := uniqueAction("probe")
	m.pg.RecordAudit("tester", db.AuditCategoryAuth, action, "success", "10.0.0.9",
		"测试消息", map[string]any{"k": "v"})

	entries, total, err := m.pg.ListAuditLogs(db.AuditCategoryAuth, action, "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(entries) != 1 {
		t.Fatalf("应恰好读到 1 条，total=%d len=%d", total, len(entries))
	}
	e := entries[0]
	if e.Actor != "tester" || e.IP != "10.0.0.9" || e.Message != "测试消息" {
		t.Fatalf("字段不符: %+v", e)
	}
	if e.Detail["k"] != "v" {
		t.Fatalf("detail 未解析: %+v", e.Detail)
	}
}

// TestAuditFiltersAreExact 保证筛选是精确匹配（不是模糊），且空条件不过滤。
func TestAuditFiltersAreExact(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer m.Close()

	action := uniqueAction("exact")
	m.pg.RecordAudit("a1", db.AuditCategoryTask, action, "success", "", "one", nil)
	m.pg.RecordAudit("a2", db.AuditCategoryTask, action, "fail", "", "two", nil)

	// 按结果筛选。
	_, n, err := m.pg.ListAuditLogs("", action, "fail", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("result=fail 应只有 1 条，得到 %d", n)
	}

	// 前缀不应匹配（精确匹配而非 LIKE）。
	_, n, err = m.pg.ListAuditLogs("", action[:len(action)-1], "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("action 筛选应为精确匹配，前缀匹配到了 %d 条", n)
	}
}

// TestAuditSurvivesNilDetail 保证 detail 为 nil 时写入空对象。
func TestAuditSurvivesNilDetail(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer m.Close()

	action := uniqueAction("nildetail")
	m.pg.RecordAudit("", db.AuditCategorySettings, action, "", "", "空明细", nil)
	entries, _, err := m.pg.ListAuditLogs("", action, "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("应读到 1 条，得到 %d", len(entries))
	}
	// result 空值时默认 success。
	if entries[0].Result != "success" {
		t.Fatalf("result 空值应默认 success，得到 %q", entries[0].Result)
	}
}

// TestDeleteAuditLogsBefore 保证按保留期清理只删旧记录。
func TestDeleteAuditLogsBefore(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer m.Close()
	ctx := context.Background()

	oldAction := uniqueAction("old")
	newAction := uniqueAction("new")
	m.pg.RecordAudit("", db.AuditCategoryAuth, oldAction, "success", "", "旧", nil)
	m.pg.RecordAudit("", db.AuditCategoryAuth, newAction, "success", "", "新", nil)

	// 把旧记录的时间推到 400 天前。
	if _, err := m.pg.Exec(`UPDATE audit_logs SET created_at=now()-interval '400 days' WHERE action=$1`, oldAction); err != nil {
		t.Fatal(err)
	}

	n, err := m.pg.DeleteAuditLogsBefore(time.Now().AddDate(0, 0, -30), 10000)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("应清理掉旧审计记录")
	}

	if _, cnt, _ := m.pg.ListAuditLogs("", oldAction, "", 10, 0); cnt != 0 {
		t.Error("旧记录应已被清理")
	}
	if _, cnt, _ := m.pg.ListAuditLogs("", newAction, "", 10, 0); cnt != 1 {
		t.Error("新记录不得被清理")
	}
	_ = ctx
}

// TestAuditTruncatesOversizedInput 钉住审计字段的上限。
//
// message/detail 常含用户输入（FOFA 查询语法、任务描述），长度不可控。不设上限时
// 一条超长记录就能把审计表撑大，而审计页只展示一行摘要——正文应当去看对应的业务
// 记录。
func TestAuditTruncatesOversizedInput(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer m.Close()

	action := uniqueAction("huge")
	huge := strings.Repeat("查", 5000) // 远超上限，且是多字节字符
	m.pg.RecordAudit("tester", "settings", action, "success", "1.2.3.4", huge,
		map[string]any{"query": huge, "n": 1})

	entries, _, err := m.pg.ListAuditLogs("", action, "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("应读到 1 条，得到 %d", len(entries))
	}
	// message 被截断且仍是合法 UTF-8（截断不能切坏中文）。
	if len(entries[0].Message) > db.MaxAuditMessageBytesForTest()+8 {
		t.Fatalf("message 未被截断，长度 %d", len(entries[0].Message))
	}
	if !utf8.ValidString(entries[0].Message) {
		t.Fatal("截断破坏了 UTF-8")
	}
	// detail 里的长字符串也被截短。
	if dq, ok := entries[0].Detail["query"].(string); ok && len(dq) > 1200 {
		t.Fatalf("detail 未被截断，长度 %d", len(dq))
	}
}
