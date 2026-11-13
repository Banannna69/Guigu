#!/usr/bin/env python3
# 一次性脚本：给移植来的报告相关文件补本项目规范的文件头，补完即删。
import os

HEADERS = {
    "report/aigen.go": ("AI 叙述层：把单薄的漏洞记录改写成交付级报告正文。", [
        "complete / cleanJSON：在报告角色 LLM 上跑一次无工具的补全。",
        "aiReportLevel / reportLevelOut：报告级叙述（背景/成果/整改结论/命中项）。",
        "aiVulnNarrative / EnrichFinding：单条漏洞的描述/风险/修复/CVSS。",
    ]),
    "report/build.go": ("报告骨架装配：确定性内容 + AI 叙述 → reportDoc。", [
        "BuildDoc：装配完整报告 JSON（骨架 + 叙述 + 分组 + 汇总）。",
        "assembleVuln / reproToBlocks：单条漏洞与复现步骤组装（复现逐字渲染）。",
        "groupByTarget / countBySeverity / worstSeverity：分组与严重度统计。",
        "loadEvidenceByStep：把证据截图按复现步骤分组。",
        "redactInternalPaths：折叠平台内部暂存路径，避免泄漏给客户。",
    ]),
    "report/content.go": ("报告 JSON schema（与 pybuilder/report_builder.py 共用的字段契约）。", [
        "reportDoc / docMeta：报告顶层与元信息。",
        "vulnDetail / procBlock：单条漏洞与复现块（text/code/image）。",
        "summarySection / sysRow / remedyRow / vulnListRow：综述与表格行。",
    ]),
    "report/defaults.go": ("内置确定性骨架（三语）：人员/流程/清单/工具/规范。", [
        "methodologyIntro / methodology：PTES 流程。",
        "checklist / tools / standards：55 项测试清单、工具、参考规范。",
        "defaultPersonnel：默认测试人员（可被报告设置覆盖）。",
    ]),
    "report/docx.go": ("交付级 .docx 生成：组装 JSON → python-docx 渲染器（内嵌子进程）。", [
        "GenerateDocx：写出渲染器与输入 JSON，调 python 产出 .docx。",
        "findPython / PythonAvailable：定位可用的 python3 + python-docx。",
        "safeName / nowStamp：文件命名的安全化与时间戳。",
    ]),
    "report/extract.go": ("从会话转录里提取被描述但未登记的确认漏洞（漏洞补录）。", [
        "ExtractFindings：调 LLM 从转录中抽取候选漏洞。",
        "sysExtract / userExtract：提取用的提示词模板。",
    ]),
    "report/md.go": ("Markdown 交付稿：与 .docx 同源骨架，图片以相对路径打进 zip。", [
        "BuildMarkdown：渲染 Markdown 正文 + 证据图清单。",
        "GenerateMarkdownFile：打包 report.md 与 assets/ 成 zip。",
        "mdCell / mdLeadNumRe：表格单元格转义与章节号处理。",
    ]),
    "report/quickreport.go": ("快速预览报告：从探索图即时渲染一段 Markdown。", [
        "Input / Markdown：最小输入与渲染入口。",
        "parseFinding / sevRank：漏洞解析与严重度排序。",
    ]),
    "report/reporti18n.go": ("报告管线的三语字符串（严重度/状态/兜底叙述）。", [
        "normLocale / pick：语言归一到 zh-CN|zh-TW|en-US。",
        "severityName / statusPending / statusRemarkFirst：本地化词条。",
        "fallback*：LLM 不可用时的确定性兜底文案。",
    ]),
    "report/skillcontent.go": ("可编辑骨架与提示词：读盘并逐文件回退内置默认。", [
        "loadSkillContent：解析某语言的骨架与提示词模板。",
        "skillContent：解析结果（方法论/清单/工具/规范/人员/提示词）。",
    ]),
    "report/skillseed.go": ("启动时把内置骨架投影到 skills/report-generation（只写缺失文件）。", [
        "SeedSkillFiles：把默认值与提示词写成可编辑文件。",
        "writeIfMissing：仅在文件不存在时写入，不覆盖用户编辑。",
    ]),
    "server/report.go": ("交付级报告的 HTTP 面：异步生成、状态轮询、下载。", [
        "generateReport：启动异步生成（返回 202），失败有 panic 兜底。",
        "reportStatus / downloadReport：轮询与下载。",
        "buildReportInput：把已确认漏洞与全局资产计数摊平成报告输入。",
    ]),
    "server/report_mgmt.go": ("报告管理：组织级设置、logo、模版 CRUD、已生成报告清单。", [
        "getReportSettings / putReportSettings：出具方/人员/CVSS 的读写。",
        "uploadReportLogo / serveReportAsset：logo 与资产文件。",
        "listReportTemplates / createReportTemplate / updateReportTemplate / deleteReportTemplate：命名模版。",
        "listReports / downloadReportByID / deleteReportByID：已生成报告管理。",
    ]),
    "server/finding_evidence.go": ("漏洞证据截图的上传/展示/管理（含上传安全基线）。", [
        "sanitizeImage：按内容校验并重编码图片（剥离 polyglot/EXIF/追加载荷）。",
        "uploadFindingEvidence / listFindingEvidence：上传与列表。",
        "deriveEvidenceCaption：按复现步骤文案自动生成图注。",
        "serveFindingEvidence / updateFindingEvidence / deleteFindingEvidence：取图与增删改。",
    ]),
    "server/findings_enrich.go": ("漏洞详情、AI 富化、复现步骤→真实流量的关联。", [
        "findingDetail：详情页数据（含富化与复现）。",
        "resolveFindingTraffic：把复现步骤映射到真实录制的 exchange id。",
        "findingSetupTraffic：从流量里捞出注册/登录报文作为前置条件素材。",
        "findingEnrich / findingEnrichStatus / runEnrich：异步 AI 富化。",
        "findingUpdate / dtoFromEnrichment / dbEnrichmentFromReport：富化的读写与转换。",
    ]),
}


def main() -> None:
    for path, (title, items) in HEADERS.items():
        if not os.path.exists(path):
            print("missing", path)
            continue
        lines = open(path, encoding="utf-8").read().split("\n")
        pkg = next((i for i, l in enumerate(lines) if l.startswith("package ")), None)
        if pkg is None:
            print("no package", path)
            continue
        if lines[0].startswith("//"):
            print("already", path)
            continue
        header = [f"// {os.path.basename(path)}：{title}", "// 主要函数/类型："]
        header += [f"//   - {it}" for it in items]
        header.append("")
        open(path, "w", encoding="utf-8").write("\n".join(header + lines[pkg:]))
        print("headered", path)


if __name__ == "__main__":
    main()
