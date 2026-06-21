#!/usr/bin/env bash
# run-selfloop.sh — 让 cogent 用自身的目标循环（cogent goal）优化自己的真实代码。
# 任务：补全 llm.finish_reason 端到端透出（见 eval/tasks/finish_reason_selfloop/task.txt）。
# 隔离：--worktree（物理隔离，通过才 Merge 回基线；不触碰主工作区未跟踪文件）。
# 判定：eval/tasks/finish_reason_selfloop/verify.sh（退出码 0 = 达标）。
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT" || exit 1

# 加载密钥与接入配置（.env 已被 .gitignore 排除；不打印密钥）。
if [ ! -f ./.env ]; then
	echo "FATAL: .env not found (需含 DEEPSEEK_API_KEY)"; exit 1
fi
set -a; . ./.env; set +a
if [ -z "${DEEPSEEK_API_KEY:-}" ]; then
	echo "FATAL: DEEPSEEK_API_KEY empty"; exit 1
fi

# 模型与可观测（file exporter，traces 落 data/traces 供观察）。
export COGENT_MODEL="deepseek-reasoner"
export COGENT_REVIEWER_MODEL="deepseek-reasoner"
export COGENT_OBSERVE_ENABLED="true"
export COGENT_TRACE_EXPORTER="file"
export COGENT_TRACE_DIR="./data/traces"
export COGENT_TRACE_SAMPLE_RATIO="1.0"

VERIFY="$ROOT/eval/tasks/finish_reason_selfloop/verify.sh"
INTENT="$(cat "$ROOT/eval/tasks/finish_reason_selfloop/task.txt")"

echo "=== cogent self-loop: optimize llm.finish_reason ==="
echo "model    : $COGENT_MODEL (reviewer=$COGENT_REVIEWER_MODEL)"
echo "isolation: --worktree (通过才 Merge)"
echo "budget   : 8 iterations / \$5 / 15m"
echo "verify   : $VERIFY"
echo "traces   : $COGENT_TRACE_DIR"
echo "===================================================="

./bin/cogent goal --worktree \
	--verify "$VERIFY" \
	--max-iterations 8 \
	--max-cost 5 \
	--max-wallclock 15m \
	"$INTENT"
echo "=== goal exit code: $? ==="
