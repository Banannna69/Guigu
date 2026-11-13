// asset 域：FOFA 资产测绘同步、资产合集、审计日志、数据留存策略。
/**
 * 主要函数/类型：
 *   - assetApi / AssetApi：资产与审计域 API 聚合对象及其类型
 *   - fofaStatus / fofaDatasource / fofaQuery / fofaSync：FOFA 数据源与同步
 *   - collections / deleteCollection：资产合集列表与删除
 *   - auditLogs：审计日志分页查询
 *   - retention / setRetention：数据留存策略读写
 */
import { del, get, post, put } from "@/lib/api/http";
import { arr } from "@/lib/api/http";

// FofaRow —— 一行检索结果（预览用字段子集，顺序对应后端固定 fields）。
export interface FofaRow {
  host: string;
  ip: string;
  port: string;
  protocol: string;
  domain: string;
  title: string;
  server: string;
}

// FofaStatus —— 数据源配置与可达性。
export interface FofaStatus {
  configured: boolean;
  email: string;
  reachable?: boolean;
  error?: string;
}

// FofaSyncResult —— 一次同步的结果汇总。
export interface FofaSyncResult {
  synced: Record<string, number>;
  total: number;
  imported: number;
  truncated: boolean;
  collection: { id: number; name: string; members: number } | null;
  errors?: string[] | null;
}

// Collection —— 资产合集：显式成员清单，可作任务目标。
export interface Collection {
  id: number;
  name: string;
  query: string;
  source: string;
  member_count: number;
  created_at: string;
}

// AuditEntry —— 一条审计记录。
export interface AuditEntry {
  id: number;
  created_at: string;
  actor: string;
  category: string;
  action: string;
  result: string;
  ip: string;
  message: string;
  detail?: Record<string, unknown>;
}

// RetentionStatus —— 留存策略与各类数据现有行数。
export interface RetentionStatus {
  enabled: boolean;
  activity_days: number;
  traffic_days: number;
  delivery_days: number;
  audit_days: number;
  activity_rows: number;
  traffic_rows: number;
  audit_rows: number;
}

export const assetApi = {
  // ---- FOFA 数据源 ----
  fofaStatus: () => get<FofaStatus>("/sync/fofa/status"),
  fofaDatasource: (body: { email?: string; key?: string }) =>
    post<{ configured: boolean; email: string }>("/sync/fofa/datasource", body),
  fofaQuery: (body: { query: string; size?: number }) =>
    post<{ total: number; mode: string; rows: FofaRow[] }>("/sync/fofa/query", body).then((r) => ({
      ...r,
      rows: arr(r.rows),
    })),
  fofaSync: (body: { query: string; size?: number; collection_name?: string; page_size?: number }) =>
    post<FofaSyncResult>("/sync/fofa/sync", body),

  // ---- 资产合集 ----
  collections: () => get<{ collections: Collection[] }>("/collections").then((r) => arr(r.collections)),
  deleteCollection: (id: number) => del<{ deleted: boolean }>(`/collections/${id}`),

  // ---- 审计日志 ----
  auditLogs: (q?: { category?: string; action?: string; result?: string; limit?: number; offset?: number }) => {
    const params = new URLSearchParams();
    if (q?.category) params.set("category", q.category);
    if (q?.action) params.set("action", q.action);
    if (q?.result) params.set("result", q.result);
    if (q?.limit) params.set("limit", String(q.limit));
    if (q?.offset) params.set("offset", String(q.offset));
    const qs = params.toString();
    return get<{ entries: AuditEntry[]; total: number }>(`/audit-logs${qs ? "?" + qs : ""}`).then((r) => ({
      ...r,
      entries: arr(r.entries),
    }));
  },

  // ---- 数据留存 ----
  retention: () => get<RetentionStatus>("/settings/retention"),
  setRetention: (body: Partial<Omit<RetentionStatus, "activity_rows" | "traffic_rows" | "audit_rows">>) =>
    put<RetentionStatus>("/settings/retention", body),
};

export type AssetApi = typeof assetApi;

// ScenarioPack —— 场景增强包：只追加不替换，可多选叠加。
export interface ScenarioPack {
  id: number;
  name: string;
  description: string;
  planner_append: string;
  worker_append: string;
  skills: string[];
  disabled_tools: string[];
  builtin: boolean;
  created_at: string;
}

export interface ScenarioPackInput {
  name: string;
  description?: string;
  planner_append?: string;
  worker_append?: string;
  skills?: string[];
  disabled_tools?: string[];
}

export const scenarioApi = {
  scenarios: () => get<{ packs: ScenarioPack[] }>("/scenarios").then((r) => arr(r.packs)),
  createScenario: (body: ScenarioPackInput) => post<{ id: number }>("/scenarios", body),
  updateScenario: (id: number, body: ScenarioPackInput) => put<{ ok: boolean }>(`/scenarios/${id}`, body),
  deleteScenario: (id: number) => del<{ deleted: boolean }>(`/scenarios/${id}`),
  taskScenarios: (taskId: string) =>
    get<{ scenarios: { pack_id: number; name: string }[] }>(`/tasks/${taskId}/scenarios`).then((r) =>
      arr(r.scenarios),
    ),
};

export type ScenarioApi = typeof scenarioApi;

// McpPreset —— 后渗透类 MCP 的一键导入模板。
export interface McpPreset {
  key: string;
  name: string;
  category: "shell" | "c2" | "browser" | "recon" | string;
  description: string;
  transport: string;
  command?: string;
  args?: string[];
  url?: string;
  needs_input: string[];
  docs_url?: string;
}

export const mcpPresetApi = {
  mcpPresets: () => get<{ presets: McpPreset[] }>("/mcp-presets").then((r) => arr(r.presets)),
  applyMcpPreset: (key: string, name?: string) =>
    post<{ name: string; enabled: boolean; needs_input: string[]; message: string }>(
      `/mcp-presets/${encodeURIComponent(key)}`,
      { name: name ?? "" },
    ),
};

export type McpPresetApi = typeof mcpPresetApi;
