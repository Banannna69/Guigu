// noa.go：noa（模型驱动上下文压缩）接入——实验性压缩器的开关式装配与失败回退。
// 主要函数/类型：
//   - enableNoa：开关开启时把 noa 压缩器接入 agentcore.Options，启用失败回退内置压缩。
//   - noaWarn：带会话标签的 noa 非致命诊断日志 sink。

package agent

import (
	"log"
	"path/filepath"

	"norma/agentcore"
	"norma/noaadapter"
)

// noaWarn returns a diagnostics sink tagging non-fatal noa messages with the
// session, routed through the package logger (agents have no per-instance one).
func noaWarn(session string) func(string) {
	return func(msg string) { log.Printf("[noa] %s: %s", session, msg) }
}

// noa 是 norma v0.4.0 引入的「模型驱动上下文压缩」机制,作为平台实验功能由用户在
// 系统设置中开关。它与内置 compaction 互斥:noaadapter.Enable 是唯一入口,一次挂上
// 上下文接管器(Compactor)、Compress 工具与三段常驻提示词,不调用 Enable 即为关闭
// (内置 compaction 照常工作)。开关由每个 agent 注入的 noaEnabledFn 解析,每 run 读
// 一次,故切换只影响之后启动的 run,无需重建 agent。

// enableNoa 在解析器报告开启时把 noa 接入 opts。archiveRoot 是压缩原文的持久化基目录
// (取全局 workDir,各 agent 统一落在 <workDir>/noa 下,不随任务/意图目录分散),sessionID
// 命名其下的归档子目录(全局唯一,故同一基目录内不冲突)。
//
// noa 是实验功能:接入失败不得中断真实任务。发生错误时经 onWarn 上报并回退内置压缩。
// 启用成功时清掉 opts.Compaction,避免 agentcore 因「两个上下文管理器同时设置」告警。
// windowTokens 是当前模型真实的上下文窗口（来自 profile 的 ContextWindowK，见
// Config.CompactionWindow）。**必须传**：noaadapter 在既不传窗口也不传 Config 时
// 会写死 200000，而 noa 的每条阈值都是窗口的百分比（提示 75%、机械截断 95%）——
// 窗口配成 32K/64K 的模型上，阈值永远触发不到，长会话直接在 provider 侧
// prompt-too-long 硬失败；而它又把内置压缩关掉了（见下方 opts.Compaction = nil），
// 等于上下文管理整体失效。窗口大于 200K 时不会失败，只是压缩过于频繁。
func enableNoa(opts *agentcore.Options, enabled func() bool, archiveRoot, sessionID string, onWarn func(string), windowTokens int) {
	if enabled == nil || !enabled() {
		return
	}
	if opts.OnWarn == nil {
		opts.OnWarn = onWarn
	}
	noaOpts := noaadapter.Options{
		ArchiveBaseDir: filepath.Join(archiveRoot, "noa"),
		SessionID:      sessionID,
		OnWarn:         onWarn,
	}
	if windowTokens > 0 {
		noaOpts.ModelContextLimit = windowTokens
	}
	if err := noaadapter.Enable(opts, noaOpts); err != nil {
		if onWarn != nil {
			onWarn("noa 压缩启用失败,回退内置压缩:" + err.Error())
		}
		return
	}
	// Compactor 覆盖 Compaction,但两者并存时 agentcore 每次会告警;明确清掉。
	opts.Compaction = nil
}
