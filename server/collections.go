// collections.go：资产合集的 HTTP 面。
// 主要函数/类型：
//   - listCollections：GET /api/collections（新→旧，带成员数）。
//   - deleteCollection：DELETE /api/collections/{id}（级联清理成员与任务引用）。
//
// 合集由 FOFA 同步产生，可在新建任务时选为测试目标（task_scope kind='collection'）。

package server

import (
	"net/http"
	"strconv"
)

// listCollections 列出全部资产合集。
func (s *Server) listCollections(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	cols, err := pg.ListCollections()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"collections": cols})
}

// deleteCollection 删除合集（成员与引用它的 task_scope 行随 CASCADE 清理）。
func (s *Server) deleteCollection(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "合集 id 无效")
		return
	}
	if err := pg.DeleteCollection(id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	pg.RecordAudit(AdminUsername, "asset", "delete_collection", "success", clientIPFrom(r),
		"删除资产合集 #"+strconv.FormatInt(id, 10), map[string]any{"collection_id": id})
	writeJSON(w, 200, map[string]any{"deleted": true})
}
