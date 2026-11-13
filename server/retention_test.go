// retention_test.go：数据留存策略与清理的回归。
// 主要函数/类型：
//   - TestRetentionDisabledByDefault：未开启时一行都不删（删数据不可逆，不能默认开）。
//   - TestRetentionPrunesOnlyArchivedTasksActivity：只清已归档任务的 activity。
//   - TestRetentionPrunesOldTraffic：按时间清理流量，且只清早于阈值的。
//   - TestRetentionRejectsNegativeDays：负数天数必须被拒绝（否则等于"立即清空"）。

package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"artex/db"
)

// TestRetentionDisabledByDefault 保证升级本身不会替用户删数据。
func TestRetentionDisabledByDefault(t *testing.T) {
	s := &Server{}
	if pol := s.retentionPolicySnapshot(); pol.Enabled {
		t.Fatal("留存清理默认必须是关闭的")
	}
}

// TestRetentionPrunesOnlyArchivedTasksActivity 是这次改动的安全属性：activity 是
// 「这条意图是怎么执行的」的审计线索，正在跑或只是暂停的任务丢了它就再也解释不清
// agent 做过什么。只有运维明确归档过的任务才可清。
func TestRetentionPrunesOnlyArchivedTasksActivity(t *testing.T) {
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

	live, err := m.CreateTaskWithOptions("留存-活跃任务", "目标", db.TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	old, err := m.CreateTaskWithOptions("留存-已归档任务", "目标", db.TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}

	liveID := mustTaskID(t, live.ID)
	oldID := mustTaskID(t, old.ID)
	// 两个任务各写一条 activity。
	addActivity := func(taskID int64, summary string) {
		t.Helper()
		var expID int64
		if err := m.pg.QueryRow(`SELECT exploration_id FROM tasks WHERE id=$1`, taskID).Scan(&expID); err != nil {
			t.Fatal(err)
		}
		if _, err := m.pg.Exec(`INSERT INTO activity(exploration_id, kind, summary) VALUES($1,'tool',$2)`, expID, summary); err != nil {
			t.Fatal(err)
		}
	}
	// 用含纳秒的唯一标记：本包所有测试共享一个库，固定字符串会撞上其他用例或
	// 上次运行留下的同 summary 行，count 断言就失去意义。
	stamp := time.Now().UnixNano()
	liveProbe := fmt.Sprintf("live-probe-%d", stamp)
	archivedProbe := fmt.Sprintf("archived-probe-%d", stamp)
	addActivity(liveID, liveProbe)
	addActivity(oldID, archivedProbe)

	// 把旧任务标记为「很久以前归档」。
	if _, err := m.pg.Exec(`UPDATE tasks SET archived_at=now()-interval '400 days' WHERE id=$1`, oldID); err != nil {
		t.Fatal(err)
	}

	n, err := s.pruneActivity(ctx, 30)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("已归档任务的旧 activity 应被清理")
	}

	countProbe := func(summary string) int {
		var c int
		_ = m.pg.QueryRow(`SELECT count(*) FROM activity WHERE summary=$1`, summary).Scan(&c)
		return c
	}
	if countProbe(archivedProbe) != 0 {
		t.Error("已归档任务的 activity 应被删除")
	}
	if countProbe(liveProbe) != 1 {
		t.Error("活跃任务的 activity 不得被删除")
	}
}

// TestRetentionPrunesOldTraffic 保证按时间清理流量，且不误删新记录。
func TestRetentionPrunesOldTraffic(t *testing.T) {
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
	tr := m.Traffic()
	if tr == nil {
		t.Skip("traffic store 未启用")
	}

	before, err := tr.Count()
	if err != nil {
		t.Fatal(err)
	}
	// 直接写两条 exchange：一条 40 天前，一条现在。
	ins := func(id string, ts int64) {
		t.Helper()
		if _, err := tr.DB().Exec(`INSERT INTO exchanges(id,ts,host,method,url,status,content_type,req_len,resp_len,path,url_template)
VALUES(?,?,?,?,?,?,?,?,?,?,?)`, id, ts, "retention.test", "GET", "http://retention.test/", 200, "text/plain", 0, 0, "/", "/"); err != nil {
			t.Fatal(err)
		}
	}
	stamp := time.Now().UnixNano()
	oldID := fmt.Sprintf("ret-old-%d", stamp)
	newID := fmt.Sprintf("ret-new-%d", stamp)
	ins(oldID, time.Now().AddDate(0, 0, -40).Unix())
	ins(newID, time.Now().Unix())

	n, err := s.pruneTraffic(ctx, 30)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		after, _ := tr.Count()
		if after != before { // 库里可能有更老的记录，那也算清理成功
			t.Fatalf("40 天前的流量应被清理，删除 %d 条", n)
		}
	}
	var oldLeft, newLeft int
	_ = tr.DB().QueryRow(`SELECT count(*) FROM exchanges WHERE id=?`, oldID).Scan(&oldLeft)
	_ = tr.DB().QueryRow(`SELECT count(*) FROM exchanges WHERE id=?`, newID).Scan(&newLeft)
	if oldLeft != 0 {
		t.Error("早于阈值的流量应被删除")
	}
	if newLeft != 1 {
		t.Error("新流量不得被删除")
	}
}

// TestRetentionRejectsNegativeDays 覆盖误操作防护：负数天数若被接受，等价于把
// 阈值放到未来，一次清理就会删光该类别下的全部数据。
func TestRetentionRejectsNegativeDays(t *testing.T) {
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

	req := httptest.NewRequest(http.MethodPut, "/api/settings/retention",
		strings.NewReader(`{"enabled":true,"activity_days":-1}`))
	req.Header.Set("Authorization", "Bearer "+mustTestToken(t, s))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("负数天数应返回 400，得到 %d：%s", rec.Code, rec.Body.String())
	}
}

// mustTestToken 签一枚当前版本的 token。
func mustTestToken(t *testing.T, s *Server) string {
	t.Helper()
	tok, err := s.signTestJWT()
	if err != nil {
		t.Fatal(err)
	}
	return tok
}