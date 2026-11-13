// llm_roles.go：LLM 角色分工——把 planner / worker / mainagent / 报告分别绑到不同的
// LLM 配置上，让「打靶要快要便宜」和「写报告要强要文笔好」这两类需求可以各自满足。
// 主要函数/类型：
//   - llmRoleKeys：可绑定配置的角色清单（引擎角色走 agents.llm_profile_id，报告走 settings）。
//   - llmRoles / llmSetRole：角色→配置矩阵的读取与设置。
//
// 为什么要这个：一个平台只有一个「激活配置」时，用户只能二选一——用便宜快的模型则
// 报告文笔差，用强的模型则每次打靶的 token 成本翻几倍。角色的用途差异很大：
// worker 每轮都在调工具（量大、容忍弱）、planner 每轮决策（量小、要判断力）、
// 报告只在最后生成一次（量小、要文笔）。分开绑是唯一合理的做法。
//
// 0 表示「跟随全局激活配置」，所以不配置时的行为与从前逐字节一致。

package server

import (
	"net/http"
	"strconv"

	"artex/db"
)

// llmRoleKeys 是可通过 agents.llm_profile_id 绑定配置的引擎角色。
// 报告角色不在这里——它走 settings.llm_role_report（见 db.ReportRoleProfile），
// 因为报告不在 agents 表里。
var llmRoleKeys = []string{"planner", "worker", "mainagent"}

// roleRow 是角色矩阵的一行。
type roleRow struct {
	Role      string `json:"role"`
	ProfileID string `json:"profile_id"`
}

// llmRoles GET /api/llm/roles — 读「角色分工」矩阵。
func (s *Server) llmRoles(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	out := make([]roleRow, 0, len(llmRoleKeys)+1)
	for _, k := range llmRoleKeys {
		pid := "0"
		// LLMProfileID 是 *int64：nil 表示「跟随全局激活配置」。
		if a, err := pg.GetAgentByKey(k); err == nil && a != nil && a.LLMProfileID != nil && *a.LLMProfileID > 0 {
			pid = strconv.FormatInt(*a.LLMProfileID, 10)
		}
		out = append(out, roleRow{Role: k, ProfileID: pid})
	}
	rp := "0"
	if id := settingInt(pg, settingLLMRoleReport); id > 0 {
		rp = strconv.FormatInt(int64(id), 10)
	}
	out = append(out, roleRow{Role: "report", ProfileID: rp})
	writeJSON(w, 200, map[string]any{"roles": out})
}

// llmSetRole POST /api/llm/roles {role, profile_id}
//
// profile_id=0 表示「跟随全局激活配置」。改完立即重建 agent，让新绑定马上生效——
// 要求用户重启容器才生效会让这个功能在实际使用中变成「配了但好像没用」。
func (s *Server) llmSetRole(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var body struct {
		Role      string `json:"role"`
		ProfileID string `json:"profile_id"`
	}
	if !decodeJSON(w, r, &body, maxSmallJSONBodyBytes) {
		return
	}
	pid, err := strconv.ParseInt(body.ProfileID, 10, 64)
	if err != nil || pid < 0 {
		writeErr(w, 400, "profile_id 必须是非负整数")
		return
	}
	// 绑到不存在的配置上会让该角色静默回退到全局激活——用户以为生效了其实没有，
	// 所以这里显式拒绝。
	if pid > 0 {
		if p, _ := pg.ProfileByID(pid); p == nil {
			writeErr(w, 400, "指定的 LLM 配置不存在")
			return
		}
	}

	switch body.Role {
	case "report":
		if err := pg.SetSetting(settingLLMRoleReport, strconv.FormatInt(pid, 10)); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	case "planner", "worker", "mainagent":
		// profile_id=0 → 传 nil（跟随全局激活配置）。
		var bind *int64
		if pid > 0 {
			bind = &pid
		}
		if err := pg.SetAgentLLMProfile(body.Role, bind); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		// 角色绑定会改变 agent 的 provider 链，因此要让缓存里的 agent 失效并按新的
		// 激活配置重建——与「切换默认配置」走同一条路径。
		s.invalidateProfileAgents()
		s.reapplyActiveProfile()
	default:
		writeErr(w, 400, "未知角色："+body.Role)
		return
	}
	pg.RecordAudit(AdminUsername, db.AuditCategorySettings, "llm_role_set", "success", clientIPFrom(r),
		"设置角色分工："+body.Role, map[string]any{"role": body.Role, "profile_id": pid})
	writeJSON(w, 200, map[string]any{"ok": true})
}

// settingLLMRoleReport 是「报告/富化档位」的 settings 键。
const settingLLMRoleReport = "llm_role_report"
