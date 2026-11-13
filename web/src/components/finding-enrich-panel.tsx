/**
 * finding-enrich-panel.tsx：漏洞详情页的「AI 富化 + 复现步骤 + 证据截图」面板。
 * 主要函数/类型：
 *   - FindingEnrichPanel：主面板，加载富化/复现/证据三份数据
 *   - ReproSteps：按步骤渲染逐字请求/响应，并给出「跳到流量」链接
 *   - EvidenceGrid：证据截图的展示、上传、图注编辑与删除
 *
 * 这一块是「报告里为什么可信」的前端出口：
 *   复现步骤不是模型事后编的，是 agent 打穿当下逐字记录的；每一步还能点进真实录制的
 *   流量包。把这条链路在详情页里显式呈现出来，用户才能在交报告前自己核一遍。
 *
 * 富化是异步的（analyzing → done），所以这里轮询而不是阻塞等待——一次富化要调 LLM，
 * 让用户盯着一个转圈的页面等几十秒是不可接受的。
 */
"use client";

import * as React from "react";

import { ImagePlusIcon, SparklesIcon, Trash2Icon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { api } from "@/lib/api";
import type { Enrichment, FindingEvidenceItem, Reproduction } from "@/lib/api/report";

// POLL_MS 富化轮询间隔。富化是一次 LLM 调用，通常十几到几十秒，3 秒足够及时。
const POLL_MS = 3000;

export function FindingEnrichPanel({ nodeId, readOnly = false }: { nodeId: string; readOnly?: boolean }) {
  const [enr, setEnr] = React.useState<Enrichment | null>(null);
  const [enrStatus, setEnrStatus] = React.useState<"idle" | "analyzing" | "done" | "failed">("idle");
  const [enrErr, setEnrErr] = React.useState("");
  const [repro, setRepro] = React.useState<Reproduction | null>(null);
  const [evItems, setEvItems] = React.useState<FindingEvidenceItem[]>([]);
  const pollRef = React.useRef<ReturnType<typeof setInterval> | null>(null);

  // loadDetail 拉漏洞详情（含富化与复现）。
  const loadDetail = React.useCallback(() => {
    api
      .getFinding(nodeId)
      .then((d) => {
        const detail = d as unknown as { enrichment?: Enrichment | null; reproduction?: Reproduction | null };
        setEnr(detail.enrichment ?? null);
        setRepro(detail.reproduction ?? null);
      })
      .catch(() => {
        // 详情加载失败不弹错：详情页本身已经有一份数据，这里只是补充。
      });
  }, [nodeId]);

  const loadEvidence = React.useCallback(() => {
    api
      .findingEvidenceList(nodeId)
      .then(setEvItems)
      .catch(() => setEvItems([]));
  }, [nodeId]);

  React.useEffect(() => {
    loadDetail();
    loadEvidence();
  }, [loadDetail, loadEvidence]);

  React.useEffect(() => {
    return () => {
      if (pollRef.current) clearInterval(pollRef.current);
    };
  }, []);

  // startPoll 轮询富化状态直到 done / failed。
  function startPoll() {
    if (pollRef.current) clearInterval(pollRef.current);
    pollRef.current = setInterval(() => {
      api
        .findingEnrichStatus(nodeId)
        .then((j) => {
          setEnrStatus(j.status);
          if (j.status === "done") {
            if (pollRef.current) clearInterval(pollRef.current);
            pollRef.current = null;
            setEnr(j.enrichment ?? null);
            toast.success("富化完成");
          } else if (j.status === "failed") {
            if (pollRef.current) clearInterval(pollRef.current);
            pollRef.current = null;
            setEnrErr(j.error ?? "未知错误");
            toast.error(`富化失败：${j.error ?? ""}`);
          }
        })
        .catch(() => {
          /* 单次失败不终止轮询 */
        });
    }, POLL_MS);
  }

  async function enrich() {
    setEnrStatus("analyzing");
    setEnrErr("");
    try {
      const j = await api.findingEnrich(nodeId);
      setEnrStatus(j.status);
      startPoll();
    } catch (e) {
      setEnrStatus("failed");
      setEnrErr((e as Error).message);
      toast.error((e as Error).message);
    }
  }

  async function upload(file: File, stepIndex: number) {
    try {
      await api.findingEvidenceUpload(nodeId, file, "", stepIndex);
      toast.success("证据已上传");
      loadEvidence();
    } catch (e) {
      toast.error((e as Error).message);
    }
  }

  async function updateCaption(it: FindingEvidenceItem, caption: string) {
    try {
      await api.findingEvidenceUpdate(nodeId, it.evidence.id, {
        caption,
        step_index: it.evidence.step_index,
        sort_order: it.evidence.sort_order,
      });
      loadEvidence();
    } catch (e) {
      toast.error((e as Error).message);
    }
  }

  async function removeEvidence(it: FindingEvidenceItem) {
    if (!window.confirm("删除这条证据截图？")) return;
    try {
      await api.findingEvidenceDelete(nodeId, it.evidence.id);
      toast.success("已删除");
      loadEvidence();
    } catch (e) {
      toast.error((e as Error).message);
    }
  }

  const busy = enrStatus === "analyzing";

  return (
    <div className="flex flex-col gap-4">
      {/* ── 复现步骤（最高优先级：这是最可信的部分）── */}
      {repro && (repro.steps?.length || repro.entry_point) && (
        <Card>
          <CardHeader>
            <CardTitle className="text-sm">复现过程</CardTitle>
            <CardDescription>
              agent 在确认漏洞当时逐字记录，报告会原样渲染。每步可跳转到真实录制的流量包。
            </CardDescription>
          </CardHeader>
          <CardContent>
            <ReproSteps repro={repro} evidence={evItems} onUpload={readOnly ? undefined : upload} />
          </CardContent>
        </Card>
      )}

      {/* ── AI 富化 ── */}
      <Card>
        <CardHeader className="flex-row items-center justify-between">
          <div>
            <CardTitle className="text-sm">AI 富化</CardTitle>
            <CardDescription>生成描述、风险分析、修复建议与 CVSS 评分，报告正文由此而来。</CardDescription>
          </div>
          <Button size="sm" variant="outline" onClick={enrich} disabled={busy || readOnly}>
            <SparklesIcon /> {busy ? "分析中…" : enr ? "重新生成" : "开始富化"}
          </Button>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          {enrStatus === "failed" && (
            <div className="rounded-md border border-destructive/40 bg-destructive/5 p-3 text-destructive text-xs">
              富化失败：{enrErr}
            </div>
          )}
          {!enr && enrStatus !== "analyzing" && enrStatus !== "failed" && (
            <p className="text-muted-foreground text-xs">
              尚未富化。漏洞状态改为「已确认」时会自动触发一次；也可以点上面的按钮手动触发。
            </p>
          )}
          {enr && (
            <div className="flex flex-col gap-3">
              <div className="flex flex-wrap items-center gap-2">
                {enr.severity && <Badge variant="outline">{enr.severity}</Badge>}
                {enr.cvss_score > 0 && (
                  <Badge variant="outline">
                    CVSS {enr.cvss_score.toFixed(1)}
                    {enr.cvss_vector ? ` · ${enr.cvss_vector}` : ""}
                  </Badge>
                )}
                {enr.module && <span className="text-muted-foreground text-xs">模块：{enr.module}</span>}
              </div>
              {enr.description?.length > 0 && (
                <Section title="漏洞描述">
                  {enr.description.map((p, i) => (
                    <p key={i} className="text-sm leading-relaxed">
                      {p}
                    </p>
                  ))}
                </Section>
              )}
              {enr.risk_analysis?.length > 0 && (
                <Section title="风险分析">
                  <ul className="list-disc pl-5 text-sm">
                    {enr.risk_analysis.map((r, i) => (
                      <li key={i}>{r}</li>
                    ))}
                  </ul>
                </Section>
              )}
              {enr.remediation?.length > 0 && (
                <Section title="修复建议">
                  <ul className="list-disc pl-5 text-sm">
                    {enr.remediation.map((r, i) => (
                      <li key={i}>{r}</li>
                    ))}
                  </ul>
                </Section>
              )}
            </div>
          )}
        </CardContent>
      </Card>

      {/* ── 证据截图 ── */}
      <Card>
        <CardHeader>
          <CardTitle className="text-sm">证据截图</CardTitle>
          <CardDescription>会作为图片插入报告对应的复现步骤下。</CardDescription>
        </CardHeader>
        <CardContent>
          <EvidenceGrid
            nodeId={nodeId}
            items={evItems}
            readOnly={readOnly}
            onUpload={(f) => upload(f, 0)}
            onCaption={updateCaption}
            onDelete={removeEvidence}
          />
        </CardContent>
      </Card>
    </div>
  );
}

// Section 是富化内容的一个小节。
function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-1">
      <h4 className="font-medium text-muted-foreground text-xs">{title}</h4>
      {children}
    </div>
  );
}

// ReproSteps 按步骤渲染逐字请求/响应，并给出跳转到流量包的链接。
function ReproSteps({
  repro,
  evidence,
  onUpload,
}: {
  repro: Reproduction;
  evidence: FindingEvidenceItem[];
  onUpload?: (f: File, stepIndex: number) => void;
}) {
  return (
    <div className="flex flex-col gap-3">
      {repro.provenance && <p className="text-muted-foreground text-xs">来源：{repro.provenance}</p>}
      {repro.entry_point && (
        <div className="text-sm">
          <span className="text-muted-foreground text-xs">入口点：</span>
          <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs">{repro.entry_point}</code>
        </div>
      )}
      {repro.prerequisites && repro.prerequisites.length > 0 && (
        <div className="text-sm">
          <div className="mb-1 text-muted-foreground text-xs">前置条件：</div>
          <ul className="list-disc pl-5 text-xs">
            {repro.prerequisites.map((p, i) => (
              <li key={i}>{p}</li>
            ))}
          </ul>
        </div>
      )}
      {repro.steps?.map((st, i) => {
        const stepEvidence = evidence.filter((e) => e.evidence.step_index === i);
        return (
          <div key={i} className="rounded-md border p-3">
            <div className="mb-2 flex flex-wrap items-center gap-2">
              <Badge variant="secondary">步骤 {i + 1}</Badge>
              {st.exchange_id && (
                <a
                  className="text-primary text-xs hover:underline"
                  href={`/function/traffic?exchange=${encodeURIComponent(st.exchange_id)}`}
                >
                  查看流量包 →
                </a>
              )}
            </div>
            {st.proves && <p className="mb-2 text-sm">{st.proves}</p>}
            <pre className="max-h-48 overflow-auto whitespace-pre-wrap rounded bg-muted px-2 py-1.5 font-mono text-xs">
              {st.request}
            </pre>
            {st.expected && <p className="mt-1 text-muted-foreground text-xs">预期：{st.expected}</p>}
            {stepEvidence.length > 0 && (
              <div className="mt-2 flex flex-wrap gap-2">
                {stepEvidence.map((e) => (
                  // eslint-disable-next-line @next/next/no-img-element
                  <img
                    key={e.evidence.id}
                    src={e.url}
                    alt={e.evidence.caption || "证据"}
                    className="max-h-40 rounded border"
                  />
                ))}
              </div>
            )}
            {onUpload && (
              <label className="mt-2 flex cursor-pointer items-center gap-1 text-muted-foreground text-xs hover:text-foreground">
                <ImagePlusIcon className="size-3" />
                为该步骤上传截图
                <input
                  type="file"
                  accept="image/*"
                  className="hidden"
                  onChange={(e) => {
                    const f = e.target.files?.[0];
                    if (f) onUpload(f, i);
                    e.currentTarget.value = "";
                  }}
                />
              </label>
            )}
          </div>
        );
      })}
    </div>
  );
}

// EvidenceGrid 是证据截图的网格：展示、改图注、删除、上传。
function EvidenceGrid({
  items,
  readOnly,
  onUpload,
  onCaption,
  onDelete,
}: {
  nodeId: string;
  items: FindingEvidenceItem[];
  readOnly: boolean;
  onUpload: (f: File) => void;
  onCaption: (it: FindingEvidenceItem, caption: string) => void;
  onDelete: (it: FindingEvidenceItem) => void;
}) {
  return (
    <div className="flex flex-col gap-3">
      {items.length === 0 && <p className="text-muted-foreground text-xs">暂无证据截图。</p>}
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {items.map((it) => (
          <div key={it.evidence.id} className="flex flex-col gap-1 rounded-md border p-2">
            {/* eslint-disable-next-line @next/next/no-img-element */}
            <img src={it.url} alt={it.evidence.caption || "证据"} className="max-h-48 rounded object-contain" />
            <div className="flex items-center gap-1">
              <Input
                defaultValue={it.evidence.caption}
                disabled={readOnly}
                placeholder="图注"
                className="h-7 text-xs"
                onBlur={(e) => {
                  if (e.target.value !== it.evidence.caption) onCaption(it, e.target.value);
                }}
              />
              {!readOnly && (
                <Button size="sm" variant="ghost" onClick={() => onDelete(it)}>
                  <Trash2Icon />
                </Button>
              )}
            </div>
            <span className="text-muted-foreground text-xs">
              {it.evidence.step_index > 0 || it.evidence.step_index === 0 ? `步骤 ${it.evidence.step_index + 1}` : ""}
            </span>
          </div>
        ))}
      </div>
      {!readOnly && (
        <label className="flex w-fit cursor-pointer items-center gap-1 rounded-md border border-dashed px-3 py-2 text-muted-foreground text-xs hover:text-foreground">
          <ImagePlusIcon className="size-3" /> 上传证据截图
          <input
            type="file"
            accept="image/*"
            className="hidden"
            onChange={(e) => {
              const f = e.target.files?.[0];
              if (f) onUpload(f);
              e.currentTarget.value = "";
            }}
          />
        </label>
      )}
    </div>
  );
}
