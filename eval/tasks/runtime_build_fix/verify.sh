#!/usr/bin/env bash
# verify.sh 校验 runtime_build_fix 任务是否完成（EVAL_SPEC §4.6 · build）：
# 在 repo 内构建可执行文件——构建成功且产物存在、单测通过方为通过。
# 客观判据：初始态存在编译错误，`go build` 必失败；正确修复后才通过。
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "${script_dir}/repo"

bin="./build_artifact_bin"
rm -f "${bin}"
trap 'rm -f "${bin}"' EXIT

if ! go build -o "${bin}" ./...; then
	echo "FAIL: build error"
	exit 1
fi
if [ ! -x "${bin}" ]; then
	echo "FAIL: build artifact missing"
	exit 1
fi
if ! go test ./...; then
	echo "FAIL: tests"
	exit 1
fi
echo "PASS: runtime_build_fix"
exit 0
