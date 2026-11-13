// knowledge.go：知识库数据层（两层 RAG：全局公共库 + 任务专属资料）。
// 主要函数/类型：
//   - KnowledgeDoc / KnowledgeConfig / KnowledgeHit / KnowledgeRetrievalLog：文档、embedder 配置、检索命中与检索日志。
//   - KnowledgeVectorAvailable / ensureKnowledgeVectorSchema：向量层可用性与守卫式建表（缺 pgvector 自动降级）。
//   - GetKnowledgeConfig / SaveKnowledgeConfig：embedder 单例配置的读写。
//   - AddKnowledgeDoc / UpdateKnowledgeDoc / GetKnowledgeDoc / ListKnowledgeDocs / DeleteKnowledgeDoc：文档增删改查。
//   - ReplaceKnowledgeChunks / ClearKnowledgeChunks：向量分块的重建与清空。
//   - SearchKnowledgeVector / SearchKnowledgeKeyword：语义检索与关键词兜底。
//   - AddKnowledgeRetrievalLog / ListKnowledgeRetrievalLogs：检索可观测性。
//
// 为什么分两层：渗透任务的资料有两类——**跨任务通用**的知识（漏洞库、0day、方法论、
// 评分规则）和**只对本次任务成立**的资料（甲方给的接口文档、测试账号、授权范围）。
// 前者重复录入是浪费，后者混进全局库会污染别的任务。scope=global|task 把它们分开，
// 检索时全局永远可见、任务库只在所属任务内可见。
//
// 为什么降级而不是硬依赖：pgvector 需要超级用户在数据库里预建扩展，很多客户环境
// 装不了。缺它就退化成 ILIKE 关键词检索——能力弱一些但功能不消失、启动不阻断。

package db

import (
	"database/sql"
	"log"
	"strconv"
	"strings"
	"time"
)

// KnowledgeDoc 是一条知识文档。列表视图不带 Content（可能很大）。
type KnowledgeDoc struct {
	ID        int64     `json:"id"`
	Scope     string    `json:"scope"` // global | task
	TaskID    *int64    `json:"task_id,omitempty"`
	Category  string    `json:"category"`
	Title     string    `json:"title"`
	Content   string    `json:"content,omitempty"`
	Source    string    `json:"source"` // manual | upload
	FileName  string    `json:"file_name,omitempty"`
	Bytes     int64     `json:"bytes"`
	Indexed   bool      `json:"indexed"`
	Chunks    int       `json:"chunks"` // 向量分块数（计算得出；关键词模式下恒为 0）
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// KnowledgeConfig 是 embedder 的单例配置。
type KnowledgeConfig struct {
	Enabled bool   `json:"enabled"`
	BaseURL string `json:"base_url"`
	Model   string `json:"model"`
	APIKey  string `json:"-"` // 永不序列化给 UI
	HasKey  bool   `json:"has_key"`
	Dim     int    `json:"dim"`
}

// KnowledgeHit 是一条检索命中（语义模式是分块，关键词模式是文档摘要）。
type KnowledgeHit struct {
	DocID    int64   `json:"doc_id"`
	Title    string  `json:"title"`
	Category string  `json:"category"`
	Scope    string  `json:"scope"`
	Content  string  `json:"content"`
	Score    float64 `json:"score"` // 语义：余弦相似度；关键词：词法优先级
}

// KnowledgeChunkInsert 是一条待落库的分块 + 它的向量。
type KnowledgeChunkInsert struct {
	Index     int
	Content   string
	Embedding []float32
}

// KnowledgeRetrievalLog 是一次检索的审计记录。
type KnowledgeRetrievalLog struct {
	ID        int64     `json:"id"`
	TaskID    *int64    `json:"task_id,omitempty"`
	AgentKey  string    `json:"agent_key"`
	Query     string    `json:"query"`
	Scope     string    `json:"scope"`
	Mode      string    `json:"mode"`
	TopK      int       `json:"top_k"`
	Hits      int       `json:"hits"`
	CreatedAt time.Time `json:"created_at"`
}

// KnowledgeVectorAvailable 报告 pgvector 语义层是否可用。
func (d *DB) KnowledgeVectorAvailable() bool { return d.GetBool("knowledge.vector_available", false) }

// ensureKnowledgeVectorSchema 尽力创建 pgvector 扩展与向量分块表。
//
// 失败不返回错误：缺扩展是部署环境问题（需超级用户预建），不该阻断启动。
// 结果写进 settings 的 knowledge.vector_available，检索层据此决定走语义还是关键词。
func (d *DB) ensureKnowledgeVectorSchema() {
	if _, err := d.Exec(`CREATE EXTENSION IF NOT EXISTS vector`); err != nil {
		log.Printf("[knowledge] pgvector 不可用（%v）——语义检索禁用，降级为关键词检索。"+
			"启用需超级用户执行一次 CREATE EXTENSION vector", err)
		_ = d.SetBool("knowledge.vector_available", false)
		return
	}
	// embedding 用不定维 vector 列 + 每分块记 embed_model/embed_dim：换模型后
	// 新旧向量维度可能不同，检索时按当前模型过滤，避免维度不匹配报错。
	if _, err := d.Exec(`CREATE TABLE IF NOT EXISTS knowledge_chunks (
    id          BIGSERIAL PRIMARY KEY,
    doc_id      BIGINT NOT NULL REFERENCES knowledge_docs(id) ON DELETE CASCADE,
    chunk_index INT NOT NULL DEFAULT 0,
    content     TEXT NOT NULL DEFAULT '',
    embedding   vector,
    embed_model TEXT NOT NULL DEFAULT '',
    embed_dim   INT NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
)`); err != nil {
		log.Printf("[knowledge] knowledge_chunks 建表失败（%v）——降级为关键词检索", err)
		_ = d.SetBool("knowledge.vector_available", false)
		return
	}
	if _, err := d.Exec(`CREATE INDEX IF NOT EXISTS idx_knowledge_chunks_doc ON knowledge_chunks(doc_id)`); err != nil {
		log.Printf("[knowledge] knowledge_chunks 索引创建失败（%v）", err)
	}
	_ = d.SetBool("knowledge.vector_available", true)
}

// ---------- embedder 配置 ----------

// GetKnowledgeConfig 读 embedder 配置；缺行时返回内置默认值（不报错）。
func (d *DB) GetKnowledgeConfig() (*KnowledgeConfig, error) {
	var c KnowledgeConfig
	err := d.QueryRow(`SELECT enabled, base_url, model, COALESCE(api_key,''), dim FROM knowledge_config WHERE id=1`).
		Scan(&c.Enabled, &c.BaseURL, &c.Model, &c.APIKey, &c.Dim)
	if err == sql.ErrNoRows {
		return &KnowledgeConfig{BaseURL: "https://open.bigmodel.cn/api/paas/v4", Model: "embedding-3", Dim: 1024}, nil
	}
	if err != nil {
		return nil, err
	}
	c.HasKey = strings.TrimSpace(c.APIKey) != ""
	return &c, nil
}

// SaveKnowledgeConfig 落库 embedder 配置。空 apiKey 表示「不改动已有的 key」，
// 这样前端不必为了改模型而重新输入密钥。
func (d *DB) SaveKnowledgeConfig(enabled bool, baseURL, model, apiKey string, dim int) error {
	if dim <= 0 {
		dim = 1024
	}
	if strings.TrimSpace(apiKey) == "" {
		_, err := d.Exec(`UPDATE knowledge_config SET enabled=$1, base_url=$2, model=$3, dim=$4, updated_at=now() WHERE id=1`,
			enabled, baseURL, model, dim)
		return err
	}
	_, err := d.Exec(`UPDATE knowledge_config SET enabled=$1, base_url=$2, model=$3, api_key=$4, dim=$5, updated_at=now() WHERE id=1`,
		enabled, baseURL, model, apiKey, dim)
	return err
}

// AdoptKnowledgeKeyFromProfile 在 embedder 还没有 key 时，复用某个 LLM 配置的
// api_key——同一家供应商（如智谱）常常既是聊天模型又是 embedding 模型，让用户
// 再抄一遍密钥没有意义。已有 key 或该配置无 key 时什么都不做。
func (d *DB) AdoptKnowledgeKeyFromProfile(profileID int64) (bool, error) {
	res, err := d.Exec(`UPDATE knowledge_config
		SET api_key = p.api_key, updated_at = now()
		FROM llm_profiles p
		WHERE knowledge_config.id = 1
		  AND COALESCE(knowledge_config.api_key,'') = ''
		  AND p.id = $1 AND COALESCE(p.api_key,'') <> ''`, profileID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ---------- 文档 ----------

// AddKnowledgeDoc 插入一条文档并返回其 id。scope=task 必须带 task_id，
// 否则静默变成孤儿行（scope=task 但 task_id=NULL，读回来像全局）。
func (d *DB) AddKnowledgeDoc(doc *KnowledgeDoc) (int64, error) {
	scope := "global"
	var tid *int64
	if doc.Scope == "task" && doc.TaskID != nil && *doc.TaskID > 0 {
		scope, tid = "task", doc.TaskID
	}
	source := doc.Source
	if source == "" {
		source = "manual"
	}
	nbytes := doc.Bytes
	if nbytes == 0 {
		nbytes = int64(len(doc.Content))
	}
	var id int64
	err := d.QueryRow(`INSERT INTO knowledge_docs (scope, task_id, category, title, content, source, file_name, bytes)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id`,
		scope, tid, strings.TrimSpace(doc.Category), strings.TrimSpace(doc.Title),
		doc.Content, source, doc.FileName, nbytes).Scan(&id)
	return id, err
}

// UpdateKnowledgeDoc 更新文档正文，并把 indexed 打回 false（内容变了，旧向量作废）。
func (d *DB) UpdateKnowledgeDoc(id int64, category, title, content string) error {
	_, err := d.Exec(`UPDATE knowledge_docs SET category=$2, title=$3, content=$4, bytes=$5, indexed=false, updated_at=now() WHERE id=$1`,
		id, strings.TrimSpace(category), strings.TrimSpace(title), content, int64(len(content)))
	return err
}

// GetKnowledgeDoc 按 id 取文档（含正文）；不存在返回 (nil, nil)。
func (d *DB) GetKnowledgeDoc(id int64) (*KnowledgeDoc, error) {
	var doc KnowledgeDoc
	var tid sql.NullInt64
	err := d.QueryRow(`SELECT id, scope, task_id, category, title, content, source, file_name, bytes, indexed, created_at, updated_at
		FROM knowledge_docs WHERE id=$1`, id).
		Scan(&doc.ID, &doc.Scope, &tid, &doc.Category, &doc.Title, &doc.Content, &doc.Source,
			&doc.FileName, &doc.Bytes, &doc.Indexed, &doc.CreatedAt, &doc.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if tid.Valid {
		v := tid.Int64
		doc.TaskID = &v
	}
	return &doc, nil
}

// ListKnowledgeDocs 按 scope 列文档（不带正文，正文用 GetKnowledgeDoc 单取）。
//
//   - scope="global"            → 只要全局库
//   - scope="task" + taskID>0   → 只要该任务库
//   - 空 scope + taskID>0       → 全局 + 该任务（agent 看到的就是这一层）
//   - 空 scope + taskID=0       → 全部
func (d *DB) ListKnowledgeDocs(scope string, taskID int64) ([]*KnowledgeDoc, error) {
	var where string
	var args []any
	switch {
	case scope == "global":
		where = "WHERE scope='global'"
	case scope == "task" && taskID > 0:
		where = "WHERE scope='task' AND task_id=$1"
		args = append(args, taskID)
	case taskID > 0:
		where = "WHERE scope='global' OR (scope='task' AND task_id=$1)"
		args = append(args, taskID)
	}
	q := `SELECT id, scope, task_id, category, title, source, file_name, bytes, indexed, created_at, updated_at
		FROM knowledge_docs ` + where + ` ORDER BY scope, category, created_at DESC`
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*KnowledgeDoc{}
	for rows.Next() {
		var doc KnowledgeDoc
		var tid sql.NullInt64
		if err := rows.Scan(&doc.ID, &doc.Scope, &tid, &doc.Category, &doc.Title, &doc.Source,
			&doc.FileName, &doc.Bytes, &doc.Indexed, &doc.CreatedAt, &doc.UpdatedAt); err != nil {
			return nil, err
		}
		if tid.Valid {
			v := tid.Int64
			doc.TaskID = &v
		}
		out = append(out, &doc)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 补上分块数（尽力而为，且只在向量层存在时才有意义）。
	if d.KnowledgeVectorAvailable() && len(out) > 0 {
		if cr, e := d.Query(`SELECT doc_id, count(*) FROM knowledge_chunks GROUP BY doc_id`); e == nil {
			counts := map[int64]int{}
			for cr.Next() {
				var id int64
				var n int
				if cr.Scan(&id, &n) == nil {
					counts[id] = n
				}
			}
			_ = cr.Err()
			cr.Close()
			for _, doc := range out {
				doc.Chunks = counts[doc.ID]
			}
		}
	}
	return out, nil
}

// DeleteKnowledgeDoc 删除文档（分块随外键 CASCADE 一并删除）。
func (d *DB) DeleteKnowledgeDoc(id int64) error {
	_, err := d.Exec(`DELETE FROM knowledge_docs WHERE id=$1`, id)
	return err
}

// ListKnowledgeCategories 列出该 scope 可见的去重分类（全局 + 指定任务）。
func (d *DB) ListKnowledgeCategories(taskID int64) ([]string, error) {
	q := `SELECT DISTINCT category FROM knowledge_docs WHERE category<>'' AND (scope='global'`
	var args []any
	if taskID > 0 {
		q += ` OR (scope='task' AND task_id=$1)`
		args = append(args, taskID)
	}
	q += `) ORDER BY category`
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// KnowledgeDocCount 返回全局库与任务库的文档总数（给统计与空状态用）。
func (d *DB) KnowledgeDocCount() (global, task int, err error) {
	err = d.QueryRow(`SELECT
		count(*) FILTER (WHERE scope='global'),
		count(*) FILTER (WHERE scope='task') FROM knowledge_docs`).Scan(&global, &task)
	return
}

// ---------- 分块（语义层） ----------

// ReplaceKnowledgeChunks 重建一条文档的分块：先删旧分块再写新分块，并把文档标记为已索引。
// 只在向量层可用时调用。
func (d *DB) ReplaceKnowledgeChunks(docID int64, model string, dim int, chunks []KnowledgeChunkInsert) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM knowledge_chunks WHERE doc_id=$1`, docID); err != nil {
		return err
	}
	for _, c := range chunks {
		if _, err := tx.Exec(`INSERT INTO knowledge_chunks (doc_id, chunk_index, content, embedding, embed_model, embed_dim)
			VALUES ($1,$2,$3,$4::vector,$5,$6)`,
			docID, c.Index, c.Content, vecLiteral(c.Embedding), model, dim); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE knowledge_docs SET indexed=true, updated_at=now() WHERE id=$1`, docID); err != nil {
		return err
	}
	return tx.Commit()
}

// ClearKnowledgeChunks 清空全部分块并把所有文档打回未索引（重建索引前调用）。
func (d *DB) ClearKnowledgeChunks() error {
	if !d.KnowledgeVectorAvailable() {
		return nil
	}
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`TRUNCATE knowledge_chunks`); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE knowledge_docs SET indexed=false`); err != nil {
		return err
	}
	return tx.Commit()
}

// SearchKnowledgeVector 做余弦相似度检索。
//
// 按当前模型过滤（embed_model=$2）是换模型的安全阀：不同模型的向量维度和语义空间
// 都不同，混在一起算距离会得到垃圾结果，不如直接跳过没重编的旧分块。
func (d *DB) SearchKnowledgeVector(queryVec []float32, model string, taskID int64, topK int) ([]KnowledgeHit, error) {
	if topK <= 0 {
		topK = 5
	}
	scopeClause := "(dc.scope='global'"
	args := []any{vecLiteral(queryVec), model}
	if taskID > 0 {
		scopeClause += " OR (dc.scope='task' AND dc.task_id=$3)"
		args = append(args, taskID)
	}
	scopeClause += ")"
	q := `SELECT dc.id, dc.title, dc.category, dc.scope, kc.content,
		1 - (kc.embedding <=> $1::vector) AS score
		FROM knowledge_chunks kc JOIN knowledge_docs dc ON dc.id = kc.doc_id
		WHERE kc.embed_model = $2 AND ` + scopeClause + `
		ORDER BY kc.embedding <=> $1::vector
		LIMIT ` + strconv.Itoa(topK)
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanKnowledgeHits(rows)
}

// SearchKnowledgeKeyword 是没有 embedder 时的兜底：标题/正文/分类上的 ILIKE 匹配。
// 分数只是粗排序（标题命中 > 分类命中 > 正文命中），不追求语义质量。
func (d *DB) SearchKnowledgeKeyword(query string, taskID int64, topK int) ([]KnowledgeHit, error) {
	if topK <= 0 {
		topK = 5
	}
	like := "%" + strings.TrimSpace(query) + "%"
	scopeClause := "(scope='global'"
	args := []any{like}
	if taskID > 0 {
		scopeClause += " OR (scope='task' AND task_id=$2)"
		args = append(args, taskID)
	}
	scopeClause += ")"
	q := `SELECT id, title, category, scope, LEFT(content, 1200) AS content,
		(CASE WHEN title ILIKE $1 THEN 1.0 WHEN category ILIKE $1 THEN 0.8 ELSE 0.5 END) AS score
		FROM knowledge_docs
		WHERE (title ILIKE $1 OR content ILIKE $1 OR category ILIKE $1) AND ` + scopeClause + `
		ORDER BY score DESC, updated_at DESC
		LIMIT ` + strconv.Itoa(topK)
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanKnowledgeHits(rows)
}

// scanKnowledgeHits 把两种检索共用的结果列扫成 KnowledgeHit。
func scanKnowledgeHits(rows *sql.Rows) ([]KnowledgeHit, error) {
	out := []KnowledgeHit{}
	for rows.Next() {
		var h KnowledgeHit
		if err := rows.Scan(&h.DocID, &h.Title, &h.Category, &h.Scope, &h.Content, &h.Score); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// ---------- 检索日志 ----------

// AddKnowledgeRetrievalLog 记一条检索日志。尽力而为：写失败不影响检索本身。
func (d *DB) AddKnowledgeRetrievalLog(taskID int64, agentKey, query, scope, mode string, topK, hits int, docIDs []int64) {
	var tid *int64
	if taskID > 0 {
		tid = &taskID
	}
	ids := make([]string, 0, len(docIDs))
	for _, id := range docIDs {
		ids = append(ids, strconv.FormatInt(id, 10))
	}
	arrLit := "{" + strings.Join(ids, ",") + "}" // 纯整数，无注入风险
	_, _ = d.Exec(`INSERT INTO knowledge_retrieval_logs (task_id, agent_key, query, scope, mode, top_k, hits, doc_ids)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8::bigint[])`, tid, agentKey, query, scope, mode, topK, hits, arrLit)
}

// ListKnowledgeRetrievalLogs 按时间倒序列最近的检索日志。
func (d *DB) ListKnowledgeRetrievalLogs(limit int) ([]*KnowledgeRetrievalLog, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := d.Query(`SELECT id, task_id, agent_key, query, scope, mode, top_k, hits, created_at
		FROM knowledge_retrieval_logs ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*KnowledgeRetrievalLog{}
	for rows.Next() {
		var l KnowledgeRetrievalLog
		var tid sql.NullInt64
		if err := rows.Scan(&l.ID, &tid, &l.AgentKey, &l.Query, &l.Scope, &l.Mode, &l.TopK, &l.Hits, &l.CreatedAt); err != nil {
			return nil, err
		}
		if tid.Valid {
			v := tid.Int64
			l.TaskID = &v
		}
		out = append(out, &l)
	}
	return out, rows.Err()
}

// vecLiteral 把 float32 切片渲染成 pgvector 的字面量 "[1,2,3]"。
func vecLiteral(v []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, x := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(x), 'f', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}
