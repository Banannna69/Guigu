// service.go：知识库服务层——入库（切分 → 向量化 → 落库）与检索的编排。
// 主要函数/类型：
//   - Service / NewService：服务主体，只持有 DB 句柄；embedder 配置每次现读。
//   - SemanticReady：语义检索是否可用（pgvector + 开关 + 密钥三者齐备）。
//   - IndexDoc / Reindex：为文档生成向量分块 / 重建全部索引。
//   - Search / SearchAndLog：检索（语义优先，失败静默降级关键词）与带审计的检索。
//   - Categories / TestEmbedder：分类列举与 embedder 连通性探测。
//
// 配置为什么每次现读而不缓存：用户在设置页改完 embedder 应该立刻生效，不该要求
// 重启容器。这条路径只在「入库」和「检索」两个低频入口上跑一次 SELECT，成本可以忽略。

package knowledge

import (
	"context"
	"fmt"
	"strings"

	"artex/db"
)

// Service 编排知识库的入库与检索，只持有 *db.DB。
type Service struct{ pg *db.DB }

// NewService 构造服务；pg 为 nil 时所有方法都安全退化。
func NewService(pg *db.DB) *Service { return &Service{pg: pg} }

const (
	chunkChars   = 700 // 单分块字符数：约 500 汉字，够一个完整的判定规则或操作步骤
	chunkOverlap = 80  // 硬切时的重叠，避免切断处的句子两边都读不到
	embedBatch   = 16  // 每次 /embeddings 调用带多少条（多数供应商的安全上限）
)

// SemanticReady 报告语义检索能否运行：向量层存在 + embedder 已启用 + 有密钥。
// false 时检索走关键词，功能不消失。
func (s *Service) SemanticReady() bool {
	if s == nil || s.pg == nil || !s.pg.KnowledgeVectorAvailable() {
		return false
	}
	cfg, err := s.pg.GetKnowledgeConfig()
	if err != nil || cfg == nil {
		return false
	}
	return cfg.Enabled && strings.TrimSpace(cfg.APIKey) != ""
}

// Categories 列出该 scope 可见的分类（全局 + 指定任务）。
func (s *Service) Categories(taskID int64) ([]string, error) {
	if s == nil || s.pg == nil {
		return nil, fmt.Errorf("知识库未启用")
	}
	return s.pg.ListKnowledgeCategories(taskID)
}

// embedConfig 读当前的 embedder 配置。
func (s *Service) embedConfig() (EmbedConfig, error) {
	cfg, err := s.pg.GetKnowledgeConfig()
	if err != nil {
		return EmbedConfig{}, err
	}
	return EmbedConfig{BaseURL: cfg.BaseURL, Model: cfg.Model, APIKey: cfg.APIKey, Dim: cfg.Dim}, nil
}

// IndexDoc 为一条文档切分 + 取向量 + 落库。
//
// 语义检索未就绪时直接返回 nil：关键词模式直接读文档正文，不需要分块，
// 此时「没索引」是正常状态而不是失败。
func (s *Service) IndexDoc(ctx context.Context, docID int64) error {
	if !s.SemanticReady() {
		return nil
	}
	doc, err := s.pg.GetKnowledgeDoc(docID)
	if err != nil || doc == nil {
		return err
	}
	chunks := ChunkText(doc.Content, chunkChars, chunkOverlap)
	if len(chunks) == 0 {
		return s.pg.ReplaceKnowledgeChunks(docID, "", 0, nil) // 清空旧分块并标记已索引
	}
	cfg, err := s.embedConfig()
	if err != nil {
		return err
	}
	inputs := make([]string, len(chunks))
	for i, c := range chunks {
		inputs[i] = embedInput(doc.Category, doc.Title, c)
	}
	// 分批取向量：一次送太多会被供应商拒（或超时），16 条在多数实现上都安全。
	vecs := make([][]float32, 0, len(inputs))
	for i := 0; i < len(inputs); i += embedBatch {
		j := min(i+embedBatch, len(inputs))
		part, err := EmbedTexts(ctx, cfg, inputs[i:j])
		if err != nil {
			return err
		}
		vecs = append(vecs, part...)
	}
	ins := make([]db.KnowledgeChunkInsert, len(chunks))
	for i := range chunks {
		ins[i] = db.KnowledgeChunkInsert{Index: i, Content: chunks[i], Embedding: vecs[i]}
	}
	return s.pg.ReplaceKnowledgeChunks(docID, cfg.Model, cfg.Dim, ins)
}

// Search 检索 top-k 相关片段，返回命中的内容与所用模式（semantic | keyword）。
//
// 语义路径上的**任何**失败（embedder 超时、供应商 5xx、维度不匹配）都静默降到
// 关键词检索：检索是 agent 的中途动作，为了「语义更好」而让整个任务因供应商抖动
// 失败是本末倒置。
func (s *Service) Search(ctx context.Context, query string, taskID int64, topK int) ([]db.KnowledgeHit, string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, "keyword", nil
	}
	if s.SemanticReady() {
		if cfg, err := s.embedConfig(); err == nil {
			if vecs, err := EmbedTexts(ctx, cfg, []string{query}); err == nil && len(vecs) == 1 {
				if hits, err := s.pg.SearchKnowledgeVector(vecs[0], cfg.Model, taskID, topK); err == nil {
					return hits, "semantic", nil
				}
			}
		}
		// 语义路径任一步失败 → 落到下面的关键词检索
	}
	hits, err := s.pg.SearchKnowledgeKeyword(query, taskID, topK)
	return hits, "keyword", err
}

// SearchAndLog 检索并记一条审计日志（agent 工具与手动检索预览都走这里）。
func (s *Service) SearchAndLog(ctx context.Context, query string, taskID int64, topK int, agentKey string) ([]db.KnowledgeHit, string, error) {
	hits, mode, err := s.Search(ctx, query, taskID, topK)
	if err == nil && s.pg != nil {
		scope := "global"
		if taskID > 0 {
			scope = "global+task"
		}
		ids := make([]int64, 0, len(hits))
		for _, h := range hits {
			ids = append(ids, h.DocID)
		}
		s.pg.AddKnowledgeRetrievalLog(taskID, agentKey, query, scope, mode, topK, len(hits), ids)
	}
	return hits, mode, err
}

// Reindex 重建全部文档的向量（换 embedder 或模型后必须做一次）。
func (s *Service) Reindex(ctx context.Context) (int, error) {
	if !s.SemanticReady() {
		return 0, fmt.Errorf("语义检索未就绪：需 pgvector + 启用 embedder + 配置 api_key")
	}
	if err := s.pg.ClearKnowledgeChunks(); err != nil {
		return 0, err
	}
	docs, err := s.pg.ListKnowledgeDocs("", 0) // 全部文档
	if err != nil {
		return 0, err
	}
	n := 0
	for _, doc := range docs {
		if err := s.IndexDoc(ctx, doc.ID); err != nil {
			return n, fmt.Errorf("文档 #%d(%s) 索引失败: %w", doc.ID, doc.Title, err)
		}
		n++
	}
	return n, nil
}

// TestEmbedder 用一句探针验证 embedder 可用，返回实际拿到的向量维度。
// 维度很关键：填错了 dim 也能拿到向量，但检索结果会离谱，这里如实回报。
func (s *Service) TestEmbedder(ctx context.Context) (int, error) {
	cfg, err := s.embedConfig()
	if err != nil {
		return 0, err
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return 0, fmt.Errorf("未配置 api_key")
	}
	vecs, err := EmbedTexts(ctx, cfg, []string{"知识库连通性测试 knowledge base connectivity test"})
	if err != nil {
		return 0, err
	}
	if len(vecs) != 1 || len(vecs[0]) == 0 {
		return 0, fmt.Errorf("embedder 返回空向量")
	}
	return len(vecs[0]), nil
}
