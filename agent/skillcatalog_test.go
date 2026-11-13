// skillcatalog_test.go：技能目录提示段生成的回归。
// 主要函数/类型：
//   - TestBuildSkillCatalogEmpty：没有技能时不注入任何内容（保持原行为）。
//   - TestBuildSkillCatalogIncludesCostAndTrigger：目录含触发条件与加载成本。
//   - TestBuildSkillCatalogStableOrder：同名集合每次渲染结果一致（提示缓存友好）。
//   - TestSkillCostLabel：成本折算分档。

package agent

import (
	"strings"
	"testing"
)

// TestBuildSkillCatalogEmpty 保证没有可见技能时完全不注入——凭空多一段
// 「你没有技能」只会浪费上下文并干扰模型。
func TestBuildSkillCatalogEmpty(t *testing.T) {
	if got := BuildSkillCatalog(nil); got != "" {
		t.Fatalf("无技能时应返回空串，得到 %q", got)
	}
	if got := BuildSkillCatalog([]SkillCatalogEntry{}); got != "" {
		t.Fatalf("空切片时应返回空串，得到 %q", got)
	}
}

// TestBuildSkillCatalogIncludesCostAndTrigger 覆盖两处针对性设计：
//  1. 触发条件（description）必须出现在目录里，否则模型仍无从判断何时该用。
//  2. 加载成本必须出现——api-recon 正文约 12K token，事前不给数字模型无法权衡。
func TestBuildSkillCatalogIncludesCostAndTrigger(t *testing.T) {
	got := BuildSkillCatalog([]SkillCatalogEntry{
		{Name: "api-recon", Description: "目标是 SPA，需要摸全后端接口面时调用。", BodyBytes: 37_801},
		{Name: "tiny", Description: "小技能。", BodyBytes: 600},
	})
	for _, want := range []string{
		"可选", "api-recon", "目标是 SPA，需要摸全后端接口面时调用。",
		"12 千 token", // 37801/3 ≈ 12600 → 12-13 千
		"很少",         // 600/3 = 200 → <500
	} {
		if !strings.Contains(got, want) {
			t.Errorf("目录缺少 %q\n---\n%s", want, got)
		}
	}
}

// TestBuildSkillCatalogStableOrder 保证同一组技能每轮渲染一致。提示词前缀一旦
// 每轮变化，provider 侧的提示缓存全部失效——这是实打实的成本。
func TestBuildSkillCatalogStableOrder(t *testing.T) {
	a := BuildSkillCatalog([]SkillCatalogEntry{
		{Name: "zeta", Description: "z", BodyBytes: 100},
		{Name: "alpha", Description: "a", BodyBytes: 100},
	})
	b := BuildSkillCatalog([]SkillCatalogEntry{
		{Name: "alpha", Description: "a", BodyBytes: 100},
		{Name: "zeta", Description: "z", BodyBytes: 100},
	})
	if a != b {
		t.Fatalf("入参顺序不同但渲染结果应一致\n---a---\n%s\n---b---\n%s", a, b)
	}
	if strings.Index(a, "alpha") > strings.Index(a, "zeta") {
		t.Fatal("条目应按名称排序")
	}
}

// TestBuildSkillCatalogMissingDescription 保证没有描述的技能仍被列出（模型必须
// 能调用每一个），但明确标注缺触发条件，而不是让它猜。
func TestBuildSkillCatalogMissingDescription(t *testing.T) {
	got := BuildSkillCatalog([]SkillCatalogEntry{{Name: "mystery", BodyBytes: 100}})
	if !strings.Contains(got, "mystery") {
		t.Fatal("无描述的技能也应出现在目录里")
	}
	if !strings.Contains(got, "没有写明适用条件") {
		t.Fatalf("无描述时应明确标注，实际：%s", got)
	}
}

func TestSkillCostLabel(t *testing.T) {
	cases := []struct {
		bytes int
		want  string
	}{
		{0, ""},
		{-1, ""},
		{600, "很少"},
		{6_000, "2 千 token"},
		{37_801, "12 千 token"},
		{58_576, "19 千 token"},
	}
	for _, c := range cases {
		if got := skillCostLabel(c.bytes); got != c.want {
			t.Errorf("skillCostLabel(%d) = %q，期望 %q", c.bytes, got, c.want)
		}
	}
}

// TestDeferredSystemInjectsCatalog 保证目录真的进了系统提示，且空目录不改变结构。
func TestDeferredSystemInjectsCatalog(t *testing.T) {
	base, _ := deferredSystem("BASE", DeferredInfo{})
	if strings.Contains(base[0], "可用技能") {
		t.Fatal("空目录不应注入技能段")
	}

	withCat, _ := deferredSystem("BASE", DeferredInfo{SkillCatalog: "\n\n## 可用技能（可选，按需加载）\n\n- x"})
	if len(withCat) != 1 || !strings.Contains(withCat[0], "可用技能") {
		t.Fatalf("目录应注入系统提示，实际：%v", withCat)
	}
}