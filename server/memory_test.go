// memory_test.go：跨任务记忆的作用域隔离与开关语义。
// 主要函数/类型：
//   - TestMemoryScopeDirName：作用域 → 目录名（企业隔离的基础）。
//   - TestMemoryStoreForTaskIsolation：不同企业拿到不同目录，绝不互相注入。
//   - TestMemoryDisabledByDefault：开关关闭时 resolver 返回 nil。
//   - TestMemoryOptionsBudget：注入条数与索引策略。

package server

import (
	"context"
	"testing"
	"time"

	"artex/agent"
)

// TestMemoryScopeDirName 钉住作用域到目录的映射。企业 id 必须体现为独立目录，
// 否则不同客户的记忆会写进同一处并在下一次 AutoInject 时互相污染。
func TestMemoryScopeDirName(t *testing.T) {
	cases := []struct {
		scope agent.MemoryScope
		want  string
	}{
		{agent.MemoryScope{Global: true}, "global"},
		{agent.MemoryScope{CompanyID: 7}, "company-7"},
		{agent.MemoryScope{CompanyID: 123}, "company-123"},
		// 无法归属（id<=0）时退回全局：宁可共享无标识的通用手法，
		// 也不把记忆散落到匿名目录里。
		{agent.MemoryScope{}, "global"},
		{agent.MemoryScope{CompanyID: -1}, "global"},
	}
	for _, c := range cases {
		if got := c.scope.DirName(); got != c.want {
			t.Errorf("%+v → %q，期望 %q", c.scope, got, c.want)
		}
	}
}

// TestMemoryStoreForTaskIsolation 是这次实现的核心安全属性：A 客户的记忆目录与
// B 客户的必须不同，且不相关任务拿到的是全局目录。
func TestMemoryStoreForTaskIsolation(t *testing.T) {
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

	// 共享库：无论进来时是什么状态，先恢复到「关」再开始，结束时还原原值。
	prev, hadPrev, _ := m.pg.GetSetting("memory_enabled")
	t.Cleanup(func() {
		if hadPrev {
			_ = m.pg.SetSetting("memory_enabled", prev)
		} else {
			_, _ = m.pg.Exec(`DELETE FROM settings WHERE key='memory_enabled'`)
		}
	})
	if err := s.m.SetMemoryEnabled(false); err != nil {
		t.Fatal(err)
	}

	// 关闭时应返回 nil。
	if got := s.memoryStoreForTask(&Task{CompanyIDs: []int64{7}}); got != nil {
		t.Fatalf("记忆关闭时应返回 nil，得到 %q", got.Dir)
	}

	if err := s.m.SetMemoryEnabled(true); err != nil {
		t.Fatal(err)
	}

	a := s.memoryStoreForTask(&Task{CompanyIDs: []int64{7}})
	b := s.memoryStoreForTask(&Task{CompanyIDs: []int64{8}})
	global := s.memoryStoreForTask(&Task{})

	if a == nil || b == nil || global == nil {
		t.Fatal("开启后应返回存储")
	}
	if a.Dir == b.Dir {
		t.Fatalf("不同企业的记忆目录必须不同，都是 %q", a.Dir)
	}
	if a.Dir == global.Dir {
		t.Fatalf("企业记忆不得与全局记忆共用目录：%q", a.Dir)
	}
	if global.Dir == b.Dir {
		t.Fatalf("全局记忆不得与任一企业记忆共用目录：%q", global.Dir)
	}
}

// TestMemoryDisabledByDefault 保证升级本身不会改变模型行为。
func TestMemoryDisabledByDefault(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer m.Close()
	// 共享库可能被其他用例置过；清掉后才是「全新实例」的默认语义。
	_, _ = m.pg.Exec(`DELETE FROM settings WHERE key='memory_enabled'`)
	if m.MemoryEnabled() {
		t.Fatal("记忆默认必须是关闭的")
	}
}

// TestMemoryOptionsBudget 钉住注入预算：AutoInject 开、最多 3 条、不注入常驻索引。
//
// 索引会随记忆增长无限变长并常驻系统提示，最终挤占正文；Recall 工具已能按需检索，
// 用宝贵上下文换这一点便利不划算。
func TestMemoryOptionsBudget(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer m.Close()
	store := agent.MemoryStoreFor(t.TempDir(), agent.MemoryScope{Global: true})
	opts := agent.MemoryOptionsFor(store)
	if opts == nil {
		t.Fatal("非 nil store 应得到选项")
	}
	if !opts.AutoInject {
		t.Error("AutoInject 应开启——记忆的价值就在于模型不必主动想起去查")
	}
	if opts.MaxInject != 3 {
		t.Errorf("MaxInject 应为 3，得到 %d", opts.MaxInject)
	}
	if opts.IncludeIndexInPrompt {
		t.Error("不应把常驻索引注入系统提示（随记忆增长无限膨胀）")
	}
	if agent.MemoryOptionsFor(nil) != nil {
		t.Error("nil store 应返回 nil 选项")
	}
}