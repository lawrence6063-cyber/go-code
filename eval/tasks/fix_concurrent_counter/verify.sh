#!/usr/bin/env bash
# verify.sh 校验 fix_concurrent_counter 任务是否完成：
# 在 repo 内运行 go test -race，退出码 0 表示通过（含竞态检测）。
# 客观判据：初始状态（无同步保护）必失败；正确修复后才通过。
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_dir="${script_dir}/repo"

cd "${repo_dir}"
if go test -race ./...; then
	echo "PASS: fix_concurrent_counter"
	exit 0
fi
echo "FAIL: fix_concurrent_counter"
exit 1
