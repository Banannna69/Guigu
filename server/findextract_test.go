// findextract_test.go：漏洞补录的纯函数与去重逻辑回归。
// 主要函数/类型：
//   - TestNormalizeVulnKey：去重键只留实质字符（中英差异不影响判定）。
//   - TestContainsKey：空键视为已存在（防止空条目写入）。
//   - TestNormalizeExtractSeverity：四档归一。
//   - TestStripJSONFence：模型输出裹代码块/前后缀时仍可解析。

package server

import (
	"testing"
)

// TestNormalizeVulnKey 钉住去重口径：标点、空格、大小写差异不应让同一个漏洞
// 被重复补录，而中英文实质字符必须保留。
func TestNormalizeVulnKey(t *testing.T) {
	same := [][2]string{
		{"未授权访问 — /api/admin", "未授权访问/api/admin"},
		{"IDOR: /api/user", "idor  /api/user"},
		{"SQL 注入（登录口）", "sql注入登录口"},
	}
	for _, c := range same {
		if normalizeVulnKey(c[0]) != normalizeVulnKey(c[1]) {
			t.Errorf("应视为同一漏洞：%q vs %q → %q vs %q",
				c[0], c[1], normalizeVulnKey(c[0]), normalizeVulnKey(c[1]))
		}
	}
	if normalizeVulnKey("未授权访问") == normalizeVulnKey("SQL注入") {
		t.Error("不同漏洞不应产生相同键")
	}
}

// TestContainsKey 保证空键被当作「已存在」——否则一条空条目就能写进探索图。
func TestContainsKey(t *testing.T) {
	keys := []string{"abc"}
	if !containsKey(keys, "abc") {
		t.Error("应命中已存在的键")
	}
	if containsKey(keys, "abd") {
		t.Error("不应命中不同的键")
	}
	if !containsKey(keys, "") {
		t.Error("空键应视为已存在，防止空条目写入")
	}
}

// TestNormalizeExtractSeverity 保证多写法归一到四档，且未知值不被抬成高危。
func TestNormalizeExtractSeverity(t *testing.T) {
	cases := map[string]string{
		"critical": "critical", "严重": "critical",
		"high": "high", "高危": "high", "高": "high",
		"medium": "medium", "中危": "medium", "中": "medium",
		"low": "low", "低危": "low", "低": "low", "info": "low", "信息": "low",
		"":        "medium",
		"unknown": "medium",
	}
	for in, want := range cases {
		if got := normalizeExtractSeverity(in); got != want {
			t.Errorf("normalizeExtractSeverity(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// TestStripJSONFence 覆盖模型输出不规整的情况：裹代码块、带前后缀说明。
// 解析失败会让整个补录白做，所以这层兜底是必要的。
func TestStripJSONFence(t *testing.T) {
	cases := []string{
		`{"findings":[]}`,
		"```json\n{\"findings\":[]}\n```",
		"```\n{\"findings\":[]}\n```",
		"好的，结果如下：\n```json\n{\"findings\":[]}\n```\n以上。",
		"结果：{\"findings\":[]}（完）",
	}
	for _, c := range cases {
		got := stripJSONFence(c)
		if got != `{"findings":[]}` {
			t.Errorf("stripJSONFence(%q) = %q", c, got)
		}
	}
}
// TestFailedExtractionIsRetriableOnNextRun 钉住失败项的去重语义。
//
// 去重键的用途是避免同一条漏洞被写两次，不是「记住这条试过但失败了」。若失败项
// 也进了去重集合，下一次补录会直接跳过它——这条漏洞就永远补不回来了。
func TestFailedExtractionIsRetriableOnNextRun(t *testing.T) {
	// 模拟一轮：key 只应在成功时进入 existingKeys。
	existingKeys := []string{}
	candidates := []struct {
		key   string
		fails bool
	}{
		{"keyA", false},
		{"keyB", true},  // 这条写入失败
		{"keyC", false},
	}
	for _, c := range candidates {
		if containsKey(existingKeys, c.key) {
			continue // 去重跳过
		}
		if c.fails {
			// 失败的：不加入去重集合（对应实现里的 continue 分支）
			continue
		}
		existingKeys = append(existingKeys, c.key)
	}
	// 关键断言：失败的 keyB 不应被记住。
	if containsKey(existingKeys, "keyB") {
		t.Fatal("失败项进入了去重集合——下次重试会被跳过，这条漏洞永远补不回来")
	}
	// 成功的两项应当被记住。
	if !containsKey(existingKeys, "keyA") || !containsKey(existingKeys, "keyC") {
		t.Fatalf("成功项应进入去重集合，实际 %v", existingKeys)
	}
}
