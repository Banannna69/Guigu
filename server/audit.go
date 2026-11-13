// audit.go：审计日志的 HTTP 面（列表查询）。
// 主要函数/类型：
//   - listAuditLogs：GET /api/audit-logs，分页 + 按类别/动作/结果过滤。

package server

import (
	"net/http"
	"strconv"
)

// listAuditLogs 返回审计日志（倒序）。过滤维度留空表示不筛选。
func (s *Server) listAuditLogs(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	entries, total, err := pg.ListAuditLogs(q.Get("category"), q.Get("action"), q.Get("result"), limit, offset)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"entries": entries, "total": total})
}
