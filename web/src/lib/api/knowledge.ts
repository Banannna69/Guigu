// knowledge 域：两层知识库（全局公共库 + 任务专属资料）、检索预览、embedder 配置。
/**
 * 主要函数/类型：
 *   - KnowledgeDoc / KnowledgeHit / KnowledgeLog / KnowledgeConfig：文档、检索命中、检索审计、embedder 配置
 *   - knowledgeApi / KnowledgeApi：知识库域 API 聚合对象及其类型
 *   - docs / doc / createDoc / updateDoc / deleteDoc：文档增删改查
 *   - upload：上传 .md/.txt/.csv/.json/.docx/.pdf 并按扩展名抽文本
 *   - categories / search / logs：分类列举、检索预览、检索审计
 *   - config / saveConfig / testEmbedder / reindex：embedder 配置与索引运维
 */
import { arr, del, get, post, put } from "@/lib/api/http";

// KnowledgeDoc —— 一条知识文档。列表视图不带 content（可能很长），详情才带。
export interface KnowledgeDoc {
  id: number;
  scope: "global" | "task";
  task_id?: number;
  category: string;
  title: string;
  content?: string;
  source: "manual" | "upload";
  file_name?: string;
  bytes: number;
  indexed: boolean;
  chunks: number;
  created_at: string;
  updated_at: string;
}

// KnowledgeHit —— 一条检索命中。语义模式下 content 是命中的分块，关键词模式下是文档摘要。
export interface KnowledgeHit {
  doc_id: number;
  title: string;
  category: string;
  scope: string;
  content: string;
  score: number;
}

// KnowledgeLog —— 一次检索的审计记录。
export interface KnowledgeLog {
  id: number;
  task_id?: number;
  agent_key: string;
  query: string;
  scope: string;
  mode: string;
  top_k: number;
  hits: number;
  created_at: string;
}

// KnowledgeConfig —— embedder 配置。后端只回 has_key（密钥永不回显）。
export interface KnowledgeConfig {
  enabled: boolean;
  base_url: string;
  model: string;
  dim: number;
  has_key: boolean;
  vector_available: boolean;
  semantic_ready: boolean;
  docs_global: number;
  docs_task: number;
}

export const knowledgeApi = {
  // ---- 文档 ----
  // scope 留空 + task 有值 → 返回「全局 + 该任务」两层；这是 agent 实际能看到的范围。
  knowledgeDocs: (params?: { scope?: string; task?: number }) => {
    const sp = new URLSearchParams();
    if (params?.scope) sp.set("scope", params.scope);
    if (params?.task) sp.set("task", String(params.task));
    return get<KnowledgeDoc[]>(`/knowledge?${sp}`).then(arr);
  },
  knowledgeDoc: (id: number) => get<KnowledgeDoc>(`/knowledge/${id}`),
  createKnowledgeDoc: (body: { scope?: string; task_id?: number; category: string; title: string; content: string }) =>
    post<KnowledgeDoc>("/knowledge", body),
  updateKnowledgeDoc: (id: number, body: { category: string; title: string; content: string }) =>
    put<KnowledgeDoc>(`/knowledge/${id}`, body),
  deleteKnowledgeDoc: (id: number) => del<{ ok: boolean }>(`/knowledge/${id}`),

  // 上传走 multipart：后端按扩展名抽文本（.docx/.pdf 交给 python）。
  knowledgeUpload: async (file: File, params?: { scope?: string; task_id?: number; category?: string }) => {
    const fd = new FormData();
    fd.append("file", file);
    if (params?.scope) fd.append("scope", params.scope);
    if (params?.task_id) fd.append("task_id", String(params.task_id));
    if (params?.category) fd.append("category", params.category);
    const r = await fetch("/api/knowledge/upload", { method: "POST", body: fd });
    if (!r.ok) throw new Error((await r.text()) || `upload: ${r.status}`);
    return (await r.json()) as KnowledgeDoc;
  },

  // ---- 检索 ----
  knowledgeCategories: (task = 0) => get<string[]>(`/knowledge/categories?task=${task}`).then(arr),
  knowledgeSearch: (query: string, taskId = 0, topK = 5) =>
    post<{ mode: string; hits: KnowledgeHit[] }>("/knowledge/search", { query, task_id: taskId, top_k: topK }),
  knowledgeLogs: () => get<KnowledgeLog[]>("/knowledge/logs").then(arr),

  // ---- embedder 配置与运维 ----
  knowledgeConfig: () => get<KnowledgeConfig>("/knowledge/config"),
  saveKnowledgeConfig: (body: { enabled: boolean; base_url: string; model: string; api_key?: string; dim: number }) =>
    put<{ ok: boolean }>("/knowledge/config", body),
  testKnowledgeEmbedder: () => post<{ ok: boolean; dim: number }>("/knowledge/config/test", {}),
  reindexKnowledge: () => post<{ ok: boolean; indexed: number }>("/knowledge/reindex", {}),
};

export type KnowledgeApi = typeof knowledgeApi;
