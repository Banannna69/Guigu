/**
 * page.tsx：审计日志页——按类型/结果/关键字筛选，展示可追溯的操作记录。
 * 主要函数/类型：
 *   - AuditPage：默认导出页组件
 *   - fmtTime：时间戳格式化
 */
"use client";

import * as React from "react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { api } from "@/lib/api";

const PAGE_SIZE = 50;

function fmtTime(ts: string) {
  const d = new Date(ts);
  if (isNaN(d.getTime())) return ts;
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

export default function AuditPage() {
  const [page, setPage] = React.useState(0);
  const [category, setCategory] = React.useState("");
  const [action, setAction] = React.useState("");
  const [result, setResult] = React.useState("");
  const [entries, setEntries] = React.useState<Awaited<ReturnType<typeof api.auditLogs>>["entries"]>([]);
  const [total, setTotal] = React.useState(0);
  const [error, setError] = React.useState("");
  const [busy, setBusy] = React.useState(false);

  const load = React.useCallback(
    (nextPage: number) => {
      setBusy(true);
      setError("");
      api
        .auditLogs({
          category: category || undefined,
          action: action || undefined,
          result: result || undefined,
          limit: PAGE_SIZE,
          offset: nextPage * PAGE_SIZE,
        })
        .then((r) => {
          setEntries(r.entries);
          setTotal(r.total);
          setPage(nextPage);
        })
        .catch((e) => setError((e as Error).message))
        .finally(() => setBusy(false));
    },
    [category, action, result],
  );

  React.useEffect(() => {
    load(0);
  }, [load]);

  const pages = Math.max(1, Math.ceil(total / PAGE_SIZE));

  return (
    <div className="flex min-w-0 flex-1 flex-col gap-4 p-4 sm:p-6">
      <div>
        <h1 className="text-lg font-semibold">审计日志</h1>
        <p className="text-muted-foreground text-sm">
          记录登录、任务删除、补录、资产同步、配置变更等关键操作。字段超长会截断，正文请到对应业务页查看。
        </p>
      </div>

      <div className="flex flex-wrap items-end gap-3 rounded-md border p-3">
        <label className="flex flex-col gap-1 text-xs">
          类型
          <select
            className="h-8 rounded-md border bg-transparent px-2 text-sm"
            value={category}
            onChange={(e) => setCategory(e.target.value)}
          >
            <option value="">全部</option>
            <option value="auth">登录</option>
            <option value="task">任务</option>
            <option value="asset">资产/同步</option>
            <option value="settings">配置</option>
            <option value="intercept">审批</option>
          </select>
        </label>
        <label className="flex flex-col gap-1 text-xs">
          结果
          <select
            className="h-8 rounded-md border bg-transparent px-2 text-sm"
            value={result}
            onChange={(e) => setResult(e.target.value)}
          >
            <option value="">全部</option>
            <option value="success">成功</option>
            <option value="fail">失败</option>
          </select>
        </label>
        <label className="flex flex-col gap-1 text-xs">
          动作（精确）
          <Input
            className="h-8 w-44"
            value={action}
            onChange={(e) => setAction(e.target.value)}
            placeholder="如 delete_task"
          />
        </label>
        <Button variant="outline" size="sm" onClick={() => load(0)} disabled={busy}>
          刷新
        </Button>
        <span className="text-muted-foreground ml-auto text-xs">共 {total} 条</span>
      </div>

      {error ? <p className="text-red-600 text-sm">读取失败：{error}</p> : null}

      <div className="overflow-x-auto rounded-md border">
        <table className="w-full text-sm">
          <thead className="bg-muted/40">
            <tr>
              {["时间", "类型", "动作", "操作者", "结果", "IP", "消息"].map((h) => (
                <th key={h} className="whitespace-nowrap px-3 py-2 text-left font-medium">
                  {h}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {entries.length === 0 ? (
              <tr>
                <td className="text-muted-foreground px-3 py-4 text-center" colSpan={7}>
                  {busy ? "加载中…" : "没有符合条件的记录"}
                </td>
              </tr>
            ) : (
              entries.map((e) => (
                <tr key={e.id} className="border-t">
                  <td className="text-muted-foreground whitespace-nowrap px-3 py-2 font-mono text-xs">
                    {fmtTime(e.created_at)}
                  </td>
                  <td className="px-3 py-2 text-xs">{e.category}</td>
                  <td className="whitespace-nowrap px-3 py-2 font-mono text-xs">{e.action}</td>
                  <td className="px-3 py-2 text-xs">{e.actor || "—"}</td>
                  <td className="px-3 py-2 text-xs">
                    <span
                      className={
                        e.result === "success"
                          ? "rounded bg-emerald-500/10 px-1.5 py-0.5 text-emerald-700 dark:text-emerald-400"
                          : "rounded bg-red-500/10 px-1.5 py-0.5 text-red-700 dark:text-red-400"
                      }
                    >
                      {e.result}
                    </span>
                  </td>
                  <td className="text-muted-foreground px-3 py-2 font-mono text-xs">{e.ip || "—"}</td>
                  <td className="px-3 py-2 text-xs">{e.message}</td>
                </tr>
              ))
            )}
          </tbody>
        </table>
      </div>

      <div className="flex items-center justify-between">
        <span className="text-muted-foreground text-xs">
          第 {page + 1} / {pages} 页
        </span>
        <div className="flex gap-2">
          <Button variant="outline" size="sm" disabled={busy || page <= 0} onClick={() => load(page - 1)}>
            上一页
          </Button>
          <Button variant="outline" size="sm" disabled={busy || page + 1 >= pages} onClick={() => load(page + 1)}>
            下一页
          </Button>
        </div>
      </div>
    </div>
  );
}
