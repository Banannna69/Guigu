// finding_enrichment.go：漏洞富化的数据层：AI 生成/人工编辑的结构化漏洞数据，覆盖在原始节点之上。
// 主要函数/类型：
//   - FindingEnrichment：富化记录（CVSS / 描述 / 复现 / 风险 / 修复）。
//   - GetFindingEnrichment / GetFindingEnrichments / UpsertFindingEnrichment：按节点 id 读写。
//   - scanFE / rawOr / strOr：扫描与空值兜底。

package db

// finding_enrichment: AI-generated / human-edited structured vuln data that
// overlays a raw finding node (keyed by exploration node id). The node payload
// is never mutated — enrichment is a report/management layer. JSONB columns are
// carried as json.RawMessage so this package stays agnostic of the report types.

import (
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

type FindingEnrichment struct {
	NodeID       int64           `json:"node_id"`
	Name         string          `json:"name"`
	Target       string          `json:"target"`
	Module       string          `json:"module"`
	Severity     string          `json:"severity"` // human override; empty = use node's
	Status       string          `json:"status"`   // open|confirmed|fixed|false_positive|ignored
	CVSSScore    float64         `json:"cvss_score"`
	CVSSVector   string          `json:"cvss_vector"`
	CVSSMetrics  json.RawMessage `json:"cvss_metrics"`
	Description  json.RawMessage `json:"description"`
	TestProcess  json.RawMessage `json:"test_process"`
	RiskAnalysis json.RawMessage `json:"risk_analysis"`
	Remediation  json.RawMessage `json:"remediation"`
	Source       string          `json:"source"` // ai | manual
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

func rawOr(r json.RawMessage, def string) string {
	if len(r) == 0 {
		return def
	}
	return string(r)
}
func strOr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

const feSelect = `SELECT node_id, name, target, module, severity, status, cvss_score, cvss_vector,
	cvss_metrics::text, description::text, test_process::text, risk_analysis::text, remediation::text,
	source, created_at, updated_at FROM finding_enrichment`

func scanFE(sc interface{ Scan(...any) error }) (*FindingEnrichment, error) {
	e := &FindingEnrichment{}
	var cvss, desc, tp, risk, rem string
	if err := sc.Scan(&e.NodeID, &e.Name, &e.Target, &e.Module, &e.Severity, &e.Status,
		&e.CVSSScore, &e.CVSSVector, &cvss, &desc, &tp, &risk, &rem, &e.Source, &e.CreatedAt, &e.UpdatedAt); err != nil {
		return nil, err
	}
	e.CVSSMetrics, e.Description = json.RawMessage(cvss), json.RawMessage(desc)
	e.TestProcess, e.RiskAnalysis, e.Remediation = json.RawMessage(tp), json.RawMessage(risk), json.RawMessage(rem)
	return e, nil
}

// GetFindingEnrichment returns the enrichment for a finding node, or (nil, nil)
// when the finding has not been enriched yet.
func (d *DB) GetFindingEnrichment(nodeID int64) (*FindingEnrichment, error) {
	e, err := scanFE(d.QueryRow(feSelect+" WHERE node_id=$1", nodeID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return e, err
}

// GetFindingEnrichments loads enrichments for a set of node ids in ONE query.
// Missing ids are simply absent from the map.
//
// 这原本是「循环调 GetFindingEnrichment」，注释还写着「the per-task finding set is
// small」——但它的调用方是报告生成，一次最多 500 条。那个循环等于 500 次数据库
// 往返，而报告页每点一次生成就要付一次。改成 = ANY($1) 一次取回后在内存分组。
func (d *DB) GetFindingEnrichments(nodeIDs []int64) (map[int64]*FindingEnrichment, error) {
	out := make(map[int64]*FindingEnrichment, len(nodeIDs))
	if len(nodeIDs) == 0 {
		return out, nil
	}
	// pq 风格的数组字面量：nodeIDs 全是 int64，直接拼安全。
	ids := make([]string, len(nodeIDs))
	for i, id := range nodeIDs {
		ids[i] = strconv.FormatInt(id, 10)
	}
	arr := "{" + strings.Join(ids, ",") + "}"
	rows, err := d.Query(feSelect+" WHERE node_id = ANY($1::bigint[])", arr)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		e, err := scanFE(rows)
		if err != nil {
			return out, err
		}
		out[e.NodeID] = e
	}
	return out, rows.Err()
}

// FindingSourceTranscripts loads source transcripts for a set of node ids in ONE
// query per underlying relation instead of the 3-queries-per-node loop that
// FindingSourceTranscript does. Missing/short transcripts are absent from the map.
//
// maxChars 对每条转录单独截断，与单条版本的语义一致。
func (d *DB) FindingSourceTranscripts(nodeIDs []int64, maxChars int) (map[int64]string, error) {
	out := make(map[int64]string, len(nodeIDs))
	if len(nodeIDs) == 0 {
		return out, nil
	}
	ids := make([]string, len(nodeIDs))
	for i, id := range nodeIDs {
		ids[i] = strconv.FormatInt(id, 10)
	}
	arr := "{" + strings.Join(ids, ",") + "}"

	// 一次取回所有相关 activity 行（按 node 分组后各自截断）。
	rows, err := d.Query(`
SELECT en.id, COALESCE(NULLIF(en.payload->>'summary',''), en.payload->>'text','')
FROM exploration_nodes en
WHERE en.id = ANY($1::bigint[])`, arr)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var text string
		if err := rows.Scan(&id, &text); err != nil {
			return out, err
		}
		if r := []rune(text); len(r) > maxChars {
			text = string(r[:maxChars])
		}
		if strings.TrimSpace(text) != "" {
			out[id] = text
		}
	}
	return out, rows.Err()
}

// UpsertFindingEnrichment inserts or replaces the enrichment for a finding node.
func (d *DB) UpsertFindingEnrichment(e *FindingEnrichment) error {
	_, err := d.Exec(`INSERT INTO finding_enrichment
		(node_id, name, target, module, severity, status, cvss_score, cvss_vector,
		 cvss_metrics, description, test_process, risk_analysis, remediation, source, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb,$10::jsonb,$11::jsonb,$12::jsonb,$13::jsonb,$14, now())
		ON CONFLICT (node_id) DO UPDATE SET name=EXCLUDED.name, target=EXCLUDED.target, module=EXCLUDED.module,
			severity=EXCLUDED.severity, status=EXCLUDED.status, cvss_score=EXCLUDED.cvss_score,
			cvss_vector=EXCLUDED.cvss_vector, cvss_metrics=EXCLUDED.cvss_metrics, description=EXCLUDED.description,
			test_process=EXCLUDED.test_process, risk_analysis=EXCLUDED.risk_analysis, remediation=EXCLUDED.remediation,
			source=EXCLUDED.source, updated_at=now()`,
		e.NodeID, e.Name, e.Target, e.Module, e.Severity, strOr(e.Status, "open"), e.CVSSScore, e.CVSSVector,
		rawOr(e.CVSSMetrics, "{}"), rawOr(e.Description, "[]"), rawOr(e.TestProcess, "[]"),
		rawOr(e.RiskAnalysis, "[]"), rawOr(e.Remediation, "[]"), strOr(e.Source, "ai"))
	return err
}

// UpdateFindingEnrichmentFields patches the human-editable fields + status of an
// existing enrichment (used by the edit endpoint). Blank fields PRESERVE the
// existing value (partial update) so an edit that only changes status doesn't wipe
// the AI-derived name/target. Creates the row if absent.
func (d *DB) UpdateFindingEnrichmentFields(nodeID int64, name, target, severity, status string) error {
	// The ON CONFLICT clause references the raw parameters ($2..$5) — NOT EXCLUDED —
	// so a blank field genuinely preserves the existing value on update, while a
	// fresh insert defaults an empty status to "open". (Binding strOr(status,"open")
	// as EXCLUDED made the status COALESCE dead code, silently resetting a triaged
	// status back to "open" on a status-omitted partial patch.)
	_, err := d.Exec(`INSERT INTO finding_enrichment (node_id, name, target, severity, status, source, updated_at)
		VALUES ($1,$2,$3,$4,COALESCE(NULLIF($5,''),'open'),'manual', now())
		ON CONFLICT (node_id) DO UPDATE SET
			name     = COALESCE(NULLIF($2, ''), finding_enrichment.name),
			target   = COALESCE(NULLIF($3, ''), finding_enrichment.target),
			severity = COALESCE(NULLIF($4, ''), finding_enrichment.severity),
			status   = COALESCE(NULLIF($5, ''), finding_enrichment.status),
			source='manual', updated_at=now()`,
		nodeID, name, target, severity, status)
	return err
}
