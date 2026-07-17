#!/usr/bin/env python3
"""select_by_tests.py — SWE-bench 复现/回归选择信号生成器（SCAFFOLD_SPEC S-B/C 的执行侧）.

在 best-of-N 候选补丁之上，为每个实例产出可执行选择信号 signals/<instance_id>.json，喂给
Go 侧纯 Selector（cogent eval scaffold-select）做硬过滤 + 去重投票。信号包含每个候选的:
  - applied:       补丁能否在实例镜像内干净 git apply
  - have_repro/repro_passed:   LLM 自造复现测试（依据 issue 文本）在打补丁后是否通过
  - have_regress/regression_ok: 自导出的「基线通过测试子集」在打补丁后是否仍通过

合规红线（SCAFFOLD_SPEC §6，务必遵守）:
  * 绝不读取/使用 instance 的 test_patch / FAIL_TO_PASS / PASS_TO_PASS（判定专用隐藏信息）。
    本脚本加载数据集后**主动删除**这些键，从源头杜绝误用。
  * 复现测试 = LLM 依据 issue 文本自造；回归集 = 在 base 镜像内自行跑测试导出的通过子集。
  * Docker 内执行不经 shell 拼接不可信输入；密钥仅 env（DEEPSEEK_API_KEY）。

鲁棒性: 任一环节不可用（无 Docker / 无镜像 / LLM 失败）时，对应信号缺省不产出——Go Selector 对
缺失信号「不否决」，自动退化为纯投票。故本脚本永不阻断主链路。

依赖: 仅标准库 + 可选 swebench（用于取实例镜像名）；LLM 调用走 urllib（OpenAI 兼容 /chat/completions）。
用法:
  DOCKER_HOST=unix://$HOME/.colima/default/docker.sock DEEPSEEK_API_KEY=sk-xxx \\
    python3 eval/scaffold/select_by_tests.py --dataset lite.jsonl --artifact-dir <dir>
"""

import argparse
import json
import os
import re
import subprocess
import sys
import tempfile
import urllib.error
import urllib.request
from collections import Counter
from pathlib import Path

# 隐藏判定信息键：加载后立即剔除，确保选择信号绝不依赖它们（合规红线）。
FORBIDDEN_KEYS = ("test_patch", "FAIL_TO_PASS", "PASS_TO_PASS")

# 单容器命令墙钟上限（秒），防卡死。
DOCKER_TIMEOUT = 900
# 回归子集最多保留的测试节点数（控制耗时）。
MAX_REGRESSION_TESTS = 20
# 复现测试采样数默认（可被 COGENT_SWEBENCH_REPRO_M 覆盖）。
DEFAULT_REPRO_M = 5


def log(msg):
    """打印带前缀的进度日志到 stderr（stdout 留给结构化输出）。"""
    print(f"[select_by_tests] {msg}", file=sys.stderr, flush=True)


# --------------------------------------------------------------------------- #
# 数据集加载（含合规剔除）
# --------------------------------------------------------------------------- #
def load_instances(dataset_path, wanted_ids):
    """加载数据集 JSONL，仅保留 wanted_ids，并剔除隐藏判定键。返回 {id: instance}."""
    out = {}
    with open(dataset_path) as fh:
        for line in fh:
            line = line.strip()
            if not line or line.startswith("#"):
                continue
            inst = json.loads(line)
            iid = inst.get("instance_id")
            if iid not in wanted_ids:
                continue
            for k in FORBIDDEN_KEYS:  # 合规：从内存中彻底移除隐藏判定信息
                inst.pop(k, None)
            out[iid] = inst
    return out


def discover_candidate_ids(artifact_dir):
    """扫描 candidates/ 下的实例目录，返回有候选补丁的 instance_id 集合。"""
    cand_root = Path(artifact_dir) / "candidates"
    if not cand_root.is_dir():
        return set()
    return {p.name for p in cand_root.iterdir() if p.is_dir()}


def read_candidates(artifact_dir, iid):
    """读取某实例的候选补丁，返回按序号升序的 [(index, patch_text), ...]."""
    d = Path(artifact_dir) / "candidates" / iid
    items = []
    for p in sorted(d.glob("*.diff")):
        try:
            idx = int(p.stem)
        except ValueError:
            continue
        text = p.read_text()
        if text.strip():
            items.append((idx, text))
    items.sort(key=lambda x: x[0])
    return items


# --------------------------------------------------------------------------- #
# Docker 辅助
# --------------------------------------------------------------------------- #
def docker_available():
    """探测 docker 是否可用（DOCKER_HOST 由调用方设置）。"""
    try:
        subprocess.run(["docker", "info"], capture_output=True, timeout=30, check=True)
        return True
    except Exception:  # noqa: BLE001 — 探活失败一律视为不可用
        return False


def instance_image(inst):
    """取实例的 swebench 镜像名。优先用 swebench.test_spec；不可用则按官方命名约定回退。"""
    iid = inst["instance_id"]
    try:
        from swebench.harness.test_spec.test_spec import make_test_spec  # type: ignore

        spec = make_test_spec(inst)
        return spec.instance_image_key
    except Exception:  # noqa: BLE001
        # 官方命名: sweb.eval.<arch>.<instance_id 小写，':' 与 '__' 保留>:latest
        arch = "arm64" if os.uname().machine in ("arm64", "aarch64") else "x86_64"
        return f"sweb.eval.{arch}.{iid.lower()}:latest"


def image_exists(image):
    """报告本地是否已构建该镜像（不自动构建，缺失即跳过该实例信号）。"""
    r = subprocess.run(["docker", "image", "inspect", image], capture_output=True)
    return r.returncode == 0


def run_in_container(image, script, mount_dir):
    """在 image 内以 bash -lc 执行 script（mount_dir 挂到 /scaffold）。返回 (rc, stdout+stderr)."""
    cmd = [
        "docker", "run", "--rm", "--network", "none",
        "-v", f"{mount_dir}:/scaffold:ro",
        image, "bash", "-lc", script,
    ]
    try:
        r = subprocess.run(cmd, capture_output=True, text=True, timeout=DOCKER_TIMEOUT)
        return r.returncode, (r.stdout + r.stderr)
    except subprocess.TimeoutExpired:
        return -1, "timeout"
    except Exception as exc:  # noqa: BLE001
        return -1, f"docker error: {exc}"


# swebench 镜像内的仓库路径与激活测试环境的前置命令（conda env testbed）。
TESTBED = "/testbed"
ACTIVATE = "source /opt/miniconda3/bin/activate testbed 2>/dev/null || true"


# --------------------------------------------------------------------------- #
# S-B: 复现测试生成（ReproGen）
# --------------------------------------------------------------------------- #
def llm_chat(prompt, system):
    """调用 OpenAI 兼容 /chat/completions（deepseek）。失败返回 None。密钥仅 env。"""
    base = os.environ.get("COGENT_LLM_BASE_URL", "https://api.deepseek.com/v1").rstrip("/")
    key = os.environ.get("DEEPSEEK_API_KEY") or os.environ.get("OPENAI_API_KEY")
    model = os.environ.get("COGENT_MODEL", "deepseek-chat")
    if not key:
        return None
    body = json.dumps({
        "model": model,
        "messages": [
            {"role": "system", "content": system},
            {"role": "user", "content": prompt},
        ],
        "temperature": 0.7,
    }).encode()
    req = urllib.request.Request(
        f"{base}/chat/completions", data=body,
        headers={"Authorization": f"Bearer {key}", "Content-Type": "application/json"},
    )
    try:
        with urllib.request.urlopen(req, timeout=120) as resp:
            data = json.load(resp)
        return data["choices"][0]["message"]["content"]
    except (urllib.error.URLError, KeyError, json.JSONDecodeError, TimeoutError) as exc:
        log(f"LLM 调用失败: {exc}")
        return None


REPRO_SYSTEM = (
    "You are a senior engineer writing a MINIMAL failing test that reproduces a reported bug. "
    "Output ONLY a single self-contained pytest test file (Python), no prose, no markdown fences. "
    "The test MUST fail on the buggy code and pass once the bug is fixed. "
    "Import from the repository's public API only; do not reference any hidden or private test files."
)


def extract_code(text):
    """从 LLM 回复中提取代码：去除 markdown 围栏，返回纯 Python 源。"""
    if text is None:
        return None
    m = re.search(r"```(?:python)?\s*(.*?)```", text, re.DOTALL)
    code = m.group(1) if m else text
    return code.strip() or None


def gen_repro_test(inst, m):
    """采样 M 个复现测试，按归一化一致性选出出现最多的一个（对齐 Agentless 多数投票）。"""
    problem = (inst.get("problem_statement") or "").strip()
    if not problem:
        return None
    prompt = (
        f"Repository: {inst.get('repo')}\n\n"
        f"Bug report / issue:\n{problem}\n\n"
        "Write one minimal pytest test that reproduces this bug."
    )
    samples = []
    for _ in range(max(1, m)):
        code = extract_code(llm_chat(prompt, REPRO_SYSTEM))
        if code:
            samples.append(code)
    if not samples:
        return None
    # 一致性选择: 归一化（去空白）后取最常见者的原始代码。
    norm = [re.sub(r"\s+", " ", s) for s in samples]
    winner_norm, _ = Counter(norm).most_common(1)[0]
    for s, n in zip(samples, norm):
        if n == winner_norm:
            return s
    return samples[0]


# --------------------------------------------------------------------------- #
# S-C: 回归集自导出（RegressionSet）
# --------------------------------------------------------------------------- #
def derive_regression(image, mount_dir):
    """在 base 镜像内收集「当前能通过」的测试节点子集（自导出，绝不读 PASS_TO_PASS）。

    做法: 收集 /testbed 下已存在的测试，跑一遍，保留通过的节点（截断到 MAX_REGRESSION_TESTS）。
    返回节点 id 列表；失败返回 []（无回归信号，Selector 不据此否决）。
    """
    script = f"""
set -e
{ACTIVATE}
cd {TESTBED}
python -m pytest --collect-only -q 2>/dev/null | grep '::' | head -n 200 > /tmp/nodes.txt || true
if [ ! -s /tmp/nodes.txt ]; then echo '__NO_NODES__'; exit 0; fi
python -m pytest $(cat /tmp/nodes.txt | tr '\\n' ' ') -q --no-header -p no:cacheprovider 2>/dev/null \
  --tb=no > /tmp/res.txt 2>&1 || true
# 打印在 base 上通过（PASSED）的节点
python - <<'PYIN'
import re
passed = []
for line in open('/tmp/res.txt', errors='ignore'):
    m = re.match(r'(\\S+::\\S+)\\s+PASSED', line)
    if m:
        passed.append(m.group(1))
for n in passed[:{MAX_REGRESSION_TESTS}]:
    print(n)
PYIN
"""
    rc, out = run_in_container(image, script, mount_dir)
    if rc != 0:
        return []
    nodes = [ln.strip() for ln in out.splitlines()
             if "::" in ln and " " not in ln.strip() and not ln.startswith("__")]
    return nodes[:MAX_REGRESSION_TESTS]


# --------------------------------------------------------------------------- #
# TestRunner: 对单个候选跑 apply + 复现 + 回归
# --------------------------------------------------------------------------- #
def run_candidate(image, mount_dir, patch_name, repro_name, regression):
    """在实例镜像内对一个候选补丁跑信号，返回 dict（applied/repro/regression）。"""
    sig = {"applied": False,
           "have_repro": repro_name is not None,
           "repro_passed": False,
           "have_regress": bool(regression),
           "regression_ok": False}

    repro_cmd = ""
    if repro_name:
        repro_cmd = (f"cp /scaffold/{repro_name} {TESTBED}/_scaffold_repro_test.py && "
                     f"python -m pytest {TESTBED}/_scaffold_repro_test.py -q --no-header "
                     f"-p no:cacheprovider --tb=no > /tmp/repro.txt 2>&1; "
                     f"echo REPRO_RC=$?")
    regress_cmd = ""
    if regression:
        nodes = " ".join(_shell_safe(n) for n in regression)
        regress_cmd = (f"python -m pytest {nodes} -q --no-header -p no:cacheprovider "
                       f"--tb=no > /tmp/reg.txt 2>&1; echo REG_RC=$?")

    script = f"""
{ACTIVATE}
cd {TESTBED}
git checkout -q . 2>/dev/null || true
git clean -qfd 2>/dev/null || true
if git apply --check /scaffold/{patch_name} 2>/dev/null && git apply /scaffold/{patch_name} 2>/dev/null; then
  echo APPLIED=1
else
  echo APPLIED=0
  exit 0
fi
{repro_cmd}
{regress_cmd}
"""
    rc, out = run_in_container(image, script, mount_dir)
    if "APPLIED=1" not in out:
        return sig
    sig["applied"] = True
    if repro_name:
        sig["repro_passed"] = "REPRO_RC=0" in out
    if regression:
        sig["regression_ok"] = "REG_RC=0" in out
    return sig


def _shell_safe(node):
    """对测试节点 id 做最小 shell 转义（只允许安全字符，防命令注入 RCE）。"""
    if re.fullmatch(r"[A-Za-z0-9_./:\-\[\]]+", node):
        return node
    return "'" + node.replace("'", "'\\''") + "'"


# --------------------------------------------------------------------------- #
# 主流程
# --------------------------------------------------------------------------- #
def process_instance(inst, artifact_dir, m, docker_ok):
    """为单个实例生成信号并落盘 signals/<id>.json。"""
    iid = inst["instance_id"]
    cands = read_candidates(artifact_dir, iid)
    if not cands:
        return
    art = Path(artifact_dir)

    # S-B: 复现测试（LLM）
    repro_code = gen_repro_test(inst, m)
    if repro_code:
        (art / "repro").mkdir(exist_ok=True)
        (art / "repro" / f"{iid}.py").write_text(repro_code)

    image = instance_image(inst)
    can_docker = docker_ok and image_exists(image)
    if not can_docker:
        log(f"{iid}: 无可用镜像 {image}，跳过 Docker 信号（退化为纯投票）")
        _write_pure_vote_signals(art, iid, cands)
        return

    with tempfile.TemporaryDirectory(dir=str(art)) as tmp:
        tmpp = Path(tmp)
        repro_name = None
        if repro_code:
            repro_name = "repro_test.py"
            (tmpp / repro_name).write_text(repro_code)

        # S-C: 回归子集
        regression = derive_regression(image, str(tmpp))
        if regression:
            (art / "regression").mkdir(exist_ok=True)
            (art / "regression" / f"{iid}.txt").write_text("\n".join(regression))

        # TestRunner: 逐候选
        results = []
        for idx, patch in cands:
            pname = f"cand_{idx}.diff"
            (tmpp / pname).write_text(patch)
            sig = run_candidate(image, str(tmpp), pname, repro_name, regression)
            sig["index"] = idx
            results.append(sig)
        _write_signals(art, iid, results)


def _write_pure_vote_signals(art, iid, cands):
    """无 Docker 时写「仅 applied=true、无测试信号」的信号，等价于纯投票。"""
    results = [{"index": idx, "applied": True, "have_repro": False,
                "repro_passed": False, "have_regress": False, "regression_ok": False}
               for idx, _ in cands]
    _write_signals(art, iid, results)


def _write_signals(art, iid, results):
    """落盘 signals/<id>.json（字段与 Go 侧 scaffold.candidateSignal 对齐）。"""
    (art / "signals").mkdir(exist_ok=True)
    payload = {"instance_id": iid, "candidates": sorted(results, key=lambda r: r["index"])}
    (art / "signals" / f"{iid}.json").write_text(json.dumps(payload, indent=2))
    log(f"{iid}: 写出 {len(results)} 候选信号")


def main():
    ap = argparse.ArgumentParser(description="SWE-bench 复现/回归选择信号生成器（S-B/C）")
    ap.add_argument("--dataset", required=True, help="SWE-bench 数据集 JSONL")
    ap.add_argument("--artifact-dir", required=True, help="scaffold 产物目录（含 candidates/）")
    ap.add_argument("--instances", default="", help="只处理这些 instance_id（逗号分隔，默认全部有候选的）")
    args = ap.parse_args()

    m = int(os.environ.get("COGENT_SWEBENCH_REPRO_M", DEFAULT_REPRO_M))
    wanted = discover_candidate_ids(args.artifact_dir)
    if args.instances:
        wanted &= {s.strip() for s in args.instances.split(",") if s.strip()}
    if not wanted:
        log("无候选实例，退出")
        return
    insts = load_instances(args.dataset, wanted)
    docker_ok = docker_available()
    if not docker_ok:
        log("Docker 不可用：全部退化为纯投票信号（仍产 applied=true）")

    for iid in sorted(insts):
        try:
            process_instance(insts[iid], args.artifact_dir, m, docker_ok)
        except Exception as exc:  # noqa: BLE001 — 单实例失败不阻断整体
            log(f"{iid}: 处理失败（跳过，退化为纯投票）: {exc}")


if __name__ == "__main__":
    main()
