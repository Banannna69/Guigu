// memory.go：跨任务的长期记忆接入（norma/memory），带作用域隔离。
// 主要函数/类型：
//   - MemoryScope / DirName：一条记忆的归属范围（全局 / 按企业）与目录名映射。
//   - memoryStoreFor / MemoryStoreFor：按作用域解析记忆目录。
//   - memoryOptions / memoryOptsFor / MemoryOptionsFor：注入预算与 resolver 折叠。
//   - normalizeMemoryQuery：把任务描述压成适合记忆检索的查询串。
//
// 为什么需要：渗透作业里有大量**跨任务可复用的经验**——某类 CMS 的通用 getshell
// 路径、某套 WAF 对哪些 payload 无效、某客户内网的网段与禁 ping 策略、哪条跳板链
// 路通。现在这些每接一个新任务都要从零再摸一遍。
//
// 为什么必须做作用域隔离（这是与直接照搬上游用法的关键差异）：
// 记忆是**跨任务共享的文件**。如果把 A 客户的打法记进一个全局目录，B 客户的 agent
// 会在 AutoInject 时被自动喂进去——这既是数据泄露（A 的资产/路径出现在 B 的上下文），
// 也是合规问题（跨授权范围的经验复用）。因此提供两级作用域：
//
//   - 全局作用域：与具体客户无关的通用手法（如「某 WAF 对 JSON content-type 不拦」）。
//     这类经验不涉及任何客户标识，跨客户复用是正当的。
//   - 企业作用域：与某个客户绑定的经验（内网结构、可用凭据、特定资产）。
//     只在处理该客户的任务时注入。
//
// 默认关闭：记忆会影响模型行为且是持久副作用，必须由运维显式开启。

package agent

import (
	"path/filepath"
	"strconv"
	"strings"

	"norma/agentcore"
	"norma/memory"
)

// MemoryScope 标识一组记忆的归属。
type MemoryScope struct {
	// Global 为 true 时使用全局目录；否则按 CompanyID 分目录。
	Global bool
	// CompanyID 是企业 id（Global 为 false 时有效）。0 表示无法归属，
	// 此时退回全局目录——宁可共享无标识的通用手法，也不把记忆散落到匿名目录。
	CompanyID int64
}

// DirName 返回该作用域对应的目录名。
func (m MemoryScope) DirName() string {
	if m.Global || m.CompanyID <= 0 {
		return "global"
	}
	return "company-" + strconv.FormatInt(m.CompanyID, 10)
}

// memoryRoot 是记忆文件在数据目录下的位置。
func memoryRoot(dataDir string) string {
	return filepath.Join(dataDir, "memory")
}

// memoryStoreFor 返回某个作用域的记忆存储。
//
// 返回值恒非 nil：调用方只在开启记忆时才调用它，且空目录本身无害（Scan 返回空，
// 注入逻辑因无条目而不产出任何内容）。
func memoryStoreFor(dataDir string, scope MemoryScope) *memory.Store {
	return memory.NewStore(filepath.Join(memoryRoot(dataDir), scope.DirName()))
}

// MemoryStoreFor 是 memoryStoreFor 的导出形式，供 server 侧按任务作用域取存储。
func MemoryStoreFor(dataDir string, scope MemoryScope) *memory.Store {
	return memoryStoreFor(dataDir, scope)
}

// memoryOptions 组装 agentcore 的记忆选项。
//
// AutoInject：把与本次输入相关的记忆注入对话，这是记忆的核心价值——模型不必
// 主动想起来去查。
//
// IncludeIndexInPrompt 关掉：索引（全部记忆的标题清单）会常驻系统提示，而它随
// 记忆增长无限变长，最终挤占正文空间。Recall 工具已经能按需检索，常驻索引属于
// 用宝贵上下文换一点便利，不划算。
func memoryOptions(store *memory.Store) *agentcore.MemoryOptions {
	if store == nil {
		return nil
	}
	return &agentcore.MemoryOptions{
		Store:      store,
		AutoInject: true,
		MaxInject:  3, // 与 fetch 成本折中：3 条足够提示方向，再多就开始挤上下文
	}
}

// normalizeMemoryQuery 把一段任务描述压成适合记忆相关性检索的查询串。
//
// memory.Relevant 是按词匹配的，整段意图描述（常含大量中文叙述与标点）会把匹配
// 稀释掉。这里只保留较长的 token，让「WAF」「getshell」「网段」这类关键词主导。
func normalizeMemoryQuery(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	fields := strings.Fields(s)
	kept := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.Trim(f, "，。；：？！、（）【】《》“”‘’\"'`.,;:!?()[]{}")
		// 中文按 rune 计，两个汉字以上才算有信息量；英文两个字符同理。
		if len([]rune(f)) >= 2 {
			kept = append(kept, f)
		}
	}
	if len(kept) > 80 {
		kept = kept[:80]
	}
	return strings.Join(kept, " ")
}
// MemoryOptionsFor 是 memoryOptions 的导出形式，供测试与 server 侧断言预算。
func MemoryOptionsFor(store *memory.Store) *agentcore.MemoryOptions {
	return memoryOptions(store)
}

// memoryOptsFor 把 resolver 折叠成 agentcore 的选项。nil resolver 或解析为 nil
// 都返回 nil，表示本次运行不启用记忆。
func memoryOptsFor(fn func() *memory.Store) *agentcore.MemoryOptions {
	if fn == nil {
		return nil
	}
	return memoryOptions(fn())
}
