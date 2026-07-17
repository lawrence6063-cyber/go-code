#!/usr/bin/env bash
# eval-swebench-ab.sh —— SWE-bench scaffold A/B 对照实验。
# 同一组实例跑两轮：baseline(COGENT_SWEBENCH_SCAFFOLD=0) vs scaffold(默认开)，对比 Resolved@1。
#
# 实例选取：跨 12 个仓库 round-robin 分层取样（稳定可复现、避免全是 django/sympy 重仓库）。
# 复用 eval-swebench-modeA.sh 跑每一轮（artifact/run_id 已按 scaffold 开关区分，两轮互不覆盖）。
#
# 用法：
#   export DEEPSEEK_API_KEY=sk-xxxx
#   nohup bash scripts/eval-swebench-ab.sh > /tmp/swebench-ab.log 2>&1 &
# 可覆盖：AB_N(30) 取样实例数；其余（模型/预算/并发）沿用 eval-swebench-modeA.sh 默认。
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
CACHE="$HOME/.cache/cogent-eval"
SWE_FILE="$CACHE/swebench/lite.jsonl"
AB_N="${AB_N:-30}"

log() { printf '\033[1;35m[AB %s]\033[0m %s\n' "$(date '+%H:%M:%S')" "$*"; }

[ -n "${DEEPSEEK_API_KEY:-}" ] || { echo "[ERR] DEEPSEEK_API_KEY 未设置（env-only）" >&2; exit 1; }
[ -f "$SWE_FILE" ] || { echo "[ERR] 缺 $SWE_FILE" >&2; exit 1; }

# 跨仓库 round-robin 分层取样 N 个 instance_id（稳定顺序，可复现）
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
        if q:
            picked.append(q.popleft())
            if len(picked) >= n:
                break
print(",".join(picked))
PY
}

# 解析某轮报告的 resolved 数与 id 集合
parse_round() {
  local artifact_dir="$1" run_id="$2"
  local report
  report=$(ls "$artifact_dir"/*."$run_id".json 2>/dev/null | head -1 || true)
  [ -f "$report" ] || { echo "NO_REPORT"; return; }
  python3 - "$report" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
sub = d.get("submitted_instances", 0); comp = d.get("completed_instances", 0)
res = d.get("resolved_instances", 0); err = d.get("error_instances", 0)
denom = comp or sub
rate = (res / denom * 100) if denom else 0.0
print(f"{res}\t{denom}\t{err}\t{rate:.1f}")
print(",".join(sorted(d.get("resolved_ids", []))))
PY
}

main() {
  local ids; ids="$(select_ids)"
  local count; count=$(echo "$ids" | tr ',' '\n' | grep -c . || true)
  log "对照实验：${count} 个实例（跨仓库分层）"
  log "实例: $ids"

  log "===== 轮 1/2：baseline（scaffold OFF）====="
  COGENT_SWEBENCH_SCAFFOLD=0 EVAL_IDS="$ids" bash "$SCRIPT_DIR/eval-swebench-modeA.sh"

  log "===== 轮 2/2：scaffold（默认 ON）====="
  COGENT_SWEBENCH_SCAFFOLD=1 EVAL_IDS="$ids" bash "$SCRIPT_DIR/eval-swebench-modeA.sh"

  # 汇总对比
  local base_dir="$REPO_ROOT/eval-artifacts/swebench-modeA-custom-baseline"
  local scaf_dir="$REPO_ROOT/eval-artifacts/swebench-modeA-custom-scaffold"
  local b s
  b=$(parse_round "$base_dir" "modeA_custom_baseline")
  s=$(parse_round "$scaf_dir" "modeA_custom_scaffold")
  log "===== A/B 结果 ====="
  BASE="$b" SCAF="$s" python3 - <<'PY'
import os
def head(x): return x.split("\n")[0].split("\t")
def ids(x):
    p = x.split("\n"); return set(p[1].split(",")) if len(p) > 1 and p[1] else set()
b, s = os.environ["BASE"], os.environ["SCAF"]
if b.startswith("NO_REPORT") or s.startswith("NO_REPORT"):
    print("  某轮报告缺失，无法对比"); raise SystemExit
br, bd, be, brate = head(b); sr, sd, se, srate = head(s)
print(f"  baseline (scaffold OFF): resolved {br}/{bd}  error={be}  Resolved@1={brate}%")
print(f"  scaffold (scaffold ON ): resolved {sr}/{sd}  error={se}  Resolved@1={srate}%")
print(f"  Δ Resolved@1 = {float(srate)-float(brate):+.1f} 个百分点")
bi, si = ids(b), ids(s)
if si - bi: print("  scaffold 新解决:", ", ".join(sorted(si - bi)))
if bi - si: print("  scaffold 反而丢失:", ", ".join(sorted(bi - si)))
if bi & si: print("  两者都解决:", ", ".join(sorted(bi & si)))
PY
  log "完成。产物：$base_dir 与 $scaf_dir"
}

main "$@"
