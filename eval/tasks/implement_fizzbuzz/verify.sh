#!/usr/bin/env bash
# verify.sh 校验 implement_fizzbuzz 任务是否完成：在 repo 内运行 go test，退出码 0 表示通过。
# 用法：在任意位置执行 `bash verify.sh`（以脚本所在目录定位 repo）。
# 客观判据：初始状态（FizzBuzz 为未实现的桩）必失败；正确实现后才通过。
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_dir="${script_dir}/repo"

cd "${repo_dir}"
if go test ./...; then
	echo "PASS: implement_fizzbuzz"
	exit 0
fi
echo "FAIL: implement_fizzbuzz"
exit 1
