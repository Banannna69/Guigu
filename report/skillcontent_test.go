package report

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSkillSeedAndLoad 覆盖骨架的四个关键行为：
//
//  1. seed → load 往返：内置默认能写成文件再读回来，两处一致。
//  2. 用户改过的文件覆盖内置默认（这是「骨架可编辑」的全部意义）。
//  3. 文件损坏时回退内置默认而不是失败——用户手写坏一个 JSON 不该让报告生成不了。
//  4. 重复 seed 不覆盖用户编辑（否则每次重启都会把用户改过的骨架冲掉）。
//
// 第 4 条是最容易写错的地方：seed 逻辑如果无条件写盘，用户每次重启都要重改一遍，
// 而且改完发现被覆盖会以为系统在丢数据。
func TestSkillSeedAndLoad(t *testing.T) {
	dir := t.TempDir()
	if err := SeedSkillFiles(dir); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// en-US 骨架能从盘上读到，且与内置默认一致。
	sc := loadSkillContent(dir, "en-US")
	if len(sc.Checklist) != len(checklistAll) {
		t.Fatalf("checklist len = %d, want %d", len(sc.Checklist), len(checklistAll))
	}
	if sc.Methodology[0].Name != "Pre-engagement Interactions" {
		t.Fatalf("en methodology[0] = %q", sc.Methodology[0].Name)
	}

	// 用户改一处：改过的生效。
	edited := `{"intro":"CUSTOM INTRO","phases":[{"name":"Phase X","desc":"custom"}]}`
	p := filepath.Join(dir, reportSkillName, "skeleton", "zh-CN", "methodology.json")
	if err := os.WriteFile(p, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	sc = loadSkillContent(dir, "zh-CN")
	if sc.MethodologyIntro != "CUSTOM INTRO" || len(sc.Methodology) != 1 || sc.Methodology[0].Name != "Phase X" {
		t.Fatalf("edit not picked up: intro=%q phases=%d", sc.MethodologyIntro, len(sc.Methodology))
	}
	// 同语言下没改过的文件仍然来自默认。
	if len(sc.Checklist) != len(checklistAll) {
		t.Fatalf("checklist after edit = %d", len(sc.Checklist))
	}

	// 文件损坏 → 回退默认，不报错。
	if err := os.WriteFile(p, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	sc = loadSkillContent(dir, "zh-CN")
	if sc.MethodologyIntro != methodologyIntro("zh-CN") {
		t.Fatalf("corrupt file did not fall back: %q", sc.MethodologyIntro)
	}

	// 重复 seed 幂等，且**绝不能**覆盖用户已编辑的文件。
	sp := filepath.Join(dir, reportSkillName, "skeleton", "en-US", "standards.json")
	custom := []byte(`{"__user_edit__":true}`)
	if err := os.WriteFile(sp, custom, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SeedSkillFiles(dir); err != nil {
		t.Fatalf("re-seed: %v", err)
	}
	if b, _ := os.ReadFile(sp); string(b) != string(custom) {
		t.Fatalf("re-seed overwrote a user-edited file: %q", string(b))
	}

	// 空 skillDir → 全部用内置默认（这是「还没播种过」的正常状态）。
	sc = loadSkillContent("", "en-US")
	if sc.MethodologyIntro != methodologyIntro("en-US") || len(sc.Tools) != len(tools("en-US")) {
		t.Fatalf("empty skillDir did not use built-in defaults")
	}

	// 提示词模板能渲染出非空正文。
	sc = loadSkillContent(dir, "en-US")
	got := renderPrompt(sc.vulnUser, vulnPromptData{VulnClass: "SQLi", Severity: "High", Summary: "s", PoC: "p", Company: "Acme", LangDirective: "English"})
	if got == "" {
		t.Fatalf("vuln prompt template did not render")
	}
}
