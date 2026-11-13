// report 域：交付级报告的生成/轮询/下载、出具方设置与 logo、命名模版、已生成报告清单，
// 以及漏洞富化、证据截图、复现步骤→真实流量的关联。
/**
 * 主要函数/类型：
 *   - ReportJob / ReportSettings / ReportTemplate / ReportRecord / Personnel：报告作业、出具方设置、模版、已生成报告、人员
 *   - Enrichment / EnrichJob / FindingEvidenceItem / FindingDetail：漏洞富化、证据截图与详情
 *   - reportApi / ReportApi：报告与漏洞深化域 API 聚合对象及其类型
 *   - generate / status / downloadUrl / extractFindings：异步生成、轮询、下载、从转录补录漏洞
 *   - settings / saveSettings / uploadLogo：出具方设置与 logo
 *   - templates / createTemplate / updateTemplate / deleteTemplate：命名模版增删改查
 *   - reports / deleteReport / reportDownloadUrl：已生成报告清单与管理
 *   - findingEnrich / findingEnrichStatus / findingUpdate：漏洞 AI 富化
 *   - evidenceList / evidenceUpload / evidenceUpdate / evidenceDelete：证据截图管理
 */
import { arr, del, get, post, put } from "@/lib/api/http";

// Personnel —— 报告封面的测试人员条目。
export interface Personnel {
  name: string;
  role: string;
  contact: string;
}

// ReportSettings —— 出具方单例设置。
export interface ReportSettings {
  company_name: string;
  company_name_en: string;
  logo_path: string;
  personnel: Personnel[];
  cvss_version: string;
  updated_at?: string;
}

// ReportTemplate —— 命名模版：出具方/logo/人员/CVSS/默认语言的预设组合。
export interface ReportTemplate {
  id: number;
  name: string;
  company_name: string;
  company_name_en: string;
  logo_path: string;
  personnel: Personnel[];
  cvss_version: string;
  locale: string;
  created_at: string;
  updated_at: string;
}

// ReportJob —— 一次异步生成作业的状态。
export interface ReportJob {
  status: "generating" | "done" | "failed";
  filename?: string;
  error?: string;
  started_at?: string;
}

// ReportRecord —— 历史已生成报告。后端不存 title/locale/format 三列：
// 标题落在文件名的 task 描述里、语言与格式由扩展名体现，所以这里只映射真实字段。
export interface ReportRecord {
  id: number;
  task_id?: number;
  task_desc: string;
  filename: string;
  size: number;
  company: string;
  created_at: string;
}

// Enrichment —— 一条漏洞的 AI 富化结果。字段名与后端 enrichmentDTO 逐字对应：
// 标题是 name（不是 title），description 是段落数组（不是单串）。
export interface Enrichment {
  node_id?: string;
  name: string;
  target: string;
  module: string;
  severity: string;
  status: string;
  cvss_score: number;
  cvss_vector: string;
  cvss_metrics?: Record<string, string>;
  description: string[];
  risk_analysis: string[];
  remediation: string[];
  test_process: { type?: string; content?: string; [k: string]: string | undefined }[];
  source?: string;
  updated_at?: string;
}

// EnrichJob —— 富化作业的异步状态。后端用 status 字段而非布尔：
// idle(还没做) / analyzing(进行中) / done(已完成) / failed(失败)。
// 失败与「还没做」必须区分开——前者要重试，后者不用。
export interface EnrichJob {
  status: "idle" | "analyzing" | "done" | "failed";
  error?: string;
  enrichment?: Enrichment;
  started_at?: string;
}

// FindingEvidenceItem —— 一条证据截图。字段与 db.FindingEvidence 逐字对应。
export interface FindingEvidenceItem {
  evidence: {
    id: number;
    node_id: number;
    filename: string;
    caption: string;
    step_index: number;
    sort_order: number;
    created_at: string;
  };
  url: string;
}

// StepRepro —— 一步复现：逐字请求/响应 + 解析到的真实流量 exchange id（可空）。
export interface StepRepro {
  request: string;
  response?: string;
  expected?: string;
  proves?: string;
  exchange_id?: string;
}

// Reproduction —— agent 在 report_finding 时**就地**记录的结构化复现过程。
// 报告优先逐字渲染它，AI 不得重建——这是报告可信的关键。
export interface Reproduction {
  entry_point?: string;
  prerequisites?: string[];
  steps?: StepRepro[];
  provenance?: string;
}

// FindingDetail —— GET /api/findings/{id}：漏洞本体 + 富化 + 复现三件套。
export interface FindingDetail {
  finding: Record<string, unknown>;
  enrichment: Enrichment | null;
  reproduction: Reproduction | null;
}

export interface ReportGenerateOpts {
  title?: string;
  client?: string;
  locale?: string;
  format?: string;
  findingIds?: (string | number)[];
  templateId?: number;
}

export const reportApi = {
  // ---- 交付级 .docx / .md ----
  // 生成是异步的：POST 立即返回 202，再按 task 轮询 status，完成后走 downloadUrl 取文件。
  reportGenerate: (taskId: string, opts: ReportGenerateOpts = {}) => {
    const qs = new URLSearchParams();
    qs.set("task", taskId);
    if (opts.title) qs.set("title", opts.title);
    if (opts.client) qs.set("client", opts.client);
    if (opts.locale) qs.set("locale", opts.locale);
    if (opts.format) qs.set("format", opts.format);
    if (opts.templateId) qs.set("template_id", String(opts.templateId));
    // 只有选了「严格子集」时才传 finding_ids；全选或不选都让后端用全部漏洞。
    if (opts.findingIds && opts.findingIds.length > 0) qs.set("finding_ids", opts.findingIds.join(","));
    return post<ReportJob>(`/report/generate?${qs}`, {});
  },
  reportStatus: (taskId: string) => get<ReportJob>(`/report/status?task=${encodeURIComponent(taskId)}`),
  reportDownloadUrlOf: (taskId: string) => `/api/report/download?task=${encodeURIComponent(taskId)}`,
  // 从会话转录里补录「被描述但没登记」的漏洞。
  reportExtractFindings: (taskId: string) =>
    post<{ extracted: number; created: number }>(`/tasks/${taskId}/extract-findings`, {}),

  // ---- 出具方设置 / logo ----
  reportSettings: () => get<ReportSettings>("/report-settings"),
  saveReportSettings: (patch: Partial<ReportSettings>) => put<ReportSettings>("/report-settings", patch),
  reportUploadLogo: async (file: File) => {
    const fd = new FormData();
    fd.append("file", file);
    const r = await fetch("/api/report-assets/logo", { method: "POST", body: fd });
    if (!r.ok) throw new Error((await r.text()) || `logo upload: ${r.status}`);
    return (await r.json()) as { logo_path: string; url: string };
  },

  // ---- 命名模版 ----
  reportTemplates: () => get<ReportTemplate[]>("/report-templates").then(arr),
  createReportTemplate: (body: Partial<ReportTemplate>) => post<ReportTemplate>("/report-templates", body),
  updateReportTemplate: (id: number, body: Partial<ReportTemplate>) =>
    put<ReportTemplate>(`/report-templates/${id}`, body),
  deleteReportTemplate: (id: number) => del<{ ok: boolean }>(`/report-templates/${id}`),

  // ---- 已生成报告清单 ----
  reportRecords: () => get<ReportRecord[]>("/reports").then(arr),
  reportRecordDownloadUrl: (id: number) => `/api/reports/${id}/download`,
  deleteReportRecord: (id: number) => del<{ ok: boolean }>(`/reports/${id}`),

  // ---- 漏洞富化（按探索 node_id 索引）----
  findingEnrich: (nodeId: string, locale?: string) =>
    post<EnrichJob>(`/findings/${nodeId}/enrich${locale ? `?locale=${encodeURIComponent(locale)}` : ""}`, {}),
  findingEnrichStatus: (nodeId: string) => get<EnrichJob>(`/findings/${nodeId}/enrich/status`),
  // 保存漏洞的属性字段；把 status 改成 confirmed 会顺带触发一次后台 AI 富化。
  // 只接受这五个字段——正文（描述/风险/修复）由富化生成，不走这条接口改。
  findingUpdate: (
    nodeId: string,
    patch: { name?: string; target?: string; severity?: string; status?: string; locale?: string },
  ) => put<{ enrichment: Enrichment | null; enriching: boolean }>(`/findings/${nodeId}`, patch),

  // ---- 证据截图 ----
  findingEvidenceList: (nodeId: string) =>
    get<{ items: FindingEvidenceItem[] }>(`/findings/${nodeId}/evidence`).then((r) => r.items ?? []),
  findingEvidenceUpload: async (nodeId: string, file: File, caption = "", stepIndex = 0) => {
    const fd = new FormData();
    fd.append("file", file);
    fd.append("caption", caption);
    fd.append("step_index", String(stepIndex));
    const r = await fetch(`/api/findings/${nodeId}/evidence`, { method: "POST", body: fd });
    if (!r.ok) throw new Error((await r.text()) || `evidence upload: ${r.status}`);
    return (await r.json()) as FindingEvidenceItem;
  },
  findingEvidenceUpdate: (
    nodeId: string,
    eid: number,
    patch: { caption: string; step_index: number; sort_order: number },
  ) => put<{ ok: boolean }>(`/findings/${nodeId}/evidence/${eid}`, patch),
  findingEvidenceDelete: (nodeId: string, eid: number) => del<{ ok: boolean }>(`/findings/${nodeId}/evidence/${eid}`),

  // ---- 角色分工 ----
  // 每个角色可以单独绑一个 LLM 配置；profile_id="0" 表示跟随全局激活配置。
  llmRoles: () => get<{ roles: { role: string; profile_id: string }[] }>("/llm/roles"),
  setLLMRole: (role: string, profileId: string) => post<{ ok: boolean }>("/llm/roles", { role, profile_id: profileId }),

  // ---- 工具 / 技能用量统计（复用可观测性）----
  // 用来判断哪些工具与技能真的在被使用、哪些装了没人碰（后者每轮都在占模型注意力）。
  toolStats: () => get<{ stats: { tool: string; calls: number }[] }>("/tools/stats").then((r) => r.stats ?? []),
  skillStats: () =>
    get<{ stats: { skill: string; calls: number; last_at?: string }[] }>("/skills/stats").then((r) => r.stats ?? []),
};

export type ReportApi = typeof reportApi;
