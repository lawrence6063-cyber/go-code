#!/usr/bin/env bash
# verify.sh 校验 review_reject_retry：在 repo 内运行 go test，退出码 0 表示通过。
# 单测锁死质量点（错误处理 + 正数边界）：缺失质量点的改动无法通过，
# 从而验证 maker/reviewer 闭环——首审必拒、带反馈重做、二审通过后才落盘。
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "${script_dir}/repo"
if go test ./...; then
	echo "PASS: review_reject_retry"
	exit 0
fi
echo "FAIL: review_reject_retry"
exit 1
