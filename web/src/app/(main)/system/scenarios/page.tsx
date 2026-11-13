/**
 * page.tsx：场景包管理页——内置与自建场景的列表、编辑、新建、删除。
 * 主要函数/类型：
 *   - ScenariosPage：默认导出页组件
 *   - PackEditor：「追加正文 / 技能 / 禁用工具」的编辑表单
 *
 * 页面上反复强调的是「追加不替换」：这是本功能与「分类带出模板」最重要的一处
 * 差异，也是用户最可能误解的地方。
 */
"use client";

import * as React from "react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { api } from "@/lib/api";
import type { ScenarioPack, ScenarioPackInput } from "@/lib/api/asset";

const EMPTY: ScenarioPackInput = {
  name: "",
  description: "",
  planner_append: "",
  worker_append: "",
  skills: [],
  disabled_tools: [],
};

export default function ScenariosPage() {
  const [packs, setPacks] = React.useState<ScenarioPack[]>([]);
  const [editing, setEditing] = React.useState<ScenarioPack | "new" | null>(null);
  const [err, setErr] = React.useState("");
  const [busy, setBusy] = React.useState(false);

  const load = React.useCallback(() => {
    setBusy(true);
    api
      .scenarios()
      .then(setPacks)
      .catch((e) => setErr((e as Error).message))
      .finally(() => setBusy(false));
  }, []);

  React.useEffect(() => {
    load();
  }, [load]);

  const remove = async (p: ScenarioPack) => {
    if (!window.confirm(`删除场景包「${p.name}」？\n\n已创建的任务会保留建任务时的快照，不受影响。`)) return;
    try {
      await api.deleteScenario(p.id);
      load();
    } catch (e) {
      setErr((e as Error).message);
    }
  };

  return (
    <div className="flex min-w-0 flex-1 flex-col gap-4 p-4 sm:p-6">
      <div>
        <h1 className="text-lg font-semibold">场景包</h1>
        <p className="text-muted-foreground text-sm">
          场景包是<strong>行业增强层</strong>：勾选后会在通用的规划/执行规则之后追加行业专属的优先级与红线，
          <strong>不替换</strong>通用能力；不勾选则行为与通用任务完全一致。多个场景可叠加使用。
        </p>
      </div>

      {err ? <p className="text-red-600 text-sm">读取失败：{err}</p> : null}

      <div className="flex items-center gap-2">
        <Button size="sm" onClick={() => setEditing("new")}>
          新建场景包
        </Button>
        <Button variant="outline" size="sm" onClick={load} disabled={busy}>
          刷新
        </Button>
      </div>

      <div className="rounded-md border">
        <table className="w-full text-sm">
          <thead className="bg-muted/40">
            <tr>
              {["名称", "说明", "启用技能", "禁用工具", ""].map((h) => (
                <th key={h} className="px-3 py-2 text-left font-medium">
                  {h}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {packs.length === 0 ? (
              <tr>
                <td className="text-muted-foreground px-3 py-4 text-center" colSpan={5}>
                  {busy ? "加载中…" : "还没有场景包"}
                </td>
              </tr>
            ) : (
              packs.map((p) => (
                <tr key={p.id} className="border-t">
                  <td className="px-3 py-2">
                    <span className="font-medium">{p.name}</span>
                    {p.builtin ? (
                      <span className="ml-2 rounded bg-muted px-1.5 py-0.5 text-xs">内置</span>
                    ) : null}
                  </td>
                  <td className="text-muted-foreground max-w-md px-3 py-2 text-xs">{p.description || "—"}</td>
                  <td className="px-3 py-2 font-mono text-xs">{p.skills.join("、") || "—"}</td>
                  <td className="px-3 py-2 font-mono text-xs">{p.disabled_tools.join("、") || "—"}</td>
                  <td className="whitespace-nowrap px-3 py-2 text-right">
                    <Button variant="ghost" size="sm" onClick={() => setEditing(p)}>
                      编辑
                    </Button>
                    <Button variant="ghost" size="sm" onClick={() => remove(p)}>
                      删除
                    </Button>
                  </td>
                </tr>
              ))
            )}
          </tbody>
        </table>
      </div>

      {editing ? (
        <PackEditor
          pack={editing === "new" ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            load();
          }}
        />
      ) : null}
    </div>
  );
}

function PackEditor({
  pack,
  onClose,
  onSaved,
}: {
  pack: ScenarioPack | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [form, setForm] = React.useState<ScenarioPackInput>(
    pack
      ? {
          name: pack.name,
          description: pack.description,
          planner_append: pack.planner_append,
          worker_append: pack.worker_append,
          skills: pack.skills,
          disabled_tools: pack.disabled_tools,
        }
      : { ...EMPTY },
  );
  const [skillText, setSkillText] = React.useState((pack?.skills ?? []).join(", "));
  const [toolText, setToolText] = React.useState((pack?.disabled_tools ?? []).join(", "));
  const [err, setErr] = React.useState("");
  const [busy, setBusy] = React.useState(false);

  const save = async () => {
    setBusy(true);
    setErr("");
    const body: ScenarioPackInput = {
      ...form,
      skills: skillText
        .split(/[,，\s]+/)
        .map((s) => s.trim())
        .filter(Boolean),
      disabled_tools: toolText
        .split(/[,，\s]+/)
        .map((s) => s.trim())
        .filter(Boolean),
    };
    try {
      if (pack) {
        await api.updateScenario(pack.id, body);
      } else {
        await api.createScenario(body);
      }
      onSaved();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="flex flex-col gap-3 rounded-md border p-4">
      <h2 className="text-sm font-semibold">{pack ? `编辑：${pack.name}` : "新建场景包"}</h2>

      <div className="flex flex-wrap gap-3">
        <label className="flex flex-col gap-1 text-xs">
          名称
          <Input
            className="h-8 w-56"
            value={form.name ?? ""}
            onChange={(e) => setForm({ ...form, name: e.target.value })}
            placeholder="如 金融 / 支付"
          />
        </label>
        <label className="flex min-w-0 flex-1 flex-col gap-1 text-xs">
          一句话说明（显示在勾选列表里）
          <Input
            className="h-8"
            value={form.description ?? ""}
            onChange={(e) => setForm({ ...form, description: e.target.value })}
            placeholder="这个场景适用于什么目标、补什么"
          />
        </label>
      </div>

      <label className="flex flex-col gap-1 text-xs">
        Planner 追加正文
        <textarea
          className="min-h-32 rounded-md border bg-transparent p-2 font-mono text-xs"
          value={form.planner_append ?? ""}
          onChange={(e) => setForm({ ...form, planner_append: e.target.value })}
          placeholder="只写「通用规则之外还要注意什么」：行业攻击面优先级、高价值 fact、方向侧重。不要重复通用规则。"
        />
      </label>

      <label className="flex flex-col gap-1 text-xs">
        Worker 追加正文
        <textarea
          className="min-h-32 rounded-md border bg-transparent p-2 font-mono text-xs"
          value={form.worker_append ?? ""}
          onChange={(e) => setForm({ ...form, worker_append: e.target.value })}
          placeholder="执行侧补充：行业红线、取证要求、验证顺序。"
        />
      </label>

      <div className="flex flex-wrap gap-3">
        <label className="flex min-w-0 flex-1 flex-col gap-1 text-xs">
          启用技能（目录名，逗号分隔）
          <Input
            className="h-8"
            value={skillText}
            onChange={(e) => setSkillText(e.target.value)}
            placeholder="banking-finance"
          />
        </label>
        <label className="flex min-w-0 flex-1 flex-col gap-1 text-xs">
          禁用工具前缀（逗号分隔，如 shell_）
          <Input className="h-8" value={toolText} onChange={(e) => setToolText(e.target.value)} placeholder="shell_" />
        </label>
      </div>

      {err ? <p className="text-red-600 text-sm">保存失败：{err}</p> : null}

      <div className="flex gap-2">
        <Button size="sm" onClick={save} disabled={busy}>
          保存
        </Button>
        <Button variant="outline" size="sm" onClick={onClose} disabled={busy}>
          取消
        </Button>
      </div>
    </div>
  );
}