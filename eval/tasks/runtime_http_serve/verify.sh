#!/usr/bin/env bash
# verify.sh 校验 runtime_http_serve 任务是否完成（EVAL_SPEC §4.6 · serve）：
# 在 repo 内运行单测——测试用 httptest 起真实回环 HTTP 服务、做真实客户端往返，
# 断言 /greet 的状态码与响应体。退出码 0 表示通过。
# 客观判据：初始态处理器返回占位响应体，断言必失败；正确补全后才通过。
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "${script_dir}/repo"

if go test ./...; then
	echo "PASS: runtime_http_serve"
	exit 0
fi
echo "FAIL: runtime_http_serve"
exit 1
