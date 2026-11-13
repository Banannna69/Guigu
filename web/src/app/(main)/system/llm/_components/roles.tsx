/**
 * roles.tsx：LLM「角色分工」面板——把每个角色单独绑到一个 LLM 配置。
 * 主要函数/类型：
 *   - LLMRolesPanel：角色→配置的矩阵编辑器
 *
 * 为什么需要这张表：平台里的四类调用对模型的要求完全不同——
 *   planner（规划）量小但要判断力；worker（执行）量大、容忍弱模型；
 *   mainagent（对话）要响应快；报告/富化只在最后跑一次，但要文笔。
 * 只给一个「激活配置」时用户只能二选一：省钱就报告写得糙，写好就每次打靶都贵。
 * 分开绑之后这种取舍就消失了。
 *
 * profile_id="0" 表示跟随全局激活配置，所以不配置任何角色的行为与从前完全一致。
 */
"use client";

import * as React from "react";

import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { api } from "@/lib/api";
import type { LLMProfile } from "@/lib/types";

// ROLE_META 给每个角色一句用途说明——只写 role key 的话用户不知道该绑什么。
const ROLE_META: Record<string, { label: string; hint: string }> = {
  planner: { label: "规划（planner）", hint: "定方向、拆意图。调用量中等，需要判断力。" },
  worker: { label: "执行（worker）", hint: "跑具体测试动作。调用量最大，可绑便宜快的模型。" },
  mainagent: { label: "主控（mainagent）", hint: "与人对话、调度任务。需要响应快。" },
  report: { label: "报告 / 富化", hint: "写交付级报告与漏洞富化。量小，但要文笔与推理。" },
};

export function LLMRolesPanel({ profiles }: { profiles: LLMProfile[] }) {
  const [roles, setRoles] = React.useState<{ role: string; profile_id: string }[]>([]);
  const [saving, setSaving] = React.useState<string>("");

  const load = React.useCallback(() => {
    api
      .llmRoles()
      .then((r) => setRoles(r.roles ?? []))
      .catch((e) => toast.error((e as Error).message));
  }, []);

  React.useEffect(() => {
    load();
  }, [load]);

  async function setRole(role: string, profileID: string) {
    setSaving(role);
    try {
      await api.setLLMRole(role, profileID);
      setRoles((prev) => prev.map((r) => (r.role === role ? { ...r, profile_id: profileID } : r)));
      toast.success("已保存并立即生效");
    } catch (e) {
      toast.error((e as Error).message);
      load(); // 失败时回到服务端的真实状态，避免界面显示的与库里不一致
    } finally {
      setSaving("");
    }
  }

  const active = profiles.find((p) => p.is_default);

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">角色分工</CardTitle>
        <CardDescription>
          不同角色的模型需求差别很大：执行量大宜用便宜快的，报告只跑一次但要好文笔。 「跟随全局」即使用当前激活配置
          {active ? `（${active.name} · ${active.model}）` : "（尚未激活任何配置）"}。
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {roles.length === 0 && <p className="text-muted-foreground text-sm">加载中…</p>}
        {roles.map((r) => {
          const meta = ROLE_META[r.role];
          const bound = r.profile_id !== "0" ? profiles.find((p) => String(p.id) === r.profile_id) : undefined;
          return (
            <div key={r.role} className="flex flex-wrap items-center gap-3 rounded-md border p-3">
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2">
                  <span className="font-medium text-sm">{meta?.label ?? r.role}</span>
                  {bound ? <Badge variant="secondary">{bound.name}</Badge> : <Badge variant="outline">跟随全局</Badge>}
                </div>
                <p className="text-muted-foreground text-xs">{meta?.hint}</p>
              </div>
              <select
                className="h-9 min-w-52 rounded-md border bg-background px-2 text-sm"
                value={r.profile_id}
                disabled={saving === r.role}
                onChange={(e) => setRole(r.role, e.target.value)}
              >
                <option value="0">跟随全局激活配置</option>
                {profiles.map((p) => (
                  <option key={p.id} value={String(p.id)}>
                    {p.name} · {p.model}
                  </option>
                ))}
              </select>
            </div>
          );
        })}
        <div className="flex justify-end">
          <Button size="sm" variant="outline" onClick={load}>
            刷新
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}
