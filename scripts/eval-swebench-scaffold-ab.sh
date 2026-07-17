#!/usr/bin/env bash
# eval-swebench-scaffold-ab.sh —— SCAFFOLD_SPEC §7 / S-M 三档对照 + 重复度量协议。
#
# 三档（同一组实例、重复 R 次，压采样噪声）：
#   1) baseline   单发（NBEST=1）——eval-swebench-modeA.sh
#   2) bestofN    best-of-N + 去重投票（A+D，ENABLE_TESTS=0）——eval-swebench-scaffold.sh
#   3) tests      best-of-N + 复现/回归选择（A+D+B+C，ENABLE_TESTS=1）——eval-swebench-scaffold.sh
#
# 主指标 Resolved@1：每档跑 R 次取均值±标准差；仅当提升幅度超出 std 才算「scaffold 有效」。
# 可选 Pass@N（oracle 上限）：对某次 bestofN 的 N 份候选各自判定，任一 resolved 即算——衡量采样上限。
#
# 安全：key 仅 env（DEEPSEEK_API_KEY）。长跑请用 nohup + log 轮询。
#
# 用法：
#   export DEEPSEEK_API_KEY=sk-xxxx
#   nohup bash scripts/eval-swebench-scaffold-ab.sh >/tmp/scaffold-ab.log 2>&1 &
# 可覆盖：AB_N(12) 实例数  REPEATS(3)  NBEST(5)  ARMS("baseline bestofN tests")  COMPUTE_PASSN(0)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
CACHE="$HOME/.cache/cogent-eval"
SWE_FILE="$CACHE/swebench/lite.jsonl"
SWE_VENV="$CACHE/swe-venv/bin/python"
DOCKER_SOCK="unix://$HOME/.colima/default/docker.sock"

AB_N="${AB_N:-12}"
REPEATS="${REPEATS:-3}"
NBEST="${NBEST:-5}"
ARMS="${ARMS:-baseline bestofN tests}"
COMPUTE_PASSN="${COMPUTE_PASSN:-0}"
OUT_DOC="${OUT_DOC:-$REPO_ROOT/eval/doc/swebench-scaffold-ab-$(date +%Y%m%d).md}"

log() { printf '\033[1;35m[SCAF-AB %s]\033[0m %s\n' "$(date '+%H:%M:%S')" "$*"; }
die() { printf '\033[1;31m[ERR]\033[0m %s\n' "$*" >&2; exit 1; }

[ -n "${DEEPSEEK_API_KEY:-}" ] || die "DEEPSEEK_API_KEY 未设置（env-only）"
[ -f "$SWE_FILE" ] || die "缺数据集 $SWE_FILE"

# 跨仓库 round-robin 分层取样 N 个 instance_id（稳定可复现）。
select_ids() {
  python3 - "$SWE_FILE" "$AB_N" <<'PY'
import json, sys
from collections import OrderedDict, deque
path, n = sys.argv[1], int(sys.argv[2])
buckets = OrderedDict()
for line in open(path):
    d = json.loads(line); buckets.setdefault(d["repo"], []).append(d["instance_id"])
queues = [deque(v) for v in buckets.values()]
picked = []
while len(picked) < n and any(queues):
    for q in queues:
        if q and len(picked) < n:
            picked.append(q.popleft())
print(",".join(picked))
PY
}

# 解析一份 swebench 判定报告，输出 "resolved<TAB>denom<TAB>resolved_ids_csv"。
parse_report() {
  local report="$1"
  [ -f "$report" ] || { echo -e "0\t0\t"; return; }
  python3 - "$report" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
res = d.get("resolved_instances", 0)
denom = d.get("completed_instances", 0) or d.get("submitted_instances", 0)
print(f"{res}\t{denom}\t" + ",".join(sorted(d.get('resolved_ids', []))))
PY
}

find_report() { ls "$1"/*."$2".json 2>/dev/null | head -1 || true; }

# 跑 baseline 单发某一次重复，回填全局 REP_REPORT。
run_baseline() {
  local rep="$1" ids="$2"
  local tag="abbase_r${rep}"
  local dir="$REPO_ROOT/eval-artifacts/swebench-modeA-custom-baseline"
  log "arm=baseline rep=$rep 跑单发 ..."
  COGENT_SWEBENCH_SCAFFOLD=0 EVAL_IDS="$ids" bash "$SCRIPT_DIR/eval-swebench-modeA.sh" || true
  REP_REPORT="$(find_report "$dir" "modeA_custom_baseline")"
}

# 跑 scaffold 某一档某一次重复（ENABLE_TESTS 区分 bestofN / tests），回填 REP_REPORT。
run_scaffold_arm() {
  local rep="$1" ids="$2" arm="$3" enable="$4"
  local tag="ab_${arm}_r${rep}"
  local dir="$REPO_ROOT/eval-artifacts/swebench-scaffold-$tag"
  log "arm=$arm rep=$rep 跑 best-of-${NBEST}（tests=$enable）..."
  SCAFFOLD_TAG="$tag" EVAL_IDS="$ids" COGENT_SWEBENCH_NBEST="$NBEST" ENABLE_TESTS="$enable" \
    bash "$SCRIPT_DIR/eval-swebench-scaffold.sh" || true
  REP_REPORT="$(find_report "$dir" "scaffold_${tag//-/_}")"
  LAST_SCAFFOLD_DIR="$dir"
}

# 可选：对某档最后一次的 N 份候选各自判定，算 Pass@N（oracle 上限）。
compute_passn() {
  local dir="$1"
  [ "$COMPUTE_PASSN" = "1" ] || return
  [ -x "$SWE_VENV" ] || return
  log "计算 Pass@N（judge 每个候选，oracle 上限）@ $dir"
  local union=""
  for k in $(seq 1 "$NBEST"); do
    local preds="$dir/scaffold-cand-$k/predictions.jsonl"
    [ -s "$preds" ] || continue
    ( cd "$dir/scaffold-cand-$k" && DOCKER_HOST="$DOCKER_SOCK" "$SWE_VENV" \
        -m swebench.harness.run_evaluation --dataset_name "$SWE_FILE" \
        --predictions_path "$preds" --max_workers 2 --run_id "passn_k$k" ) || true
    local rep; rep="$(find_report "$dir/scaffold-cand-$k" "passn_k$k")"
    [ -f "$rep" ] && union="$union,$(parse_report "$rep" | cut -f3)"
  done
  PASSN_IDS="$(echo "$union" | tr ',' '\n' | sort -u | grep -c . || true)"
}

# 累积各 arm 各 rep 的 "resolved/denom" 到临时文件，供末尾聚合。
STATS="$(mktemp)"
trap 'rm -f "$STATS"' EXIT

main() {
  local ids; ids="$(select_ids)"
  local count; count=$(echo "$ids" | tr ',' '\n' | grep -c . || true)
  log "三档对照：$count 实例 × $REPEATS 次重复；档位=[$ARMS] N=$NBEST"
  log "实例: $ids"

  PASSN_IDS=""
  for rep in $(seq 1 "$REPEATS"); do
    for arm in $ARMS; do
      REP_REPORT=""
      case "$arm" in
        baseline) FORCE=1 run_baseline "$rep" "$ids" ;;
        bestofN)  FORCE=1 run_scaffold_arm "$rep" "$ids" "bestofN" 0 ;;
        tests)    FORCE=1 run_scaffold_arm "$rep" "$ids" "tests" 1 ;;
        *) die "未知档位 $arm" ;;
      esac
      local rd; rd="$(parse_report "$REP_REPORT" | cut -f1,2)"
      echo -e "${arm}\t${rep}\t${rd}" >> "$STATS"
      log "  → arm=$arm rep=$rep resolved/denom = $rd"
    done
  done
  [ "$COMPUTE_PASSN" = "1" ] && compute_passn "${LAST_SCAFFOLD_DIR:-}"

  aggregate "$ids" "$count"
}

# 聚合：各 arm 的 Resolved@1 均值±std，写 markdown 报告。
aggregate() {
  local ids="$1" count="$2"
  log "聚合结果 → $OUT_DOC"
  mkdir -p "$(dirname "$OUT_DOC")"
  STATS_FILE="$STATS" IDS="$ids" COUNT="$count" NBEST="$NBEST" REPEATS="$REPEATS" \
  PASSN="${PASSN_IDS:-}" OUT_DOC="$OUT_DOC" python3 - <<'PY'
import os, statistics
from collections import defaultdict
rows = defaultdict(list)  # arm -> [(res,denom),...]
with open(os.environ["STATS_FILE"]) as f:
    for line in f:
        parts = line.rstrip("\n").split("\t")
        if len(parts) < 4:
            continue
        arm, _rep, res, denom = parts[0], parts[1], int(parts[2] or 0), int(parts[3] or 0)
        rows[arm].append((res, denom))

def rate(res, denom):
    return (res / denom * 100) if denom else 0.0

lines = []
lines.append(f"# SWE-bench Scaffold A/B — 三档对照（{os.environ['COUNT']} 实例 × {os.environ['REPEATS']} 次）\n")
lines.append(f"- N(best-of) = {os.environ['NBEST']}；实例集: `{os.environ['IDS']}`\n")
lines.append("\n| 档位 | 各次 Resolved@1 | 均值 | 标准差 |\n|---|---|---|---|\n")
order = ["baseline", "bestofN", "tests"]
means = {}
for arm in order:
    if arm not in rows:
        continue
    rates = [rate(r, d) for r, d in rows[arm]]
    mean = statistics.mean(rates) if rates else 0.0
    std = statistics.pstdev(rates) if len(rates) > 1 else 0.0
    means[arm] = (mean, std)
    per = ", ".join(f"{x:.1f}%" for x in rates)
    lines.append(f"| {arm} | {per} | {mean:.1f}% | ±{std:.1f} |\n")

if "baseline" in means and "bestofN" in means:
    dm = means["bestofN"][0] - means["baseline"][0]
    std = max(means["baseline"][1], means["bestofN"][1])
    verdict = "有效" if dm > std else "未超噪声"
    lines.append(f"\n- Δ(bestofN − baseline) = {dm:+.1f} 个百分点（噪声 std≈{std:.1f}）→ **{verdict}**\n")
if "tests" in means and "bestofN" in means:
    dm = means["tests"][0] - means["bestofN"][0]
    lines.append(f"- Δ(tests − bestofN) = {dm:+.1f} 个百分点（复现/回归选择相对纯投票的增量）\n")
if os.environ.get("PASSN"):
    lines.append(f"- Pass@N（oracle 上限，去重 resolved 数）= {os.environ['PASSN']}\n")

lines.append("\n> 诚实标准（§7）：仅当 Resolved@1 提升幅度超过重复实验标准差，才判定 scaffold 有效。\n")
open(os.environ["OUT_DOC"], "w").writelines(lines)
print("".join(lines))
PY
  log "完成。报告：$OUT_DOC"
}

main "$@"
