#!/usr/bin/env bash
# eval-swebench-scaffold.sh —— SWE-bench test-time scaling 编排（SCAFFOLD_SPEC S-A + S-D，可选 S-B/C）。
#
# 链路（守 SCAFFOLD_SPEC §3.1 数据流）：
#   1) Sampler(S-A)：对同一组实例跑 cogent N 次（COGENT_TEMPERATURE 采样多样性），
#      各自独立 artifact 产 predictions.jsonl。
#   2) 布局：把 N 份 predictions 里每实例的 model_patch 抽成 candidates/<instance_id>/<k>.diff。
#   3) 选择信号(S-B/C，可选 ENABLE_TESTS=1)：调 eval/scaffold/select_by_tests.py 在实例镜像内跑
#      LLM 自造复现测试 + 自导出回归测试，产 signals/<instance_id>.json（绝不碰隐藏测试，守 §6）。
#   4) Selector(S-D)：cogent eval scaffold-select 纯 Go 去重+投票（+信号硬过滤）选每实例 final patch。
#   5) 判定：swebench 官方 Docker run_evaluation 出 Resolved@1。
#
# 安全：API key 只从环境变量 DEEPSEEK_API_KEY 读取，绝不写文件（Secrets: env-only）。
#
# 用法：
#   export DEEPSEEK_API_KEY=sk-xxxx
#   EVAL_IDS="psf__requests-2317,pallets__flask-4045" bash scripts/eval-swebench-scaffold.sh
#   长跑：把命令写入 /tmp/x.sh 后 nohup bash /tmp/x.sh >/tmp/scaffold.log 2>&1 &
#
# 可覆盖环境变量（均有默认）：
#   COGENT_SWEBENCH_NBEST(5)  采样候选数 N
#   SAMPLE_TEMP(0.7)          best-of-N 采样温度（透传 COGENT_TEMPERATURE；N=1 时置 0）
#   ENABLE_TESTS(0)           是否跑复现/回归选择信号（需 swebench venv+镜像；1 开启 S-B/C）
#   COGENT_SWEBENCH_REPRO_M(5) 每实例复现测试采样数 M（传给 select_by_tests.py）
#   SCAFFOLD_TAG(custom)      产物目录标签
#   COGENT_MODEL(deepseek-v4-pro) COGENT_LLM_BASE_URL(https://api.deepseek.com/v1)
#   EVAL_IDS(必填或 EVAL_SCOPE) EVAL_SCOPE(small) EVAL_LIMIT(0)
#   MAX_ITER(12) MAX_COST(3) MAX_WALL(20m) CONCURRENCY(2) MAX_WORKERS(2) FORCE(0)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
CACHE="$HOME/.cache/cogent-eval"
SWE_FILE="$CACHE/swebench/lite.jsonl"
SWE_REPOS="$CACHE/swebench/repos"
SWE_VENV="$CACHE/swe-venv/bin/python"
DOCKER_SOCK="unix://$HOME/.colima/default/docker.sock"

COGENT_MODEL="${COGENT_MODEL:-deepseek-v4-pro}"
COGENT_LLM_BASE_URL="${COGENT_LLM_BASE_URL:-https://api.deepseek.com/v1}"
NBEST="${COGENT_SWEBENCH_NBEST:-5}"
SAMPLE_TEMP="${SAMPLE_TEMP:-0.7}"
ENABLE_TESTS="${ENABLE_TESTS:-0}"
REPRO_M="${COGENT_SWEBENCH_REPRO_M:-5}"
SCAFFOLD_TAG="${SCAFFOLD_TAG:-custom}"
EVAL_SCOPE="${EVAL_SCOPE:-small}"
EVAL_IDS="${EVAL_IDS:-}"
EVAL_LIMIT="${EVAL_LIMIT:-0}"
MAX_ITER="${MAX_ITER:-12}"
MAX_COST="${MAX_COST:-3}"
MAX_WALL="${MAX_WALL:-20m}"
CONCURRENCY="${CONCURRENCY:-2}"
MAX_WORKERS="${MAX_WORKERS:-2}"
FORCE="${FORCE:-0}"

ARTIFACT_DIR="$REPO_ROOT/eval-artifacts/swebench-scaffold-$SCAFFOLD_TAG"
RUN_ID="scaffold_${SCAFFOLD_TAG//-/_}"

log() { printf '\033[1;36m[SCAF %s]\033[0m %s\n' "$(date '+%H:%M:%S')" "$*"; }
die() { printf '\033[1;31m[ERR]\033[0m %s\n' "$*" >&2; exit 1; }

preflight() {
  log "前置检查 ..."
  [ -n "${DEEPSEEK_API_KEY:-}" ] || die "DEEPSEEK_API_KEY 未设置（key 只走 env）"
  [ -f "$SWE_FILE" ]  || die "数据集缺失：$SWE_FILE"
  [ -d "$SWE_REPOS" ] || die "仓库镜像缺失：$SWE_REPOS"
  command -v git >/dev/null || die "git 不在 PATH"
  command -v python3 >/dev/null || die "python3 不在 PATH"
  command -v colima >/dev/null || die "colima 未安装"
}

docker_up() { DOCKER_HOST="$DOCKER_SOCK" docker info >/dev/null 2>&1; }

start_colima_if_down() {
  if docker_up; then log "Colima Docker 已就绪"; return; fi
  log "Colima 未运行，启动中 ..."
  colima start --cpu 4 --memory 8 --disk 100 --vm-type=vz --vz-rosetta
  docker_up || die "Colima 启动后仍无法连接 Docker"
}

# 全量判定需更大磁盘装 300 条实例镜像：按物理资源自适应，必要时重启 Colima 扩容（改 disk 会重建 VM）。
ensure_colima() {
  local need_disk="$1" phys_mem_gb phys_cpu col_mem col_cpu cur_disk
  local cfg="$HOME/.colima/default/colima.yaml"
  phys_mem_gb=$(( $(sysctl -n hw.memsize) / 1073741824 ))
  phys_cpu=$(sysctl -n hw.ncpu)
  col_mem=$(( phys_mem_gb / 2 )); [ "$col_mem" -gt 12 ] && col_mem=12; [ "$col_mem" -lt 4 ] && col_mem=4
  col_cpu=$(( phys_cpu / 2 ));    [ "$col_cpu" -gt 8 ]  && col_cpu=8;  [ "$col_cpu" -lt 4 ] && col_cpu=4
  cur_disk=0
  [ -f "$cfg" ] && cur_disk=$(awk -F: '/^disk:/{gsub(/[^0-9]/,"",$2);print $2;exit}' "$cfg")
  [ -z "$cur_disk" ] && cur_disk=0
  if docker_up && [ "${cur_disk:-0}" -ge "$need_disk" ]; then
    log "Colima 已运行且 disk=${cur_disk}G ≥ ${need_disk}G，无需重启"; return
  fi
  log "调整 Colima：cpu=$col_cpu mem=${col_mem}G disk=${need_disk}G（当前 disk=${cur_disk}G，会重建 VM）"
  colima stop >/dev/null 2>&1 || true
  colima start --cpu "$col_cpu" --memory "$col_mem" --disk "$need_disk" --vm-type=vz --vz-rosetta
  docker_up || die "Colima 扩容后仍无法连接 Docker"
}

resolve_ids() {
  if [ -n "$EVAL_IDS" ]; then echo "$EVAL_IDS"; return; fi
  if [ "$EVAL_SCOPE" = "full" ]; then echo ""; return; fi   # full：不限 id，跑全量
  python3 - "$SWE_FILE" <<'PY'
import json, sys
want = {"pallets/flask": 3, "psf/requests": 2}
got = {k: [] for k in want}
for line in open(sys.argv[1]):
    d = json.loads(line); r = d["repo"]
    if r in want and len(got[r]) < want[r]:
        got[r].append(d["instance_id"])
print(",".join(i for lst in got.values() for i in lst))
PY
}

build_cogent() {
  log "构建 cogent（GOTOOLCHAIN=auto）..."
  ( cd "$REPO_ROOT" && GOTOOLCHAIN=auto go build -o bin/cogent ./cmd/cogent )
  [ -x "$REPO_ROOT/bin/cogent" ] || die "cogent 构建失败"
}

# 采样第 k 个候选（独立 artifact 目录），产 predictions.jsonl。
sample_one() {
  local k="$1" ids="$2"
  local cand_dir="$ARTIFACT_DIR/scaffold-cand-$k"
  local preds="$cand_dir/predictions.jsonl"
  if [ "$FORCE" != "1" ] && [ -s "$preds" ]; then
    log "候选 #$k 已存在，复用：$preds"; return
  fi
  mkdir -p "$cand_dir"
  local temp="$SAMPLE_TEMP"
  [ "$NBEST" -le 1 ] && temp=0        # 单候选不加温度，保持确定性
  log "采样候选 #$k/${NBEST}（temp=${temp}）→ $cand_dir"
  local id_flag=()
  [ -n "$ids" ] && id_flag=(--id "$ids")
  ( cd "$REPO_ROOT" && \
    DEEPSEEK_API_KEY="$DEEPSEEK_API_KEY" \
    COGENT_MODEL="$COGENT_MODEL" \
    COGENT_LLM_BASE_URL="$COGENT_LLM_BASE_URL" \
    COGENT_TEMPERATURE="$temp" \
    ./bin/cogent eval run \
      --dataset=swebench \
      --swebench-file="$SWE_FILE" \
      --swebench-repos="$SWE_REPOS" \
      --artifact-dir="$cand_dir" \
      --model="$COGENT_MODEL" \
      --max-iterations="$MAX_ITER" \
      --max-cost="$MAX_COST" \
      --max-wallclock="$MAX_WALL" \
      --n-concurrent="$CONCURRENCY" \
      --limit="$EVAL_LIMIT" \
      "${id_flag[@]}" ) || log "候选 #$k 运行返回非零（部分实例可能失败，继续）"
  [ -s "$preds" ] || die "候选 #$k 未产出 predictions.jsonl"
}

# 把 N 份 predictions 的每实例 model_patch 抽为 candidates/<instance_id>/<k>.diff。
layout_candidates() {
  log "布局候选补丁 → $ARTIFACT_DIR/candidates/"
  python3 - "$ARTIFACT_DIR" "$NBEST" <<'PY'
import json, os, sys
root, n = sys.argv[1], int(sys.argv[2])
cand_root = os.path.join(root, "candidates")
os.makedirs(cand_root, exist_ok=True)
count = {}
for k in range(1, n + 1):
    p = os.path.join(root, f"scaffold-cand-{k}", "predictions.jsonl")
    if not os.path.exists(p):
        continue
    for line in open(p):
        line = line.strip()
        if not line:
            continue
        d = json.loads(line)
        iid, patch = d["instance_id"], d.get("model_patch", "")
        if not patch.strip():
            continue
        d_dir = os.path.join(cand_root, iid)
        os.makedirs(d_dir, exist_ok=True)
        with open(os.path.join(d_dir, f"{k}.diff"), "w") as f:
            f.write(patch)
        count[iid] = count.get(iid, 0) + 1
print(f"  {len(count)} instance(s), candidates per instance:",
      ", ".join(f"{i}={c}" for i, c in sorted(count.items())))
PY
}

# 可选：跑复现/回归选择信号（S-B/C），产 signals/<instance_id>.json。
run_test_signals() {
  [ "$ENABLE_TESTS" = "1" ] || { log "ENABLE_TESTS!=1，跳过复现/回归信号（仅 best-of-N + 投票）"; return; }
  [ -x "$SWE_VENV" ] || die "ENABLE_TESTS=1 需 swebench venv：$SWE_VENV"
  log "跑复现/回归选择信号（S-B/C，M=${REPRO_M}）..."
  ( cd "$ARTIFACT_DIR" && \
    DEEPSEEK_API_KEY="$DEEPSEEK_API_KEY" \
    COGENT_LLM_BASE_URL="$COGENT_LLM_BASE_URL" \
    COGENT_MODEL="$COGENT_MODEL" \
    DOCKER_HOST="$DOCKER_SOCK" \
    COGENT_SWEBENCH_REPRO_M="$REPRO_M" \
    "$SWE_VENV" "$REPO_ROOT/eval/scaffold/select_by_tests.py" \
      --dataset "$SWE_FILE" \
      --artifact-dir "$ARTIFACT_DIR" ) || log "选择信号生成部分失败（缺信号的实例退化为纯投票）"
}

# Selector（S-D）：纯 Go 去重+投票（+信号过滤）选出每实例 final patch。
select_final() {
  log "Selector 选择 final patch（去重+投票+信号过滤）..."
  ( cd "$REPO_ROOT" && ./bin/cogent eval scaffold-select \
      --artifact-dir "$ARTIFACT_DIR" \
      --model "$COGENT_MODEL" )
  [ -s "$ARTIFACT_DIR/predictions.jsonl" ] || die "scaffold-select 未产出 predictions.jsonl"
}

judge() {
  [ -x "$SWE_VENV" ] || { log "无 swebench venv，跳过官方判定（仅产 predictions.jsonl）"; return; }
  log "官方 Docker 判定（run_id=${RUN_ID}）..."
  ( cd "$ARTIFACT_DIR" && \
    DOCKER_HOST="$DOCKER_SOCK" "$SWE_VENV" -m swebench.harness.run_evaluation \
      --dataset_name "$SWE_FILE" \
      --predictions_path "$ARTIFACT_DIR/predictions.jsonl" \
      --max_workers "$MAX_WORKERS" \
      --run_id "$RUN_ID" ) || log "判定返回非零（部分实例可能 error）"
}

summarize() {
  local report
  report=$(ls "$ARTIFACT_DIR"/*."$RUN_ID".json 2>/dev/null | head -1 || true)
  [ -z "$report" ] && report=$(ls "$REPO_ROOT"/*."$RUN_ID".json 2>/dev/null | head -1 || true)
  [ -f "$report" ] || { log "未找到判定报告"; return; }
  log "判定报告：$report"
  python3 - "$report" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
res = d.get("resolved_instances", 0)
comp = d.get("completed_instances", 0) or d.get("submitted_instances", 0)
rate = (res / comp * 100) if comp else 0.0
print(f"  Resolved@1 = {rate:.1f}% ({res}/{comp})  error={d.get('error_instances',0)}")
if d.get("unresolved_ids"): print("  未解决:", ", ".join(d["unresolved_ids"][:20]))
PY
}

main() {
  preflight
  local ids; ids="$(resolve_ids)"
  local count; count=$(echo "$ids" | tr ',' '\n' | grep -c . || true)
  log "范围=${ids:-<全量>}（$count 实例）N=$NBEST temp=$SAMPLE_TEMP tests=$ENABLE_TESTS"
  log "产物目录=$ARTIFACT_DIR"
  if [ "$EVAL_SCOPE" = "full" ] && [ -z "$EVAL_IDS" ]; then
    ensure_colima 200          # 全量：判定 300 条镜像需 ~200G（会重建 VM）
  else
    start_colima_if_down
  fi
  build_cogent
  for k in $(seq 1 "$NBEST"); do sample_one "$k" "$ids"; done
  layout_candidates
  run_test_signals
  select_final
  judge
  summarize
  log "完成。final predictions=$ARTIFACT_DIR/predictions.jsonl，过程报告=$ARTIFACT_DIR/scaffold-select-report.json"
}

main "$@"
