#!/usr/bin/env bash
# verify.sh 校验 feedback_convergence：在 repo 内运行 go test，退出码 0 表示通过。
# 初始态存在多个失败用例（Max/Mean 各有缺陷），需迭代收敛到全绿。
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "${script_dir}/repo"
if go test ./...; then
	echo "PASS: feedback_convergence"
	exit 0
fi
echo "FAIL: feedback_convergence"
exit 1
