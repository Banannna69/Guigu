// knowledge.go：知识库的 HTTP 面——文档 CRUD、上传、检索预览、检索日志、embedder 配置。
// 主要函数/类型：
//   - kb / indexKnowledgeAsync：服务实例与后台索引。
//   - listKnowledge / getKnowledge / createKnowledge / updateKnowledge / deleteKnowledge：文档增删改查。
//   - uploadKnowledge：multipart 上传并按扩展名抽文本。
//   - knowledgeCategories / searchKnowledge / knowledgeLogs：分类、检索预览、检索审计。
//   - getKnowledgeConfig / saveKnowledgeConfig / testKnowledgeEmbedder / reindexKnowledge：embedder 配置与索引运维。
//
// 分层的意义：这里只做参数解析与鉴权边界，切分/向量化/检索全在 knowledge 包里。
// 入库后的向量化一律异步（go s.indexKnowledgeAsync）——一次 200 页 PDF 的 embedding
// 要跑几十秒，不能把上传请求挂在那里等。

package server

import (
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"artex/db"
	"artex/knowledge"
)

// kb 返回知识库服务实例（embedder 配置每次现读，因此可以临时构造）。
func (s *Server) kb() *knowledge.Service { return knowledge.NewService(s.m.pg) }

// 知识库请求体上限。文档正文可能很长（整份接口文档/评分规则），所以比普通 JSON
// 接口宽得多；但仍有上界，避免一次请求把内存吃穿。
const (
	maxKnowledgeJSONBodyBytes = 8 << 20
	maxKnowledgeUploadBytes   = 32 << 20
)

// indexKnowledgeAsync 后台为一条文档生成向量分块；失败只记日志，不影响已保存的文档
// （它仍能被关键词检索到，用户也可以在设置里点重建索引补救）。
func (s *Server) indexKnowledgeAsync(id int64) {
	if err := s.kb().IndexDoc(s.ctx, id); err != nil {
		log.Printf("[knowledge] 索引文档 #%d 失败: %v", id, err)
	}
}

// GET /api/knowledge?scope=&task=
func (s *Server) listKnowledge(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	scope := r.URL.Query().Get("scope")
	taskID, _ := strconv.ParseInt(r.URL.Query().Get("task"), 10, 64)
	docs, err := pg.ListKnowledgeDocs(scope, taskID)
	if err != nil {
		writeErr(w, 500, "读取失败: "+err.Error())
		return
	}
	writeJSON(w, 200, docs)
}

// GET /api/knowledge/{id}
func (s *Server) getKnowledge(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	doc, err := pg.GetKnowledgeDoc(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if doc == nil {
		writeErr(w, 404, "文档不存在")
		return
	}
	writeJSON(w, 200, doc)
}

// knowledgeDocBody 是新建/更新文档的请求体。
type knowledgeDocBody struct {
	Scope    string `json:"scope"`
	TaskID   int64  `json:"task_id"`
	Category string `json:"category"`
	Title    string `json:"title"`
	Content  string `json:"content"`
}

// POST /api/knowledge
func (s *Server) createKnowledge(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var b knowledgeDocBody
	if !decodeJSON(w, r, &b, maxKnowledgeJSONBodyBytes) {
		return
	}
	if strings.TrimSpace(b.Title) == "" {
		writeErr(w, 400, "标题不能为空")
		return
	}
	if b.Scope == "task" && b.TaskID <= 0 {
		writeErr(w, 400, "任务知识需指定所属任务")
		return
	}
	doc := &db.KnowledgeDoc{Scope: b.Scope, Category: b.Category, Title: b.Title, Content: b.Content, Source: "manual"}
	if b.Scope == "task" && b.TaskID > 0 {
		doc.TaskID = &b.TaskID
	}
	id, err := pg.AddKnowledgeDoc(doc)
	if err != nil {
		writeErr(w, 500, "保存失败: "+err.Error())
		return
	}
	go s.indexKnowledgeAsync(id)
	out, _ := pg.GetKnowledgeDoc(id)
	writeJSON(w, 200, out)
}

// PUT /api/knowledge/{id}
func (s *Server) updateKnowledge(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var b knowledgeDocBody
	if !decodeJSON(w, r, &b, maxKnowledgeJSONBodyBytes) {
		return
	}
	if err := pg.UpdateKnowledgeDoc(id, b.Category, b.Title, b.Content); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	go s.indexKnowledgeAsync(id)
	out, _ := pg.GetKnowledgeDoc(id)
	writeJSON(w, 200, out)
}

// DELETE /api/knowledge/{id}
func (s *Server) deleteKnowledge(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err := pg.DeleteKnowledgeDoc(id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// POST /api/knowledge/upload — multipart：file + scope + task_id + category。
func (s *Server) uploadKnowledge(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	if err := r.ParseMultipartForm(maxKnowledgeUploadBytes); err != nil {
		writeErr(w, 400, "上传解析失败")
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		writeErr(w, 400, "缺少文件")
		return
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxKnowledgeUploadBytes))
	if err != nil {
		writeErr(w, 400, "读取失败")
		return
	}
	text, err := knowledge.ExtractText(hdr.Filename, raw)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if strings.TrimSpace(text) == "" {
		writeErr(w, 400, "文档内容为空或无法抽取文本")
		return
	}
	scope := r.FormValue("scope")
	taskID, _ := strconv.ParseInt(r.FormValue("task_id"), 10, 64)
	// 任务库上传必须指明任务：否则会落成 scope=task 但 task_id=NULL 的孤儿行，
	// 读回来表现得像全局文档——这类脏数据事后极难排查，直接拒绝更干净。
	if scope == "task" && taskID <= 0 {
		writeErr(w, 400, "任务知识需指定所属任务：请选择要归属的任务")
		return
	}
	doc := &db.KnowledgeDoc{
		Scope:    scope,
		Category: r.FormValue("category"),
		Title:    strings.TrimSuffix(hdr.Filename, filepath.Ext(hdr.Filename)),
		Content:  text,
		Source:   "upload",
		FileName: hdr.Filename,
		Bytes:    int64(len(raw)),
	}
	if scope == "task" && taskID > 0 {
		doc.TaskID = &taskID
	}
	id, err := pg.AddKnowledgeDoc(doc)
	if err != nil {
		writeErr(w, 500, "保存失败: "+err.Error())
		return
	}
	go s.indexKnowledgeAsync(id)
	out, _ := pg.GetKnowledgeDoc(id)
	writeJSON(w, 200, out)
}

// GET /api/knowledge/categories?task=
func (s *Server) knowledgeCategories(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	taskID, _ := strconv.ParseInt(r.URL.Query().Get("task"), 10, 64)
	cats, err := pg.ListKnowledgeCategories(taskID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, cats)
}

// POST /api/knowledge/search {query, task_id, top_k} — 前端检索预览。
func (s *Server) searchKnowledge(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	var b struct {
		Query  string `json:"query"`
		TaskID int64  `json:"task_id"`
		TopK   int    `json:"top_k"`
	}
	if !decodeJSON(w, r, &b, maxSmallJSONBodyBytes) {
		return
	}
	hits, mode, err := s.kb().SearchAndLog(s.ctx, b.Query, b.TaskID, b.TopK, "手动检索")
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"mode": mode, "hits": hits})
}

// GET /api/knowledge/logs — 最近 200 条检索审计。
func (s *Server) knowledgeLogs(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	logs, err := pg.ListKnowledgeRetrievalLogs(200)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, logs)
}

// GET /api/knowledge/config
func (s *Server) getKnowledgeConfig(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	cfg, err := pg.GetKnowledgeConfig()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	global, task, _ := pg.KnowledgeDocCount()
	writeJSON(w, 200, map[string]any{
		"enabled":          cfg.Enabled,
		"base_url":         cfg.BaseURL,
		"model":            cfg.Model,
		"dim":              cfg.Dim,
		"has_key":          cfg.HasKey,
		"vector_available": pg.KnowledgeVectorAvailable(),
		"semantic_ready":   s.kb().SemanticReady(),
		"docs_global":      global,
		"docs_task":        task,
	})
}

// PUT /api/knowledge/config {enabled, base_url, model, api_key, dim}
func (s *Server) saveKnowledgeConfig(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var b struct {
		Enabled bool   `json:"enabled"`
		BaseURL string `json:"base_url"`
		Model   string `json:"model"`
		APIKey  string `json:"api_key"`
		Dim     int    `json:"dim"`
	}
	if !decodeJSON(w, r, &b, maxSmallJSONBodyBytes) {
		return
	}
	if err := pg.SaveKnowledgeConfig(b.Enabled, b.BaseURL, b.Model, b.APIKey, b.Dim); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// POST /api/knowledge/config/test — 探测 embedder 连通性。
func (s *Server) testKnowledgeEmbedder(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	dim, err := s.kb().TestEmbedder(s.ctx)
	if err != nil {
		writeErr(w, 400, "连通失败: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "dim": dim})
}

// POST /api/knowledge/reindex — 换 embedder/模型后重建全部向量。
func (s *Server) reindexKnowledge(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	n, err := s.kb().Reindex(s.ctx)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "indexed": n})
}
