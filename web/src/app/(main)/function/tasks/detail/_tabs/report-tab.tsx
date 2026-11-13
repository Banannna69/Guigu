/**
 * report-tab.tsx：任务报告面板——快速预览(Markdown) + 交付级报告(.docx/.md)生成与下载。
 * 主要函数/类型：
 *   - ReportTab：任务报告主组件
 *   - startGenerate / poll：启动异步生成并按 5 秒轮询状态
 *
 * 这一页有两个层次的产出，用户很容易只看到其中一个：
 *   1. 快速预览是**即时**的、从探索图直接渲染的 Markdown，用来确认漏洞记录齐不齐。
 *   2. 交付级报告是**异步**的 AI 叙述 + python-docx 排版，才是有封面/目录/页码、
 *      能直接交给客户的成品。
 * 之前只做了前者，用户以为「报告就长这样」，所以这里的按钮和说明都要把差别讲清楚。
 */
"use client";

import * as React from "react";

import { CheckIcon, CopyIcon, DownloadIcon, FileTextIcon, Loader2Icon, PackageIcon } from "lucide-react";
import { toast } from "sonner";

import { Markdown } from "@/components/markdown";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api } from "@/lib/api";
import type { ReportJob } from "@/lib/api/report";
import { copyText } from "@/lib/utils";

// POLL_MS 与后端生成耗时相匹配：一次完整 docx 构建（AI 叙述 + python-docx）通常在
// 十几秒到一分钟量级，5 秒轮询足够及时又不会把后端刷满。
const POLL_MS = 5000;
// MAX_POLLS 给轮询设上限（60 × 5s = 5 分钟）。没有上限的话，后端卡死时前端会永远
// 转圈，而用户唯一的线索只是一个不动的进度条。
const MAX_POLLS = 60;

export function ReportTab({ taskId }: { taskId: string }) {
  const [report, setReport] = React.useState<string>("");
  const [loading, setLoading] = React.useState(true);
  const [copied, setCopied] = React.useState(false);

  // 交付级报告的状态
  const [title, setTitle] = React.useState("");
  const [client, setClient] = React.useState("");
  const [locale, setLocale] = React.useState("zh-CN");
  const [format, setFormat] = React.useState<"docx" | "md">("docx");
  const [job, setJob] = React.useState<ReportJob | null>(null);
  const [generating, setGenerating] = React.useState(false);
  const pollRef = React.useRef<ReturnType<typeof setInterval> | null>(null);

  React.useEffect(() => {
    let active = true;
    setLoading(true);
    api
      .report(taskId)
      .then((text) => {
        if (active) setReport(text);
      })
      .catch(() => {
        if (active) setReport("");
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, [taskId]);

  // 组件卸载时清掉轮询，避免对已卸载组件 setState。
  React.useEffect(() => {
    return () => {
      if (pollRef.current) {
        clearInterval(pollRef.current);
        pollRef.current = null;
      }
    };
  }, []);

  async function copy() {
    if (!report) return;
    const ok = await copyText(report);
    if (ok) {
      setCopied(true);
      toast.success("已复制 Markdown");
      setTimeout(() => setCopied(false), 1500);
    } else {
      toast.error("复制失败，请手动选择文本复制");
    }
  }

  // poll 轮询生成状态，完成或失败后停止。
  function poll() {
    let n = 0;
    const stop = () => {
      if (pollRef.current) {
        clearInterval(pollRef.current);
        pollRef.current = null;
      }
    };
    stop(); // 永不并行两个轮询器
    pollRef.current = setInterval(() => {
      n += 1;
      if (n > MAX_POLLS) {
        stop();
        setGenerating(false);
        toast.error("生成超时，请检查后端日志");
        return;
      }
      api
        .reportStatus(taskId)
        .then((j) => {
          setJob(j);
          if (j.status === "done") {
            stop();
            setGenerating(false);
            toast.success("报告已生成，可下载");
          } else if (j.status === "failed") {
            stop();
            setGenerating(false);
            toast.error(`生成失败：${j.error ?? "未知原因"}`);
          }
        })
        .catch(() => {
          // 单次轮询失败不终止：后端可能正在重建连接，下一次就通了。
        });
    }, POLL_MS);
  }

  async function startGenerate() {
    setGenerating(true);
    setJob(null);
    try {
      await api.reportGenerate(taskId, {
        title: title || undefined,
        client: client || undefined,
        locale,
        format,
      });
      toast.success("已开始生成");
      poll();
    } catch (e) {
      toast.error((e as Error).message);
      setGenerating(false);
    }
  }

  let preview: React.ReactNode;
  if (loading) {
    preview = (
      <div className="flex flex-col items-center justify-center gap-2 rounded-md border border-dashed py-16 text-muted-foreground text-sm">
        <FileTextIcon className="size-8 opacity-40" />
        加载中…
      </div>
    );
  } else if (report) {
    preview = (
      <div className="max-h-[60vh] overflow-auto rounded-md border bg-muted/20 p-4">
        <Markdown text={report} />
      </div>
    );
  } else {
    preview = (
      <div className="flex flex-col items-center justify-center gap-2 rounded-md border border-dashed py-16 text-muted-foreground text-sm">
        <FileTextIcon className="size-8 opacity-40" />
        暂无报告
      </div>
    );
  }

  const done = job?.status === "done";

  return (
    <div className="flex flex-col gap-4">
      {/* ── 交付级报告生成 ── */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-sm">
            <PackageIcon className="size-4" /> 交付级报告
          </CardTitle>
          <CardDescription>
            {format === "docx"
              ? "AI 叙述 + python-docx 排版：封面、目录、页码、每条漏洞的属性表与 7 个编号小节。适合直接交付客户。"
              : "Markdown 交付包（.zip，含 assets/ 证据图），解压后在 Typora / VSCode 里可继续编辑。"}
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          <div className="flex flex-wrap items-end gap-3">
            <div className="flex flex-col gap-1">
              <Label htmlFor="rp-title">报告标题</Label>
              <Input
                id="rp-title"
                value={title}
                onChange={(e) => setTitle(e.target.value)}
                placeholder="留空则用任务描述"
                className="w-56"
              />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor="rp-client">客户名</Label>
              <Input
                id="rp-client"
                value={client}
                onChange={(e) => setClient(e.target.value)}
                placeholder="选填"
                className="w-44"
              />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor="rp-locale">语言</Label>
              <select
                id="rp-locale"
                className="h-9 rounded-md border bg-background px-2 text-sm"
                value={locale}
                onChange={(e) => setLocale(e.target.value)}
              >
                <option value="zh-CN">简体中文</option>
                <option value="zh-TW">繁體中文</option>
                <option value="en-US">English</option>
              </select>
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor="rp-format">格式</Label>
              <select
                id="rp-format"
                className="h-9 rounded-md border bg-background px-2 text-sm"
                value={format}
                onChange={(e) => setFormat(e.target.value as "docx" | "md")}
              >
                <option value="docx">Word (.docx)</option>
                <option value="md">Markdown 包 (.zip)</option>
              </select>
            </div>
            <Button onClick={startGenerate} disabled={generating}>
              {generating ? <Loader2Icon className="animate-spin" /> : <DownloadIcon />}
              {generating ? "生成中…" : "生成报告"}
            </Button>
          </div>

          {generating && (
            <div className="text-muted-foreground text-xs">
              AI 正在为每条漏洞写叙述并排版。这个过程是异步的，通常需要十几秒到一分钟。
            </div>
          )}
          {job?.status === "failed" && (
            <div className="rounded-md border border-destructive/40 bg-destructive/5 p-3 text-destructive text-xs">
              生成失败：{job.error}
            </div>
          )}
          {done && (
            <div className="flex items-center gap-3 rounded-md border bg-muted/30 p-3">
              <span className="text-xs">已生成：{job?.filename}</span>
              <Button size="sm" asChild>
                <a href={api.reportDownloadUrlOf(taskId)} download>
                  <DownloadIcon /> 下载
                </a>
              </Button>
            </div>
          )}
        </CardContent>
      </Card>

      {/* ── 快速预览 ── */}
      <Card>
        <CardHeader className="flex-row items-center justify-between">
          <div>
            <CardTitle className="flex items-center gap-2 text-sm">
              <FileTextIcon className="size-4" /> 快速预览（Markdown）
            </CardTitle>
            <CardDescription>从探索图即时渲染，用来快速确认漏洞记录齐不齐，不是交付件。</CardDescription>
          </div>
          <div className="flex gap-2">
            {report && (
              <Button size="sm" variant="outline" onClick={copy}>
                {copied ? <CheckIcon /> : <CopyIcon />} 复制
              </Button>
            )}
          </div>
        </CardHeader>
        <CardContent>{preview}</CardContent>
      </Card>
    </div>
  );
}
