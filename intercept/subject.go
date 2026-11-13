// subject.go：把一次工具调用展开成多个等价匹配视图（解码/扁平化/分片重组）。
// 主要函数/类型：
//   - subjectsFor / walkSubjects：原始 JSON、解码字符串、k=v 扁平化、shell hex 字节等视图。
//   - addStringVariants / decodeHexEscapes / decodeHexBytes：百分号与 hex 转义的还原。
//   - appendShellCarry：交互式 shell 分片输入的拼接（两片各看无害，拼起来才是完整命令）。

package intercept

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// 拦截规则曾经直接拿 tool_input 的**原始 JSON 文本**做匹配，而执行是按语义发生的：
// 只要调用方换一种等价写法，全部内置规则一起失效（均已实测零命中）——
//
//	{"command":"curl \"http://t/api/x\" -X DELETE"}    JSON 转义后的 \" 打断了 curl 规则的字符类
//	{"command":"curl --request=DELETE http://t/api/x"} 规则只认 -X DELETE / --request DELETE
//	{"method":"DELETE","url":"..."}                    method 规则要求 method 后紧跟 : 或 =，JSON 键尾是 "
//	{"session_id":"s1","hex":["0x72","0x6d",...]}      shell_send 的原始字节，规则只看到十六进制串
//	…/d%65lete · …/delete.do · …/delete;jsessionid     URL 编码、后缀、会话参数
//
// subjectsFor 把一次调用展开成**多个等价视图**：原始 JSON（保持既有自定义规则的行为不变）、
// 每个解码后的字符串、百分号解码与 \xNN 转义解码的变体、结构化参数的 k=v 扁平化、
// 以及 shell_send 的 hex 字节解码。规则命中任意一个视图即算命中。
//
// 方向是刻意 fail-safe 的：多视图只会让规则更容易命中（宁可多拦一次，也不少拦一次），
// 而所有视图都来自这次调用本身，不会把别的调用的内容算进来。
const (
	maxSubjectsPerCall = 256
	maxSubjectBytes    = 256 << 10
	maxFlattenDepth    = 8
)

// subjectsFor 返回该 tool_input 的全部匹配视图（顺序稳定，去重）。
func subjectsFor(input []byte) []string {
	out := make([]string, 0, 8)
	seen := make(map[string]bool, 8)
	add := func(s string) {
		if s == "" || len(out) >= maxSubjectsPerCall || seen[s] {
			return
		}
		if len(s) > maxSubjectBytes {
			s = s[:maxSubjectBytes]
		}
		seen[s] = true
		out = append(out, s)
	}
	add(string(input)) // 原始 JSON：保留旧行为（含跨字段匹配）
	var v any
	if err := json.Unmarshal(input, &v); err != nil {
		return out
	}
	walkSubjects(v, "", 0, add)
	return out
}

// walkSubjects 递归展开解码后的结构：字符串连同其变体入视图；map 额外产出 k=v 扁平行。
func walkSubjects(v any, key string, depth int, add func(string)) {
	if depth > maxFlattenDepth {
		return
	}
	switch val := v.(type) {
	case string:
		addStringVariants(val, add)
	case bool:
		add(fmt.Sprintf("%s=%t", key, val))
	case float64:
		add(fmt.Sprintf("%s=%v", key, val))
	case []any:
		// shell_send 的 hex 参数：原始字节的十六进制表示。规则只看得到 "0x72" 这种串，
		// 而 PTY 收到的是 rm -rf /var —— 这里把它解码回文本再匹配一次。
		if strings.EqualFold(key, "hex") {
			if decoded, ok := decodeHexBytes(val); ok {
				add(decoded)
			}
		}
		for _, e := range val {
			walkSubjects(e, key, depth+1, add)
		}
	case map[string]any:
		flat := make([]string, 0, len(val))
		for k, e := range val {
			child := k
			if key != "" {
				child = key + "." + k
			}
			walkSubjects(e, child, depth+1, add)
			if s, ok := e.(string); ok && s != "" {
				flat = append(flat, child+"="+s)
			}
		}
		if len(flat) > 0 {
			// 扁平行让「method=DELETE url=…」这类结构化参数能被针对键的规则命中
			// （原始 JSON 里键后紧跟引号，正则无从下嘴）。
			add(strings.Join(flat, " "))
		}
	}
}

// addStringVariants 加入字符串本身及其解码变体（百分号编码、\xNN 转义）。
func addStringVariants(s string, add func(string)) {
	add(s)
	if strings.ContainsRune(s, '%') {
		cur := s
		for i := 0; i < 2; i++ { // 双重编码也解一次
			next, err := url.PathUnescape(cur)
			if err != nil || next == cur {
				break
			}
			cur = next
			add(cur)
		}
	}
	if strings.Contains(s, `\x`) {
		if decoded, ok := decodeHexEscapes(s); ok {
			add(decoded)
		}
	}
}

// decodeHexEscapes 把 $'\x72\x6d ...' 这类 \xNN 转义还原成文本。
// 只有整串都能解析时才返回，避免把普通文本里的字面 \x 误当转义。
func decodeHexEscapes(s string) (string, bool) {
	var b strings.Builder
	changed := false
	for i := 0; i < len(s); {
		if i+4 <= len(s) && s[i] == '\\' && (s[i+1] == 'x' || s[i+1] == 'X') {
			n, err := strconv.ParseUint(s[i+2:i+4], 16, 8)
			if err != nil {
				return "", false // 只有整串都是转义时才用它当视图，半截解码会得到误导性的串
			}
			b.WriteByte(byte(n))
			i += 4
			changed = true
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	if !changed {
		return "", false
	}
	return b.String(), true
}

// decodeHexBytes 解码 shell_send 的 hex 数组（["0x72","0x6d",…] 或 ["72","6d",…]）。
func decodeHexBytes(arr []any) (string, bool) {
	if len(arr) == 0 {
		return "", false
	}
	buf := make([]byte, 0, len(arr))
	for _, e := range arr {
		s, ok := e.(string)
		if !ok {
			return "", false
		}
		s = strings.TrimSpace(s)
		s = strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
		s = strings.TrimPrefix(s, `\x`)
		if len(s) == 0 || len(s) > 2 {
			// 也可能是 0x 后面跟多字节，或直接给了十进制；解析不了就整体放弃
			n, err := strconv.ParseUint(s, 16, 16)
			if err != nil || n > 255 {
				return "", false
			}
			buf = append(buf, byte(n))
			continue
		}
		n, err := strconv.ParseUint(s, 16, 8)
		if err != nil {
			return "", false
		}
		buf = append(buf, byte(n))
	}
	if len(buf) == 0 {
		return "", false
	}
	return string(buf), true
}

// --- 交互式 shell 的分片输入 ---

// shellCarryLimit / shellCarrySessions 界定「未提交输入」缓冲的规模。
// 分片绕过是实测可用的：同一 session 内 {"text":"rm -r"} 与 {"text":"f /var/www","submit":true}
// 两片各自零命中，拼起来才是破坏性命令。这里把上一片记住，下一片到达时按「拼接后」再匹配一次。
const (
	shellCarryLimit    = 512
	shellCarrySessions = 64
)

// shellInput 是 shell_send/shell_open 这类交互输入调用的关键字段。
type shellInput struct {
	SessionID string `json:"session_id"`
	Text      string `json:"text"`
	Submit    bool   `json:"submit"`
}

// appendShellCarry 处理交互 shell 的分片输入，返回额外的匹配视图（可能为空）。
//
// 只有同一 session 且**未 submit** 的文本才会被记住；submit 之后立即清空，
// 保证缓冲不会把一个已完成命令的残留带到很久以后的调用里。
func (i *Interceptor) appendShellCarry(toolName string, input []byte) []string {
	if !isInteractiveShellTool(toolName) {
		return nil
	}
	var in shellInput
	if json.Unmarshal(input, &in) != nil || strings.TrimSpace(in.Text) == "" {
		return nil
	}
	sid := in.SessionID
	if sid == "" {
		sid = "-"
	}
	i.shellMu.Lock()
	defer i.shellMu.Unlock()
	if i.shellBuf == nil {
		i.shellBuf = make(map[string]string, 8)
	}
	var extra []string
	if prev := i.shellBuf[sid]; prev != "" {
		extra = append(extra, prev+in.Text) // 拼接后的完整命令
	}
	if in.Submit {
		delete(i.shellBuf, sid)
		return extra
	}
	merged := i.shellBuf[sid] + in.Text
	if len(merged) > shellCarryLimit {
		merged = merged[len(merged)-shellCarryLimit:]
	}
	if len(i.shellBuf) >= shellCarrySessions {
		// 会话数超上限时整体丢弃（交互 shell 会话数本就极少，宁可漏记不留无界内存）
		i.shellBuf = make(map[string]string, 8)
	}
	i.shellBuf[sid] = merged
	return extra
}

func isInteractiveShellTool(name string) bool {
	switch name {
	case "shell_send", "shell_open":
		return true
	}
	return false
}
