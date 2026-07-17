#!/usr/bin/env bash
# eval-swebench-modeA.sh —— SWE-bench 模式 A 端到端闭环（cogent 产补丁 → 官方 Docker 判定）。
#
# 链路：cogent eval run --dataset=swebench（让 agent 按 issue 改代码，导出 predictions.jsonl）
#      → swebench.harness.run_evaluation（Colima Docker 内应用补丁+隐藏测试判定）→ 解析 resolved 率。
# 模式 A 优势：cogent 侧只产补丁、不在本地跑各仓库测试；判定全交官方 Docker，免复刻 12 个 Python 环境。
#
# 安全：API key 只从环境变量 DEEPSEEK_API_KEY 读取，绝不写入本脚本或任何文件（Secrets: env-only）。
#
# 用法：
#   export DEEPSEEK_API_KEY=sk-xxxx            # 必需，仅走环境变量
#   bash scripts/eval-swebench-modeA.sh        # 默认 small 范围（flask+requests 5 个实例）
#   EVAL_SCOPE=full  bash scripts/eval-swebench-modeA.sh    # 全量 300（会自动扩容 Colima）
#   EVAL_IDS="django__django-11099,psf__requests-2317" bash scripts/eval-swebench-modeA.sh
#   长跑建议：nohup bash scripts/eval-swebench-modeA.sh > /tmp/swebench-modeA.log 2>&1 &
#
# 可覆盖的环境变量（均有默认）：
#   COGENT_MODEL(deepseek-v4-pro) COGENT_LLM_BASE_URL(https://api.deepseek.com/v1)
#   EVAL_SCOPE(small|full) EVAL_IDS(逗号分隔，覆盖 scope) EVAL_LIMIT(0)
#   MAX_ITER(12) MAX_COST(3) MAX_WALL(20m) CONCURRENCY(2) MAX_WORKERS(2) FORCE(0)
set -euo pipefail

# ---- 路径常量 ----
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
CACHE="$HOME/.cache/cogent-eval"
SWE_FILE="$CACHE/swebench/lite.jsonl"
SWE_REPOS="$CACHE/swebench/repos"
SWE_VENV="$CACHE/swe-venv/bin/python"
DOCKER_SOCK="unix://$HOME/.colima/default/docker.sock"
COLIMA_CFG="$HOME/.colima/default/colima.yaml"

# ---- 可调参数（env 覆盖）----
COGENT_MODEL="${COGENT_MODEL:-deepseek-v4-pro}"
COGENT_LLM_BASE_URL="${COGENT_LLM_BASE_URL:-https://api.deepseek.com/v1}"
EVAL_SCOPE="${EVAL_SCOPE:-small}"
EVAL_IDS="${EVAL_IDS:-}"
EVAL_LIMIT="${EVAL_LIMIT:-0}"
MAX_ITER="${MAX_ITER:-12}"
MAX_COST="${MAX_COST:-3}"
MAX_WALL="${MAX_WALL:-20m}"
CONCURRENCY="${CONCURRENCY:-2}"
MAX_WORKERS="${MAX_WORKERS:-2}"
FORCE="${FORCE:-0}"

log() { printf '\033[1;34m[%s]\033[0m %s\n' "$(date '+%H:%M:%S')" "$*"; }
die() { printf '\033[1;31m[ERR]\033[0m %s\n' "$*" >&2; exit 1; }

# ---- 1) 前置检查 ----
preflight() {
  log "前置检查 ..."
  [ -n "${DEEPSEEK_API_KEY:-}" ] || die "环境变量 DEEPSEEK_API_KEY 未设置（key 只走 env，不写文件）"
  [ -f "$SWE_FILE" ]  || die "数据集缺失：${SWE_FILE}（先下载 SWE-bench Lite）"
  [ -d "$SWE_REPOS" ] || die "仓库镜像缺失：${SWE_REPOS}（先下载 12 个仓库镜像）"
  [ -x "$SWE_VENV" ]  || die "swebench venv 缺失：${SWE_VENV}（先建 swe-venv 并 pip install swebench）"
  command -v git >/dev/null || die "git 不在 PATH"
  command -v colima >/dev/null || die "colima 未安装"
}

# ---- 2) 按物理资源自适应，必要时重启 Colima 扩容（仅 full 需要）----
ensure_colima() {
  local need_disk="$1"           # 目标最小 disk(GB)
  local phys_mem_gb phys_cpu col_mem col_cpu cur_disk
  phys_mem_gb=$(( $(sysctl -n hw.memsize) / 1073741824 ))
  phys_cpu=$(sysctl -n hw.ncpu)
  col_mem=$(( phys_mem_gb / 2 )); [ "$col_mem" -gt 12 ] && col_mem=12; [ "$col_mem" -lt 4 ] && col_mem=4
  col_cpu=$(( phys_cpu / 2 ));    [ "$col_cpu" -gt 8 ]  && col_cpu=8;  [ "$col_cpu" -lt 4 ] && col_cpu=4

  # 当前 disk 配置（读 colima.yaml；读不到按 0 处理触发扩容）
  cur_disk=0
  [ -f "$COLIMA_CFG" ] && cur_disk=$(awk -F: '/^disk:/{gsub(/[^0-9]/,"",$2);print $2;exit}' "$COLIMA_CFG")
  [ -z "$cur_disk" ] && cur_disk=0

  if docker_up && [ "${cur_disk:-0}" -ge "$need_disk" ]; then
    log "Colima 已运行且 disk=${cur_disk}G ≥ ${need_disk}G，无需重启"
    return
  fi
  log "调整 Colima：cpu=$col_cpu mem=${col_mem}G disk=${need_disk}G（当前 disk=${cur_disk}G）"
  log "注意：改 disk 会重建 VM，已拉取的镜像将丢失、需重新拉取。"
  colima stop >/dev/null 2>&1 || true
  colima start --cpu "$col_cpu" --memory "$col_mem" --disk "$need_disk" --vm-type=vz --vz-rosetta
}

docker_up() { DOCKER_HOST="$DOCKER_SOCK" docker info >/dev/null 2>&1; }

start_colima_if_down() {
  if docker_up; then log "Colima Docker 已就绪"; return; fi
  log "Colima 未运行，启动中 ..."
  colima start --cpu 4 --memory 8 --disk 100 --vm-type=vz --vz-rosetta
  docker_up || die "Colima 启动后仍无法连接 Docker"
}

# ---- 3) 计算要跑的 instance_ids ----
resolve_ids() {
  if [ -n "$EVAL_IDS" ]; then echo "$EVAL_IDS"; return; fi
  if [ "$EVAL_SCOPE" = "full" ]; then echo ""; return; fi   # full: 不限 id
  # small: flask 前 3 + requests 前 2（小仓库，构建快，验证闭环）
  python3 - "$SWE_FILE" <<'PY'
import json, sys
want = {"pallets/flask": 3, "psf/requests": 2}
got = {k: [] for k in want}
for line in open(sys.argv[1]):
    d = json.loads(line); r = d["repo"]
    if r in want and len(got[r]) < want[r]:
        got[r].append(d["instance_id"])
ids = [i for lst in got.values() for i in lst]
print(",".join(ids))
PY
}

# ---- 4) 构建 cogent ----
build_cogent() {
  log "构建 cogent（GOTOOLCHAIN=auto）..."
  ( cd "$REPO_ROOT" && GOTOOLCHAIN=auto go build -o bin/cogent ./cmd/cogent )
  [ -x "$REPO_ROOT/bin/cogent" ] || die "cogent 构建失败"
}

# ---- 5) Step A：cogent 产 predictions.jsonl ----
produce_predictions() {
  local artifact_dir="$1" ids="$2"
  local preds="$artifact_dir/predictions.jsonl"
  if [ "$FORCE" != "1" ] && [ -s "$preds" ]; then
    log "predictions 已存在，复用（FORCE=1 可强制重跑）：$preds"; return
  fi
  mkdir -p "$artifact_dir"
  log "Step A：cogent 跑 SWE-bench 产补丁 → predictions.jsonl（model=${COGENT_MODEL}）"
  local id_flag=()
  [ -n "$ids" ] && id_flag=(--id "$ids")
  # key/端点/模型名均走 env；--model 与 COGENT_MODEL 对齐（predictions 的 model_name_or_path 取 COGENT_MODEL）
  ( cd "$REPO_ROOT" && \
    DEEPSEEK_API_KEY="$DEEPSEEK_API_KEY" \
    COGENT_MODEL="$COGENT_MODEL" \
    COGENT_LLM_BASE_URL="$COGENT_LLM_BASE_URL" \
    ./bin/cogent eval run \
      --dataset=swebench \
      --swebench-file="$SWE_FILE" \
      --swebench-repos="$SWE_REPOS" \
      --artifact-dir="$artifact_dir" \
      --model="$COGENT_MODEL" \
      --max-iterations="$MAX_ITER" \
      --max-cost="$MAX_COST" \
      --max-wallclock="$MAX_WALL" \
      --n-concurrent="$CONCURRENCY" \
      --limit="$EVAL_LIMIT" \
      "${id_flag[@]}" )
  [ -s "$preds" ] || die "Step A 未产出 predictions.jsonl（检查上面 cogent 日志）"
  log "predictions 就绪：$(wc -l < "$preds" | tr -d ' ') 条 → $preds"
}

# ---- 6) Step B：swebench 官方 Docker 判定 ----
judge() {
  local artifact_dir="$1" run_id="$2"
  local preds="$artifact_dir/predictions.jsonl"
  log "Step B：swebench 官方 Docker 判定（run_id=${run_id}, max_workers=${MAX_WORKERS}）"
  ( cd "$artifact_dir" && \
    DOCKER_HOST="$DOCKER_SOCK" "$SWE_VENV" -m swebench.harness.run_evaluation \
      --dataset_name "$SWE_FILE" \
      --predictions_path "$preds" \
      --max_workers "$MAX_WORKERS" \
      --run_id "$run_id" )
}

# ---- 7) Step C：解析判定报告 ----
summarize() {
  local artifact_dir="$1" run_id="$2"
  # swebench 报告名 = <model_name_or_path>.<run_id>.json，斜杠会被替换为 __
  local report
  report=$(ls "$artifact_dir"/*."$run_id".json 2>/dev/null | head -1 || true)
  [ -z "$report" ] && report=$(ls "$REPO_ROOT"/*."$run_id".json 2>/dev/null | head -1 || true)
  if [ -z "$report" ] || [ ! -f "$report" ]; then
    log "未找到判定报告（run_id=${run_id}）"; return
  fi
  log "判定报告：$report"
  python3 - "$report" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
dataset_n = d.get("total_instances", 0)         # 数据集全量规模（非分母）
sub = d.get("submitted_instances", 0)
comp = d.get("completed_instances", 0)
res = d.get("resolved_instances", 0)
err = d.get("error_instances", 0)
denom = comp or sub or dataset_n                 # 分母用本次实际跑的数
rate = (res / denom * 100) if denom else 0.0
print(f"  dataset={dataset_n} submitted={sub} completed={comp} resolved={res} error={err}  Resolved@1={rate:.1f}% ({res}/{denom})")
if d.get("unresolved_ids"): print("  未解决:", ", ".join(d["unresolved_ids"][:20]))
if d.get("error_ids"):      print("  出错  :", ", ".join(d["error_ids"][:20]))
PY
}

# scaffold_tag 按 COGENT_SWEBENCH_SCAFFOLD 返回 baseline|scaffold（与 Go 侧 scaffoldEnabled 同语义），
# 用于区分 artifact 目录/run_id，避免 A/B 两轮因幂等复用 predictions 而串味。
scaffold_tag() {
  case "$(echo "${COGENT_SWEBENCH_SCAFFOLD:-1}" | tr 'A-Z' 'a-z')" in
    0|false|off|no) echo "baseline" ;;
    *)              echo "scaffold" ;;
  esac
}

main() {
  preflight
  local ids; ids="$(resolve_ids)"
  local tag; tag="$EVAL_SCOPE"; [ -n "$EVAL_IDS" ] && tag="custom"
  tag="${tag}-$(scaffold_tag)"
  local artifact_dir="$REPO_ROOT/eval-artifacts/swebench-modeA-$tag"
  local run_id="modeA_${tag//-/_}"

  log "范围=$EVAL_SCOPE  模型=$COGENT_MODEL  实例=${ids:-<全量>}"
  log "artifact-dir=$artifact_dir  run_id=$run_id"

  if [ "$EVAL_SCOPE" = "full" ]; then
    ensure_colima 200          # 全量：自动扩容到 200G（会重建 VM）
  else
    start_colima_if_down       # 小批：沿用现有 100G，仅在未运行时启动
  fi
  docker_up || die "Docker 未就绪"

  build_cogent
  produce_predictions "$artifact_dir" "$ids"
  judge "$artifact_dir" "$run_id"
  summarize "$artifact_dir" "$run_id"
  log "完成。产物在 ${artifact_dir}（predictions.jsonl / logs/ / 判定报告 json）"
}

main "$@"
