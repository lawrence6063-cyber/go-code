# SWE-bench Lite 全量实验方案 · N=3 纯投票（不跑 A/B）

> 版本 v1（2026-07-14）｜承接 [`../../spec/SCAFFOLD_SPEC.md`](../../spec/SCAFFOLD_SPEC.md) S-A/S-D
> 目标：在**控成本**前提下，用 best-of-3 + 去重多数投票的 test-time scaling，跑出 SWE-bench Lite 全量
> （300 实例）的 `Resolved@1`，验证 scaffold 相对历史单发基线（60%）是否抬升命中率。

## 1. 目标与假设

### 1.1 目标
- 产出 SWE-bench Lite 全量 300 实例的 **Resolved@1**（官方 Docker 判定）。
- 单档跑（不跑 A/B、不跑重复 R、不算 Pass@N），把成本压到最低同时拿到有意义的全量数字。

### 1.2 核心假设
- 单发正确率 p≈0.60（历史 30 实例 A/B 实测），独立近似下 **Pass@3 上限≈93.6%**。
- 多数投票（N=3 可 2:1 决胜）+「最小改动」破平，能把一部分 Pass@3 上限兑现为 Pass@1。
- 预期区间：Resolved@1 **62%–70%**（保守）；只要**显著高于 60% 单发基线**即算 scaffold 有效。
  > 注：本方案不含同组重复，无法给标准差；结论按"相对历史基线的方向性提升"解读，严谨的
  > 均值±std 见 A/B 协议 `eval-swebench-scaffold-ab.sh`（成本更高，本次不跑）。

### 1.3 为什么是 N=3 纯投票（而非 N=2 / 开测试信号）
- **N=2 投票退化失效**（1:1 平票无决定力），要兑现必须开 Docker 复现/回归信号（重成本）。
- **N=3** 是性价比甜点：奇数可 2:1 决胜、Pass@N 上限 93.6%、且**无需**全量开测试信号——省掉
  300 实例的 ReproGen LLM 调用与每候选跑测试的算力/时间。

## 2. 配置

| 项 | 值 | 说明 |
|---|---|---|
| 数据集 | SWE-bench Lite 300 | `~/.cache/cogent-eval/swebench/lite.jsonl` |
| 模型 | deepseek-v4-pro | `COGENT_MODEL` |
| N（best-of） | **3** | `COGENT_SWEBENCH_NBEST=3` |
| 采样温度 | 0.7 | `SAMPLE_TEMP=0.7`（透传 `COGENT_TEMPERATURE`，制造候选多样性） |
| 测试信号 | 关 | `ENABLE_TESTS=0`（纯投票，不跑 Docker 复现/回归） |
| 定位增强 | 关 | `COGENT_SWEBENCH_LOCALIZE` 不设（默认关，保持与历史口径一致） |
| prompt-scaffold | 开 | `COGENT_SWEBENCH_SCAFFOLD` 默认 1 |
| 单实例预算 | iter≤12, cost≤$1, wall≤15m | 护栏，防单条空耗 |
| 采样并发 | 4 | `CONCURRENCY=4`（看 API 限流调整） |
| 判定并发 | 4 | `MAX_WORKERS=4`（看内存调整，OOM 则降 2） |
| 分片大小 | 50 | `SHARD_SIZE=50` → 6 片，断点续跑友好 |

## 3. 执行步骤

### Step 0 · 冒烟（必做，先验证链路）
先跑 2 条确认 采样→布局→选择→判定 全链路通、成本符合预期：
```bash
export DEEPSEEK_API_KEY=sk-xxxx
EVAL_IDS="psf__requests-2317,pallets__flask-4045" \
COGENT_SWEBENCH_NBEST=3 SAMPLE_TEMP=0.7 ENABLE_TESTS=0 SCAFFOLD_TAG=smoke \
bash scripts/eval-swebench-scaffold.sh
```
检查 `eval-artifacts/swebench-scaffold-smoke/`：candidates 每实例 3 份、predictions.jsonl 非空、
判定报告有 Resolved@1、`scaffold-select-report.json` 的 reason 合理（2/3 或 3/3 投票）。

### Step 1 · 全量分片跑（推荐，稳健）
用 driver 分 6 片、片级幂等、自动扩容 Colima 到 200G、跑完聚合：
```bash
cat > /tmp/swe-lite-full.sh <<'EOF'
export DEEPSEEK_API_KEY=sk-xxxx
cd /Users/alaindong/Desktop/new_career/resume/ai项目/cogent
SHARD_SIZE=50 NBEST=3 SAMPLE_TEMP=0.7 \
COGENT_MODEL=deepseek-v4-pro \
MAX_ITER=12 MAX_COST=1 MAX_WALL=15m CONCURRENCY=4 MAX_WORKERS=4 \
bash scripts/eval-swebench-lite-full.sh
EOF
nohup bash /tmp/swe-lite-full.sh > /tmp/swe-lite-full.log 2>&1 &
tail -f /tmp/swe-lite-full.log
```
（后台长跑坑：execute_command 300s 超时会杀后台进程，必须 `nohup bash /tmp/x.sh &` + 轮询 log。）

### Step 2 · 结果
driver 聚合各片判定报告，产出 `eval/doc/swebench-lite-full-n3-result-<date>.md`：全量 Resolved@1（resolved/denom）。

## 4. 指标与验收
- **主指标**：全量 Resolved@1 = Σ各片 resolved / Σ各片 completed。
- **过程指标**：各实例 `scaffold-select-report.json` 的候选数 / 选中理由 / 补丁字节数（补丁聚焦度）；
  各片 predictions 的候选 apply 成功率。
- **验收（DoD）**：
  1. 300 实例全部产出 final patch 或明确记录未产出原因（空补丁）。
  2. 全量 Resolved@1 数字落 `eval/doc/`，可与历史单发基线（60%）方向性对比。
  3. 无合规越界（选择仅用投票，未触碰隐藏 test_patch / FAIL_TO_PASS / PASS_TO_PASS）。

## 5. 成本与时间预算（deepseek，粗估，以 report 实际 cost 为准）
- **采样 API**：3×300 = 900 次 agent run，单条护栏 $1（实际常 $0.05–0.3）→ **约 $45–135**。
- **判定**：本地 Colima Docker，**零 API 成本**；时间是大头——300 实例镜像构建+跑测试，
  `MAX_WORKERS=4` 下约数十小时级；磁盘需 ~200G（driver 自动扩容）。
- 不跑 A/B / 不重复 / 不算 Pass@N → 相比完整 A/B 协议省 3–5×。

## 6. 风控与断点续跑
- **整轮采样脆弱点**：单片单轮采样整批跑完才写 predictions.jsonl，中途中断丢该轮 →
  用分片（50/片）把爆炸半径限制在单片；片级幂等（该片最终报告存在即跳过）。
- **候选级幂等**：`scaffold-cand-<k>/predictions.jsonl` 存在即跳过该轮（`FORCE=1` 强制重跑）。
- **Colima**：一次扩到 200G 后各片复用；`colima status` 偶发阻塞用 `docker info` 探活；空闲 `colima stop` 省资源。
- **API 限流**：降 `CONCURRENCY`；**成本失控**：调低 `MAX_COST` / `MAX_ITER`。
- **判定 OOM**：降 `MAX_WORKERS` 到 2。
- **端点坑**：必须 `COGENT_LLM_BASE_URL=https://api.deepseek.com/v1`，否则默认打 OpenAI 端点 401 空耗预算。

## 7. 合规红线（SCAFFOLD_SPEC §6）
- 本方案 `ENABLE_TESTS=0`，选择信号**只有去重多数投票 + 最小改动破平**，全程不涉及任何隐藏判定信息。
- 官方判定（`swebench.harness.run_evaluation`）在独立 Docker 内套隐藏测试，与选择严格分离。
