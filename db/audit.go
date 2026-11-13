// audit.go：审计日志——业务语义动作的持久记录（谁、何时、对什么、结果）。
// 主要函数/类型：
//   - AuditEntry / RecordAudit：写入一条审计（best-effort，绝不影响主流程）。
//   - ListAuditLogs：分页倒序查询，支持按类别/动作/结果过滤。

package db

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// AuditEntry 是一条审计记录。
type AuditEntry struct {
	ID        int64          `json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	Actor     string         `json:"actor"`
	Category  string         `json:"category"`
	Action    string         `json:"action"`
	Result    string         `json:"result"`
	IP        string         `json:"ip"`
	Message   string         `json:"message"`
	Detail    map[string]any `json:"detail"`
}

// 审计类别。集中定义避免各处写字面量后拼错（拼错不会报错，只会让筛选悄悄失效）。
const (
	AuditCategoryAuth      = "auth"
	AuditCategoryTask      = "task"
	AuditCategoryIntercept = "intercept"
	AuditCategorySettings  = "settings"
	AuditCategoryAsset     = "asset"
	AuditCategoryAgent     = "agent"
)

// RecordAudit 写一条审计日志。
//
// best-effort 是刻意的：审计写入失败绝不能连累被审计的业务操作。反过来也成立——
// 业务操作失败不该导致审计丢失，所以这里是独立执行而非共用调用方事务。
// 需要与业务操作原子一致的关键动作（如删除）应在调用方事务内显式写入。
func (d *DB) RecordAudit(actor, category, action, result, ip, message string, detail map[string]any) {
	if d == nil {
		return
	}
	if result == "" {
		result = "success"
	}
	// 截断：message 常含用户输入（查询语法、任务名）或模型输出片段，长度不可控。
	// 不设上限时一条超长 message 就能把审计表撑大，而审计页只展示一行摘要。
	message = clipAuditText(message, maxAuditMessageBytes)
	var dj []byte
	if detail != nil {
		var err error
		if dj, err = json.Marshal(detail); err != nil {
			dj = []byte("{}")
		} else if len(dj) > maxAuditDetailBytes {
			// detail 常由调用方塞入用户输入（查询语法、任务描述）。超限时保留键名
			// 但把值截断，比整条丢弃更有用：至少知道记了哪些字段。
			dj = truncateAuditDetail(detail)
		}
	} else {
		dj = []byte("{}")
	}
	_, _ = d.Exec(`INSERT INTO audit_logs(actor,category,action,result,ip,message,detail)
		VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb)`,
		strings.TrimSpace(actor), category, action, result,
		strings.TrimSpace(ip), message, string(dj))
}

// ListAuditLogs 分页倒序列出审计日志。空字符串表示该维度不筛选。
func (d *DB) ListAuditLogs(category, action, result string, limit, offset int) ([]AuditEntry, int, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	var where []string
	var args []any
	add := func(cond string, v string) {
		if strings.TrimSpace(v) == "" {
			return
		}
		args = append(args, v)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	add("category=$%d", category)
	add("action=$%d", action)
	add("result=$%d", result)
	clause := ""
	if len(where) > 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}

	var total int
	if err := d.QueryRow(`SELECT count(*) FROM audit_logs`+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := d.Query(`SELECT id, created_at, actor, category, action, result, ip, message, detail::text
		FROM audit_logs`+clause+fmt.Sprintf(` ORDER BY id DESC LIMIT $%d OFFSET $%d`, len(args)+1, len(args)+2),
		append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := make([]AuditEntry, 0, limit)
	for rows.Next() {
		var e AuditEntry
		var detail string
		if err := rows.Scan(&e.ID, &e.CreatedAt, &e.Actor, &e.Category, &e.Action, &e.Result, &e.IP, &e.Message, &detail); err != nil {
			return nil, 0, err
		}
		if detail != "" {
			_ = json.Unmarshal([]byte(detail), &e.Detail)
		}
		out = append(out, e)
	}
	return out, total, rows.Err()
}

// DeleteAuditLogsBefore 按留存策略清理旧审计。
//
// 单独提供而不是复用其他清理：审计有独立的合规保留期，通常远长于活动/流量。
func (d *DB) DeleteAuditLogsBefore(cutoff time.Time, limit int) (int64, error) {
	if limit <= 0 {
		return 0, nil
	}
	res, err := d.Exec(`DELETE FROM audit_logs WHERE id IN (
		SELECT id FROM audit_logs WHERE created_at < $1 ORDER BY id LIMIT $2)`, cutoff, limit)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
// ExplorationSynthesis 汇总某次探索里 agent 产生的可见文本，供「漏洞补录」分析。
//
// 只取 kind in ('text','result')——那是模型真正说出口的结论，不含 thinking 与工具
// 流量。主 Agent 的综合通常含漏洞盘点，排最前；其余按时间顺序。
func (d *DB) ExplorationSynthesis(expID int64, maxChars int) string {
	if expID <= 0 {
		return ""
	}
	rows, err := d.Query(`
		SELECT coalesce(NULLIF(detail,''), summary, '') AS content
		FROM activity
		WHERE exploration_id=$1 AND kind IN ('text','result')
		  AND coalesce(NULLIF(detail,''), summary, '') <> ''
		ORDER BY (worker='mainagent') DESC, id ASC`, expID)
	if err != nil {
		return ""
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var s string
		if rows.Scan(&s) != nil {
			continue
		}
		if maxChars > 0 && b.Len()+len(s) > maxChars {
			if b.Len() == 0 {
				// 单条就超限：截断后仍给一点内容，别整体丢失。
				r := []rune(s)
				if len(r) > maxChars {
					r = r[:maxChars]
				}
				b.WriteString(string(r))
			}
			break
		}
		b.WriteString(s)
		b.WriteString("\n\n")
	}
	return b.String()
}

// 审计字段的长度上限。审计是「够追溯」而不是「全文存档」：过长的正文应当去看
// 对应的业务记录（任务、漏洞、流量），审计只留定位线索。
const (
	maxAuditMessageBytes = 2000
	maxAuditDetailBytes  = 8000
)

// clipAuditText 按字节截断并保证不切坏 UTF-8。
func clipAuditText(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	// 从 max 往回找到合法边界，避免截出半个多字节字符。
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// truncateAuditDetail 把每个字符串值截短后重新序列化，尽量让整体落到上限内。
func truncateAuditDetail(detail map[string]any) []byte {
	out := make(map[string]any, len(detail))
	for k, v := range detail {
		if s, ok := v.(string); ok {
			out[k] = clipAuditText(s, 500)
			continue
		}
		out[k] = v
	}
	b, err := json.Marshal(out)
	if err != nil || len(b) > maxAuditDetailBytes {
		// 仍然超限（例如嵌套结构很深）：退化成只记键名。
		keys := make([]string, 0, len(detail))
		for k := range detail {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fallback, _ := json.Marshal(map[string]any{"_truncated_keys": keys})
		return fallback
	}
	return b
}

// MaxAuditMessageBytesForTest 暴露 message 上限，供测试断言使用。
func MaxAuditMessageBytesForTest() int { return maxAuditMessageBytes }
