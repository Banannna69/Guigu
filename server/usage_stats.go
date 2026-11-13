// usage_stats.go：工具与技能的调用统计聚合面——让「哪些工具/技能真的在用、哪些
// 装了没人碰」变成可查的数字，而不是靠猜。
// 主要函数/类型：
//   - toolUsageStats：GET /api/tools/stats，按工具键聚合调用次数。
//   - skillUsageStats：GET /api/skills/stats，按技能聚合调用次数与最近一次调用。
//
// 为什么值得单独做一页：这个平台的工具和技能可以装上几十个，其中真正被 agent 用到的
// 往往只有一小部分。没有统计数据时，用户只能凭印象判断——要么留着一堆从不触发的
// 定制工具占着工具列表（每一轮都在消耗模型的注意力与 token），要么误删了其实在用的那个。
// 有了数字，清理就有依据。
//
// 这两个接口只读、不写库，技能统计走的是已有的 skill_usage 台账（db/skill_usage.go），
// 工具统计走 tool_usage（每次工具调用都由 meterTool 记一行）。

package server

import (
	"net/http"

	"artex/db"
)

// toolUsageStats GET /api/tools/stats
func (s *Server) toolUsageStats(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	counts, err := pg.ToolUsageCounts()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	out := make([]map[string]any, 0, len(counts))
	for k, n := range counts {
		out = append(out, map[string]any{"tool": k, "calls": n})
	}
	writeJSON(w, 200, map[string]any{"stats": out})
}

// skillUsageStats GET /api/skills/stats
func (s *Server) skillUsageStats(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	stats, err := pg.SkillStats()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"stats": stats})
}

// 编译期确认 db 包被使用（SkillStat 类型出现在返回值的 JSON 编码里）。
var _ = db.SkillStat{}
