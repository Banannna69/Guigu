/**
 * page.tsx：系统 → 代理池：多上游出口的配置、批量导入、连通性验证与运行状态。
 * 主要函数/类型：
 *   - ProxyPoolPage：默认导出页组件
 *   - EntriesCard：条目增删改、启停与权重
 *   - HealthCard：策略选择、主动健康检查参数与「立即验证」
 *   - BulkDialog：粘贴批量导入
 *   - FetchDialog：从公开清单自动抓取并验证
 *
 * 为什么这个页面单独存在而不是塞进「系统配置」：代理池是**多行资源**（几十上百个
 * 出口、各有权重与运行态），塞进扁平的键值设置里既放不下也用不了。这和通知渠道
 * 独立成页是同一个理由。
 *
 * 页面上刻意把「连败」与「失败占比」并排显示：连败为 0 不代表出口健康——一个
 * 75% 请求失败、但每隔三次能成功一次的出口连败永远到不了熔断阈值，却拖慢了大部分
 * 扫描请求。只显示连败会让人误以为这类出口是好的。
 *
 * 保存即热应用：不用重启，下一次 agent 请求就按新配置选出口。
 */
"use client";

import * as React from "react";

import {
  AlertTriangleIcon,
  CheckCircle2Icon,
  ClipboardPasteIcon,
  CloudDownloadIcon,
  FlaskConicalIcon,
  PlusIcon,
  RefreshCwIcon,
  SaveIcon,
  ShieldCheckIcon,
  Trash2Icon,
  XCircleIcon,
} from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { ProxyPoolCheckResult, ProxyPoolEntry, ProxyPoolRunning, ProxyPoolState } from "@/lib/api/proxypool";

// STRATEGY_LABEL 给策略一个人话名字——只显示 round_robin 这种 key，用户没法选。
const STRATEGY_LABEL: Record<string, { label: string; hint: string }> = {
  round_robin: { label: "轮询（按权重）", hint: "每个请求轮换到下一个出口；权重大的出现更频繁。" },
  sticky_host: { label: "按目标粘性（固定散列）", hint: "同一目标始终走同一出口，像真实用户。目标多时分布均匀。" },
  sticky_host_random: { label: "按目标粘性（首次随机）", hint: "同一目标固定出口，但首次是随机挑的——避免固定散列在少数目标上扎堆。" },
  random: { label: "随机", hint: "每个请求随机挑一个出口。" },
};

// Suggestion 把「这条代理现在到底好不好」翻译成一句人话，避免让用户自己比对三个数字。
function verdict(r: ProxyPoolRunning): { text: string; cls: string } {
  if (r.break_remaining && r.break_remaining > 0) {
    const m = Math.ceil(r.break_remaining / 60);
    return { text: `熔断中（剩 ${m} 分钟）`, cls: "text-destructive" };
  }
  if ((r.samples ?? 0) >= 3 && (r.fail_ratio ?? 0) > 0.5) {
    return { text: `失败率 ${Math.round((r.fail_ratio ?? 0) * 100)}%（已降级）`, cls: "text-amber-600" };
  }
  if ((r.samples ?? 0) >= 3) {
    return { text: "正常", cls: "text-emerald-600" };
  }
  return { text: "样本不足", cls: "text-muted-foreground" };
}

export default function ProxyPoolPage() {
  const [state, setState] = React.useState<ProxyPoolState | null>(null);
  const [entries, setEntries] = React.useState<ProxyPoolEntry[]>([]);
  const [strategy, setStrategy] = React.useState("round_robin");
  const [loading, setLoading] = React.useState(true);
  const [saving, setSaving] = React.useState(false);
  const [checking, setChecking] = React.useState(false);
  const [results, setResults] = React.useState<ProxyPoolCheckResult[]>([]);

  const load = React.useCallback(() => {
    setLoading(true);
    api
      .proxyPoolGet()
      .then((s) => {
        setState(s);
        setEntries(s.entries ?? []);
        setStrategy(s.strategy || "round_robin");
      })
      .catch((e) => toast.error((e as Error).message))
      .finally(() => setLoading(false));
  }, []);

  React.useEffect(() => {
    load();
  }, [load]);

  function setEntry(i: number, patch: Partial<ProxyPoolEntry>) {
    setEntries((prev) => prev.map((e, j) => (j === i ? { ...e, ...patch } : e)));
  }

  function addEntry() {
    setEntries((prev) => [...prev, { url: "", name: "", enabled: true, weight: 1 }]);
  }

  function removeEntry(i: number) {
    setEntries((prev) => prev.filter((_, j) => j !== i));
  }

  async function save() {
    // 空地址的条目直接丢掉，而不是拦下来报错——用户点了「+」又没填是常态。
    const clean = entries.filter((e) => e.url.trim() !== "");
    setSaving(true);
    try {
      const s = await api.proxyPoolPut({ entries: clean, strategy });
      setState(s);
      setEntries(s.entries ?? []);
      toast.success(clean.length > 0 ? `已保存 ${clean.length} 个出口，立即生效` : "已清空代理池（回退全局代理/直连）");
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setSaving(false);
    }
  }

  async function runCheck() {
    setChecking(true);
    try {
      const r = await api.proxyPoolCheck({});
      setResults(r.results ?? []);
      const ok = (r.results ?? []).filter((x) => x.ok).length;
      toast.success(`验证完成：${ok}/${r.results?.length ?? 0} 可用（目标 ${r.target}）`);
      load();
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setChecking(false);
    }
  }

  const runningByName = React.useMemo(() => {
    const m = new Map<string, ProxyPoolRunning>();
    for (const r of state?.running ?? []) {
      m.set(r.url, r);
    }
    return m;
  }, [state]);

  const enabledCount = entries.filter((e) => e.enabled && e.url.trim() !== "").length;

  return (
    <div className="flex flex-1 flex-col gap-4 md:gap-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold tracking-tight">代理池</h1>
          <p className="text-muted-foreground text-sm">
            agent 执行的所有扫描请求（curl / nmap / python 脚本 / WebFetch）都会按策略从池里选出口。
            agent 侧无需任何操作——只要池非空，出口选择自动生效。
          </p>
        </div>
        <div className="flex items-center gap-2">
          {state && (
            <Badge variant={state.capturing ? "secondary" : "outline"}>
              {state.capturing ? "流量录制中（经代理池转发）" : "录制关闭（池仍经全局代理路径生效）"}
            </Badge>
          )}
        </div>
      </div>

      <EntriesCard
        entries={entries}
        runningByName={runningByName}
        enabledCount={enabledCount}
        loading={loading}
        saving={saving}
        onSet={setEntry}
        onAdd={addEntry}
        onRemove={removeEntry}
        onSave={save}
        onReload={load}
        onBulk={(list) => setEntries(list)}
        onFetch={(list) => setEntries(list)}
      />

      <HealthCard
        state={state}
        strategy={strategy}
        onStrategy={setStrategy}
        checking={checking}
        results={results}
        onCheck={runCheck}
      />
    </div>
  );
}

// EntriesCard 是条目列表与编辑。
function EntriesCard({
  entries,
  runningByName,
  enabledCount,
  loading,
  saving,
  onSet,
  onAdd,
  onRemove,
  onSave,
  onReload,
  onBulk,
  onFetch,
}: {
  entries: ProxyPoolEntry[];
  runningByName: Map<string, ProxyPoolRunning>;
  enabledCount: number;
  loading: boolean;
  saving: boolean;
  onSet: (i: number, patch: Partial<ProxyPoolEntry>) => void;
  onAdd: () => void;
  onRemove: (i: number) => void;
  onSave: () => void;
  onReload: () => void;
  onBulk: (list: ProxyPoolEntry[]) => void;
  onFetch: (list: ProxyPoolEntry[]) => void;
}) {
  const [bulkOpen, setBulkOpen] = React.useState(false);
  const [fetchOpen, setFetchOpen] = React.useState(false);

  return (
    <Card>
      <CardHeader className="flex-row items-start justify-between">
        <div>
          <CardTitle className="text-base">上游出口</CardTitle>
          <CardDescription>
            共 {entries.length} 条，启用 {enabledCount} 条。支持 http / https / socks5，可带 user:pass。
          </CardDescription>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button size="sm" variant="outline" onClick={onReload} disabled={loading}>
            <RefreshCwIcon /> 刷新
          </Button>
          <Button size="sm" variant="outline" onClick={() => setFetchOpen(true)}>
            <CloudDownloadIcon /> 自动抓取
          </Button>
          <Button size="sm" variant="outline" onClick={() => setBulkOpen(true)}>
            <ClipboardPasteIcon /> 批量导入
          </Button>
          <Button size="sm" variant="outline" onClick={onAdd}>
            <PlusIcon /> 添加
          </Button>
          <Button size="sm" onClick={onSave} disabled={saving}>
            <SaveIcon /> {saving ? "保存中…" : "保存并生效"}
          </Button>
        </div>
      </CardHeader>
      <CardContent>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="w-16">启用</TableHead>
              <TableHead className="w-36">名称</TableHead>
              <TableHead>地址</TableHead>
              <TableHead className="w-20">权重</TableHead>
              <TableHead className="w-32">状态</TableHead>
              <TableHead className="w-16" />
            </TableRow>
          </TableHeader>
          <TableBody>
            {entries.length === 0 && (
              <TableRow>
                <TableCell colSpan={6} className="h-24 text-center text-muted-foreground text-sm">
                  {loading ? "加载中…" : "代理池为空。为空时回退到「系统配置」里的全局代理或直连。"}
                </TableCell>
              </TableRow>
            )}
            {entries.map((e, i) => {
              const rt = runningByName.get(e.url);
              const v = rt ? verdict(rt) : null;
              return (
                <TableRow key={i}>
                  <TableCell>
                    <Switch checked={e.enabled} onCheckedChange={(v) => onSet(i, { enabled: v })} />
                  </TableCell>
                  <TableCell>
                    <Input
                      value={e.name}
                      onChange={(ev) => onSet(i, { name: ev.target.value })}
                      placeholder="选填"
                      className="h-8 text-xs"
                    />
                  </TableCell>
                  <TableCell>
                    <Input
                      value={e.url}
                      onChange={(ev) => onSet(i, { url: ev.target.value })}
                      placeholder="http://1.2.3.4:8080 或 socks5://user:pass@1.2.3.4:1080"
                      className="h-8 font-mono text-xs"
                    />
                  </TableCell>
                  <TableCell>
                    <Input
                      type="number"
                      min={1}
                      value={e.weight}
                      onChange={(ev) => onSet(i, { weight: Math.max(1, Number(ev.target.value) || 1) })}
                      className="h-8 w-16 text-xs"
                    />
                  </TableCell>
                  <TableCell className="text-xs">
                    {v ? (
                      <span className={v.cls} title={`连败 ${rt?.failures ?? 0} · 样本 ${rt?.samples ?? 0}`}>
                        {v.text}
                      </span>
                    ) : (
                      <span className="text-muted-foreground">—</span>
                    )}
                    {rt && (rt.samples ?? 0) > 0 && (
                      <div className="text-muted-foreground tabular-nums">
                        失败率 {Math.round((rt.fail_ratio ?? 0) * 100)}% · {rt.samples} 次
                      </div>
                    )}
                  </TableCell>
                  <TableCell>
                    <Button size="sm" variant="ghost" onClick={() => onRemove(i)}>
                      <Trash2Icon />
                    </Button>
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      </CardContent>

      {bulkOpen && <BulkDialog onClose={() => setBulkOpen(false)} onDone={onBulk} />}
      {fetchOpen && <FetchDialog onClose={() => setFetchOpen(false)} onDone={onFetch} />}
    </Card>
  );
}

// BulkDialog 是批量导入对话框：支持常见付费代理商的导出格式。
// FetchDialog 从公开清单源自动抓取代理。
//
// 为什么要「验证」这个选项：公开清单的可用率通常低于 10%，不验证直接入池会让
// 每条扫描请求先超时一次才回退——比池子空着还糟。默认开验证，关掉只用于调试。
function FetchDialog({ onClose, onDone }: { onClose: () => void; onDone: (list: ProxyPoolEntry[]) => void }) {
  const [verify, setVerify] = React.useState(true);
  const [max, setMax] = React.useState(100);
  const [busy, setBusy] = React.useState(false);
  const [result, setResult] = React.useState<{
    fetched: number;
    alive: number;
    added: number;
    total: number;
    sources: { url: string; count: number; error?: string }[];
  } | null>(null);

  async function run() {
    setBusy(true);
    setResult(null);
    try {
      const r = await api.proxyPoolFetch({ verify, max });
      setResult(r);
      // 后端已经把新条目写进池，回读一遍覆盖编辑器——前端不自己拼状态。
      const st = await api.proxyPoolGet();
      onDone(st.entries ?? []);
      toast.success(`抓取完成：候选 ${r.fetched}，可用 ${r.alive}，新增 ${r.added}`);
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="flex flex-col gap-4 rounded-md border p-4">
      <div>
        <div className="font-medium text-sm">从公开清单自动抓取</div>
        <p className="text-muted-foreground mt-1 text-xs">
          并发拉取内置的五个公开代理清单（TheSpeedX / clarketm / monosans），去重后逐条验证连通性，
          只把真的能用的并入池。短效代理会陆续失效，定期执行即可保持池子可用。
        </p>
      </div>

      <div className="flex flex-wrap items-end gap-4">
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={verify} onChange={(e) => setVerify(e.target.checked)} />
          验证连通性后再入库
        </label>
        <div className="flex flex-col gap-1">
          <Label htmlFor="fp-max">最多入库</Label>
          <Input
            id="fp-max"
            type="number"
            min={1}
            max={2000}
            value={max}
            onChange={(e) => setMax(Math.max(1, Number(e.target.value) || 100))}
            className="h-8 w-24 text-xs"
          />
        </div>
        <div className="ml-auto flex gap-2">
          <Button variant="outline" onClick={onClose}>
            关闭
          </Button>
          <Button onClick={run} disabled={busy}>
            {busy ? "抓取中…" : "开始抓取"}
          </Button>
        </div>
      </div>

      {busy && (
        <p className="text-muted-foreground text-xs">
          正在拉取清单并并发验证。公开代理可用率通常低于 10%，验证这一步会花掉大部分时间。
        </p>
      )}

      {result && (
        <div className="flex flex-col gap-2 rounded-md border bg-muted/30 p-3 text-xs">
          <div className="flex flex-wrap gap-x-4 gap-y-1">
            <span>
              候选 <b className="tabular-nums">{result.fetched}</b> 条
            </span>
            <span>
              可用 <b className="tabular-nums text-emerald-600">{result.alive}</b> 条
            </span>
            <span>
              新增入库 <b className="tabular-nums">{result.added}</b> 条
            </span>
            <span>
              池内共 <b className="tabular-nums">{result.total}</b> 条
            </span>
          </div>
          <div className="text-muted-foreground">
            {result.sources.map((src) => (
              <div key={src.url} className="flex items-center gap-2">
                <span className={src.error ? "text-destructive" : "text-emerald-600"}>
                  {src.error ? "✗" : "✓"}
                </span>
                <span className="truncate" title={src.url}>
                  {src.url.replace("https://raw.githubusercontent.com/", "")}
                </span>
                <span className="ml-auto tabular-nums">{src.error ?? `${src.count} 条`}</span>
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}

function BulkDialog({ onClose, onDone }: { onClose: () => void; onDone: (list: ProxyPoolEntry[]) => void }) {
  const [text, setText] = React.useState("");
  const [mode, setMode] = React.useState<"append" | "replace">("append");
  const [busy, setBusy] = React.useState(false);
  const [invalid, setInvalid] = React.useState<{ line: number; raw: string; reason: string }[]>([]);

  async function run() {
    setBusy(true);
    try {
      const r = await api.proxyPoolBulk({ text, mode });
      setInvalid(r.invalid ?? []);
      // 导入后回读整池覆盖编辑器内容——后端是唯一真相，前端不该自己拼。
      const s = await api.proxyPoolGet();
      onDone(s.entries ?? []);
      if ((r.invalid ?? []).length === 0) {
        toast.success(`导入完成：新增 ${r.added}，更新 ${r.updated}，共 ${r.total} 条`);
        onClose();
      } else {
        toast.warning(`导入完成但有 ${r.invalid.length} 行无法识别，见下方明细`);
      }
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="flex flex-col gap-3 rounded-md border p-4">
      <div>
        <Label>粘贴代理列表（一行一个）</Label>
        <p className="text-muted-foreground mt-1 text-xs">
          支持 <code>1.2.3.4:8080</code>、<code>http://1.2.3.4:8080</code>、
          <code>socks5://user:pass@1.2.3.4:1080</code>、<code>1.2.3.4:8080:user:pass</code>、
          <code>名字 | 地址</code>；<code>#</code> 开头为注释。省略协议按 http 处理。
        </p>
      </div>
      <Textarea
        value={text}
        onChange={(e) => setText(e.target.value)}
        rows={10}
        className="font-mono text-xs"
        placeholder={"1.2.3.4:8080\nsocks5://user:pass@5.6.7.8:1080\n香港节点 | 9.9.9.9:3128"}
      />
      <div className="flex flex-wrap items-center gap-3">
        <Label>导入方式</Label>
        <select
          className="h-9 rounded-md border bg-background px-2 text-sm"
          value={mode}
          onChange={(e) => setMode(e.target.value as "append" | "replace")}
        >
          <option value="append">追加（保留现有条目）</option>
          <option value="replace">整体替换</option>
        </select>
        <div className="ml-auto flex gap-2">
          <Button variant="outline" onClick={onClose}>
            关闭
          </Button>
          <Button onClick={run} disabled={busy || !text.trim()}>
            {busy ? "导入中…" : "导入"}
          </Button>
        </div>
      </div>
      {invalid.length > 0 && (
        <div className="rounded-md border border-destructive/40 bg-destructive/5 p-3">
          <div className="mb-1 font-medium text-destructive text-xs">以下行无法识别：</div>
          <ul className="flex flex-col gap-0.5 text-xs">
            {invalid.map((x) => (
              <li key={x.line}>
                第 {x.line} 行 <code className="text-muted-foreground">{x.raw}</code> —— {x.reason}
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}

// HealthCard 是策略、健康检查参数与连通性验证。
function HealthCard({
  state,
  strategy,
  onStrategy,
  checking,
  results,
  onCheck,
}: {
  state: ProxyPoolState | null;
  strategy: string;
  onStrategy: (s: string) => void;
  checking: boolean;
  results: ProxyPoolCheckResult[];
  onCheck: () => void;
}) {
  return (
    <Card>
      <CardHeader className="flex-row items-start justify-between">
        <div>
          <CardTitle className="flex items-center gap-2 text-base">
            <ShieldCheckIcon className="size-4" /> 选择策略与健康检查
          </CardTitle>
          <CardDescription>
            策略在保存时生效。主动健康检查会定时验证所有启用出口，不通的立刻熔断——
            避免一个已知挂掉的代理还要再试几次才被淘汰。
          </CardDescription>
        </div>
        <Button size="sm" variant="outline" onClick={onCheck} disabled={checking}>
          <FlaskConicalIcon /> {checking ? "验证中…" : "立即验证全部"}
        </Button>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <div className="flex flex-col gap-2">
          <Label>选择策略</Label>
          {Object.entries(STRATEGY_LABEL).map(([key, meta]) => (
            <label
              key={key}
              className={`flex cursor-pointer items-start gap-2 rounded-md border p-2 ${
                strategy === key ? "border-foreground/40 bg-muted/40" : ""
              }`}
            >
              <input
                type="radio"
                name="strategy"
                value={key}
                checked={strategy === key}
                onChange={() => onStrategy(key)}
                className="mt-1"
              />
              <span>
                <span className="text-sm">{meta.label}</span>
                <span className="text-muted-foreground block text-xs">{meta.hint}</span>
              </span>
            </label>
          ))}
          <p className="text-muted-foreground text-xs">改完记得点上方「保存并生效」。</p>
        </div>

        {state && (
          <div className="text-muted-foreground flex flex-wrap gap-x-6 gap-y-1 text-xs">
            <span>验证目标：{state.check_target || "—"}</span>
            <span>主动检查间隔：{state.health_minutes ?? 0} 分钟（0 = 关闭）</span>
            <span>熔断冷却：{state.health_cooldown ?? 0} 分钟</span>
            {state.last_check && (
              <span>
                上次检查：{state.last_check.at} · {state.last_check.ok_count}/{state.last_check.count} 可用
                {state.last_check.auto ? "（自动）" : ""}
              </span>
            )}
          </div>
        )}

        {results.length > 0 && (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="w-16">结果</TableHead>
                <TableHead>地址</TableHead>
                <TableHead className="w-24">延迟</TableHead>
                <TableHead className="w-40">出口 IP</TableHead>
                <TableHead>错误</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {results.map((r) => (
                <TableRow key={`${r.index}-${r.url}`}>
                  <TableCell>
                    {r.ok ? (
                      <CheckCircle2Icon className="size-4 text-emerald-600" />
                    ) : (
                      <XCircleIcon className="size-4 text-destructive" />
                    )}
                  </TableCell>
                  <TableCell className="font-mono text-xs">{r.url}</TableCell>
                  <TableCell className="text-xs tabular-nums">{r.ok ? `${r.latency_ms} ms` : "—"}</TableCell>
                  <TableCell className="font-mono text-xs">{r.exit_ip || "—"}</TableCell>
                  <TableCell className="text-destructive text-xs">{r.error || ""}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}

        {results.length === 0 && (
          <div className="flex items-center gap-2 text-muted-foreground text-xs">
            <AlertTriangleIcon className="size-3" />
            点「立即验证全部」可以确认每个出口真的能连通、并看到它对外呈现的 IP。
          </div>
        )}
      </CardContent>
    </Card>
  );
}