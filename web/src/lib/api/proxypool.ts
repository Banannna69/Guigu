// proxypool 域：出口代理池的配置、批量导入、连通性验证与运行状态。
/**
 * 主要函数/类型：
 *   - ProxyPoolEntry / ProxyPoolRunning / ProxyPoolState：池条目、运行态与整体状态
 *   - ProxyPoolCheckResult：一次连通性验证的逐条结果
 *   - proxyPoolApi / ProxyPoolApi：代理池域 API 聚合对象及其类型
 *   - proxyPoolGet / proxyPoolPut / proxyPoolBulk / proxyPoolCheck / proxyPoolFetch：读取、全量替换、批量导入、连通性验证、自动抓取
 */
import { get, post, put } from "@/lib/api/http";

// ProxyPoolEntry 是池里的一条配置（用户维护的部分）。
export interface ProxyPoolEntry {
  url: string;
  name: string;
  enabled: boolean;
  weight: number;
}

// ProxyPoolRunning 是一条上游的运行态。fail_ratio / samples 是这个平台在
// 「连败计数」之外新增的判定依据：连败为 0 不代表出口健康——一个 75% 失败但
// 每隔三次能成功一次的出口，连败永远到不了阈值，却拖慢了大部分请求。
export interface ProxyPoolRunning {
  url: string;
  name: string;
  failures: number;
  weight?: number;
  last_ok?: string;
  last_fail?: string;
  break_remaining?: number;
  fail_ratio?: number;
  samples?: number;
}

// ProxyPoolState 是 GET /api/proxypool 的整体视图。
export interface ProxyPoolState {
  entries: ProxyPoolEntry[];
  strategy: string;
  running: ProxyPoolRunning[];
  capturing: boolean;
  check_target?: string;
  health_minutes?: number;
  health_cooldown?: number;
  last_check?: { at: string; target: string; count: number; ok_count: number; auto?: boolean } | null;
}

// ProxyPoolCheckResult 是一条代理的验证结果。
export interface ProxyPoolCheckResult {
  index: number;
  url: string;
  ok: boolean;
  latency_ms: number;
  exit_ip?: string;
  error?: string;
  checked_at: string;
}

export const proxyPoolApi = {
  proxyPoolGet: () => get<ProxyPoolState>("/proxypool"),
  proxyPoolPut: (payload: { entries: { url: string; name: string; enabled: boolean; weight: number }[]; strategy: string }) =>
    put<ProxyPoolState>("/proxypool", payload),
  // 批量导入：一行一个代理，自动识别 host:port / http(s):// / socks5:// / host:port:user:pass
  proxyPoolBulk: (payload: { text: string; mode?: "append" | "replace"; strategy?: string; weight?: number }) =>
    post<{
      ok: boolean;
      added: number;
      updated: number;
      total: number;
      invalid: { line: number; raw: string; reason: string }[];
    }>("/proxypool/bulk", payload),
  // 从公开清单源自动抓取：抓取 → 去重 → 验证 → 并入池。
  // sources 为空用内置源；verify=false 则抓到就入池（快但脏，仅调试用）。
  proxyPoolFetch: (payload?: {
    sources?: { url: string; protocol: string }[];
    verify?: boolean;
    max?: number;
    concurrency?: number;
    target?: string;
  }) =>
    post<{
      ok: boolean;
      fetched: number;
      alive: number;
      added: number;
      total: number;
      verified: boolean;
      target: string;
      sources: { url: string; count: number; error?: string }[];
    }>("/proxypool/fetch", payload ?? {}),
  // 连通性验证：urls 为空 = 全部已启用条目
  proxyPoolCheck: (payload: { urls?: string[]; target?: string; timeout_seconds?: number; break_on_fail?: boolean }) =>
    post<{ target: string; results: ProxyPoolCheckResult[] }>("/proxypool/check", payload),
};

export type ProxyPoolApi = typeof proxyPoolApi;