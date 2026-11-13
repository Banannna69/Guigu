package intercept

import (
	"sync"
	"testing"

	"artex/db"
)

// 这组用例钉住的是「换一种等价写法就绕过全部内置规则」这件事本身：
// 规则匹配的对象曾经是 tool_input 的**原始 JSON 文本**，而执行是按语义发生的。
// 下面每一条都是实测曾经零命中的写法（见 subject.go 的说明）。
//
// 需要数据库：内置规则是从库里读的（db.Open 会播种 v1..v4）。
// 未配置数据库时跳过，与 db/server 包的用例策略一致。
var (
	testDBOnce sync.Once
	testDB     *db.DB
	testDBErr  error
)

// sharedTestDB 整个包只开一次（db.Open 会重放整份 schema，每个用例各开一次太浪费）。
func sharedTestDB(t *testing.T) *db.DB {
	t.Helper()
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip("未配置数据库，跳过（ARTEX_PG_DSN）")
	}
	testDBOnce.Do(func() { testDB, testDBErr = db.Open(dsn) })
	if testDBErr != nil {
		t.Skipf("数据库不可用，跳过: %v", testDBErr)
	}
	return testDB
}

func newSeededInterceptor(t *testing.T) *Interceptor {
	t.Helper()
	return New(sharedTestDB(t))
}

func TestBuiltinRulesCatchEquivalentSpellings(t *testing.T) {
	ic := newSeededInterceptor(t)
	cases := []struct {
		name  string
		tool  string
		input string
	}{
		// —— 曾经全部零命中的写法 ——
		{"双引号包 URL 的 curl DELETE", "Bash", `{"command":"curl \"http://t/api/user/1\" -X DELETE"}`},
		{"带 -H 双引号的 DELETE", "Bash", `{"command":"curl -H \"Authorization: Bearer x\" -X DELETE http://t/api/user/1"}`},
		{"双引号包删除路径", "Bash", `{"command":"curl \"http://t/api/user/delete\""}`},
		{"--request=DELETE", "Bash", `{"command":"curl --request=DELETE http://t/api/user/1"}`},
		{"-X 'DELETE' 加引号", "Bash", `{"command":"curl -X 'DELETE' http://t/api/user/1"}`},
		{"JSON 形态的 method", "Bash", `{"method":"DELETE","url":"http://t/api/orders/8821"}`},
		{"python http.client request", "Bash", `{"command":"python3 -c 'import http.client;c=http.client.HTTPConnection(\"t\");c.request(\"DELETE\",\"/api/user/1\")'"}`},
		{"requests.request DELETE", "Bash", `{"command":"python3 -c 'import requests;requests.request(\"DELETE\",\"http://t/api/user/1\")'"}`},
		{"/delete.do 后缀", "Bash", `{"command":"curl 'http://t/api/user/delete.do?id=1'"}`},
		{"/delete;jsessionid", "Bash", `{"command":"curl 'http://t/api/user/delete;jsessionid=9F3A'"}`},
		{"/delete.json 后缀", "Bash", `{"command":"curl 'http://t/api/user/delete.json'"}`},
		{"百分号编码 d%65lete", "Bash", `{"command":"curl 'http://t/api/user/d%65lete?id=1'"}`},
		{"shell_send hex 原始字节", "shell_send", `{"session_id":"s1","hex":["0x72","0x6d","0x20","0x2d","0x72","0x66","0x20","0x2f","0x76","0x61","0x72"]}`},
		{"\\xNN 转义", "Bash", `{"command":"bash -c $'\\x72\\x6d\\x20\\x2d\\x72\\x66\\x20\\x2f\\x76\\x61\\x72'"}`},
		{"mysql 无条件 DELETE FROM", "Bash", `{"command":"mysql -e 'DELETE FROM orders'"}`},
		{"find -delete", "Bash", `{"command":"find /var/www -delete"}`},
		{"覆写 /etc 配置", "Bash", `{"command":"echo '' > /etc/nginx/nginx.conf"}`},
		{"WebFetch 形态的删除路径", "WebFetch", `{"url":"https://t.example.com/api/user/delete?id=1"}`},
		// —— 对照组：本来就该命中的 ——
		{"裸 rm -rf", "Bash", `{"command":"rm -rf /var"}`},
		{"裸 curl -X DELETE", "Bash", `{"command":"curl http://t/api/user/1 -X DELETE"}`},
		{"单引号删除路径", "Bash", `{"command":"curl 'http://t/api/user/delete?id=1'"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dec, matched := ic.Match(c.tool, []byte(c.input))
			if !matched {
				t.Fatalf("没有被任何规则命中（可被等价写法绕过）: %s", c.input)
			}
			if dec.Action != "deny" {
				t.Fatalf("命中的规则动作为 %q，期望 deny（规则：%s）", dec.Action, dec.RuleName)
			}
		})
	}
}

// 分片输入：同一交互会话里，未 submit 的一段 + 下一段拼起来才是完整命令。
// 两片单独看都无害（实测各零命中），必须按拼接后的结果匹配。
func TestShellFragmentReassembledBeforeMatch(t *testing.T) {
	ic := newSeededInterceptor(t)
	if dec, matched := ic.Match("shell_send", []byte(`{"session_id":"frag-1","text":"rm -r"}`)); matched {
		t.Fatalf("第一片不该单独命中（前提条件变了）：%s", dec.RuleName)
	}
	dec, matched := ic.Match("shell_send", []byte(`{"session_id":"frag-1","text":"f /var/www","submit":true}`))
	if !matched || dec.Action != "deny" {
		t.Fatalf("拼接后的 `rm -rf /var/www` 未命中：matched=%v action=%q", matched, dec.Action)
	}
	// 提交之后缓冲要清空，不能把残留带到后面的调用里
	if _, matched := ic.Match("shell_send", []byte(`{"session_id":"frag-1","text":"ls -la","submit":true}`)); matched {
		t.Fatal("提交后缓冲未清空：残留内容与后续输入拼接后误命中")
	}
}

// 只读工具的默认豁免：输入被当作数据读的工具不进拦截（例如 grep 一个含
// "curl -X DELETE" 的文件），而执行类工具默认全部进。
func TestShouldInspectDefaults(t *testing.T) {
	d := sharedTestDB(t)
	ic := New(d)

	// 库里可能已被其它用例/包写过显式拦截范围（agent 包的 review_context 用例会
	// GetEnabledTools→SetEnabledTools 原样回写一次）。先清掉，才有「从未保存过」的
	// 默认语义可测；测完恢复。
	if prev, ok, _ := d.GetSetting("intercept_enabled_tools"); ok {
		t.Cleanup(func() { _ = d.SetSetting("intercept_enabled_tools", prev) })
	} else {
		t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM settings WHERE key='intercept_enabled_tools'`) })
	}
	if _, err := d.Exec(`DELETE FROM settings WHERE key='intercept_enabled_tools'`); err != nil {
		t.Fatal(err)
	}
	ic.Invalidate()

	for _, name := range []string{"Bash", "WebFetch", "Write", "shell_send",
		"mcp__browser__browser_evaluate", "http_call", "任意新工具"} {
		if !ic.ShouldInspect(name) {
			t.Errorf("%s 应当进拦截（默认 = 除只读外全部）", name)
		}
	}
	for _, name := range []string{"Read", "Grep", "Glob", "list_facts", "node_detail", "graph_overview",
		"report_finding", "record_fact", "update_finding_report", "add_hint"} {
		if ic.ShouldInspect(name) {
			t.Errorf("%s 的输入是数据/内容而非动作，不该进拦截", name)
		}
	}

	// 显式保存过之后，以那份名单为准（默认语义让位给用户的显式选择）。
	if err := ic.SetEnabledTools([]string{"Bash"}); err != nil {
		t.Fatal(err)
	}
	if !ic.ShouldInspect("Bash") || ic.ShouldInspect("mcp__browser__browser_evaluate") {
		t.Fatal("显式保存的名单未被尊重")
	}
	if !ic.HasExplicitScope() {
		t.Fatal("HasExplicitScope 应为 true")
	}
}

// 「内容型」工具必须豁免：agent 记录删除类漏洞时，PoC 正文里必然出现
// `curl 'http://t/api/user/delete?id=1'` 这类字符串。规则层确实会命中它
// （这正是防御该有的样子 —— 执行这条命令的 Bash 调用会被拦），但**上报漏洞**这个
// 动作本身不该被拦，否则 agent 无法记录它刚刚证明的问题。
func TestContentToolsAreNotInspected(t *testing.T) {
	ic := newSeededInterceptor(t)
	cases := []struct{ tool, input string }{
		{"report_finding", `{"vulnclass":"AUTH_BYPASS","severity":"high","summary":"未授权删除","evidence":{"by":"work#1","poc":"curl 'http://t/api/user/delete?id=1' 返回 200"}}`},
		{"record_fact", `{"summary":"触发 /api/user/delete 需要管理员权限"}`},
		{"update_finding_report", `{"report":"复现：curl -X DELETE http://t/api/orders/1 成功"}`},
	}
	for _, c := range cases {
		if ic.ShouldInspect(c.tool) {
			t.Errorf("%s 不该进拦截", c.tool)
		}
		if _, matched := ic.Match(c.tool, []byte(c.input)); !matched {
			t.Errorf("前提变了：规则本应命中这段内容（%s）", c.tool)
		}
	}
}
