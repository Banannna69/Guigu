// resolvesteps.go：把报告里的复现步骤映射回**真实录制的流量包**。
// 主要函数/类型：
//   - StepRef / ResolveSteps / resolveStepInWindow：按时间窗+请求特征定位 exchange。
//   - parseStepRequest / respDiscriminators / bodyPhrase：把一步的请求/响应解析成可匹配特征。
//
// 为什么重要：报告里「复现步骤」最容易变成 AI 编造的东西。这里让每一步都绑定到一条
// 真实录制过的 exchange（请求/响应逐字可查），AI 就没有编造空间——它只能引用已发生
// 的事实。这是本平台相对「AI 写报告」类方案的核心差异。
//
// 实现要点（照搬 Antifox，做了两处适配）：
//   - 时间窗 + 步骤顺序双约束：同端点的重复请求靠「上一步之后再出现」区分。
//   - 判别器只在仍有多个候选时才施加：改写过的字段不会把匹配打成零。
//   - FTS 用 本项目的 ex_fts（contentless trigram），故用 ftsQuote 而非 fts5Quote。

package traffic

import (
	"net/url"
	"regexp"
	"strings"
)

// StepRef is one reproduction step's verbatim request/response text, to be resolved
// to the real recorded exchange that cites it.
type StepRef struct {
	Request  string
	Response string
}

// ResolveSteps maps an ORDERED list of reproduction steps to real recorded
// exchanges, constrained to the task's traffic time window [sinceUnix, untilUnix].
//
// Steps resolve in order — each step's exchange must come at-or-after the previous
// step's timestamp, which disambiguates repeated identical requests. Per step:
// narrow method+path to the window, refine by body/query/response discriminators
// (each applied only while ≥1 candidate remains), then take the EARLIEST candidate.
// Returns one exchange id per step ("" = unresolved).
func (t *Traffic) ResolveSteps(sinceUnix, untilUnix int64, steps []StepRef) []string {
	out := make([]string, len(steps))
	cursor := int64(0) // previous step's exchange ts (chronological lower bound)
	for i, st := range steps {
		method, path, query, bodyToken := parseStepRequest(st.Request)
		if method == "" || path == "" {
			continue
		}
		if id, ts, ok := t.resolveStepInWindow(sinceUnix, untilUnix, cursor, method, path, query, bodyToken, st.Response); ok {
			out[i] = id
			cursor = ts
		}
	}
	return out
}

// resolveStepInWindow finds the earliest exchange matching one step, at-or-after
// cursor, inside the task's time window.
func (t *Traffic) resolveStepInWindow(sinceUnix, untilUnix, cursor int64, method, path, query, bodyToken, respText string) (string, int64, bool) {
	where := `method=? AND url LIKE ? ESCAPE '\' AND ts BETWEEN ? AND ? AND ts >= ?`
	args := []any{strings.ToUpper(method), likeContains(path), sinceUnix, untilUnix, cursor}
	// tryNarrow applies a discriminator only if it still leaves ≥1 candidate, so a
	// paraphrased/over-specific field can't zero out a match that should succeed.
	tryNarrow := func(cond string, arg any) {
		w, a := where+" AND "+cond, append(append([]any{}, args...), arg)
		if ids, _ := t.queryIDs(w, a); len(ids) >= 1 {
			where, args = w, a
		}
	}
	if t.fts && bodyToken != "" {
		tryNarrow("rowid IN (SELECT rowid FROM ex_fts WHERE ex_fts MATCH ?)", ftsQuote(bodyToken))
	}
	if query != "" {
		tryNarrow(`url LIKE ? ESCAPE '\'`, likeContains(query))
	}
	if t.fts {
		for _, disc := range respDiscriminators(respText) {
			if ids, _ := t.queryIDs(where, args); len(ids) <= 1 {
				break
			}
			tryNarrow("rowid IN (SELECT rowid FROM ex_fts WHERE ex_fts MATCH ?)", ftsQuote(disc))
		}
	}
	var id string
	var ts int64
	if err := t.db.QueryRow(`SELECT id, ts FROM exchanges WHERE `+where+` ORDER BY ts ASC LIMIT 1`, args...).Scan(&id, &ts); err != nil {
		return "", 0, false
	}
	return id, ts, true
}

// queryIDs returns up to 10 exchange ids matching where (newest first).
//
// 中途出错时把已读到的返回、并把 error 一并交出。调用点用 len() 判断「还剩几个
// 候选」来决定要不要继续收窄条件；如果错误被吞掉、ids 比实际少，判定就会从
// 「还有多个」翻成「只剩一个」，于是收窄被跳过或匹配失败——表现是报告里的复现
// 步骤静默丢失流量关联，而且没有任何日志。所以这里把 error 显式交出去。
func (t *Traffic) queryIDs(where string, args []any) ([]string, error) {
	rows, err := t.db.Query(`SELECT id FROM exchanges WHERE `+where+` ORDER BY ts DESC LIMIT 10`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return ids, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// 复现步骤的请求文本有两种形态：原始 HTTP 报文，或 agent 执行的 curl/shell 命令。
var (
	reHTTPReqLine = regexp.MustCompile(`^([A-Z]+)\s+(\S+)\s+HTTP/\S+\s*$`)
	reURLInText   = regexp.MustCompile("https?://[^\\s\"'`;)<>]+")
	reCurlMethod  = regexp.MustCompile(`(?:-X|--request)\s+([A-Za-z]+)`)
	reBodyFlag    = regexp.MustCompile(`(?:^|\s)(?:-F|--form|-d|--data|--data-raw|--data-binary)(?:\s|=)`)
	reUploadName  = regexp.MustCompile(`filename=["']?([^"'\s;,]+)`)
	reCurlData    = regexp.MustCompile(`(?:-d|--data(?:-raw|-binary)?)[ =]\s*["']?([^"'\n]+)`)
	reLongToken   = regexp.MustCompile(`[A-Za-z0-9]{8,}`)
)

// parseStepRequest extracts method, url PATH (query stripped), the url QUERY, and a
// body discriminator token from a step's request text — whether it's a raw HTTP
// request packet or a curl/shell command the agent executed.
func parseStepRequest(text string) (method, path, query, bodyToken string) {
	// 1) Raw HTTP request packet.
	if m := reHTTPReqLine.FindStringSubmatch(firstNonEmptyLine(text)); m != nil {
		method = strings.ToUpper(m[1])
		path, query = splitQuery(m[2])
		if i := strings.Index(text, "\n\n"); i >= 0 {
			body := text[i+2:]
			// Prefer the upload filename (strongest discriminator, and avoids a
			// whole-body FTS miss when the agent paraphrased the boundary).
			if m := reUploadName.FindStringSubmatch(body); m != nil {
				bodyToken = m[1]
			} else {
				bodyToken = bodyPhrase(body)
			}
		}
		return
	}
	// 2) curl / shell command citing a target URL.
	u := reURLInText.FindString(text)
	if u == "" {
		return "", "", "", ""
	}
	if parsed, err := url.Parse(u); err == nil && parsed.Path != "" {
		path, query = parsed.Path, parsed.RawQuery
	} else {
		path, query = splitQuery(u)
	}
	if m := reCurlMethod.FindStringSubmatch(text); m != nil {
		method = strings.ToUpper(m[1])
	} else if reBodyFlag.MatchString(text) {
		method = "POST"
	} else {
		method = "GET"
	}
	if method != "GET" && method != "HEAD" {
		if m := reUploadName.FindStringSubmatch(text); m != nil {
			bodyToken = m[1]
		} else if m := reCurlData.FindStringSubmatch(text); m != nil {
			bodyToken = bodyPhrase(m[1])
		}
	}
	return
}

func firstNonEmptyLine(s string) string {
	for _, ln := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(ln); t != "" {
			return t
		}
	}
	return ""
}

func splitQuery(target string) (path, query string) {
	if i := strings.IndexByte(target, '?'); i >= 0 {
		return target[:i], target[i+1:]
	}
	return target, ""
}

// likeContains escapes LIKE metacharacters and wraps in %…%.
func likeContains(s string) string {
	return "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s) + "%"
}

// bodyPhrase returns a verbatim leading slice of the body as the trigram
// disambiguator (empty when too short for trigram).
func bodyPhrase(body string) string {
	b := strings.TrimSpace(body)
	r := []rune(b)
	if len(r) < 3 {
		return ""
	}
	if len(r) > 400 {
		r = r[:400]
	}
	return string(r)
}

// respDiscriminators returns candidate strings from a step's response, in priority
// order: distinctive long tokens first (they survive "…" paraphrasing), then the
// verbatim leading slice.
func respDiscriminators(resp string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range reLongToken.FindAllString(resp, -1) {
		if len(out) >= 5 {
			break
		}
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	if p := bodyPhrase(resp); p != "" {
		out = append(out, p)
	}
	return out
}