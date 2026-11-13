/**
 * page.tsx：报告管理页——出具方设置（公司/logo/人员/CVSS）、命名模版、已生成报告清单。
 * 主要函数/类型：
 *   - ReportsPage：默认导出页组件
 *   - SettingsCard：出具方单例设置与 logo 上传
 *   - TemplatesCard：命名模版的增删改查
 *   - RecordsCard：已生成报告的下载与删除
 *
 * 为什么把「出具方设置」和「模版」分开：设置是**当前默认值**，每次生成都读它；
 * 模版是**可切换的预设**，用来在「给 A 客户出报告」和「给 B 客户出报告」之间快速
 * 切换而不用改全局默认。把它俩混成一个东西会让用户不知道该改哪个。
 */
"use client";

import * as React from "react";

import { DownloadIcon, FileTextIcon, PlusIcon, RefreshCwIcon, SaveIcon, Trash2Icon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api } from "@/lib/api";
import type { Personnel, ReportRecord, ReportSettings, ReportTemplate } from "@/lib/api/report";

// fmtSize / fmtTime 只做展示用格式化。
function fmtSize(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}
function fmtTime(ts: string): string {
  const d = new Date(ts);
  return Number.isNaN(d.getTime()) ? ts : d.toLocaleString("zh-CN", { hour12: false });
}

// emptyTemplate 是新建模版的初始值。
function emptyTemplate(): Partial<ReportTemplate> {
  return {
    name: "",
    company_name: "",
    company_name_en: "",
    logo_path: "",
    personnel: [],
    cvss_version: "3.1",
    locale: "zh-CN",
  };
}

export default function ReportsPage() {
  return (
    <div className="flex flex-1 flex-col gap-4 md:gap-6">
      <div>
        <h1 className="font-semibold text-xl tracking-tight">报告管理</h1>
        <p className="text-muted-foreground text-sm">
          出具方信息与报告模版决定封面、目录和人员清单；历史报告在这里下载或清理。
        </p>
      </div>
      <SettingsCard />
      <TemplatesCard />
      <RecordsCard />
    </div>
  );
}

// SettingsCard 是出具方单例设置。
function SettingsCard() {
  const [s, setS] = React.useState<ReportSettings | null>(null);
  const [saving, setSaving] = React.useState(false);

  React.useEffect(() => {
    api
      .reportSettings()
      .then((r) => setS({ ...r, personnel: r.personnel ?? [] }))
      .catch((e) => toast.error((e as Error).message));
  }, []);

  function setPerson(i: number, patch: Partial<Personnel>) {
    setS((prev) =>
      prev ? { ...prev, personnel: prev.personnel.map((p, j) => (j === i ? { ...p, ...patch } : p)) } : prev,
    );
  }
  function addPerson() {
    setS((prev) => (prev ? { ...prev, personnel: [...prev.personnel, { name: "", role: "", contact: "" }] } : prev));
  }
  function removePerson(i: number) {
    setS((prev) => (prev ? { ...prev, personnel: prev.personnel.filter((_, j) => j !== i) } : prev));
  }

  async function onLogo(e: React.ChangeEvent<HTMLInputElement>) {
    const f = e.target.files?.[0];
    if (!f) return;
    try {
      const r = await api.reportUploadLogo(f);
      setS((prev) => (prev ? { ...prev, logo_path: r.logo_path } : prev));
      toast.success("logo 已上传");
    } catch (err) {
      toast.error((err as Error).message);
    }
    e.currentTarget.value = "";
  }

  async function save() {
    if (!s) return;
    setSaving(true);
    try {
      const r = await api.saveReportSettings(s);
      setS({ ...r, personnel: r.personnel ?? [] });
      toast.success("已保存");
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setSaving(false);
    }
  }

  if (!s) return null;

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">出具方设置</CardTitle>
        <CardDescription>这些值会写进报告封面与页脚。中英文公司名按报告语言自动选用。</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <div className="grid gap-3 md:grid-cols-3">
          <div className="flex flex-col gap-1">
            <Label htmlFor="rs-cn">公司名（中文）</Label>
            <Input id="rs-cn" value={s.company_name} onChange={(e) => setS({ ...s, company_name: e.target.value })} />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor="rs-en">公司名（英文）</Label>
            <Input
              id="rs-en"
              value={s.company_name_en}
              onChange={(e) => setS({ ...s, company_name_en: e.target.value })}
            />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor="rs-cvss">CVSS 版本</Label>
            <select
              id="rs-cvss"
              className="h-9 rounded-md border bg-background px-2 text-sm"
              value={s.cvss_version}
              onChange={(e) => setS({ ...s, cvss_version: e.target.value })}
            >
              <option value="3.1">CVSS 3.1</option>
              <option value="4.0">CVSS 4.0</option>
            </select>
          </div>
        </div>

        <div className="flex flex-wrap items-center gap-3">
          <Label htmlFor="rs-logo">Logo</Label>
          <Input id="rs-logo" type="file" accept="image/*" className="max-w-xs" onChange={onLogo} />
          {s.logo_path && <span className="text-muted-foreground text-xs">当前：{s.logo_path}</span>}
        </div>

        <div className="flex flex-col gap-2">
          <div className="flex items-center justify-between">
            <Label>测试人员</Label>
            <Button size="sm" variant="outline" onClick={addPerson}>
              <PlusIcon /> 添加
            </Button>
          </div>
          {s.personnel.length === 0 && <p className="text-muted-foreground text-xs">未配置时报告用内置默认人员。</p>}
          {s.personnel.map((p, i) => (
            <div key={i} className="flex flex-wrap items-center gap-2">
              <Input
                value={p.name}
                onChange={(e) => setPerson(i, { name: e.target.value })}
                placeholder="姓名"
                className="w-36"
              />
              <Input
                value={p.role}
                onChange={(e) => setPerson(i, { role: e.target.value })}
                placeholder="角色（如：渗透测试工程师）"
                className="w-56"
              />
              <Input
                value={p.contact}
                onChange={(e) => setPerson(i, { contact: e.target.value })}
                placeholder="联系方式（选填）"
                className="w-52"
              />
              <Button size="sm" variant="ghost" onClick={() => removePerson(i)}>
                <Trash2Icon />
              </Button>
            </div>
          ))}
        </div>

        <div className="flex justify-end">
          <Button onClick={save} disabled={saving}>
            <SaveIcon /> {saving ? "保存中…" : "保存"}
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}

// TemplatesCard 是命名模版管理：一套出具方配置存成一个模版，生成报告时一键套用。
function TemplatesCard() {
  const [items, setItems] = React.useState<ReportTemplate[]>([]);
  const [editing, setEditing] = React.useState<Partial<ReportTemplate> | null>(null);
  const [busy, setBusy] = React.useState(false);

  const load = React.useCallback(() => {
    api
      .reportTemplates()
      .then(setItems)
      .catch((e) => toast.error((e as Error).message));
  }, []);

  React.useEffect(() => {
    load();
  }, [load]);

  async function save() {
    if (!editing) return;
    if (!editing.name?.trim()) {
      toast.error("模版名不能为空");
      return;
    }
    setBusy(true);
    try {
      if (editing.id) {
        await api.updateReportTemplate(editing.id, editing);
      } else {
        await api.createReportTemplate(editing);
      }
      toast.success("已保存");
      setEditing(null);
      load();
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function remove(t: ReportTemplate) {
    if (!window.confirm(`删除模版「${t.name}」？已生成的报告不受影响。`)) return;
    try {
      await api.deleteReportTemplate(t.id);
      toast.success("已删除");
      load();
    } catch (e) {
      toast.error((e as Error).message);
    }
  }

  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between">
        <div>
          <CardTitle className="text-base">命名模版</CardTitle>
          <CardDescription>把一套出具方配置存成模版，生成报告时按客户一键切换。</CardDescription>
        </div>
        <Button size="sm" onClick={() => setEditing(emptyTemplate())}>
          <PlusIcon /> 新建模版
        </Button>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>名称</TableHead>
              <TableHead className="w-48">公司</TableHead>
              <TableHead className="w-28">语言</TableHead>
              <TableHead className="w-24">CVSS</TableHead>
              <TableHead className="w-28" />
            </TableRow>
          </TableHeader>
          <TableBody>
            {items.length === 0 && (
              <TableRow>
                <TableCell colSpan={5} className="h-20 text-center text-muted-foreground text-sm">
                  暂无模版。不建模版也能出报告，用的是上面的出具方设置。
                </TableCell>
              </TableRow>
            )}
            {items.map((t) => (
              <TableRow key={t.id}>
                <TableCell className="font-medium">
                  <button type="button" className="hover:underline" onClick={() => setEditing({ ...t })}>
                    {t.name}
                  </button>
                </TableCell>
                <TableCell className="text-muted-foreground text-sm">{t.company_name || "—"}</TableCell>
                <TableCell className="text-xs">{t.locale}</TableCell>
                <TableCell className="text-xs">{t.cvss_version}</TableCell>
                <TableCell>
                  <Button size="sm" variant="ghost" onClick={() => remove(t)}>
                    <Trash2Icon />
                  </Button>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>

        {editing && (
          <div className="flex flex-col gap-3 rounded-md border p-4">
            <div className="grid gap-3 md:grid-cols-2">
              <div className="flex flex-col gap-1">
                <Label>模版名</Label>
                <Input value={editing.name ?? ""} onChange={(e) => setEditing({ ...editing, name: e.target.value })} />
              </div>
              <div className="flex flex-col gap-1">
                <Label>公司名（中文）</Label>
                <Input
                  value={editing.company_name ?? ""}
                  onChange={(e) => setEditing({ ...editing, company_name: e.target.value })}
                />
              </div>
              <div className="flex flex-col gap-1">
                <Label>公司名（英文）</Label>
                <Input
                  value={editing.company_name_en ?? ""}
                  onChange={(e) => setEditing({ ...editing, company_name_en: e.target.value })}
                />
              </div>
              <div className="flex flex-col gap-1">
                <Label>默认语言</Label>
                <select
                  className="h-9 rounded-md border bg-background px-2 text-sm"
                  value={editing.locale ?? "zh-CN"}
                  onChange={(e) => setEditing({ ...editing, locale: e.target.value })}
                >
                  <option value="zh-CN">简体中文</option>
                  <option value="zh-TW">繁體中文</option>
                  <option value="en-US">English</option>
                </select>
              </div>
            </div>
            <div className="flex justify-end gap-2">
              <Button variant="outline" onClick={() => setEditing(null)}>
                取消
              </Button>
              <Button onClick={save} disabled={busy}>
                保存
              </Button>
            </div>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

// RecordsCard 是历史已生成报告的清单。
function RecordsCard() {
  const [items, setItems] = React.useState<ReportRecord[]>([]);
  const [busy, setBusy] = React.useState(false);

  const load = React.useCallback(() => {
    setBusy(true);
    api
      .reportRecords()
      .then(setItems)
      .catch((e) => toast.error((e as Error).message))
      .finally(() => setBusy(false));
  }, []);

  React.useEffect(() => {
    load();
  }, [load]);

  async function remove(r: ReportRecord) {
    if (!window.confirm(`删除报告「${r.filename}」？文件会一并删除。`)) return;
    try {
      await api.deleteReportRecord(r.id);
      toast.success("已删除");
      load();
    } catch (e) {
      toast.error((e as Error).message);
    }
  }

  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between">
        <div>
          <CardTitle className="flex items-center gap-2 text-base">
            <FileTextIcon className="size-4" /> 已生成报告
          </CardTitle>
          <CardDescription>每次生成的成品都留档在这里，可直接重新下载。</CardDescription>
        </div>
        <Button size="sm" variant="outline" onClick={load} disabled={busy}>
          <RefreshCwIcon /> 刷新
        </Button>
      </CardHeader>
      <CardContent>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>文件</TableHead>
              <TableHead className="w-56">任务</TableHead>
              <TableHead className="w-24">格式</TableHead>
              <TableHead className="w-24">大小</TableHead>
              <TableHead className="w-40">生成时间</TableHead>
              <TableHead className="w-32" />
            </TableRow>
          </TableHeader>
          <TableBody>
            {items.length === 0 && (
              <TableRow>
                <TableCell colSpan={6} className="h-20 text-center text-muted-foreground text-sm">
                  {busy ? "加载中…" : "还没有生成过报告。到任务的「报告」标签页里生成。"}
                </TableCell>
              </TableRow>
            )}
            {items.map((r) => (
              <TableRow key={r.id}>
                <TableCell className="font-medium">
                  {r.filename}
                  {r.company && <div className="text-muted-foreground text-xs">{r.company}</div>}
                </TableCell>
                <TableCell className="text-muted-foreground text-xs">
                  {r.task_desc ? `#${r.task_id} ${r.task_desc}` : `#${r.task_id ?? "—"}`}
                </TableCell>
                <TableCell>
                  {/* 后端不单独存格式列，按扩展名判断即可。 */}
                  <Badge variant="outline" className="text-xs">
                    {r.filename.endsWith(".docx") ? "Word" : "Markdown"}
                  </Badge>
                </TableCell>
                <TableCell className="text-xs tabular-nums">{fmtSize(r.size)}</TableCell>
                <TableCell className="text-muted-foreground text-xs tabular-nums">{fmtTime(r.created_at)}</TableCell>
                <TableCell className="flex gap-1">
                  <Button size="sm" variant="ghost" asChild>
                    <a href={api.reportRecordDownloadUrl(r.id)} download>
                      <DownloadIcon />
                    </a>
                  </Button>
                  <Button size="sm" variant="ghost" onClick={() => remove(r)}>
                    <Trash2Icon />
                  </Button>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </CardContent>
    </Card>
  );
}
