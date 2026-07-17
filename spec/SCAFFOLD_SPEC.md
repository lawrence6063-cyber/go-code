# Cogent · SWE-bench Scaffold 有效性方案 — SCAFFOLD_SPEC

> 版本 0.1（设计草案，待实现）｜代号 `cogent-scaffold`｜主语言 **Golang**（选择器/内核）+ 少量 Python（Docker 内测试执行）
> 定位：把 SWE-bench 当前「prompt 级 scaffold（对强模型 Resolved@1 净零、仅补丁更聚焦）」升级为「**可执行信号驱动的 test-time scaling**」，让 Resolved@1 真正提升。
> 关系：承接 [`EVAL_SPEC.md`](EVAL_SPEC.md) §5.2（`swebench.Adapter` / 模式 A predictions 导出）与 §7（Docker 沙箱衔接）；实证与业内对标见 [`../eval/doc/swebench-scaffold-plan.md`](../eval/doc/swebench-scaffold-plan.md)。
> 性质：本文档为**实现规格**（Go 接口为签名级草案，供另一 agent 直接落地）；各阶段实现状态见 §8。

## 目录
1. [定位与目标](#1-定位与目标)
2. [背景：实测诊断与业内对标](#2-背景实测诊断与业内对标)
3. [架构、组件与不变量](#3-架构组件与不变量)
4. [组件设计（签名级）](#4-组件设计签名级)
5. [CLI / 环境变量 / 产物布局](#5-cli--环境变量--产物布局)
6. [合规红线与安全](#6-合规红线与安全)
7. [度量与 A/B 协议](#7-度量与-ab-协议)
8. [分阶段里程碑与 DoD](#8-分阶段里程碑与-dod)
9. [与 EVAL_SPEC / 内核的关系](#9-与-eval_spec--内核的关系)
10. [文档状态声明](#10-文档状态声明)

---

## 1. 定位与目标

### 1.1 目标
在**不改（或仅极小、env 门控地改）cogent 内核**的前提下，为 SWE-bench 模式 A 引入 test-time scaling，使 `Resolved@1` 在同组实例、多次重复下**统计显著**高于单发 baseline。

### 1.2 非目标
- 不训练/微调模型；不改判定口径（判定仍走官方 `swebench.harness.run_evaluation`）。
- 不追求 leaderboard SOTA；目标是**证明 scaffold 工程能把命中率抬起来**并可量化。
- 不引入对隐藏测试的任何依赖（见 §6 合规红线）。

### 1.3 要求等级
【必须】= 违反视为错误；【推荐】= 应遵循可例外；【可选】= 增强项。

## 2. 背景：实测诊断与业内对标

### 2.1 本项目 A/B 实测（2026-07-14，30 实例，deepseek-v4-pro，官方 Docker 判定）
| | Resolved@1 | 补丁均文件数 | 补丁均行数 |
|---|---|---|---|
| baseline（prompt-scaffold OFF） | 18/30 = 60.0% | 2.10 | 52 |
| prompt-scaffold ON | 18/30 = 60.0% | 1.23 | 31 |

诊断：prompt-scaffold 对命中率净零（±2 在 Pass@1 单采样噪声内），仅让补丁更聚焦。**强模型单发已 60%，提示措辞压不过采样噪声**——命中率上限由单次采样随机性决定。

### 2.2 业内公因子（Agentless / mini-SWE-agent / Kimi-Dev）
抬 Pass@1 的关键是 **多候选补丁 + 可执行信号筛选**，而非提示：
- **Agentless 三阶段**：分层定位（file→类/函数→行，温度采样多组）→ repair 采样 ~40 候选 → 回归测试（自导出，非 PASS_TO_PASS）+ 复现测试（LLM 自造，多数投票选 1）过滤 → 去重 + 多数投票 rerank 选 1。
- **mini-SWE-agent**：100 行极简 + 环境反馈闭环即达 Verified 74%。
- 结论：**test-time scaling（采样 + 可执行选择）是对强模型仍有效的最高 ROI 杠杆。**

## 3. 架构、组件与不变量

### 3.1 数据流
```
issue ──▶ [Localizer(可选,E)] ──▶ [Sampler: 跑 cogent N 次] ──▶ N 个候选补丁
                                                                    │
        ┌───────────────────────────────────────────────────────────┘
        ▼
[ReproGen: LLM 生成复现测试] + [RegressionSet: 自导出基线通过测试]
        ▼
[TestRunner: 在 swebench 实例镜像内对每个候选跑 复现+回归]  ← Docker(合规沙盒)
        ▼
[Selector: 过滤(复现通过∧回归不破) → 归一化去重 → 多数投票] ──▶ 1 个 final patch
        ▼
predictions.jsonl ──▶ 官方 swebench.harness 判定 ──▶ Resolved@1
```

### 3.2 组件与落点（守 EVAL_SPEC §5.3 依赖方向）
| 组件 | 落点 | 语言 | 是否触内核 |
|---|---|---|---|
| Sampler（best-of-N 编排） | `scripts/eval-swebench-scaffold.sh` | bash | 否 |
| Selector（去重/归一化/投票，纯逻辑） | `internal/eval/scaffold/` | Go | 否（评测层，仅被 cmd 用） |
| ReproGen + TestRunner（Docker 内跑复现/回归） | `eval/scaffold/select_by_tests.py` | Python | 否（复用 swebench venv/镜像） |
| Localizer（可选） | `internal/eval/adapter/swebench/`（提示注入） | Go | 否 |
| 温度旋钮（可选增强） | `internal/engine/`（读 `COGENT_TEMPERATURE`） | Go | **是（唯一，env 门控，默认不变）** |

### 3.3 不变量【必须】
- Selector 为**纯函数**（输入候选集合+测试结果，输出选择），不依赖网络/Docker，单测覆盖。
- 候选采样默认靠 **provider 默认温度的自然采样**跑 N 次即可（零内核改动）；`COGENT_TEMPERATURE` 为可选增强（显式控制/可复现），是本方案**唯一允许的内核触点**，且必须 env 门控、未设时行为与现状一致。
- 判定与选择**严格分离**：选择信号绝不使用官方判定所用的隐藏测试（§6）。

## 4. 组件设计（签名级）

### 4.1 Selector（`internal/eval/scaffold`，Phase D 核心，纯 Go 可单测）
```go
// Candidate 是一个候选补丁及其可执行信号结果。
type Candidate struct {
    Index        int    // 采样序号
    Patch        string // 统一 diff（git a/.. b/..）
    Applied      bool   // 是否能干净 apply（不能 apply 直接淘汰）
    ReproPassed  bool   // 复现测试是否通过（nil 语义用 *bool 或单独 Have 位）
    RegressionOK bool   // 回归测试是否未被打破
    HaveRepro    bool   // 是否有复现测试信号
    HaveRegress  bool   // 是否有回归信号
}

// NormalizeDiff 归一化补丁用于去重：剥离 diff 头噪声、统一空白/行序无关的可比形式（不改语义）。
func NormalizeDiff(patch string) string

// Select 从候选集选出最终补丁：先按 (Applied ∧ ReproPassed ∧ RegressionOK) 过滤（信号缺失则该项不参与否决），
// 再对存活候选按 NormalizeDiff 分组做多数投票；平票用打分（复现通过>回归不破>最小改动:文件数↑行数）破平。
// 返回选中补丁与选择理由（供报告解释）；空集返回 ("", reason)。
func Select(cands []Candidate) (patch string, reason string)
```
【必须】单测覆盖：全 apply 失败、无任何测试信号（退化为纯投票）、复现信号决定胜者、平票破平、去重正确。

### 4.2 Sampler（`scripts/eval-swebench-scaffold.sh`，Phase A）
- 对给定实例集跑 `cogent eval run --dataset=swebench ...` **N 次**（N=`COGENT_SWEBENCH_NBEST`，默认 5），每次独立 artifact-dir（`.../scaffold-cand-<k>/`），从各自 `predictions.jsonl` 抽每实例的 `model_patch` 汇成候选集 `candidates/<instance_id>/<k>.diff`。
- 复用现有可存活后台模式（`nohup bash /tmp/x.sh`，见记忆经验）与 `scaffold_tag` 目录区分。

### 4.3 ReproGen + TestRunner（`eval/scaffold/select_by_tests.py`，Phase B/C）
- **ReproGen**【B】：对每个实例，用一次 LLM 调用（deepseek，端点/密钥走 env）依据 issue 文本生成 **M 个复现测试脚本**（期望"修复后通过、base 上失败"）；多数投票/一致性选 1（对齐 Agentless）。产物 `repro/<instance_id>.py`。
- **RegressionSet**【C】：在实例的 swebench base 镜像内跑测试收集**基线能通过的测试子集**（自导出，**不读 PASS_TO_PASS**）；产物 `regression/<instance_id>.txt`。
- **TestRunner**：对每个候选补丁，在**该实例的 swebench Docker 镜像**内 `git apply` 后：跑复现测试→`ReproPassed`；跑回归子集→`RegressionOK`；apply 失败→`Applied=false`。输出 `signals/<instance_id>.json`（喂给 Selector）。
- 【必须】复用 EVAL_SPEC §7 的 Docker 沙盒与 swebench 镜像；不得联网拉隐藏内容。

### 4.4 Localizer（Phase E，可选）
- repair 前用 `grep`/`find_files` + 轻量 BM25（`eval/scaffold` 内实现，或 embedding 可选）对 issue 关键词排序候选文件，取 top-k 注入 `swebench.Adapter.intent()` 的定位提示段（复用 `COGENT_SWEBENCH_SCAFFOLD` 提示框架）。

## 5. CLI / 环境变量 / 产物布局

### 5.1 环境变量
| 变量 | 默认 | 含义 |
|---|---|---|
| `COGENT_SWEBENCH_NBEST` | 5 | 每实例候选采样数 N |
| `COGENT_SWEBENCH_REPRO_M` | 5 | 每实例复现测试采样数 M |
| `COGENT_TEMPERATURE` | 未设=provider 默认 | 【可选】主循环采样温度（唯一内核触点；越界/非法回退默认） |
| `COGENT_SWEBENCH_SCAFFOLD` | 1 | 沿用：prompt-scaffold 开关 |
| `COGENT_SWEBENCH_LOCALIZE` | 0（关） | 【可选，S-E】结构化定位：BM25 排序 top-k 相关文件注入意图 |
| `COGENT_SWEBENCH_LOCALIZE_K` | 10 | S-E 注入的相关文件数 |
| `DEEPSEEK_API_KEY` / `COGENT_LLM_BASE_URL` | env-only | 沿用 |

### 5.2 产物布局（`eval-artifacts/swebench-scaffold-<tag>/`）
```
candidates/<instance_id>/<k>.diff        # N 个候选补丁
repro/<instance_id>.py                   # 选定的复现测试
regression/<instance_id>.txt             # 基线通过测试子集
signals/<instance_id>.json               # 每候选的 Applied/ReproPassed/RegressionOK
predictions.jsonl                        # Selector 选出的每实例 final patch
report.json                              # 官方判定 + 本方案过程指标
```

## 6. 合规红线与安全【必须】
- **绝不读取/使用** SWE-bench 的 `FAIL_TO_PASS` / `PASS_TO_PASS` / `test_patch` 作为选择信号（那是判定专用）；回归集**自导出**、复现测试**LLM 自造**。
- 违反即视为"面向测试作弊"，结果无效。
- Docker 内执行遵循 EVAL_SPEC §7 与全局安全规则：不经 shell 拼接执行不可信输入（RCE）、密钥仅 env、不联网拉内部资源（SSRF）。
- 候选 `git apply` 在实例镜像内的隔离副本进行，判定用干净副本（与现有 `instanceVerifier` pristine 原则一致）。

## 7. 度量与 A/B 协议
- **主指标 `Resolved@1`**（Selector 选出后官方判定）——【必须】在**同一组实例、重复 R≥3 次**下比较，报均值±标准差；只有超出标准差才算"有效"。
- **`Pass@N`（oracle 上限）**：对全部 N 候选各自官方判定，任一 resolved 即算——衡量采样上限与 Selector 的"选择损失"。
- 过程指标：复现测试选择准确率、补丁聚焦度（均文件/行数）、每实例 token/美元成本、候选 apply 成功率。
- 三档对照：`单发 baseline` vs `best-of-N + 投票(A+D)` vs `+复现/回归选择(A+D+B+C)`。

## 8. 分阶段里程碑与 DoD

| 编号 | 阶段 | 优先级 | 规模 | 状态 |
|---|---|---|---|---|
| S-A | 候选采样 best-of-N（Sampler 脚本 + 候选布局） | P0 | M | [x] 已实现 `scripts/eval-swebench-scaffold.sh` |
| S-D | Selector（归一化去重 + 多数投票，纯 Go + 单测） | P0 | M | [x] 已实现 `internal/eval/scaffold` + `cogent eval scaffold-select` |
| S-B | 复现测试生成 + Docker 内执行选择 | P1 | L | [x] 已实现 `eval/scaffold/select_by_tests.py`（ReproGen） |
| S-C | 回归筛选（自导出基线通过测试） | P1 | M | [x] 已实现 `select_by_tests.py`（RegressionSet，自导出） |
| S-E | 结构化定位（BM25/embedding 注入提示） | P2 | M | [x] 已实现 `scaffold.RankFiles` + adapter env 门控注入 |
| S-M | A/B 度量协议 + 三档对照报告 | P1 | S | [x] 已实现 `scripts/eval-swebench-scaffold-ab.sh` |

> 实现状态说明：以上代码均已落地并通过编译/单测/合规冒烟（Selector 全场景单测绿；Python harness 无 API/无 Docker 时优雅退化为纯投票且证实不写隐藏测试）。**runtime A/B 数值（Resolved@1 均值±std、Pass@N）待一次付费 + Docker 判定的真跑填入 `eval/doc/`**。

**里程碑建议**：先 **S-A + S-D + S-M**（best-of-5 + 投票 + 重复度量），量出 `Pass@N` 与 `Pass@1` 的 gap；再上 **S-B（复现选择）** 把 gap 转成 Pass@1；视情况加 S-C / S-E。

**DoD（逐阶段验收）**
- S-A：对给定 30 实例产出每实例 N=5 候选补丁，布局完整、可重跑幂等；不改内核（或仅 `COGENT_TEMPERATURE` env 门控且默认行为不变）。
- S-D：`internal/eval/scaffold` 单测全绿（覆盖 §4.1 场景）；给定候选+信号能确定性选出 final patch；gofmt/vet/build 干净。
- S-B：能在实例镜像内对候选跑 LLM 自造复现测试并产出 `ReproPassed`；**证明未触碰隐藏测试**（代码审查 + 不引用 FAIL_TO_PASS/test_patch）。
- S-C：产出自导出回归子集并对候选跑出 `RegressionOK`。
- S-M：同组实例重复 R≥3 跑出 baseline vs best-of-N vs +测试选择的 `Resolved@1` 均值±std + `Pass@N`，落 `eval/doc/`。
- 通用：守 Go 规范（导出注释、error 末位、函数≤80 行、嵌套≤4、参数≤5、import 分组）；评测层只 import 内核不被内核 import。

## 9. 与 EVAL_SPEC / 内核的关系
- 本 spec 是 EVAL_SPEC §5.2（swebench 模式 A）之上的**运行侧增强**，不改判定语义与 Adapter 判定路径。
- 唯一可能的内核触点：`internal/engine` 读 `COGENT_TEMPERATURE` 透传 `llm.Request.Temperature`（已存在字段，§llm.client），env 门控、默认不变；若不做该增强则**零内核改动**（靠 provider 默认温度的自然采样）。
- 选择器/采样/测试执行均在评测层与 `scripts/`、`eval/scaffold/`，复用 Colima + swebench 镜像（EVAL_SPEC §7）。

## 10. 文档状态声明
- 本文档为**实现规格**（v0.1）；文中 Go 接口的落地遵循项目 Go 规范。
- 各阶段（S-A/D/B/C/E/M）代码均已落地（见 §8 状态与落点），通过 gofmt/vet/build、Selector 全场景单测、
  Localizer 单测、温度旋钮单测，及 Python harness 的合规冒烟（不泄露隐藏测试、无依赖时优雅退化）。
- 被引用的既有能力（`swebench.Adapter` / `predictions.jsonl` 导出 / `instanceVerifier` / Colima + `swebench` harness / `llm.Request.Temperature`）均已在仓库落地并核对。
- 实测数据来源：`eval-artifacts/swebench-modeA-custom-{baseline,scaffold}/` 与 `eval/doc/swebench-scaffold-plan.md`；
  三档 A/B 的 runtime 数值将由 `scripts/eval-swebench-scaffold-ab.sh` 真跑后写入 `eval/doc/swebench-scaffold-ab-<date>.md`。
