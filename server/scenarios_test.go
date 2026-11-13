// scenarios_test.go：场景包的核心语义回归。
// 主要函数/类型：
//   - TestScenarioAppendIsAdditive：场景是追加，不是替换（空场景零影响）。
//   - TestScenarioSnapshotFrozenAtCreation：建任务后改场景包不影响已建任务。
//   - TestScenarioSkillsAndDisabledTools：场景带来的 skill 与工具禁用能读到。
//   - TestScenarioMultipleStack：多场景按选择顺序叠加。

package server

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"artex/db"
)

func newScenarioServer(t *testing.T) *Server {
	t.Helper()
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { m.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	td := t.TempDir()
	s := New(ctx, m, td, td, td)
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = s.Stop(stopCtx)
	})
	return s
}

// TestScenarioAppendIsAdditive 钉住本次设计的核心：**不选场景时零影响**。
//
// 这是与「分类带出模板」最重要的差异。若 TaskScenarioAppend 在无绑定时返回非空，
// 通用任务就会被莫名注入行业文本——那正是这个功能最不能犯的错。
func TestScenarioAppendIsAdditive(t *testing.T) {
	s := newScenarioServer(t)

	task, err := s.m.CreateTaskWithOptions("无场景任务", "目标", db.TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	tid := mustTaskID(t, task.ID)
	t.Cleanup(func() { _, _ = s.m.pg.Exec(`DELETE FROM tasks WHERE id=$1`, tid) })

	for _, agent := range []string{"planner", "worker"} {
		if got := s.m.pg.TaskScenarioAppend(tid, agent); got != "" {
			t.Fatalf("未绑定场景时 %s 追加必须为空串，得到 %q", agent, got)
		}
	}
	if skills := s.m.pg.TaskScenarioSkills(tid); len(skills) != 0 {
		t.Fatalf("未绑定场景时不应有额外 skill，得到 %v", skills)
	}
	if tools := s.m.pg.TaskScenarioDisabledTools(tid); len(tools) != 0 {
		t.Fatalf("未绑定场景时不应禁用工具，得到 %v", tools)
	}
}

// TestScenarioSnapshotFrozenAtCreation 保证建任务后再改场景包，不影响已建任务。
// 与 prompt 快照同一策略：任务的可复现性优先于「配置即时生效」。
func TestScenarioSnapshotFrozenAtCreation(t *testing.T) {
	s := newScenarioServer(t)
	stamp := time.Now().UnixNano()

	packID, err := s.m.pg.CreateScenarioPack(db.ScenarioPackInput{
		Name:          fmt.Sprintf("快照测试-%d", stamp),
		PlannerAppend: "原始 planner 追加",
		WorkerAppend:  "原始 worker 追加",
		Skills:        []string{"skill-a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = s.m.pg.Exec(`DELETE FROM scenario_packs WHERE id=$1`, packID) })

	task, err := s.m.CreateTaskWithOptions("快照任务", "目标", db.TaskCreateOptions{
		ScenarioPackIDs: []int64{packID},
	})
	if err != nil {
		t.Fatal(err)
	}
	tid := mustTaskID(t, task.ID)
	t.Cleanup(func() { _, _ = s.m.pg.Exec(`DELETE FROM tasks WHERE id=$1`, tid) })

	if got := s.m.pg.TaskScenarioAppend(tid, "planner"); got != "原始 planner 追加" {
		t.Fatalf("建任务时应读到原始追加，得到 %q", got)
	}

	// 改场景包。
	if err := s.m.pg.UpdateScenarioPack(packID, db.ScenarioPackInput{
		Name:          fmt.Sprintf("快照测试-%d", stamp),
		PlannerAppend: "改过的 planner 追加",
		WorkerAppend:  "改过的 worker 追加",
	}); err != nil {
		t.Fatal(err)
	}

	// 已建任务必须仍读原始快照。
	if got := s.m.pg.TaskScenarioAppend(tid, "planner"); got != "原始 planner 追加" {
		t.Fatalf("改包后已建任务应保持原始快照，得到 %q", got)
	}
	if got := s.m.pg.TaskScenarioAppend(tid, "worker"); got != "原始 worker 追加" {
		t.Fatalf("改包后已建任务应保持原始快照，得到 %q", got)
	}
}

// TestScenarioSkillsAndDisabledTools 保证场景能带出 skill 与工具禁用。
func TestScenarioSkillsAndDisabledTools(t *testing.T) {
	s := newScenarioServer(t)
	stamp := time.Now().UnixNano()

	packID, err := s.m.pg.CreateScenarioPack(db.ScenarioPackInput{
		Name:          fmt.Sprintf("技能测试-%d", stamp),
		WorkerAppend:  "x",
		Skills:        []string{"banking-finance", "api-recon"},
		DisabledTools: []string{"shell_"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = s.m.pg.Exec(`DELETE FROM scenario_packs WHERE id=$1`, packID) })

	task, err := s.m.CreateTaskWithOptions("技能任务", "目标", db.TaskCreateOptions{
		ScenarioPackIDs: []int64{packID},
	})
	if err != nil {
		t.Fatal(err)
	}
	tid := mustTaskID(t, task.ID)
	t.Cleanup(func() { _, _ = s.m.pg.Exec(`DELETE FROM tasks WHERE id=$1`, tid) })

	skills := s.m.pg.TaskScenarioSkills(tid)
	if len(skills) != 2 {
		t.Fatalf("应带出 2 个 skill，得到 %v", skills)
	}
	tools := s.m.pg.TaskScenarioDisabledTools(tid)
	if len(tools) != 1 || tools[0] != "shell_" {
		t.Fatalf("应禁用 shell_，得到 %v", tools)
	}
}

// TestScenarioMultipleStack 保证多场景按选择顺序叠加，且拼接不丢任一段。
func TestScenarioMultipleStack(t *testing.T) {
	s := newScenarioServer(t)
	stamp := time.Now().UnixNano()

	mk := func(tag string) int64 {
		id, err := s.m.pg.CreateScenarioPack(db.ScenarioPackInput{
			Name:          fmt.Sprintf("%s-%d", tag, stamp),
			PlannerAppend: "【" + tag + "】",
			Skills:        []string{"skill-" + tag},
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = s.m.pg.Exec(`DELETE FROM scenario_packs WHERE id=$1`, id) })
		return id
	}
	a, b := mk("AAA"), mk("BBB")

	task, err := s.m.CreateTaskWithOptions("叠加任务", "目标", db.TaskCreateOptions{
		ScenarioPackIDs: []int64{a, b},
	})
	if err != nil {
		t.Fatal(err)
	}
	tid := mustTaskID(t, task.ID)
	t.Cleanup(func() { _, _ = s.m.pg.Exec(`DELETE FROM tasks WHERE id=$1`, tid) })

	got := s.m.pg.TaskScenarioAppend(tid, "planner")
	if !strings.Contains(got, "【AAA】") || !strings.Contains(got, "【BBB】") {
		t.Fatalf("多场景应叠加，得到 %q", got)
	}
	// 顺序：先选的在前。
	if strings.Index(got, "【AAA】") > strings.Index(got, "【BBB】") {
		t.Fatalf("应按选择顺序拼接，得到 %q", got)
	}
	if skills := s.m.pg.TaskScenarioSkills(tid); len(skills) != 2 {
		t.Fatalf("两个场景的 skill 都应生效，得到 %v", skills)
	}
}