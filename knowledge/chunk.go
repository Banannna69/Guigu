// chunk.go：文档切分——按段落打包成定长分块，并给分块加上文档上下文。
// 主要函数/类型：
//   - ChunkText：把正文切成约 maxChars 的分块（段落优先，超长段落才硬切）。
//   - splitParagraphs：按空行切段落。
//   - embedInput：给分块前置「分类 + 标题」，让向量带上「这段出自哪份文档」。
//
// 切分策略的分寸：按固定字符数硬切会把一句话劈成两半，检索时两半都不完整；
// 所以先按段落打包，只有单个段落本身超长时才在段落内部硬切（并带上少量重叠，
// 避免切断处的句子彻底丢失）。分块的语义完整性直接决定检索质量。

package knowledge

import "strings"

// ChunkText 把正文切成约 maxChars 字符的分块。
//
// 段落整包优先：能装下就装进当前分块，装不下就另起一块；只有单段本身超过
// maxChars 时才硬切，硬切处带 overlap 字符的重叠。
func ChunkText(text string, maxChars, overlap int) []string {
	if maxChars <= 0 {
		maxChars = 700
	}
	if overlap < 0 || overlap >= maxChars {
		overlap = 80
	}
	var chunks []string
	var cur strings.Builder
	curLen := 0
	push := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			chunks = append(chunks, s)
		}
		cur.Reset()
		curLen = 0
	}
	add := func(p string) {
		pr := []rune(p)
		for len(pr) > maxChars { // 单段超长：硬切
			if curLen > 0 {
				push()
			}
			chunks = append(chunks, strings.TrimSpace(string(pr[:maxChars])))
			step := maxChars - overlap
			if step < 1 {
				step = maxChars
			}
			pr = pr[step:]
		}
		if len(pr) == 0 {
			return
		}
		if curLen > 0 && curLen+len(pr)+1 > maxChars {
			push()
		}
		if curLen > 0 {
			cur.WriteByte('\n')
			curLen++
		}
		cur.WriteString(string(pr))
		curLen += len(pr)
	}
	for _, p := range splitParagraphs(text) {
		add(p)
	}
	push()
	return chunks
}

// splitParagraphs 按空行切段落；没有空行时就整篇算一段。
func splitParagraphs(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	var out []string
	for _, p := range strings.Split(text, "\n\n") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		if t := strings.TrimSpace(text); t != "" {
			out = []string{t}
		}
	}
	return out
}

// embedInput 给分块前置分类与标题再送去 embedding。
//
// 不这么做的话，一段「返回 200 但未校验签名」的正文向量里没有任何「这是哪份文档
// 的哪一部分」的信息，检索「支付回调安全要求」时它就很难被召回。前缀成本极低
// （几个 token），但显著提升相关度。
func embedInput(category, title, chunk string) string {
	var b strings.Builder
	if category != "" {
		b.WriteString("[分类：" + category + "] ")
	}
	if title != "" {
		b.WriteString("[标题：" + title + "]\n")
	}
	b.WriteString(chunk)
	return b.String()
}
