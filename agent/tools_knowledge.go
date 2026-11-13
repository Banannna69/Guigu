// tools_knowledge.go：知识库检索类工具（search_knowledge / list_knowledge_categories）。
// 主要函数/类型：
//   - SetKnowledgeStore：注入知识库服务；nil 表示工具报「未启用」。
//   - searchKnowledge：语义（或关键词兜底）检索全局 + 任务两层知识库。
//   - listKnowledgeCategories：先列分类，便于 agent 在定向检索前了解有什么。
//
// 这两个工具解决的是「模型不知道甲方的判定标准」问题：渗透质量的差别往往不在技术，
// 而在于用什么口径判定「这算不算漏洞」「这个风险该定几级」。把这些写进知识库比
// 塞进 system prompt 更好——prompt 是定长的、全局的、每轮都付费的；知识库是按需
// 检索的、可随时改的、只在实际用到时占上下文。

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	actool "norma/tool"

	"artex/knowledge"
)

// SetKnowledgeStore 注入知识库服务，使 search_knowledge / list_knowledge_categories 生效。
// nil = 工具报「知识库未启用」而不是报错，这样没配知识库的部署也能正常跑。
func (t *ToolSet) SetKnowledgeStore(kb *knowledge.Service) { t.kb = kb }

// searchKnowledge 在两层知识库上检索：全局通用知识 + 当前任务专属资料。
func (t *ToolSet) searchKnowledge() actool.CoreTool {
	return readTool("search_knowledge",
		"检索知识库：全局通用知识（漏洞库/0day/技巧/方法论/判定标准）+ 当前任务专属资料（评分规则/授权范围/目标接口文档/检查项）。"+
			"用自然语言描述你要找什么，返回最相关的知识片段。执行任务时应主动检索相关知识来指导方法选择与判定标准。",
		obj(map[string]any{
			"query": str("要检索的问题或主题，自然语言描述"),
			"top_k": intp("返回条数，默认 5"),
		}, "query"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Query string `json:"query"`
				TopK  int    `json:"top_k"`
			}
			_ = json.Unmarshal(in, &a)
			if strings.TrimSpace(a.Query) == "" {
				return actool.Errorf("query 不能为空"), nil
			}
			if t.kb == nil {
				return actool.Text("知识库未启用。"), nil
			}
			if a.TopK <= 0 {
				a.TopK = 5
			}
			hits, mode, err := t.kb.SearchAndLog(ctx, a.Query, t.taskID, a.TopK, t.worker)
			if err != nil {
				return actool.Errorf(fmt.Sprintf("检索失败: %v", err)), nil
			}
			if len(hits) == 0 {
				return actool.Text("知识库中未找到相关内容。"), nil
			}
			var b strings.Builder
			fmt.Fprintf(&b, "检索模式: %s，命中 %d 条：\n", mode, len(hits))
			for i, h := range hits {
				scope := "全局"
				if h.Scope == "task" {
					scope = "任务专属"
				}
				fmt.Fprintf(&b, "\n--- 结果 %d (相似度 %.0f%%, %s) ---\n[%s] %s\n%s\n",
					i+1, h.Score*100, scope, h.Category, h.Title, strings.TrimSpace(h.Content))
			}
			return actool.Text(b.String()), nil
		})
}

// listKnowledgeCategories 列出可见分类，让 agent 先看有什么再定向检索。
func (t *ToolSet) listKnowledgeCategories() actool.CoreTool {
	return readTool("list_knowledge_categories",
		"列出知识库现有分类（全局 + 当前任务）。先看有哪些类，再用 search_knowledge 针对性检索。",
		obj(map[string]any{}),
		func(_ context.Context, _ json.RawMessage) (actool.Result, error) {
			if t.kb == nil {
				return actool.Text("知识库未启用。"), nil
			}
			cats, err := t.kb.Categories(t.taskID)
			if err != nil {
				return actool.Errorf(fmt.Sprintf("读取失败: %v", err)), nil
			}
			if len(cats) == 0 {
				return actool.Text("知识库暂无分类。"), nil
			}
			return actool.Text("知识库分类：\n- " + strings.Join(cats, "\n- ")), nil
		})
}
