// scenarios_api.go：场景包的 HTTP 面与内置场景包播种。
// 主要函数/类型：
//   - scenarioList / scenarioCreate / scenarioUpdate / scenarioDelete：CRUD。
//   - taskScenarioInfo：任务绑定了哪些场景（只读展示）。
//
// 内置场景包的播种在 server/assembly.go 里完成（正文单源在 agent 包）。
//
// 内置包用 ON CONFLICT (nkey) DO NOTHING 播种：用户改过或删过的不会被重启复活，
// 与拦截规则/技能可见性同一套策略。

package server

import (
	"net/http"
	"strconv"

	"artex/db"
)

// scenarioList GET /api/scenarios
func (s *Server) scenarioList(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	packs, err := pg.ListScenarioPacks()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"packs": packs})
}

// scenarioCreate POST /api/scenarios
func (s *Server) scenarioCreate(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var in db.ScenarioPackInput
	if !decodeJSON(w, r, &in, maxMediumJSONBodyBytes) {
		return
	}
	id, err := pg.CreateScenarioPack(in)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	pg.RecordAudit(AdminUsername, db.AuditCategorySettings, "scenario_create", "success", clientIPFrom(r),
		"新建场景包："+in.Name, map[string]any{"scenario_id": id})
	writeJSON(w, 200, map[string]any{"id": id})
}

// scenarioUpdate PUT /api/scenarios/{id}
func (s *Server) scenarioUpdate(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "场景包 id 无效")
		return
	}
	var in db.ScenarioPackInput
	if !decodeJSON(w, r, &in, maxMediumJSONBodyBytes) {
		return
	}
	if err := pg.UpdateScenarioPack(id, in); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	pg.RecordAudit(AdminUsername, db.AuditCategorySettings, "scenario_update", "success", clientIPFrom(r),
		"修改场景包："+in.Name, map[string]any{"scenario_id": id})
	writeJSON(w, 200, map[string]any{"ok": true})
}

// scenarioDelete DELETE /api/scenarios/{id}
//
// 已建任务的快照不受影响——快照是独立副本，删包不会改变那些任务的行为。
func (s *Server) scenarioDelete(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "场景包 id 无效")
		return
	}
	if err := pg.DeleteScenarioPack(id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	pg.RecordAudit(AdminUsername, db.AuditCategorySettings, "scenario_delete", "success", clientIPFrom(r),
		"删除场景包 #"+strconv.FormatInt(id, 10), map[string]any{"scenario_id": id})
	writeJSON(w, 200, map[string]any{"deleted": true})
}

// taskScenarioInfo GET /api/tasks/{id}/scenarios — 任务绑定了哪些场景（只读展示）。
func (s *Server) taskScenarioInfo(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	t := s.m.ResolveTask(r.PathValue("id"))
	if t == nil {
		writeErr(w, 404, "任务不存在")
		return
	}
	tid, _ := strconv.ParseInt(t.ID, 10, 64)
	writeJSON(w, 200, map[string]any{"scenarios": pg.TaskScenarioSummaries(tid)})
}