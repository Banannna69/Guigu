// findextract.go：漏洞补录——从任务转录里捞回「说了但没记」的确认漏洞。
// 主要函数/类型：
//   - extractFindings：POST /api/tasks/{id}/extract-findings 的处理入口。
//   - buildExtractTranscript：转录 + 已记录事实 → 分析输入。
//   - normalizeVulnKey：去重用的规范化键。
//
// 为什么需要：主 Agent 的提示词已经写明「确认即记录，不要只在回复里罗列漏洞——
// 写在回复里但没记录=白做」，但模型仍会犯这个错。探索图、发现页、报告读的都是
// finding 节点，漏记就等于这次测试白打。
//
// 与直接照搬上游做法的差异：**同时喂已记录的 fact 节点**。只喂对话转录会漏掉
// 「worker 用 record_fact 写回了暴露面、但没升级成 finding」这一类——而信息泄露、
// 未授权可达的管理接口恰恰最常以 fact 形式留在图里。这是本实现对上游的实质改进。

package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"artex/db"
)

const (
	// extractTranscriptMax 限制喂给模型的转录体量。太小会漏结论，太大挤占上下文且
	// 推理变慢；48K 字符约等于一次完整任务的主 Agent 综合加若干 worker 结论。
	extractTranscriptMax = 48_000
	// extractFactMax 是附带的事实条目上限。
	extractFactMax = 200
	// extractFactDetailMax 限制单条事实的明细长度。
	extractFactDetailMax = 200
)

const sysExtractFindings = `你是渗透测试平台的漏洞补录助手。你的唯一任务：从给定的「agent 会话记录」与「探索图已记录的事实」中，找出**已经被证实、但从未正式登记为漏洞（finding）**的问题。

判定要求（严格）：
- 只提取有**实际验证证据**的问题：会话里出现过真实请求/命令与实际响应，或事实节点里写明了已验证的暴露。仅凭版本号、CVE 匹配、「看起来可能」的一律不算。
- 已经在「已登记漏洞」清单里的不要重复提取（可能措辞略有不同，按同一问题判断）。
- 信息泄露、未授权可达的管理/调试接口、公开的配置或备份、敏感数据端点，都属于应当补录的问题。
- 宁缺毋滥：拿不准的不要报，漏报比误报代价小。

严格只输出 JSON，不要 markdown 代码块：
{"findings":[{"vulnclass":"漏洞类型","severity":"critical|high|medium|low","summary":"一句话说明受影响目标与问题","evidence":"支撑该结论的关键请求/响应/观察原文摘要"}]}`

const userExtractFindings = `【已登记漏洞（不要重复）】
%s

【探索图已记录的事实（可能含未升级为漏洞的暴露/攻击面）】
%s

【agent 会话记录】
%s

请输出 JSON。`

// extractedFinding 是模型返回的一条候选。
type extractedFinding struct {
	VulnClass string `json:"vulnclass"`
	Severity  string `json:"severity"`
	Summary   string `json:"summary"`
	Evidence  string `json:"evidence"`
}

// extractFindings 从任务转录中补录漏洞。
//
// 默认只做「预览」：返回提取结果供人工确认，不落库。带 commit=1 才真正写入——
// 补录是 AI 判断的结果，直接写进探索图会让误报混进报告，人工过一遍更稳妥。
func (s *Server) extractFindings(w http.ResponseWriter, r *http.Request) {
	t := s.m.ResolveTask(r.PathValue("id"))
	if t == nil {
		writeErr(w, 404, "任务不存在")
		return
	}
	commit := r.URL.Query().Get("commit") == "1"

	transcript := s.buildExtractTranscript(t)
	if strings.TrimSpace(transcript) == "" {
		writeErr(w, 400, "该任务暂无可分析的会话记录")
		return
	}

	labels, existingKeys := existingFindingLabels(t)
	found, err := s.aiExtractFindings(r, t, transcript, labels)
	if err != nil {
		writeErr(w, 502, "AI 提取失败："+err.Error())
		return
	}

	// 服务端再去重一次：模型可能返回近似重复项，也可能返回已登记过的。
	created := 0
	accepted := make([]map[string]any, 0, len(found))
	for _, e := range found {
		summary := strings.TrimSpace(e.Summary)
		class := strings.TrimSpace(e.VulnClass)
		if summary == "" && class == "" {
			continue
		}
		key := normalizeVulnKey(class + summary)
		if containsKey(existingKeys, key) {
			continue
		}
		item := map[string]any{
			"vulnclass": class,
			"severity":  normalizeExtractSeverity(e.Severity),
			"summary":   summary,
			"evidence":  strings.TrimSpace(e.Evidence),
			"recorded":  false,
		}
		if commit {
			if err := s.commitExtractedFinding(t, item); err != nil {
				// 失败必须回传原因：否则调用方看到 accepted>0 而 created=0 却不知道
				// 为什么，只能反复重试同一批。
				item["record_error"] = err.Error()
				// 失败项**不**加入去重键：下次重试还要再试一次。若这里也加进去，
				// 这条就永远补不回来了——去重键的存在是为了避免重复写入，不是为了
				// 记住失败。
				accepted = append(accepted, item)
				continue
			}
			item["recorded"] = true
			created++
		}
		existingKeys = append(existingKeys, key)
		accepted = append(accepted, item)
	}
	if commit {
		s.m.pg.RecordAudit(AdminUsername, db.AuditCategoryTask, "extract_findings", "success", clientIPFrom(r),
			fmt.Sprintf("补录 %d 条漏洞", created), map[string]any{"task_id": t.ID, "created": created})
	}
	writeJSON(w, 200, map[string]any{
		"extracted": len(found),
		"accepted":  len(accepted),
		"created":   created,
		"committed": commit,
		"findings":  accepted,
	})
}

// buildExtractTranscript 把转录与已记录的 fact 拼成分析输入。
func (s *Server) buildExtractTranscript(t *Task) string {
	expID := t.Store.ID()
	text := strings.TrimSpace(s.m.pg.ExplorationSynthesis(expID, extractTranscriptMax))

	var facts strings.Builder
	if nodes, err := t.Store.ListByKind(db.KindFact, extractFactMax); err == nil {
		for _, n := range nodes {
			var p struct {
				Summary string `json:"summary"`
				Detail  string `json:"detail"`
			}
			if json.Unmarshal(n.Payload, &p) != nil {
				continue
			}
			sm := strings.TrimSpace(p.Summary)
			if sm == "" {
				continue
			}
			facts.WriteString("- ")
			facts.WriteString(sm)
			if d := strings.TrimSpace(p.Detail); d != "" {
				if r := []rune(d); len(r) > extractFactDetailMax {
					d = string(r[:extractFactDetailMax]) + "…"
				}
				facts.WriteString("：")
				facts.WriteString(d)
			}
			facts.WriteString("\n")
		}
	}
	blob := text
	if facts.Len() > 0 {
		blob += "\n\n【事实】\n" + facts.String()
	}
	return blob
}

// existingFindingLabels 返回已登记漏洞的展示标签与去重键。
func existingFindingLabels(t *Task) (labels, keys []string) {
	nodes, err := t.Store.ListByKind(db.KindFinding, 500)
	if err != nil {
		return nil, nil
	}
	for _, n := range nodes {
		var p struct {
			VulnClass string `json:"vulnclass"`
			Summary   string `json:"summary"`
		}
		if json.Unmarshal(n.Payload, &p) != nil {
			continue
		}
		cls, sm := strings.TrimSpace(p.VulnClass), strings.TrimSpace(p.Summary)
		if cls == "" && sm == "" {
			continue
		}
		labels = append(labels, cls+" — "+sm)
		keys = append(keys, normalizeVulnKey(cls+sm))
	}
	return labels, keys
}

// aiExtractFindings 调一次模型做提取。
func (s *Server) aiExtractFindings(r *http.Request, t *Task, transcript string, labels []string) ([]extractedFinding, error) {
	prov, _, ok := s.providerForProfile(0)
	if !ok || prov == nil {
		return nil, fmt.Errorf("未配置可用的 LLM")
	}
	var lb strings.Builder
	if len(labels) == 0 {
		lb.WriteString("（无）")
	} else {
		for _, l := range labels {
			lb.WriteString("- ")
			lb.WriteString(l)
			lb.WriteString("\n")
		}
	}
	user := fmt.Sprintf(userExtractFindings, lb.String(), "（见下方转录末尾）", transcript)

	text, err := streamCollectText(r.Context(), prov, sysExtractFindings, user)
	if err != nil {
		return nil, err
	}
	var out struct {
		Findings []extractedFinding `json:"findings"`
	}
	if err := json.Unmarshal([]byte(stripJSONFence(text)), &out); err != nil {
		return nil, fmt.Errorf("AI 输出解析失败")
	}
	return out.Findings, nil
}

// commitExtractedFinding 把一条候选写成 finding 节点 + 独立 finding 行，并入图。
func (s *Server) commitExtractedFinding(t *Task, item map[string]any) error {
	class, _ := item["vulnclass"].(string)
	sev, _ := item["severity"].(string)
	summary, _ := item["summary"].(string)
	evidence, _ := item["evidence"].(string)
	if strings.TrimSpace(summary) == "" {
		return fmt.Errorf("缺少摘要")
	}
	payload := map[string]any{
		"vulnclass": class, "severity": sev, "summary": summary,
		// by 标为「补录」：报告与发现页据此能区分哪些是人工/AI 事后补的，
		// 而不是 agent 当场 report_finding 记的。
		"evidence": map[string]any{"by": "补录", "poc": evidence},
	}
	nodeID, err := t.Store.AddNode(db.KindFinding, payload, 9, "confirmed", "补录", nil)
	if err != nil {
		return err
	}
	if begin, oerr := t.Store.OriginFactID(); oerr == nil && begin > 0 {
		_ = t.Store.Link(begin, db.RelYields, nodeID) // 接入探索图血缘
	}
	taskID, _ := strconv.ParseInt(t.ID, 10, 64)
	_, err = t.Store.AddStandaloneFinding(taskID, nodeID, class, class, sev, summary, evidence, "补录", nil)
	return err
}

// normalizeVulnKey 生成去重用的键：去掉空白与标点差异，只留实质字符。
func normalizeVulnKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r >= 0x4e00 && r <= 0x9fff: // 汉字
			b.WriteRune(r)
		}
	}
	return b.String()
}

func containsKey(keys []string, k string) bool {
	if k == "" {
		return true // 空键视为已存在，避免无内容项被写入
	}
	for _, x := range keys {
		if x == k {
			return true
		}
	}
	return false
}

// normalizeExtractSeverity 把模型可能给的多种写法归一到平台的四档。
func normalizeExtractSeverity(s string) string {
	v := strings.ToLower(strings.TrimSpace(s))
	switch {
	case strings.Contains(v, "crit"), strings.Contains(v, "严重"), v == "high", strings.Contains(v, "高危"), v == "高":
		if strings.Contains(v, "crit") || strings.Contains(v, "严重") {
			return "critical"
		}
		return "high"
	case strings.Contains(v, "med"), strings.Contains(v, "中危"), v == "中":
		return "medium"
	case strings.Contains(v, "low"), strings.Contains(v, "低"), strings.Contains(v, "info"), strings.Contains(v, "信息"):
		return "low"
	default:
		return "medium" // 无法判断时给中间档，避免把未知项抬成高危
	}
}
// stripJSONFence 去掉模型偶尔裹上的 ```json 代码块与首尾说明文字。
//
// 提示词已要求"严格只输出 JSON"，但实际仍会出现围栏或前后缀说明；解析失败会让
// 整个补录白做，所以这里做一层宽松兜底而不是直接报错。
func stripJSONFence(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "```"); i >= 0 {
		s = s[i+3:]
		if j := strings.Index(s, "```"); j >= 0 {
			s = s[:j]
		}
		s = strings.TrimPrefix(strings.TrimSpace(s), "json")
		s = strings.TrimSpace(s)
	}
	// 兜底：截取第一个 { 到最后一个 } 之间。
	if i := strings.Index(s, "{"); i > 0 {
		s = s[i:]
	}
	if j := strings.LastIndex(s, "}"); j >= 0 && j+1 < len(s) {
		s = s[:j+1]
	}
	return strings.TrimSpace(s)
}
