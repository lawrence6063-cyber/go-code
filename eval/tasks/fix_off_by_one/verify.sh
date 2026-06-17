#!/usr/bin/env bash
# verify.sh 校验 fix_off_by_one 任务是否完成：在 repo 内运行 go test，退出码 0 表示通过。
# 用法：在 task 目录或任意位置执行 `bash verify.sh`（以脚本所在目录定位 repo）。
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_dir="${script_dir}/repo"

cd "${repo_dir}"
if go test ./...; then
	echo "PASS: fix_off_by_one"
	exit 0
fi
echo "FAIL: fix_off_by_one"
exit 1
