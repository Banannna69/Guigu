// skillcatalog.go：把可见技能整理成「带触发条件与成本」的系统提示段。
// 主要函数/类型：
//   - SkillCatalogEntry：一个可见技能的展示信息（名称/触发条件/体量）。
//   - BuildSkillCatalog：按目录生成提示段；空清单返回空串（不改变原行为）。
//   - skillCostLabel：把正文字节数折算成模型可读的成本提示。
//
// 为什么要这段提示：技能原本只以 Skill 工具的描述形式出现，而那份描述是
// 「Available skills: - name: desc」的平铺清单，模型看不出「什么时候该用」，
// 也看不出「用了要花多少上下文」。实测结果是几乎从不主动调用。
//
// 这里的三处针对性设计：
//   1. 只列 description 里真正带触发条件的条目；描述为空或是套话的，标成
//      「无触发条件说明」——让模型知道不该凭它猜。
//   2. 列出加载成本。api-recon 正文约 12K tokens，模型事前无从判断值不值；
//      给出数字后它才能权衡。
//   3. 放在系统提示末尾且明确说「不是必须」。模型对必选指令会产生抵触，
//      对可选建议则更愿意在合适时机采用。

package agent

import (
	"fmt"
	"sort"
	"strings"
)

// SkillCatalogEntry 是一个可见技能在提示词里的展示信息。
type SkillCatalogEntry struct {
	Name        string // 技能名（目录名）
	Description string // 触发条件说明
	BodyBytes   int    // 正文体量，用于折算上下文成本
}

// maxCatalogDescRunes 限制单条描述的展示长度。过长的描述会把目录本身撑大，
// 反而挤占正文空间。
const maxCatalogDescRunes = 220

// BuildSkillCatalog 生成技能目录提示段。
//
// 返回空串表示「没有可见技能」或「全部条目都没有可用描述」——调用方应据此
// 完全不注入，保持与原行为一致。这一点很重要：没有技能时凭空多出一段
// 「你没有技能」的提示只会浪费上下文并干扰模型。
func BuildSkillCatalog(entries []SkillCatalogEntry) string {
	if len(entries) == 0 {
		return ""
	}
	// 按名字排序，保证同一组技能每轮渲染结果稳定——否则提示词前缀每轮变化，
	// provider 侧的提示缓存全部失效。
	sorted := make([]SkillCatalogEntry, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	var b strings.Builder
	b.WriteString("\n\n## 可用技能（可选，按需加载）\n\n")
	b.WriteString("以下技能是**可选的现成流程**，不是必须执行。当当前工作与某个技能的适用条件吻合时，")
	b.WriteString("用 `Skill` 工具加载它并按其中的步骤执行；不吻合就不要为了用而用。\n")
	b.WriteString("加载会把该技能的完整正文读进上下文（成本见每条的「约」值），所以只在确实要用时加载。\n")

	for _, e := range sorted {
		desc := strings.TrimSpace(e.Description)
		if desc == "" {
			desc = "（该技能没有写明适用条件，仅凭名称判断是否相关）"
		} else if r := []rune(desc); len(r) > maxCatalogDescRunes {
			desc = string(r[:maxCatalogDescRunes]) + "…"
		}
		b.WriteString("\n- **")
		b.WriteString(e.Name)
		b.WriteString("** — ")
		b.WriteString(desc)
		if cost := skillCostLabel(e.BodyBytes); cost != "" {
			b.WriteString("（加载约 ")
			b.WriteString(cost)
			b.WriteString("）")
		}
	}
	return b.String()
}

// skillCostLabel 把正文字节数折算成模型可读的上下文成本。
//
// 用一个保守的近似（约 3 字节/token，中英混排的常见比例）而不是精确分词：
// 目的只是让模型感知「这是一次昂贵的加载」，精确到百位没有意义。
func skillCostLabel(bodyBytes int) string {
	if bodyBytes <= 0 {
		return ""
	}
	tokens := bodyBytes / 3
	switch {
	case tokens < 500:
		return "很少"
	case tokens < 2000:
		return fmt.Sprintf("%d 千 token", (tokens+500)/1000)
	case tokens < 1000_000:
		return fmt.Sprintf("%d 千 token", tokens/1000)
	default:
		return "极多"
	}
}
