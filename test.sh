#!/bin/sh
# test.sh：本仓库测试的正确跑法。
#
# 为什么不能直接 `go test ./...`：所有包共享同一个 PostgreSQL，而 db/agent/server
# 的用例会改写**全局表**（公司归属刷新、llm_usage 计量、任务/资产清理）。默认的
# 并行包执行会让这些写互相踩——实测 `go test ./...` 在 agent/llmrec/db 上随机失败，
# 而单包或串行执行全绿。这不是 flaky 代码，是测试之间的数据竞争。
#
# 用法：
#   ./test.sh                 串行跑全部包（推荐）
#   ./test.sh ./server/       只跑指定包
#   ./test.sh -race           带竞态检测
#
# 环境：需要可连的 PostgreSQL。ARTEX_PG_DSN 未设置时自动跳过依赖数据库的用例。
set -eu

cd "$(cd "$(dirname "$0")" && pwd)"

RACE=""
PKGS=""
for arg in "$@"; do
	case "$arg" in
	-race) RACE="-race" ;;
	-h | --help)
		sed -n '2,16p' "$0" | sed 's/^# \{0,1\}//'
		exit 0
		;;
	# 累积所有非选项参数，否则多个包只会剩下最后一个。
	*) PKGS="$PKGS $arg" ;;
	esac
done
[ -n "$PKGS" ] || PKGS="./..."

if [ -z "${ARTEX_PG_DSN:-}" ]; then
	printf '\033[33m[!]\033[0m 未设置 ARTEX_PG_DSN —— 依赖数据库的用例会被跳过\n' >&2
fi

# -p 1 是关键：串行执行包，避免跨包改写同一批全局表。
printf '\033[36m[*]\033[0m go test %s -p 1 %s\n' "$RACE" "$PKGS"
exec go test $RACE -p 1 -count=1 -timeout 900s $PKGS
