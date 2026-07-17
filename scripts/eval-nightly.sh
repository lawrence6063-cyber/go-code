#!/usr/bin/env bash
# eval-nightly.sh — 非门禁的夜间评测跑分（EVAL_SPEC E5-2「夜间 CI 跑分，非门禁，质量基线」）。
#
# 流程：构建 cogent → 跑 native 评测集 → 产出时间戳报告 → 若有基线则 eval compare（非门禁，
#      退化仅告警不阻断）。定位为「质量基线体检」而非 CI 门禁：默认始终以 0 退出。
#
# 密钥仅来自环境变量（DEEPSEEK_API_KEY），脚本绝不硬编码密钥（全局安全规则 Secrets: env-only）。
#
# 环境变量（均可覆盖）：
#   DEEPSEEK_API_KEY    必填，LLM 密钥（CI 走 secret 注入 / 本地走 ~/.cogent/config.env 或 export）
#   COGENT_MODEL        模型，默认 deepseek-chat（省成本）
#   COGENT_LLM_BASE_URL 端点，默认 https://api.deepseek.com/v1（deepseek 必设，否则打 OpenAI 端点 401）
#   EVAL_ARGS           额外透传给 `eval run` 的参数（如 "--capability=convergence" 或 "--dataset=polyglot ..."）
#   EVAL_CONCURRENCY    并发样本数，默认 3
#   EVAL_BASELINE       compare 基线 report.json 路径，默认 eval/doc/nightly-baseline.json（缺则跳过 compare）
#   EVAL_STRICT=1       改为透传 eval run 退出码（默认 0=非门禁）
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "${ROOT}" || exit 1

: "${DEEPSEEK_API_KEY:?set DEEPSEEK_API_KEY via env / CI secret / ~/.cogent/config.env}"
export GOTOOLCHAIN="${GOTOOLCHAIN:-auto}"
export COGENT_LLM_BASE_URL="${COGENT_LLM_BASE_URL:-https://api.deepseek.com/v1}"
export COGENT_MODEL="${COGENT_MODEL:-deepseek-chat}"

conc="${EVAL_CONCURRENCY:-3}"
ts="$(date -u +%Y%m%d-%H%M%S)"
art="eval-artifacts/nightly/${ts}"
baseline="${EVAL_BASELINE:-eval/doc/nightly-baseline.json}"

echo "[nightly] building cogent (GOTOOLCHAIN=${GOTOOLCHAIN}) ..."
if ! go build -o bin/cogent ./cmd/cogent; then
	echo "[nightly] FATAL: build failed" >&2
	exit 1
fi

echo "[nightly] eval run — suite=native model=${COGENT_MODEL} conc=${conc} args=[${EVAL_ARGS:-}]"
# 注意：不传全局 --max-* 覆盖，让 budget 任务用各自 task.yaml 预算（保反向评测语义）。
# shellcheck disable=SC2086
./bin/cogent eval run --n-concurrent="${conc}" --artifact-dir="${art}" ${EVAL_ARGS:-}
run_rc=$?

report_json="${art}/report.json"
if [ -f "${baseline}" ] && [ -f "${report_json}" ]; then
	echo "[nightly] eval compare (non-gating) against ${baseline} ..."
	./bin/cogent eval compare --base="${baseline}" --head="${report_json}" || true
elif [ -f "${report_json}" ]; then
	echo "[nightly] no baseline at ${baseline}; skip compare. seed via: cp ${report_json} ${baseline}"
fi

echo "[nightly] done (rc=${run_rc}). report: ${art}/report.md"
if [ "${EVAL_STRICT:-0}" = "1" ]; then
	exit "${run_rc}"
fi
exit 0
