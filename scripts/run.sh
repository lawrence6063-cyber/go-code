#!/usr/bin/env bash
# run.sh — 一键进入 cogent 交互式 REPL。
# 自动加载 .env（含 DEEPSEEK_API_KEY），优先用已编译的 bin/cogent，缺省时回退 go run。
# 用法：
#   ./scripts/run.sh                          # 进入交互对话
#   ./scripts/run.sh "给 internal/foo 加日志"  # 携带首轮任务
#   ./scripts/run.sh --mode=plan "梳理项目"    # 透传任意 cogent 参数
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT" || exit 1

# ---- 加载 .env（.gitignore 已排除；不打印密钥）----
if [ ! -f ./.env ]; then
	echo "FATAL: .env not found（请从 .env.example 复制并填入 DEEPSEEK_API_KEY）" >&2
	exit 1
fi
set -a; . ./.env; set +a
if [ -z "${DEEPSEEK_API_KEY:-}" ]; then
	echo "FATAL: .env 中 DEEPSEEK_API_KEY 为空" >&2
	exit 1
fi

# ---- 选择可执行文件：优先已编译二进制，否则 go run ----
if [ -x ./bin/cogent ]; then
	EXEC=(./bin/cogent)
else
	echo "[run.sh] bin/cogent 不存在，回退 go run（首次会编译，稍候）..." >&2
	EXEC=(go run ./cmd/cogent)
fi

# ---- 透传所有参数给 cogent run ----
exec "${EXEC[@]}" run "$@"
