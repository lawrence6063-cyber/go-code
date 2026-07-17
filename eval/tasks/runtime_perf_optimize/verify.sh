#!/usr/bin/env bash
# verify.sh 校验 runtime_perf_optimize 任务是否完成（EVAL_SPEC §4.6 · perf）：
# 在 repo 内运行单测（含性能预算测试），退出码 0 表示通过。
# 客观判据：初始 O(n^2) 实现在大输入下超出时间预算必失败；优化到线性后才通过。
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "${script_dir}/repo"

if go test ./...; then
	echo "PASS: runtime_perf_optimize"
	exit 0
fi
echo "FAIL: runtime_perf_optimize"
exit 1
