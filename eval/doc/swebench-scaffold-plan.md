# SWE-bench Scaffold 有效性方案（let scaffold actually move Resolved@1）

> 定位：把当前「prompt 级 scaffold（净零、只让补丁更聚焦）」升级为「可执行信号驱动的 test-time scaling」，
> 让 Resolved@1 真正提升。结论与做法均对齐业内主流（Agentless / mini-SWE-agent / Kimi-Dev）并结合本项目 A/B 实测。
> 落点：全部在 **scaffold 编排层 + adapter/prompt + 独立 selection harness**，不改 cogent 内核（守 §5.3）。

## 1. 问题（来自本项目 A/B 实测，2026-07-14）

30 实例、deepseek-v4-pro、官方 Docker 判定：

| | Resolved@1 | 补丁均文件数 | 补丁均行数 |
|---|---|---|---|
| baseline（scaffold OFF） | 18/30 = 60.0% | 2.10 | 52 |
| scaffold（prompt，ON） | 18/30 = 60.0% | **1.23** | **31** |

- prompt-scaffold **对命中率净零**（±2 落在 Pass@1 单采样噪声内：新解决 2 个来自"散弹→聚焦"，丢失 2 个两轮都单文件、纯采样抖动）。
- prompt-scaffold **确实让补丁更聚焦**（文件 2.1→1.2、行 52→31），但这是"补丁质量"收益，不是命中率收益。

**诊断**：强模型（v4-pro）单发已经不错（60%），靠"提示措辞"很难再压过采样噪声。命中率的上限由**单次采样的随机性**决定；要突破它，必须引入**测试时计算（test-time compute）+ 可执行的选择信号**。

## 2. 业内怎么解决（grounded）

| 方案 | 核心手段 | 对我们的启示 |
|---|---|---|
| **Agentless**（无 agent 三阶段） | ①分层定位 file→类/函数→行；②repair **采样 40 个候选**（1 greedy+温度采样，×4 组定位）；③**回归测试 + 复现测试**过滤；④**去重 + 多数投票 rerank** 选 1 个 | 命中率提升几乎全来自 **多候选 + 可执行信号选择**，非提示 |
| **mini-SWE-agent** | 100 行极简 agent，SWE-bench Verified 达 74% | 强模型下"环境反馈闭环"比复杂提示更重要 |
| **Kimi-Dev** | 把 Agentless 流程蒸馏成模型技能，Verified 60.4% | 定位+修复+验证的**流程**本身就是能力 |

**共识**：SWE-bench 上真正把 Pass@1 拉起来的，是 **(a) 结构化定位收窄范围 + (b) 生成多个候选补丁 + (c) 用可执行测试（复现 + 回归）筛选/投票选最终补丁**。其中 (b)+(c) 是最高 ROI，且对强模型依然有效。

**合规红线**：选择信号**绝不能看** SWE-bench 的隐藏 `FAIL_TO_PASS`/`test_patch`（那是判定用的）。业内做法（我们照搬）：
- 回归测试 = **自己从原库跑出的能通过的测试**（不是 PASS_TO_PASS 字段）；
- 复现测试 = **LLM 依据 issue 文本自己生成**的复现脚本。
两者都不碰隐藏测试，合法。

## 3. 方案：分阶段落地到 cogent（按 ROI 排序）

前提能力：cogent `llm.Request.Temperature` 可调；已具备 Colima + swebench 每实例 Docker 镜像（可作为"可执行信号"的沙盒，且不暴露隐藏测试）。

### Phase A —— 候选采样（best-of-N）【最高 ROI，先做】
- 给主循环加温度/种子旋钮（`COGENT_TEMPERATURE`，engine 透传到 `llm.Request.Temperature`；不改内核语义，只读 env）。
- scaffold 编排：对每个实例跑 **N 次**（建议 N=5，temp≈0.7），收集 N 个候选 `model_patch`（存 `candidates/<instance>/<k>.diff`）。
- 仅此一步 + 朴素选择（见 Phase D 投票）通常就能把 Pass@1 抬高数个点（best-of-N 覆盖了单采样噪声）。

### Phase B —— 复现测试选择（reproduction-driven）【核心信号】
- 编排让 cogent（或一次 LLM 调用）依据 issue **生成复现测试脚本**（"写一个能触发该 bug 的最小测试，期望修复后通过"）。
- 在**该实例的 swebench Docker 镜像**里：对每个候选补丁 `git apply` 后跑复现测试；**保留使复现测试通过的候选**。
- 复现测试本身可生成多个、用 majority/一致性选 1 个（对齐 Agentless），降低测试自身错误率。

### Phase C —— 回归筛选（regression guard）
- 在实例镜像里先跑出**基线能通过的测试子集**（自导出，不用 PASS_TO_PASS）；
- 丢弃任何**打破回归**的候选（避免"修了 A 弄坏 B"）。

### Phase D —— 去重 + 多数投票 rerank【选择器】
- 对通过 B/C 过滤的候选：**归一化 diff（去空白/顺序）后多数投票**选最终补丁；平票用"复现通过 + 回归不破 + 最小改动"打分。
- 输出最终 `predictions.jsonl`（每实例 1 个）交官方判定。

### Phase E —— 结构化定位（可选增强）
- repair 前加定位子步：`grep`/`find_files` + 轻量 BM25（或 embedding）对 issue 关键词排序候选文件 → 把 top-k 文件/函数注入 repair 提示，收窄上下文。
- 我们的补丁聚焦数据已证明"targeting"有效（requests-2148 从 6 文件→1 文件才修对）。

## 4. 度量（A/B 对照，验证方案有效）

- 主指标：**Resolved@1（best-of-N 选择后）** vs baseline（单发）——需在**同一组实例、多次重复**下比较，压住采样噪声。
- 过程指标：`Pass@N`（N 候选里至少 1 个对，衡量采样上限）、复现测试选择的准确率、补丁聚焦度（已有）、每实例 token/美元成本。
- 诚实标准：只有当 Resolved@1 提升幅度**超过重复实验的标准差**才算"scaffold 有效"。

## 5. 实施顺序 / 预期 / 成本

| 阶段 | 工作量 | 预期收益 | 成本 |
|---|---|---|---|
| A 候选采样 | 小（temp 旋钮 + 编排循环） | Pass@N 打开上限、best-of-N 抬 Pass@1 | ×N LLM 调用 |
| B 复现选择 | 中（生成+Docker 内跑复现测试 + 选择器） | 把 Pass@N 上限转化为 Pass@1 的主力 | Docker 判定 ×候选 |
| C 回归筛选 | 中（镜像内跑基线测试子集） | 减少"修坏"型失败 | Docker 判定 |
| D 投票 rerank | 小（diff 归一化 + 打分） | 无测试信号时的兜底选择 | 纯计算 |
| E 结构化定位 | 中（检索 + 提示注入） | 收窄范围、降 token、提聚焦 | 少量嵌入成本 |

建议里程碑：**先 A + D**（best-of-5 + 投票，最快见效、无需额外 Docker 信号）→ 量到 Pass@N 与 Pass@1 gap → 再上 **B（复现选择）** 把 gap 吃掉 → 视情况加 C/E。

## 6. 与 cogent 架构的契合

- **不改内核**：Phase A 只加 `COGENT_TEMPERATURE` env 透传（engine 读 env 设 `llm.Request.Temperature`）；其余全在 `scripts/` 编排 + swebench adapter/prompt + 一个独立 `selection`（Python，复用 swebench 每实例镜像跑复现/回归）。
- **合规**：选择信号只用"自导出回归测试 + LLM 自造复现测试"，绝不读 `FAIL_TO_PASS`/`test_patch`。
- **可 A/B**：沿用 `COGENT_SWEBENCH_SCAFFOLD` 及新增 `COGENT_SWEBENCH_NBEST` 等开关，跑「单发 vs best-of-N vs +复现选择」三档对照。

## 7. 一句话结论
prompt-scaffold 已榨干（对强模型只剩补丁质量收益）。**要让 scaffold 抬命中率，必须做 test-time scaling：多候选补丁 + 用"自造复现测试/自导出回归测试"这类不碰隐藏测试的可执行信号来筛选与投票。** 这是 Agentless 等业内 SOTA 的公因子，也是本项目下一步最高 ROI 的工程增量。
