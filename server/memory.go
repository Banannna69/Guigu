// memory.go：按任务作用域解析记忆存储，并把开关接到三个 agent。
// 主要函数/类型：
//   - (s *Server) memoryStoreForTask：按任务所属企业解析记忆目录（未启用返回 nil）。
//   - wireMemory：把 resolver 挂到 worker/planner/mainagent 上。

package server

import (
	"artex/agent"
	"norma/memory"
)

// memoryStoreForTask 返回该任务可见的记忆存储，未启用（或无法归属）时返回 nil。
//
// 作用域规则（见 agent/memory.go 的说明）：任务关联了企业就用该企业的私有记忆，
// 否则用全局记忆。**绝不把企业私有记忆喂给不相关的任务**——那既是数据泄露，也是
// 跨授权范围的经验复用。
//
// 一个任务关联多个企业时取第一个：记忆注入是"提示方向"而非精确匹配，多企业任务
// 本就少见，为它引入记忆合并的复杂度不值得。
func (s *Server) memoryStoreForTask(t *Task) *memory.Store {
	if s == nil || s.m == nil || !s.m.MemoryEnabled() {
		return nil
	}
	scope := agent.MemoryScope{Global: true}
	if t != nil && len(t.CompanyIDs) > 0 {
		scope = agent.MemoryScope{CompanyID: t.CompanyIDs[0]}
	}
	return agent.MemoryStoreFor(s.m.dir, scope)
}

// wireMemory 把记忆 resolver 挂到三个执行类 agent 上。
//
// chat 不接：对话页是非任务场景，既没有稳定作用域也容易把不同客户的记忆混到一起。
func (s *Server) wireMemory(t *Task, wk *agent.Worker, pl *agent.Planner, main *agent.MainAgent) {
	resolver := func() *memory.Store { return s.memoryStoreForTask(t) }
	if wk != nil {
		wk.SetMemory(resolver)
	}
	if pl != nil {
		pl.SetMemory(resolver)
	}
	if main != nil {
		main.SetMemory(resolver)
	}
}
