/**
 * page.tsx：知识库管理页——两层文档（全局公共库 / 任务专属资料）、上传、检索预览、
 * 检索审计与 embedder 配置。
 * 主要函数/类型：
 *   - KnowledgePage：默认导出页组件，聚合四个标签页
 *   - DocsTab：文档列表 + 新建/编辑/删除 + 文件上传
 *   - SearchTab：检索预览，显示本词走的是语义还是关键词模式
 *   - LogsTab：检索审计（谁问了什么、命中几条）
 *   - ConfigTab：embedder 配置、连通性测试、重建索引
 *
 * 设计上的两个要点：
 *   1. 页面上始终显示「当前是语义检索还是关键词检索」。用户配完 embedder 却看到结果
 *      很差时，最需要知道的就是「它到底有没有在走向量」——把模式明写在检索结果上方，
 *      比让用户去猜 pgvector 装没装要直接得多。
 *   2. 任务库上传必须选任务。这里在下拉里显式要求选，而不是让后端落一条孤儿行。
 */
"use client";

import * as React from "react";

import { DatabaseIcon, RefreshCwIcon, SearchIcon, Trash2Icon, UploadIcon, ZapIcon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { KnowledgeConfig, KnowledgeDoc, KnowledgeHit, KnowledgeLog } from "@/lib/api/knowledge";
import type { Task } from "@/lib/types";

// fmtBytes 把字节数转成易读体积。
function fmtBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

// fmtTime 只显示到分钟，列表里秒没有信息量。
function fmtTime(ts: string): string {
  const d = new Date(ts);
  if (Number.isNaN(d.getTime())) return ts;
  return d.toLocaleString("zh-CN", { hour12: false });
}

export default function KnowledgePage() {
  const [cfg, setCfg] = React.useState<KnowledgeConfig | null>(null);
  const [tasks, setTasks] = React.useState<Task[]>([]);

  const loadCfg = React.useCallback(() => {
    api
      .knowledgeConfig()
      .then(setCfg)
      .catch(() => setCfg(null));
  }, []);

  React.useEffect(() => {
    loadCfg();
    api
      .tasks()
      .then((r) => setTasks(r.tasks))
      .catch(() => setTasks([]));
  }, [loadCfg]);

  // 语义模式的可用性是整页的上下文，放在顶部常驻。
  const modeBadge = cfg ? (
    cfg.semantic_ready ? (
      <Badge variant="secondary" className="gap-1">
        <ZapIcon className="size-3" /> 语义检索已启用
      </Badge>
    ) : cfg.vector_available ? (
      <Badge variant="outline" className="gap-1">
        向量层就绪，但未配置 embedder（当前走关键词检索）
      </Badge>
    ) : (
      <Badge variant="outline" className="gap-1">
        无 pgvector（当前走关键词检索）
      </Badge>
    )
  ) : null;

  return (
    <div className="flex flex-1 flex-col gap-4 md:gap-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="font-semibold text-xl tracking-tight">知识库</h1>
          <p className="text-muted-foreground text-sm">
            全局公共库（漏洞库 / 方法论 / 判定标准）与任务专属资料（评分规则 / 接口文档 / 授权范围）。agent
            执行时会主动检索这两层。
          </p>
        </div>
        {modeBadge}
      </div>

      <Tabs defaultValue="docs">
        <TabsList>
          <TabsTrigger value="docs">文档</TabsTrigger>
          <TabsTrigger value="search">检索预览</TabsTrigger>
          <TabsTrigger value="logs">检索审计</TabsTrigger>
          <TabsTrigger value="config">Embedder 配置</TabsTrigger>
        </TabsList>
        <TabsContent value="docs" className="mt-4">
          <DocsTab tasks={tasks} onChanged={loadCfg} />
        </TabsContent>
        <TabsContent value="search" className="mt-4">
          <SearchTab tasks={tasks} />
        </TabsContent>
        <TabsContent value="logs" className="mt-4">
          <LogsTab />
        </TabsContent>
        <TabsContent value="config" className="mt-4">
          <ConfigTab cfg={cfg} onChanged={loadCfg} />
        </TabsContent>
      </Tabs>
    </div>
  );
}

// DocsTab 是文档列表与编辑器。
function DocsTab({ tasks, onChanged }: { tasks: Task[]; onChanged: () => void }) {
  const [docs, setDocs] = React.useState<KnowledgeDoc[]>([]);
  const [scope, setScope] = React.useState<"all" | "global" | "task">("all");
  const [editing, setEditing] = React.useState<KnowledgeDoc | "new" | null>(null);
  const [busy, setBusy] = React.useState(false);

  const load = React.useCallback(() => {
    setBusy(true);
    api
      .knowledgeDocs({ scope: scope === "all" ? "" : scope })
      .then(setDocs)
      .catch((e) => toast.error((e as Error).message))
      .finally(() => setBusy(false));
  }, [scope]);

  React.useEffect(() => {
    load();
  }, [load]);

  async function remove(d: KnowledgeDoc) {
    if (!window.confirm(`删除文档「${d.title}」？其向量分块会一并删除。`)) return;
    try {
      await api.deleteKnowledgeDoc(d.id);
      toast.success("已删除");
      load();
      onChanged();
    } catch (e) {
      toast.error((e as Error).message);
    }
  }

  async function upload(file: File) {
    try {
      await api.knowledgeUpload(file, { scope: "global" });
      toast.success(`已上传：${file.name}`);
      load();
      onChanged();
    } catch (e) {
      toast.error((e as Error).message);
    }
  }

  return (
    <div className="flex flex-col gap-4">
      <Card>
        <CardHeader className="flex-row items-center justify-between">
          <div>
            <CardTitle className="text-base">文档列表</CardTitle>
            <CardDescription>
              支持 .md / .txt / .csv / .json / .docx / .pdf。上传后自动抽文本并在后台生成向量。
            </CardDescription>
          </div>
          <div className="flex items-center gap-2">
            <select
              className="h-9 rounded-md border bg-background px-2 text-sm"
              value={scope}
              onChange={(e) => setScope(e.target.value as typeof scope)}
            >
              <option value="all">全部</option>
              <option value="global">仅全局</option>
              <option value="task">仅任务</option>
            </select>
            <Button size="sm" variant="outline" onClick={load} disabled={busy}>
              <RefreshCwIcon /> 刷新
            </Button>
            <Button size="sm" onClick={() => setEditing("new")}>
              新建
            </Button>
          </div>
        </CardHeader>
        <CardContent>
          <div className="mb-3 flex items-center gap-2 rounded-md border border-dashed p-3">
            <UploadIcon className="size-4 text-muted-foreground" />
            <span className="text-muted-foreground text-sm">上传到全局库：</span>
            <Input
              type="file"
              accept=".md,.markdown,.txt,.csv,.json,.log,.yaml,.yml,.html,.docx,.pdf"
              className="max-w-sm"
              onChange={(e) => {
                const f = e.target.files?.[0];
                if (f) void upload(f);
                e.currentTarget.value = "";
              }}
            />
          </div>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>标题</TableHead>
                <TableHead className="w-28">范围</TableHead>
                <TableHead className="w-32">分类</TableHead>
                <TableHead className="w-24">大小</TableHead>
                <TableHead className="w-32">索引</TableHead>
                <TableHead className="w-40">更新时间</TableHead>
                <TableHead className="w-28" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {docs.length === 0 && (
                <TableRow>
                  <TableCell colSpan={7} className="h-24 text-center text-muted-foreground text-sm">
                    {busy ? "加载中…" : "暂无文档。点「新建」录入判定标准，或上传现成的资料文件。"}
                  </TableCell>
                </TableRow>
              )}
              {docs.map((d) => (
                <TableRow key={d.id}>
                  <TableCell className="font-medium">
                    <button type="button" className="text-left hover:underline" onClick={() => setEditing(d)}>
                      {d.title}
                    </button>
                    {d.file_name && <div className="text-muted-foreground text-xs">{d.file_name}</div>}
                  </TableCell>
                  <TableCell>
                    <Badge variant={d.scope === "global" ? "secondary" : "outline"}>
                      {d.scope === "global" ? "全局" : `任务 #${d.task_id ?? "?"}`}
                    </Badge>
                  </TableCell>
                  <TableCell className="text-muted-foreground text-xs">{d.category || "—"}</TableCell>
                  <TableCell className="text-muted-foreground text-xs tabular-nums">{fmtBytes(d.bytes)}</TableCell>
                  <TableCell className="text-xs">
                    {d.indexed ? (
                      <span className="text-muted-foreground">已索引 · {d.chunks} 块</span>
                    ) : (
                      <span className="text-muted-foreground">未索引</span>
                    )}
                  </TableCell>
                  <TableCell className="text-muted-foreground text-xs tabular-nums">{fmtTime(d.updated_at)}</TableCell>
                  <TableCell>
                    <Button size="sm" variant="ghost" onClick={() => remove(d)}>
                      <Trash2Icon />
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      {editing && (
        <DocEditor
          doc={editing === "new" ? null : editing}
          tasks={tasks}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            load();
            onChanged();
          }}
        />
      )}
    </div>
  );
}

// DocEditor 是文档的新建/编辑表单。
function DocEditor({
  doc,
  tasks,
  onClose,
  onSaved,
}: {
  doc: KnowledgeDoc | null;
  tasks: Task[];
  onClose: () => void;
  onSaved: () => void;
}) {
  const [title, setTitle] = React.useState(doc?.title ?? "");
  const [category, setCategory] = React.useState(doc?.category ?? "");
  const [content, setContent] = React.useState(doc?.content ?? "");
  const [scope, setScope] = React.useState<"global" | "task">(doc?.scope ?? "global");
  const [taskId, setTaskId] = React.useState<number>(doc?.task_id ?? 0);
  const [saving, setSaving] = React.useState(false);

  // 编辑已有文档时先取回正文（列表视图不带 content）。
  React.useEffect(() => {
    if (doc && doc.content === undefined) {
      api
        .knowledgeDoc(doc.id)
        .then((full) => {
          setContent(full.content ?? "");
          setCategory(full.category);
          setTitle(full.title);
        })
        .catch((e) => toast.error((e as Error).message));
    }
  }, [doc]);

  async function save() {
    if (!title.trim()) {
      toast.error("标题不能为空");
      return;
    }
    // 任务库必须指定任务：否则会落成「scope=task 但 task_id=NULL」的孤儿行。
    if (scope === "task" && !taskId) {
      toast.error("任务知识必须选择所属任务");
      return;
    }
    setSaving(true);
    try {
      if (doc) {
        await api.updateKnowledgeDoc(doc.id, { category, title, content });
      } else {
        await api.createKnowledgeDoc({
          scope,
          task_id: scope === "task" ? taskId : undefined,
          category,
          title,
          content,
        });
      }
      toast.success("已保存，正在后台生成向量");
      onSaved();
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setSaving(false);
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">{doc ? `编辑：${doc.title}` : "新建文档"}</CardTitle>
        <CardDescription>正文会按段落切成约 700 字的分块再向量化，所以请保留段落结构。</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <div className="flex flex-wrap items-end gap-3">
          <div className="flex flex-col gap-1">
            <Label htmlFor="kb-title">标题</Label>
            <Input id="kb-title" value={title} onChange={(e) => setTitle(e.target.value)} className="w-64" />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor="kb-cat">分类</Label>
            <Input
              id="kb-cat"
              value={category}
              onChange={(e) => setCategory(e.target.value)}
              placeholder="如：判定标准 / 漏洞库"
              className="w-48"
            />
          </div>
          {/* 已有文档不允许改范围：换范围等于换可见性，容易误操作，删除重建更明确。 */}
          {!doc && (
            <>
              <div className="flex flex-col gap-1">
                <Label htmlFor="kb-scope">范围</Label>
                <select
                  id="kb-scope"
                  className="h-9 rounded-md border bg-background px-2 text-sm"
                  value={scope}
                  onChange={(e) => setScope(e.target.value as "global" | "task")}
                >
                  <option value="global">全局公共库</option>
                  <option value="task">任务专属</option>
                </select>
              </div>
              {scope === "task" && (
                <div className="flex flex-col gap-1">
                  <Label htmlFor="kb-task">所属任务</Label>
                  <select
                    id="kb-task"
                    className="h-9 max-w-xs rounded-md border bg-background px-2 text-sm"
                    value={taskId}
                    onChange={(e) => setTaskId(Number(e.target.value))}
                  >
                    <option value={0}>请选择…</option>
                    {tasks.map((t) => (
                      <option key={t.id} value={Number(t.id)}>
                        #{t.id} {t.description}
                      </option>
                    ))}
                  </select>
                </div>
              )}
            </>
          )}
        </div>
        <Textarea
          value={content}
          onChange={(e) => setContent(e.target.value)}
          rows={14}
          placeholder="粘贴文档正文：判定标准、接口说明、测试账号约定、评分规则等。"
          className="font-mono text-xs"
        />
        <div className="flex justify-end gap-2">
          <Button variant="outline" onClick={onClose}>
            取消
          </Button>
          <Button onClick={save} disabled={saving}>
            {saving ? "保存中…" : "保存"}
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}

// SearchTab 是检索预览，用来在把知识交给 agent 之前先自己验一遍。
function SearchTab({ tasks }: { tasks: Task[] }) {
  const [query, setQuery] = React.useState("");
  const [taskId, setTaskId] = React.useState(0);
  const [hits, setHits] = React.useState<KnowledgeHit[]>([]);
  const [mode, setMode] = React.useState("");
  const [busy, setBusy] = React.useState(false);

  async function run() {
    if (!query.trim()) return;
    setBusy(true);
    try {
      const r = await api.knowledgeSearch(query, taskId, 8);
      setHits(r.hits ?? []);
      setMode(r.mode);
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">检索预览</CardTitle>
        <CardDescription>
          这就是 agent 调 search_knowledge 时看到的内容。配完知识库先在这里验一遍，比等 agent 跑一轮快。
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <div className="flex flex-wrap gap-2">
          <Input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && run()}
            placeholder="如：支付回调签名校验要求"
            className="max-w-md"
          />
          <select
            className="h-9 rounded-md border bg-background px-2 text-sm"
            value={taskId}
            onChange={(e) => setTaskId(Number(e.target.value))}
          >
            <option value={0}>仅全局库</option>
            {tasks.map((t) => (
              <option key={t.id} value={Number(t.id)}>
                + 任务 #{t.id}
              </option>
            ))}
          </select>
          <Button onClick={run} disabled={busy}>
            <SearchIcon /> {busy ? "检索中…" : "检索"}
          </Button>
        </div>
        {mode && (
          <div className="text-muted-foreground text-xs">
            本次走的是 <b>{mode === "semantic" ? "语义检索（向量相似度）" : "关键词检索（ILIKE）"}</b>
            ，命中 {hits.length} 条。
          </div>
        )}
        <div className="flex flex-col gap-2">
          {hits.map((h, i) => (
            <div key={`${h.doc_id}-${i}`} className="rounded-md border p-3">
              <div className="mb-1 flex flex-wrap items-center gap-2">
                <Badge variant="outline">{(h.score * 100).toFixed(0)}%</Badge>
                <span className="font-medium text-sm">{h.title}</span>
                {h.category && <span className="text-muted-foreground text-xs">[{h.category}]</span>}
                <span className="text-muted-foreground text-xs">{h.scope === "task" ? "任务专属" : "全局"}</span>
              </div>
              <p className="whitespace-pre-wrap text-xs leading-relaxed">{h.content}</p>
            </div>
          ))}
        </div>
      </CardContent>
    </Card>
  );
}

// LogsTab 是检索审计：能看出知识库到底有没有被用起来、问的都是什么。
function LogsTab() {
  const [logs, setLogs] = React.useState<KnowledgeLog[]>([]);
  const [busy, setBusy] = React.useState(false);

  const load = React.useCallback(() => {
    setBusy(true);
    api
      .knowledgeLogs()
      .then(setLogs)
      .catch((e) => toast.error((e as Error).message))
      .finally(() => setBusy(false));
  }, []);

  React.useEffect(() => {
    load();
  }, [load]);

  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between">
        <div>
          <CardTitle className="text-base">检索审计</CardTitle>
          <CardDescription>最近 200 次检索。命中数为 0 的查询通常意味着知识库还缺对应内容。</CardDescription>
        </div>
        <Button size="sm" variant="outline" onClick={load} disabled={busy}>
          <RefreshCwIcon /> 刷新
        </Button>
      </CardHeader>
      <CardContent>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="w-40">时间</TableHead>
              <TableHead className="w-28">发起方</TableHead>
              <TableHead>查询</TableHead>
              <TableHead className="w-24">模式</TableHead>
              <TableHead className="w-20">命中</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {logs.length === 0 && (
              <TableRow>
                <TableCell colSpan={5} className="h-24 text-center text-muted-foreground text-sm">
                  {busy ? "加载中…" : "暂无检索记录。"}
                </TableCell>
              </TableRow>
            )}
            {logs.map((l) => (
              <TableRow key={l.id}>
                <TableCell className="text-muted-foreground text-xs tabular-nums">{fmtTime(l.created_at)}</TableCell>
                <TableCell className="text-xs">{l.agent_key || "—"}</TableCell>
                <TableCell className="max-w-md truncate text-xs" title={l.query}>
                  {l.query}
                </TableCell>
                <TableCell>
                  <Badge variant="outline" className="text-xs">
                    {l.mode}
                  </Badge>
                </TableCell>
                <TableCell className="text-xs tabular-nums">{l.hits}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </CardContent>
    </Card>
  );
}

// ConfigTab 是 embedder 配置与索引运维。
function ConfigTab({ cfg, onChanged }: { cfg: KnowledgeConfig | null; onChanged: () => void }) {
  const [enabled, setEnabled] = React.useState(false);
  const [baseURL, setBaseURL] = React.useState("");
  const [model, setModel] = React.useState("");
  const [apiKey, setApiKey] = React.useState("");
  const [dim, setDim] = React.useState(1024);
  const [busy, setBusy] = React.useState(false);
  const [testing, setTesting] = React.useState(false);

  React.useEffect(() => {
    if (!cfg) return;
    setEnabled(cfg.enabled);
    setBaseURL(cfg.base_url);
    setModel(cfg.model);
    setDim(cfg.dim);
    // 密钥永不回显，留空即表示「不改动已有的 key」。
    setApiKey("");
  }, [cfg]);

  async function save() {
    setBusy(true);
    try {
      await api.saveKnowledgeConfig({
        enabled,
        base_url: baseURL,
        model,
        api_key: apiKey || undefined,
        dim,
      });
      toast.success("已保存");
      setApiKey("");
      onChanged();
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function test() {
    setTesting(true);
    try {
      const r = await api.testKnowledgeEmbedder();
      // 维度是对照项：和这里填的 dim 不一致时，语义检索的相似度会失真。
      toast.success(`连通成功，模型实际返回 ${r.dim} 维向量`);
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setTesting(false);
    }
  }

  async function reindex() {
    if (!window.confirm("重建全部向量？换 embedder 或模型后必须做一次，文档多时会跑一段时间。")) return;
    setBusy(true);
    try {
      const r = await api.reindexKnowledge();
      toast.success(`已重建 ${r.indexed} 篇文档的索引`);
      onChanged();
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <DatabaseIcon className="size-4" /> Embedder 配置
        </CardTitle>
        <CardDescription>
          任意 OpenAI 兼容的 /embeddings 端点（智谱 / SiliconFlow / OpenAI / Ollama 均可）。不配置时知识库仍可用，
          只是检索退化为关键词匹配。
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <div className="flex items-center gap-3">
          <Switch id="kb-enabled" checked={enabled} onCheckedChange={setEnabled} />
          <Label htmlFor="kb-enabled">启用语义检索</Label>
        </div>
        <div className="grid gap-3 md:grid-cols-2">
          <div className="flex flex-col gap-1">
            <Label htmlFor="kb-base">Base URL</Label>
            <Input
              id="kb-base"
              value={baseURL}
              onChange={(e) => setBaseURL(e.target.value)}
              placeholder="https://open.bigmodel.cn/api/paas/v4"
            />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor="kb-model">模型</Label>
            <Input id="kb-model" value={model} onChange={(e) => setModel(e.target.value)} placeholder="embedding-3" />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor="kb-key">API Key</Label>
            <Input
              id="kb-key"
              type="password"
              value={apiKey}
              onChange={(e) => setApiKey(e.target.value)}
              placeholder={cfg?.has_key ? "已配置（留空则不修改）" : "未配置"}
            />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor="kb-dim">维度</Label>
            <Input
              id="kb-dim"
              type="number"
              value={dim}
              onChange={(e) => setDim(Number(e.target.value))}
              placeholder="1024"
            />
          </div>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button onClick={save} disabled={busy}>
            保存
          </Button>
          <Button variant="outline" onClick={test} disabled={testing || !cfg?.has_key}>
            {testing ? "测试中…" : "测试连通性"}
          </Button>
          <Button variant="outline" onClick={reindex} disabled={busy || !cfg?.semantic_ready}>
            <RefreshCwIcon /> 重建全部索引
          </Button>
        </div>
        {cfg && (
          <div className="text-muted-foreground text-xs">
            现有文档：全局 {cfg.docs_global} 篇 · 任务 {cfg.docs_task} 篇
            {!cfg.vector_available && (
              <span className="ml-2 text-amber-600">
                未检测到 pgvector 扩展，语义检索不可用。用 pgvector/pgvector 镜像起 PostgreSQL 即可。
              </span>
            )}
          </div>
        )}
      </CardContent>
    </Card>
  );
}
