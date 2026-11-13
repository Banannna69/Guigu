package report

import (
	"encoding/json"
	"strings"
	"testing"
)

// sampleRepro 模拟 agent 在 report_finding 时**就地**记录的复现过程。
// 这份文本的可信度来自它是击穿当下逐字抄下来的，不是事后由模型重写的。
const sampleRepro = `{
  "entry_point": "POST /user/validate/otp/forgetPasswordVerify",
  "prerequisites": [
    "注册测试账号：POST /user/register {\"mobile\":\"13800000000\",\"pwd\":\"P@ssw0rd123\"} → 200 {\"code\":0}",
    "登录取会话：POST /user/login → 返回 Set-Cookie: SESSION=abc"
  ],
  "steps": [
    {"request": "POST /user/validate/otp/forgetPasswordVerify HTTP/1.1\nHost: t.example.com\nContent-Type: application/json\n\n{\"mobile\":\"13800000000\",\"otp\":\"000000\"}", "expected": "200 {\"code\":0,\"token\":\"...\"}", "proves": "OTP 未校验即返回重置 token"},
    {"request": "POST /user/resetPassword HTTP/1.1\n\n{\"token\":\"...\",\"pwd\":\"Hacked1.\"}", "expected": "200 {\"code\":0}", "proves": "任意账户密码可被重置"}
  ],
  "provenance": "由『认证绕过』意图发现，依赖忘记密码流程枚举"
}`

// TestReproToBlocks_ZhCN 验证复现块渲染：标签齐全、每步一个逐字请求代码块、
// 且请求原文一字不改。
//
// 「一字不改」是本测试的核心断言：复现步骤一旦被改写，报告里的东西就不再是
// 实际发生过的事，读报告的人照着做也复现不出来。
func TestReproToBlocks_ZhCN(t *testing.T) {
	blocks := reproToBlocks(json.RawMessage(sampleRepro), "zh-CN", nil)
	if len(blocks) == 0 {
		t.Fatal("expected blocks, got none")
	}
	joined := ""
	codeCount := 0
	for _, b := range blocks {
		joined += b.Type + ":" + b.Content + "\n"
		if b.Type == "code" {
			codeCount++
		}
	}
	for _, want := range []string{"【来源】", "【入口点】", "【前置条件】", "【复现步骤】", "注册测试账号", "OTP 未校验", "任意账户密码可被重置"} {
		if !strings.Contains(joined, want) {
			t.Errorf("rendered blocks missing %q\n---\n%s", want, joined)
		}
	}
	// 两步 → 两个逐字请求代码块
	if codeCount != 2 {
		t.Errorf("expected 2 code blocks (verbatim requests), got %d", codeCount)
	}
	// 逐字请求必须原样保留
	if !strings.Contains(joined, "POST /user/validate/otp/forgetPasswordVerify HTTP/1.1") {
		t.Errorf("verbatim request not preserved:\n%s", joined)
	}
}

// TestReproToBlocks_EnUS_Labels 验证英文报告的标签也完整（客户常要求英文交付）。
func TestReproToBlocks_EnUS_Labels(t *testing.T) {
	blocks := reproToBlocks(json.RawMessage(sampleRepro), "en-US", nil)
	joined := ""
	for _, b := range blocks {
		joined += b.Content + "\n"
	}
	for _, want := range []string{"Origin:", "Entry point:", "Prerequisites:", "Reproduction steps:", "Step 1", "Step 2"} {
		if !strings.Contains(joined, want) {
			t.Errorf("en-US labels missing %q\n---\n%s", want, joined)
		}
	}
}

// TestReproToBlocks_Empty 验证没有复现数据时产出空块，而不是渲染出空的标题骨架。
// 空的【复现步骤】小标题会让客户以为「这一步他们没测」，比不出现更糟。
func TestReproToBlocks_Empty(t *testing.T) {
	for _, raw := range []string{"", "{}", "null"} {
		if b := reproToBlocks(json.RawMessage(raw), "zh-CN", nil); len(b) != 0 {
			t.Errorf("reproToBlocks(%q) = %d blocks, want 0", raw, len(b))
		}
	}
}

// TestAssembleVuln_PrefersReproduction 是本包最重要的一条断言：
// 有就地记录的复现时，AI 生成的 test_process 与原始 PoC **都不能**抢占它。
//
// 这条优先级就是「报告可信」与「报告像编的」的分界线。一旦让 AI 叙述的
// test_process 优先，报告里的步骤就会退化成「模型觉得应该怎么做」。
func TestAssembleVuln_PrefersReproduction(t *testing.T) {
	f := FindingInput{
		VulnClass:    "OTP 校验绕过",
		Severity:     "critical",
		Summary:      "忘记密码流程 OTP 未校验",
		PoC:          "some raw poc that should NOT win",
		Reproduction: json.RawMessage(sampleRepro),
	}
	enr := Enrichment{
		Title:       "OTP 校验绕过导致任意账户接管",
		TestProcess: []procBlock{{Type: "text", Content: "AI-reconstructed step that should NOT win"}},
	}
	d := assembleVuln("zh-CN", f, enr, "3.1.1", "", nil)
	joined := ""
	for _, b := range d.TestProcess {
		joined += b.Content + "\n"
	}
	if strings.Contains(joined, "should NOT win") {
		t.Errorf("captured reproduction did not take priority; got AI/PoC content:\n%s", joined)
	}
	if !strings.Contains(joined, "【入口点】") || !strings.Contains(joined, "任意账户密码可被重置") {
		t.Errorf("captured reproduction not rendered into test_process:\n%s", joined)
	}
}
