/**
 * page.tsx：资产同步页——FOFA 数据源配置、检索预览、导入资产并归入合集。
 * 主要函数/类型：
 *   - AssetsSyncPage：默认导出页组件
 *   - FofaPanel：数据源 + 检索 + 导入
 *   - CollectionsPanel：合集列表与删除
 */
"use client";

import * as React from "react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { api } from "@/lib/api";
import type { Collection, FofaRow, FofaStatus, FofaSyncResult } from "@/lib/api/asset";

const PREVIEW_MAX = 100;
const IMPORT_MAX = 5000;

export default function AssetsSyncPage() {
  return (
    <div className="flex min-w-0 flex-1 flex-col gap-4 p-4 sm:p-6">
      <div>
        <h1 className="text-lg font-semibold">资产同步</h1>
        <p className="text-muted-foreground text-sm">
          FOFA 是网络空间测绘搜索引擎。填 email + API key 后用 FOFA 语法检索，预览结果后导入资产图；
          每次导入会归入一个以查询命名的「合集」，新建任务时可选它为测试目标。
        </p>
      </div>
      <FofaPanel />
      <CollectionsPanel />
    </div>
  );
}

function FofaPanel() {
  const [status, setStatus] = React.useState<FofaStatus | null>(null);
  const [email, setEmail] = React.useState("");
  const [apiKey, setApiKey] = React.useState("");
  const [query, setQuery] = React.useState("");
  const [size, setSize] = React.useState("100");
  const [collectionName, setCollectionName] = React.useState("");
  const [rows, setRows] = React.useState<FofaRow[]>([]);
  const [total, setTotal] = React.useState(0);
  const [result, setResult] = React.useState<FofaSyncResult | null>(null);
  const [msg, setMsg] = React.useState("");
  const [err, setErr] = React.useState("");
  const [busy, setBusy] = React.useState(false);

  const loadStatus = React.useCallback(() => {
    api
      .fofaStatus()
      .then((s) => {
        setStatus(s);
        setEmail(s.email || "");
      })
      .catch((e) => setErr((e as Error).message));
  }, []);

  React.useEffect(() => {
    loadStatus();
  }, [loadStatus]);

  const saveCreds = async () => {
    setBusy(true);
    setErr("");
    setMsg("");
    try {
      await api.fofaDatasource({ email: email.trim(), key: apiKey.trim() });
      setApiKey(""); // 密钥永不回显
      setMsg("已保存 FOFA 账号");
      loadStatus();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const preview = async () => {
    if (!query.trim()) return setErr("请填写 FOFA 查询语句");
    setBusy(true);
    setErr("");
    setMsg("");
    try {
      const n = Math.max(1, Math.min(PREVIEW_MAX, Math.floor(Number(size) || PREVIEW_MAX)));
      const r = await api.fofaQuery({ query: query.trim(), size: n });
      setRows(r.rows);
      setTotal(r.total);
      setMsg(`预览 ${r.rows.length} 条（FOFA 报告总数 ${r.total}）`);
    } catch (e) {
      setErr((e as Error).message);
      setRows([]);
    } finally {
      setBusy(false);
    }
  };

  const runSync = async () => {
    if (!query.trim()) return setErr("请填写 FOFA 查询语句");
    setBusy(true);
    setErr("");
    setMsg("");
    try {
      const target = Math.max(1, Math.min(IMPORT_MAX, Math.floor(Number(size) || 100)));
      const r = await api.fofaSync({
        query: query.trim(),
        size: target,
        collection_name: collectionName.trim() || undefined,
      });
      setResult(r);
      setMsg(
        `导入 ${r.imported} 条` +
          (r.truncated ? "（结果被截断，可调大条数）" : "") +
          (r.collection ? `，已归入合集「${r.collection.name}」` : ""),
      );
      loadStatus();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="flex flex-col gap-3 rounded-md border p-4">
      <h2 className="text-sm font-semibold">FOFA 数据源</h2>

      <div className="flex flex-wrap items-end gap-3">
        <label className="flex flex-col gap-1 text-xs">
          Email（可选）
          <Input className="h-8 w-56" value={email} onChange={(e) => setEmail(e.target.value)} />
        </label>
        <label className="flex flex-col gap-1 text-xs">
          API Key
          <Input
            className="h-8 w-56"
            type="password"
            value={apiKey}
            onChange={(e) => setApiKey(e.target.value)}
            placeholder={status?.configured ? "已配置，留空表示不修改" : "FOFA API Key"}
          />
        </label>
        <Button variant="outline" size="sm" onClick={saveCreds} disabled={busy}>
          保存账号
        </Button>
        <span className="text-muted-foreground text-xs">
          {status?.configured
            ? status.reachable
              ? "已配置 · 可达"
              : "已配置 · 不可达"
            : "未配置"}
        </span>
      </div>

      <div className="flex flex-wrap items-end gap-3 border-t pt-3">
        <label className="flex min-w-0 flex-1 flex-col gap-1 text-xs">
          查询语句（FOFA 语法）
          <Input
            className="h-8"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder='例如 app="Apache" && country="CN"'
          />
        </label>
        <label className="flex flex-col gap-1 text-xs">
          条数
          <Input className="h-8 w-24" value={size} onChange={(e) => setSize(e.target.value)} />
        </label>
        <label className="flex flex-col gap-1 text-xs">
          合集名（可选）
          <Input
            className="h-8 w-48"
            value={collectionName}
            onChange={(e) => setCollectionName(e.target.value)}
            placeholder="默认用查询语法"
          />
        </label>
        <Button variant="outline" size="sm" onClick={preview} disabled={busy}>
          预览
        </Button>
        <Button size="sm" onClick={runSync} disabled={busy}>
          导入
        </Button>
      </div>

      <p className="text-muted-foreground text-xs">
        预览最多 {PREVIEW_MAX} 条且不写库；导入最多 {IMPORT_MAX} 条并写入资产图。
      </p>

      {msg ? <p className="text-sm">{msg}</p> : null}
      {err ? <p className="text-red-600 text-sm">失败：{err}</p> : null}

      {rows.length > 0 ? (
        <div className="max-h-80 overflow-auto rounded-md border">
          <table className="w-full text-xs">
            <thead className="bg-muted/40">
              <tr>
                {["Host", "IP", "端口", "协议", "域名", "标题"].map((h) => (
                  <th key={h} className="whitespace-nowrap px-2 py-1.5 text-left font-medium">
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {rows.map((r, i) => (
                <tr key={`${r.host}-${i}`} className="border-t">
                  <td className="px-2 py-1 font-mono">{r.host}</td>
                  <td className="px-2 py-1 font-mono">{r.ip}</td>
                  <td className="px-2 py-1 font-mono">{r.port}</td>
                  <td className="px-2 py-1">{r.protocol}</td>
                  <td className="px-2 py-1 font-mono">{r.domain}</td>
                  <td className="max-w-xs truncate px-2 py-1">{r.title}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : null}

      {result ? (
        <div className="text-muted-foreground flex flex-wrap gap-3 text-xs">
          {Object.entries(result.synced || {}).map(([k, v]) => (
            <span key={k}>
              {k}: {v}
            </span>
          ))}
          <span>合计 FOFA 报告 {result.total} 条</span>
        </div>
      ) : null}

      {result?.errors && result.errors.length > 0 ? (
        <ul className="text-red-600 list-inside list-disc text-xs">
          {result.errors.slice(0, 10).map((e, i) => (
            <li key={i}>{e}</li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}

function CollectionsPanel() {
  const [cols, setCols] = React.useState<Collection[]>([]);
  const [err, setErr] = React.useState("");
  const [busy, setBusy] = React.useState(false);

  const load = React.useCallback(() => {
    setBusy(true);
    api
      .collections()
      .then(setCols)
      .catch((e) => setErr((e as Error).message))
      .finally(() => setBusy(false));
  }, []);

  React.useEffect(() => {
    load();
  }, [load]);

  const remove = async (c: Collection) => {
    if (!window.confirm(`删除合集「${c.name}」？\n\n只删除分组与引用它的任务目标，不删除资产本身。`)) return;
    try {
      await api.deleteCollection(c.id);
      load();
    } catch (e) {
      setErr((e as Error).message);
    }
  };

  return (
    <div className="flex flex-col gap-3 rounded-md border p-4">
      <div className="flex items-center justify-between">
        <h2 className="text-sm font-semibold">资产合集</h2>
        <Button variant="outline" size="sm" onClick={load} disabled={busy}>
          刷新
        </Button>
      </div>
      {err ? <p className="text-red-600 text-sm">读取失败：{err}</p> : null}
      {cols.length === 0 ? (
        <p className="text-muted-foreground text-sm">还没有合集。用上方 FOFA 导入后会自动生成。</p>
      ) : (
        <div className="rounded-md border">
          <table className="w-full text-sm">
            <thead className="bg-muted/40">
              <tr>
                {["名称", "查询", "资产数", ""].map((h) => (
                  <th key={h} className="px-3 py-2 text-left font-medium">
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {cols.map((c) => (
                <tr key={c.id} className="border-t">
                  <td className="px-3 py-2">{c.name}</td>
                  <td className="text-muted-foreground max-w-xs truncate px-3 py-2 font-mono text-xs">
                    {c.query || "—"}
                  </td>
                  <td className="px-3 py-2 tabular-nums">{c.member_count}</td>
                  <td className="px-3 py-2 text-right">
                    <Button variant="ghost" size="sm" onClick={() => remove(c)}>
                      删除
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
