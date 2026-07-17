#!/usr/bin/env bash
# eval-swebench-lite-full.sh —— SWE-bench Lite 全量（300）N=3 纯投票 省成本实验编排（不跑 A/B）。
#
# 为什么分片：单轮采样是「整批跑完才写 predictions.jsonl」，300 条一把梭中断即丢整轮。
# 本 driver 把 300 条按稳定顺序切成若干片，每片独立完整跑 eval-swebench-scaffold.sh（采样→选择→判定），
# 断点续跑友好（片级幂等：该片最终判定报告已存在则跳过），最后聚合各片得到全量 Resolved@1。
#
# 档位：单档 scaffold，N=3，ENABLE_TESTS=0（纯投票，不开 Docker 复现/回归信号）——性价比甜点。
#
# 安全：key 仅 env（DEEPSEEK_API_KEY）。长跑请 nohup + log 轮询。
#
# 用法：
#   export DEEPSEEK_API_KEY=sk-xxxx
#   nohup bash scripts/eval-swebench-lite-full.sh >/tmp/swe-lite-full.log 2>&1 &
#   tail -f /tmp/swe-lite-full.log
# 可覆盖：SHARD_SIZE(50) NBEST(3) SAMPLE_TEMP(0.7) MAX_ITER(12) MAX_COST(1) MAX_WALL(15m)
#         CONCURRENCY(4) MAX_WORKERS(4) COGENT_MODEL(deepseek-v4-pro)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
CACHE="$HOME/.cache/cogent-eval"
SWE_FILE="$CACHE/swebench/lite.jsonl"
DOCKER_SOCK="unix://$HOME/.colima/default/docker.sock"
COLIMA_CFG="$HOME/.colima/default/colima.yaml"

SHARD_SIZE="${SHARD_SIZE:-50}"
NBEST="${NBEST:-3}"
SAMPLE_TEMP="${SAMPLE_TEMP:-0.7}"
MAX_ITER="${MAX_ITER:-12}"
MAX_COST="${MAX_COST:-1}"
MAX_WALL="${MAX_WALL:-15m}"
CONCURRENCY="${CONCURRENCY:-4}"
MAX_WORKERS="${MAX_WORKERS:-4}"
COGENT_MODEL="${COGENT_MODEL:-deepseek-v4-pro}"
OUT_DOC="${OUT_DOC:-$REPO_ROOT/eval/doc/swebench-lite-full-n3-result-$(date +%Y%m%d).md}"

log() { printf '\033[1;32m[FULL %s]\033[0m %s\n' "$(date '+%H:%M:%S')" "$*"; }
die() { printf '\033[1;31m[ERR]\033[0m %s\n' "$*" >&2; exit 1; }

[ -n "${DEEPSEEK_API_KEY:-}" ] || die "DEEPSEEK_API_KEY 未设置（env-only）"
[ -f "$SWE_FILE" ] || die "缺数据集 $SWE_FILE"
command -v colima >/dev/null || die "colima 未安装"

docker_up() { DOCKER_HOST="$DOCKER_SOCK" docker info >/dev/null 2>&1; }

# 全量镜像需 ~200G：起手把 Colima 扩到 200G（后续各片复用，不再重建）。
ensure_colima_200() {
  local phys_mem_gb phys_cpu col_mem col_cpu cur_disk
  phys_mem_gb=$(( $(sysctl -n hw.memsize) / 1073741824 ))
  phys_cpu=$(sysctl -n hw.ncpu)
  col_mem=$(( phys_mem_gb / 2 )); [ "$col_mem" -gt 12 ] && col_mem=12; [ "$col_mem" -lt 4 ] && col_mem=4
  col_cpu=$(( phys_cpu / 2 ));    [ "$col_cpu" -gt 8 ]  && col_cpu=8;  [ "$col_cpu" -lt 4 ] && col_cpu=4
  cur_disk=0
  [ -f "$COLIMA_CFG" ] && cur_disk=$(awk -F: '/^disk:/{gsub(/[^0-9]/,"",$2);print $2;exit}' "$COLIMA_CFG")
  [ -z "$cur_disk" ] && cur_disk=0
  if docker_up && [ "${cur_disk:-0}" -ge 200 ]; then
    log "Colima 已运行且 disk=${cur_disk}G≥200G，复用"; return
  fi
  log "扩容 Colima：cpu=$col_cpu mem=${col_mem}G disk=200G（会重建 VM，已有镜像丢失需重拉）"
  colima stop >/dev/null 2>&1 || true
  colima start --cpu "$col_cpu" --memory "$col_mem" --disk 200 --vm-type=vz --vz-rosetta
  docker_up || die "Colima 扩容后仍无法连接 Docker"
}

# 按稳定顺序把全部 instance_id 切片，第 i 片（1-based）输出到 stdout（逗号分隔）。
shard_ids() {
  local idx="$1"
  python3 - "$SWE_FILE" "$SHARD_SIZE" "$idx" <<'PY'
import json, sys
path, size, idx = sys.argv[1], int(sys.argv[2]), int(sys.argv[3])
ids = [json.loads(l)["instance_id"] for l in open(path) if l.strip() and not l.startswith("#")]
start = (idx - 1) * size
chunk = ids[start:start + size]
print(",".join(chunk))
PY
}

num_shards() {
  python3 - "$SWE_FILE" "$SHARD_SIZE" <<'PY'
import json, math, sys
path, size = sys.argv[1], int(sys.argv[2])
n = sum(1 for l in open(path) if l.strip() and not l.startswith("#"))
print(max(1, math.ceil(n / size)))
PY
}

find_report() { ls "$1"/*."$2".json 2>/dev/null | head -1 || true; }

run_shard() {
  local i="$1" ids="$2"
  local tag="lite-full-s${i}"
  local dir="$REPO_ROOT/eval-artifacts/swebench-scaffold-$tag"
  local run_id="scaffold_lite_full_s${i}"
  if [ -f "$(find_report "$dir" "$run_id")" ]; then
    log "片 #$i 判定报告已存在，跳过（断点续跑）"; return
  fi
  log "片 #$i 跑 best-of-$NBEST 纯投票（$(echo "$ids" | tr ',' '\n' | grep -c .) 实例）..."
  SCAFFOLD_TAG="$tag" \
  EVAL_IDS="$ids" \
  COGENT_SWEBENCH_NBEST="$NBEST" \
  SAMPLE_TEMP="$SAMPLE_TEMP" \
  ENABLE_TESTS=0 \
  COGENT_MODEL="$COGENT_MODEL" \
  MAX_ITER="$MAX_ITER" MAX_COST="$MAX_COST" MAX_WALL="$MAX_WALL" \
  CONCURRENCY="$CONCURRENCY" MAX_WORKERS="$MAX_WORKERS" \
    bash "$SCRIPT_DIR/eval-swebench-scaffold.sh" || log "片 #$i 返回非零（部分实例可能失败，继续）"
}

# 聚合所有片的判定报告 → 全量 Resolved@1，写 markdown。
aggregate() {
  local shards="$1"
  log "聚合 $shards 片结果 → $OUT_DOC"
  mkdir -p "$(dirname "$OUT_DOC")"
  local reports=()
  for i in $(seq 1 "$shards"); do
    local dir="$REPO_ROOT/eval-artifacts/swebench-scaffold-lite-full-s${i}"
    local r; r="$(find_report "$dir" "scaffold_lite_full_s${i}")"
    [ -f "$r" ] && reports+=("$r")
  done
  [ "${#reports[@]}" -gt 0 ] || { log "无任何片报告，跳过聚合"; return; }
  NBEST="$NBEST" MODEL="$COGENT_MODEL" OUT_DOC="$OUT_DOC" python3 - "${reports[@]}" <<'PY'
import json, os, sys
resolved, denom, err = 0, 0, 0
res_ids, unres_ids = [], []
for p in sys.argv[1:]:
    d = json.load(open(p))
    resolved += d.get("resolved_instances", 0)
    denom += d.get("completed_instances", 0) or d.get("submitted_instances", 0)
    err += d.get("error_instances", 0)
    res_ids += d.get("resolved_ids", [])
    unres_ids += d.get("unresolved_ids", [])
rate = (resolved / denom * 100) if denom else 0.0
lines = [
    f"# SWE-bench Lite 全量 · N={os.environ['NBEST']} 纯投票 结果（{len(sys.argv)-1} 片聚合）\n\n",
    f"- 模型: `{os.environ['MODEL']}`；档位: best-of-{os.environ['NBEST']} + 去重多数投票（ENABLE_TESTS=0）\n",
    f"- **Resolved@1 = {rate:.1f}%（{resolved}/{denom}）**  error={err}\n\n",
    f"- 已解决实例数: {len(set(res_ids))}；未解决: {len(set(unres_ids))}\n",
]
open(os.environ["OUT_DOC"], "w").writelines(lines)
print("".join(lines))
PY
  log "完成。结果文档：$OUT_DOC"
}

# 每片判定后回收磁盘：删该片大体积 SWE-bench 实例镜像 + 候选/工作区副本（保留判定报告与 predictions）。
# 仅在该片已产出判定报告时才删候选（失败片保留以便排查/续跑）；docker 清理无条件执行（安全）。
cleanup_shard() {
  local i="$1"
  local dir="$REPO_ROOT/eval-artifacts/swebench-scaffold-lite-full-s${i}"
  log "片 #$i 清理磁盘：删实例镜像 + 停止容器 + 清工作区副本 ..."
  DOCKER_HOST="$DOCKER_SOCK" docker container prune -f >/dev/null 2>&1 || true
  # 先用 || true 捕获镜像列表（避免 grep 无匹配在 set -e+pipefail 下崩脚本），再循环删（无管道）。
  local imgs
  imgs=$(DOCKER_HOST="$DOCKER_SOCK" docker images --format '{{.Repository}}:{{.Tag}}' 2>/dev/null | grep '^sweb\.eval' || true)
  if [ -n "$imgs" ]; then
    while IFS= read -r img; do
      [ -n "$img" ] || continue
      DOCKER_HOST="$DOCKER_SOCK" docker rmi -f "$img" >/dev/null 2>&1 || true
    done <<< "$imgs"
  fi
  DOCKER_HOST="$DOCKER_SOCK" docker image prune -f >/dev/null 2>&1 || true   # 清悬垂层
  rm -rf "${TMPDIR:-/tmp}/cogent-eval" 2>/dev/null || true                    # agent 工作区副本
  if [ -f "$(find_report "$dir" "scaffold_lite_full_s${i}")" ]; then
    rm -rf "$dir"/scaffold-cand-* "$dir"/candidates 2>/dev/null || true       # 判定已留报告，候选可删
  fi
}

main() {
  local shards; shards="$(num_shards)"
  log "SWE-bench Lite 全量：分 ${shards} 片（每片 ${SHARD_SIZE}），N=${NBEST} 纯投票，不跑 A/B"
  ensure_colima_200
  for i in $(seq 1 "$shards"); do
    local ids; ids="$(shard_ids "$i")"
    [ -n "$ids" ] || continue
    run_shard "$i" "$ids"
    cleanup_shard "$i"
  done
  aggregate "$shards"
}

main "$@"
