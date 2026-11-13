# 更新日志

本项目的变更记录，格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)。

版本说明：`1.0.0` 起为独立版本序列。`0.1.x` 及更早条目属于历史迭代记录，仅作留存。

## [1.0.0] - 2026-11-13

首个正式版本。在 ARTEX 引擎之上整合了三方项目的长处，并补齐了此前缺失的能力。

### 交付级报告管线

- 移植完整报告流水线：AI 叙述层（`report/aigen.go`）+ 确定性 python-docx 渲染
  （`report/pybuilder/report_builder.py`），产出带封面、目录、页码的 .docx。
- **复现已落地为事实**：`report_finding` 新增 `reproduction` 参数，agent 在击穿当下逐字
  记录入口点/前置条件/有序步骤/来源，落库到 `findings.reproduction`。报告优先逐字渲染，
  AI 不得重建——这是报告可信与「AI 编报告」的分界线。
- **复现步骤绑定真实流量**：`traffic.ResolveSteps` 用时间窗 + 步骤顺序双约束，把每一步
  映射到真实录制的 exchange id，报告里可逐字核对。判别器只在仍有多个候选时才施加，
  改写过的字段不会把匹配打成零。
- **骨架可编辑**：方法论/测试清单/工具/规范/人员投影到 `skills/report-generation/`，
  支持简繁英三语，改完下次生成生效，逐文件回退默认。
- 新增前端：`/system/reports`（出具方设置、logo、命名模版、历史报告）、任务报告页区分
  「快速预览」与「交付级成品」、漏洞详情接入富化/复现步骤/证据截图。

### 知识库（两层 RAG）

- 全局公共库（漏洞库/0day/方法论/判定标准，跨任务复用）+ 任务专属资料（接口文档/评分
  规则/授权范围，只对本任务可见）。检索时全局永远可见，任务库只在所属任务内可见。
- 任意 OpenAI 兼容的 embedding 端点；缺 pgvector 或未配 embedder 时**自动降级为关键词
  检索**，功能不消失、启动不阻断。语义路径上任何失败（供应商 5xx、超时、维度不匹配）
  都静默降到关键词——检索是 agent 的中途动作，不该因供应商抖动让整个任务失败。
- agent 工具 `search_knowledge` / `list_knowledge_categories`（planner + worker）。
- 新增 `/system/knowledge` 页面：文档管理、上传（.docx/.pdf 走 python 抽文本）、
  检索预览、检索审计、embedder 配置与重建索引。

### 角色分工

- planner / worker / mainagent 各绑一个 LLM 配置，报告单独一档（`settings.llm_role_report`）。
  执行量大宜用便宜快的模型，报告只跑一次但要文笔——分开绑之后这种取舍就消失了。
- `profile_id=0` 表示跟随全局激活配置，所以不配置时的行为与从前完全一致。

### 场景包（附加式）

- 场景是**追加层**，不是替换层：选中后把行业特定的判断口径、技能、禁用工具追加到通用
  prompt 之上，通用能力不受影响。空场景时 system prompt 与变更前逐字节一致。
- 内置金融/支付场景（提炼自真实 playbook，含硬性红线：只用自有账号、0.01 即停止、
  绝不修改收款人、绝不重放付款回调），也支持用户自建。
- 新建任务页可选场景，创建时快照进 `task_scenarios`。

### 后渗透

- MCP 一键导入模板（GodzillaSuper / Playwright / ScopeSentry）。模板只预填非敏感字段，
  密钥与会话信息一律留空由用户填写；创建后默认停用，避免指向错误地址的服务被自动拉起。

### 安全加固

- 登录限流改为双维度（IP + 账号）滑动窗口；`clientIPFrom` 只信任 `ARTEX_TRUSTED_PROXIES`
  配置的代理，避免伪造 `X-Forwarded-For` 绕过。
- 证据截图上传按内容校验并重编码，剥离 polyglot / EXIF / 追加载荷。
- 流量录制代理默认只监听 `127.0.0.1`。

### 工程

- 注释规范检查器移除内部工单编号体系的配对检查，改为只查文件头、doc 贴附与锚点。
- 测试统一走 `./test.sh`（`-p 1` 串行）：所有包共享同一 PostgreSQL，并行会互相踩。
- `docker-compose.yml` 改用 pgvector 镜像，否则语义检索永远无法启用。

## [0.1.5] - 2026-11-07

### 首个意图与 Agent 编排

- 自定义 Agent 新增 `planner_controlled` 配置（默认 `true` 兼容旧数据），Agent 编辑器可设置首个意图执行期间是否受 Planner 控制；`seedFirstIntent` 按创建时配置写入 `planner_policy` 快照，后续修改 Agent 不影响已有 intent，config-export/import 同步支持该字段。
- 首个 `hold_until_done` custom seed 的首次 kick、edge、heartbeat、goalless 和超时终局规划统一由 DB 门控控制：paused/stopped/blocked/exhausted 不再自动唤醒 Planner 或改派默认 Worker，只有真实 done 或人工删除放行；普通 Worker 与其他 custom intent 保持原有失败重规划。
- custom runner 终态改为 fail-closed，只有 `ReasonCompleted` 可写 done，hook/abort 写 stopped，未知或空 reason 写 blocked，避免未完成被误判完成。
- 补齐 Agent 配置 PATCH、DB 状态矩阵、真实 Planner edge/heartbeat、超时收尾、快照兼容和浏览器开关保存测试；实施与验证详见 `dev/FIRST_INTENT_GATE_IMPLEMENTATION.md`、`dev/FIRST_INTENT_GATE_TEST_RECORD.md`。

### 稳定性修复

- 主 Agent 单轮会话增加 10 分钟超时兜底、超时提示和 panic 恢复；正常、超时及 panic 退出均清理会话占用与 LLM 调用计数，手动取消原因保持不变。
- LLM 熔断状态落库改为单 writer + 2000 容量有界队列，队列满时丢弃持久化并记录日志，避免 PostgreSQL 卡顿时按事件堆积 goroutine；新增真实 PostgreSQL 行为测试。
- 冷图压缩和会话执行增加 panic 恢复与堆栈日志，防止单个后台任务异常终止整个服务。

## [0.1.4] - 2026-11-05

### Finding 显式编排

- `spawn_task` 子任务改用独立探索链，不再创建 `source_task_ids` 来源关系；带 `finding_id` 时仅在任务创建事务内复制该 Finding 沿 `yields/derived_from` 反向可达的 `intent/fact/finding` 祖先子图，重映射 origin、节点、边和锚点，复制 intent 固定为 `done`，父 Finding 记录与无关分支保持不变。工具 Schema 和默认 Dispatcher 提示词提供幂等迁移，自定义提示词不覆盖。
- 全部系统工具及 `reporter`、`vuln_dispatcher`、`retester`、`trace_cleaner` 均由代码幂等 seed，首次启动即可使用，不再依赖导入 `config-export`；数据库中的后续修改与删除仍保留。MCP 连接、凭据和可见性继续由用户配置。
- `trace_cleaner` 新增事务化 Agent、默认提示词、4 项工具绑定及显式派发触发器 seed，避免部分初始化；其全局 `on_finding` 默认关闭。

- 新增 Planner 专用 `dispatch_finding_to_agent` 工具：可将当前任务的独立漏洞记录（findings.id）显式派发给任意已启用、非内置的自定义 Agent；具体场景目标和证据门槛由系统提示词约束。
- `vuln_dispatcher` 与 `trace_cleaner` 默认关闭全局 `on_finding` 订阅，改由“外网渗透（内置）”Planner 在确认立足点或仅漏洞路径证据后显式派发；Worker 内置提示词同步强化立足点证据规则。
- 外网 Planner 的 Finding 派发提示词改为可扩展的 Agent 清单和独立规则块，明确 `vuln_dispatcher` 与 `trace_cleaner` 不互斥、清理确认不属于派发前提，避免已取得立足点且存在 `[投放]` 事实时漏派清理审计。
- 新增 `finding_agent_invocations` 持久记录、Conversation 回填和 10 分钟原子补发；合并 Trigger Run 会回填全部 Finding，重复调用返回首次 Conversation ID。
- 新增外网内置 Planner/Worker 提示词、默认任务快照及旧 Dispatcher/Trace Cleaner 触发配置的幂等迁移；自定义模板和自定义任务快照保持不变。
- AI 预审 `note` 改为单条意图的完整中文说明，禁止 ID/分号堆叠；保持 JSON 契约、200 字上限和既有展示方式。
- Worker WebShell 上传要求使用与目标技术栈匹配的正常应用文件名，禁止攻击特征/随机命名及覆盖既有文件，并保持落地路径、`shell_add` 与 `[投放]` 记录一致。
- 外网渗透场景默认关闭意图审核；存量分类一次性迁移，之后手动重开不会被重启覆盖，其他场景默认值不变。

### 修复与发布

- Docker 源码构建恢复 `ARTEX_VERSION` → `main.version` 注入，当前发布版本统一为 **0.1.4**，恢复健康检查版本展示和更新比较能力；运行镜像补齐 `start.sh` 启动必需的 `scripts/artex-config.sh` 与升级 SQL，避免入口脚本启动即退出。
- 拓扑自动整理与 C 段归组增加同步防重入、错误提示与失败重载；归组标记读写容错，手动归组后不再被意外自动归回。
- 人工预审 `pass` / `revise` 的 `note` 统一为必填；空值不进入预审结果，合法理由保留 trim 后内容，自动处置契约不变。
- Agent 触发列表对 Planner 显式派发消息模板显示 `工具调用(dispatch_finding_to_agent)`，不再误显示“无条件”；不额外开启 Scheduler `on_tool_call`，避免重复入队。

## [0.1.3] - 2026-11-04

### 新增

- **内网拓扑图**：任务详情新增资产覆盖图 / 内网拓扑切换视图，配套拓扑画布、编辑工具栏与分组布局，数据表与 API 路由同步落地。

### 变更

- **部署包内置场景预设**：Release 包自带 `config-export/` 编排配置，支持导入场景分类、planner 模板与审核触发器。
- **代码优化**：例行重构与清理，提升可维护性。
- **文档优化**：README 重写为快速开始，部署手册与功能详解同步更新。

## [0.1.2] - 2026-10-30

### 二开功能

- **意图审核流**：新增待审意图、review agent 预审、人工批准 / 驳回、修订重送、乐观锁与可选自动处置；预审建议不会在人工全审模式下直接放行。
- **planner 模板与场景预设**：新增模板库、创建时快照、分类默认模板 / 审核 / 种子意图 / 执行器，以及任务级 worker 并发。
- **自定义执行器**：自定义 agent 可声明 `executor_key`，由种子意图或送审 / 修订时的 `target_executor` 路由；失效时告警并回退 worker。
- **finding 后编排**：通用触发器支持运行策略与水位线；首启播种 `vuln_dispatcher` 和 `reporter`，支持派生场景子任务和自动报告。
- **证据交接**：`report_finding` 支持 `evidence_hint_id`，报告流程按 `evidence_version` 防止旧证据覆盖新报告。
- **可观测性与迁移**：补充关键链路日志、日志级别热切换、访问日志，以及 19 类业务配置的导出 / 幂等导入和提示词升级命令。

### 安全与发布

- 新增首启安装令牌、文件工具任务目录边界与 `python_interpreter` 校验。
- Docker Compose 改为通过 `Dockerfile.source` 从当前二开源码构建，避免误拉上游镜像；JWT key 持久化到数据目录。
- 新增脱敏 `config-export/` 示例包：不含 LLM 档和密钥，MCP 默认禁用；`retester` / `trace_cleaner` 是可选配置，不冒充源码 seed。
- 修正 review 种子提示词与升级 SQL，使“预审只写建议、人工或自动处置才改状态”的文案和代码行为一致。

### 执行模式与任务编排

- 新增 `foothold` / `vulnerability_only` 双执行模式，由后端根据任务目标和描述识别，并注入 Goals、Planner 与漏洞派发上下文。
- 新增 Worker 场景模板、任务级 Worker 提示词快照、场景默认模板和模板校验接口；历史任务保留创建时的执行上下文。
- 新增宿主资产视图、内网任务拓扑、资产派发记录和同资产 Finding 接续能力，减少重复任务链。
- 内网渗透子任务名称由服务端统一生成为 `内网渗透·<目标资产>`，不再依赖模型自觉填写，也不再把长任务摘要作为任务名。

### 提示词与漏洞派发

- 重构 Goals、Planner、Worker、Pentest、Auto、Vuln Dispatcher 和 Trace Cleaner 提示词，统一目标意图、证据验收、投放登记和清理规则。
- GetShell 证据验收区分完整 Shell 与单次命令执行：单次 RCE 回显不再直接等同于取得立足点。
- 漏洞派发仅接受明确的 `foothold` 路径，并要求 `critical/high` 与 WebShell、交互式 Shell 或可用 C2 等实证；仅 RCE、文件上传、信息泄露、未授权接口、弱口令和模糊猜测不创建内网渗透任务。
- 暂停独立漏洞利用和权限提升场景的自动派发；权限提升只作为已创建内网渗透子任务中的内部步骤。
- 派发判断继续服从父任务授权范围。取得 foothold 不会覆盖“禁止内网扫描、横向移动、代理或隧道”等明确限制。

### Review 与前端

- Review 会话恢复原有上下布局，不再把审核操作框错误显示到 Planner、系统审计和普通 Worker 会话。
- 待审队列由多卡片纵向滚动改为 `1、2、3…` 编号切换，一次只显示当前待审意图；保留通过、驳回、修订和 Review 对话。
- 修复会话内容区 flex 高度和静态导出缓存问题，确保部署的前端资源与源码一致。

### MCP 与配置迁移

- `start.sh` 支持 MCP 本地程序、插件和配置目录随包导出；记录文件 SHA256、大小和执行权限。
- 导入时重写 `{{MCP_ROOT}}` 路径、逐文件校验并恢复可执行位；可用 `ARTEX_IMPORT_MCP_BIN=0` 仅导入 MCP 配置。
- 配置包包含 Agent、提示词、触发器、工具绑定、任务分类、Worker 模板和 MCP 配置；同步/Release 配置包不包含 LLM API Key。
- 部署文档统一为源码编译和 Docker 两种方式，并分别提供配置导入命令。

### 构建与源码修复

- 恢复 `evidence/store.go`、`evidence/store_test.go` 和 `evidence/testmain_test.go`，解决源码仍引用 evidence 包但目录缺失导致的构建阻塞。
- 明确前端静态构建使用 `NEXT_EXPORT=1`；清理旧 `.next/out` 后重建，避免旧 Review 分栏资源混入新部署包。
- 修复漏洞派发提示词导入时的 PostgreSQL 角色环境问题，导入失败使用单文件事务回滚，成功后再重启验证。

### 数据库与升级注意事项

- 新增 Worker 模板、资产派发和拓扑相关表及字段；升级前必须备份 PostgreSQL。
- 配置导入不会导入 LLM Key，也不会替代数据库备份；已有任务的历史提示词快照不会被自动改写。
- 二进制升级、配置导入和提示词升级是不同操作，应分别执行并验证。

### 验证

- 子任务命名定向 Go 测试通过；前端 TypeScript 和静态构建通过。
- Chrome 验证主 Agent、Planner、系统审计和 Worker 不显示审核框；Review 编号切换、通过、对话和移动布局通过。
- Linux 二进制部署、健康检查、首页和嵌入前端资源断言通过。
- 完整 `go test ./server` 需要有效的 `ARTEX_PG_DSN`；本次环境未执行完整数据库测试。Docker 部署链路已核读，但未在本次 Windows 环境实机构建镜像。