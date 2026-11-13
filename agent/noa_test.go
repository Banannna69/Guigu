// noa_test.go：noa 压缩器接入行为的回归。
// 主要函数/类型：
//   - TestEnableNoaAttachesAndClearsCompaction：开启时接入 Compactor 并清掉内置 Compaction。
//   - TestEnableNoaSkipsWhenDisabled：开关关闭时完全不接入。

package agent

import (
	"testing"

	"norma/agentcore"
)

// TestEnableNoaAttachesAndClearsCompaction 覆盖接入的两条不变式：
//  1. Compactor 必须挂上，否则 noa 等于没开。
//  2. Compaction 必须清掉——两者并存时 agentcore 每次都会告警，而且两个上下文
//     管理器同时生效行为未定义。
//
// 窗口传递本身由 noaadapter 包内的 TestNewSessionHonoursModelContextLimit 覆盖：
// 那里能直接读回 session 的 Config，这里只能看到 Compactor 是否挂上。
func TestEnableNoaAttachesAndClearsCompaction(t *testing.T) {
	opts := &agentcore.Options{Compaction: compactionConfig(128_000)}
	enableNoa(opts, func() bool { return true }, t.TempDir(), "sess-on", nil, 128_000)
	if opts.Compactor == nil {
		t.Fatal("noa 未接入（Compactor 为空）")
	}
	if opts.Compaction != nil {
		t.Fatal("noa 接管后必须清掉内置 Compaction")
	}
	// 开关型接入还应把 Compress 工具与常驻提示词一起挂上。
	if len(opts.Tools) == 0 {
		t.Error("noa 接入后应追加 Compress 工具")
	}
	if len(opts.AppendSystemPrompt) == 0 {
		t.Error("noa 接入后应追加常驻提示词")
	}
}

// TestEnableNoaSkipsWhenDisabled 保证开关关闭时完全不接入。
func TestEnableNoaSkipsWhenDisabled(t *testing.T) {
	opts := &agentcore.Options{}
	enableNoa(opts, func() bool { return false }, t.TempDir(), "sess-off", nil, 128_000)
	if opts.Compactor != nil {
		t.Fatal("开关关闭时不应接入 noa")
	}
	if len(opts.Tools) != 0 || len(opts.AppendSystemPrompt) != 0 {
		t.Fatal("开关关闭时不应注入任何工具或提示词")
	}
}

// TestEnableNoaSkipsWithNilResolver 保证未注入解析器时按关闭处理（默认关）。
func TestEnableNoaSkipsWithNilResolver(t *testing.T) {
	opts := &agentcore.Options{}
	enableNoa(opts, nil, t.TempDir(), "sess-nil", nil, 128_000)
	if opts.Compactor != nil {
		t.Fatal("没有开关解析器时不应接入 noa")
	}
}