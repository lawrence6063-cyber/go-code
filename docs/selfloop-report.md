# cogent 自循环优化实验报告 · finish_reason

> 实验日期：2026-06-21｜模型：**deepseek-reasoner**｜隔离：`--worktree`｜预算：8 轮 / \$5 / 15m
> 形态：让 cogent 用自身的 `cogent goal` 目标循环优化 cogent 自己的真实代码（dogfood）。
> **结论先行：循环机制全程运转正常，但目标未达成（reviewer 双闸门在 verify 之前持续拒绝）。这是一份不粉饰的真实记录。**

---

## 1. 实验目标与设计

### 1.1 选定的优化点（真实 gap）

补全 `llm.finish_reason` 端到端透出——这是上一轮 OPTIMIZE_SPEC（O2/C2）主动记录的**诚实边界**：

- 现状：`internal/llm/client.go` 仅在 `processFrame` 内部用 `choice.FinishReason == openai.FinishReasonToolCalls` 检测工具调用，未把 finish_reason 透出为 `Delta` 字段；`engine` 的 `llm.stream` span 缺 `llm.finish_reason` 属性（DEV_SPEC §8.3 标注「暂未落地」）。
- 期望循环产出：给 `Delta` 加 `FinishReason string`，在 `processFrame` 透出；在 `engine.streamAssistant` 的 `streamStats` 捕获并补 `llm.finish_reason` span 属性。

### 1.2 goal 与 verify（客观判据）

- **goal**：`eval/tasks/finish_reason_selfloop/task.txt`（自然语言意图 + 明确验收点）。
- **verify**：`eval/tasks/finish_reason_selfloop/verify.sh`，退出码 0 = 达标，遵循 `eval/README` 约定「初始必失败」：
  1. `gofmt -l` / `go vet` / `go build`；
  2. 结构检查 `grep llm.finish_reason internal/engine/`；
  3. **即时材料化一份独立验收测试**（写入 `internal/llm/zz_finish_reason_acceptance_test.go`，跑完即删），用 `newSSEServer` 注入含 `"finish_reason":"stop"` 的 SSE，断言 `Delta.FinishReason=="stop"`——即时写入+用完删，**防止 loop 篡改判据**（防自评虚高）。
- **初始态验证**：`Delta` 无 `FinishReason` 字段 → 验收测试引用不存在符号 → `go vet` 失败 → verify 退出码非 0。✅ 已确认。

### 1.3 运行配置

```
COGENT_MODEL=deepseek-reasoner  COGENT_REVIEWER_MODEL=deepseek-reasoner
COGENT_OBSERVE_ENABLED=true     COGENT_TRACE_EXPORTER=file
COGENT_YES=1                    # 无人值守自动批准（见 §4 发现①）
cogent goal --worktree --verify <abs>/verify.sh \
  --max-iterations 8 --max-cost 5 --max-wallclock 15m "<intent>"
```

隔离用 `--worktree`：每轮在 `os.TempDir()` 下的独立 git worktree 内改→审，**通过才 Merge 回基线**，物理隔离主工作区。

---

## 2. 运行结果（最终结局）

| 项 | 值 |
| --- | --- |
| 结局 outcome | **未达成**（`cogent.session` 属性 `outcome=cancelled`；墙钟超时后手动 SIGINT 优雅停止） |
| 迭代轮数 | 3 轮（2 轮完整记录，第 3 轮被中断） |
| reviewer 裁决 | **3/3 全部拒绝** → 0 次 Merge |
| worktree | 创建 3 个、丢弃 2 个（第 3 个随中断清理） |
| LLM 调用 | 59 次 `llm.stream`，累计 **~883,004 tokens**（prompt+completion） |
| 首 token 延迟 | 1.8s ~ 4.7s+（reasoner 思考开销大） |
| 估算成本 | 约 \$0.5~0.7（远低于 \$5 预算；且未接 CostMeter，见 §4 发现⑤） |
| 主仓改动 | **无**（reviewer 全拒 → 无 Merge → `internal/llm`、`internal/engine` 未变） |
| gap 是否闭合 | **否**——运行后 `verify.sh` 仍失败（`d.FinishReason undefined`） |

### 逐轮过程

- **迭代 1**：maker 在 worktree 内正确定位并编辑了 `client.go`（加 `FinishReason` 字段）与 `engine.go`（`st.finishReason = d.FinishReason` + `attrs` 追加 `llm.finish_reason`）——**改动本身是对的**。reviewer 审查后**拒绝**，反馈是一大段 reasoner 思维链（非规范裁决）。worktree 丢弃。
- **迭代 2**：带反馈续跑，maker 再次编辑。reviewer 再次输出思维链式文本、**拒绝**。worktree 丢弃。
- **迭代 3**：maker 仍在反复探索（多次 `grep`/`ls` 确认文件状态），单次 reasoner 调用耗时长，超过 15m 墙钟后被 SIGINT 中断。

---

## 3. 可观测证据（trace 自证）

本次运行的 trace（`data/traces/traces-20260621-202756.jsonl`）正好**用上一轮 OPTIMIZE 落地的可观测成果**来观测这次循环，形成闭环自证：

```
span 分布：cogent.session×1  loop.iteration×2  agent.maker×2  agent.reviewer×2
          worktree.create×3  worktree.discard×2  llm.stream×59  react.step×58
          tool.call×88  tool.batch×68  permission.check×44  sandbox.exec×29
```

- **`cogent.session`**（O1）：`session.id=20260621-122756…`、`mode=auto`、`outcome=cancelled`。
- **`llm.stream`**（O2/O3）：每个 span 带 `llm.prompt_tokens` / `llm.completion_tokens` / `llm.ttft_ms`，例：`{prompt:8907, completion:439, ttft_ms:4711}`——成本/延迟可逐调用归因。
- **`agent.maker` / `agent.reviewer`**（O4）：`agent.role=maker|reviewer`、`summary.bytes`（reviewer 输出 2324/1071 字节，印证其冗长）。
- **`worktree.create/discard`**（O5）：`worktree.branch=cogent/wt-6d99…` 等，3 创建 2 丢弃。
- **关键缺失：`goal.verify` span 数 = 0**——证明客观判据 `verify.sh` **全程从未执行**（见 §4 发现②）。

---

## 4. 诚实评估与关键发现

> 本节如实记录失败模式与机制摩擦，这些恰是 dogfood 最有价值的产出。

### 发现① 无人值守 loop 缺「自动批准」开关（已就地修复）
maker 的写/执行工具经 `Guard` + 空 `StaticPolicy{}` → 一律 `BehaviorAsk` → 交互式 CLI prompter。后台无 TTY 时 **直接卡死**（实测 0% CPU 阻塞 6 分钟），`yes a` 管道在 nohup 下也不可靠。
**修复**：新增 env 门控的 `yesPrompter`（`COGENT_YES=1` 自动批准；危险命令仍被 sandbox 确定性拦截、worktree 物理隔离兜底），`goal`/`loop` 用 `newPrompter` 按需选择。这是无人值守 loop 的**必备能力**，本次实验暴露并补上。

### 发现② reviewer 双闸门在 verify 之前拦截，objective 判据从未运行 ⚠️（最重要）
worktree 流水线是「maker 改 → reviewer 审 → **通过才** Merge → 再跑 verify.sh」的**双闸门**。reviewer 3/3 拒绝 → 永不 Merge → `goal.verify` span 数为 0 → 精心设计的客观 `verify.sh` **一次都没执行**。
**含义**：当 reviewer 失灵/过严时，主观闸门会**完全屏蔽**客观闸门。对「达目标才停」的循环，这是致命的——它退化为「reviewer 满意才停」，而非「verify 通过才停」。

### 发现③ deepseek-reasoner 作 reviewer：裁决不合规导致 fail-closed 恒拒
reviewer 被要求首行回 `APPROVED`/`REJECTED`，但 reasoner 输出的是**大段思维链**（"I'll start by inspecting…"）。`parseVerdict` fail-closed（仅首个非空行以 APPROVED 开头才通过）→ 一律判为拒绝。这不是 maker 改错（改动其实正确），而是**reviewer 的格式遵循 + 裁决解析**的错配。
**候选改进**：reviewer 用指令遵循更稳的对话模型（如 deepseek-chat）而非 reasoner；或强化 `parseVerdict` 对结构化裁决的提取（如扫描全文找 `APPROVED`/`REJECTED` 标记，而非仅首行）。

### 发现④ reasoner 的 cwd 幻觉与高延迟
maker（reasoner）频繁执行 `cd /workspace && …`（幻觉路径，真实 worktree 在 `/private/var/.../cogent-wt-*`），命令失败后反复探索，浪费迭代预算；单次 ttft 高达 4.7s+，59 次调用累计 88 万 tokens。
**含义**：reasoner 强于推理但**工具编排/环境感知**偏弱且慢，不一定优于 deepseek-chat 作 maker。

### 发现⑤ \$5 成本护栏未真正生效
`buildOrchestrator` 装配 `loop.New` 时未注入 `CostMeter` → `overCost` 恒 false → `--max-cost` 形同虚设，实际只有轮数与墙钟两道护栏在起作用。
**候选改进**：把基于 `cogent.tokens` 指标 + 模型单价的 CostMeter 接入 loop，让成本护栏真正闭环。

### 发现⑥ 墙钟取消有滞后
15m 墙钟到点后，循环未立即停止（单次慢 reasoner 调用 + ctx 取消传播延迟），最终靠 SIGINT 收尾。属可接受范围（受单次 LLM 调用超时 120s 上界约束），但说明「墙钟」是软上界而非硬上界。

---

## 5. 对 Loop 机制的反思

1. **客观判据必须能被触达**：本实验最大教训是 verify.sh 设计得再严谨，只要它在 reviewer 闸门之后，就可能永远跑不到。「达目标才停」的前提是**目标判据处于循环的终止路径上且不被前置主观闸门短路**。可考虑：verify 与 reviewer 并联（任一为硬判据），或 verify 前置。
2. **reviewer 的模型选型与裁决协议是系统可靠性的一部分**：fail-closed 是对的（防自评虚高），但若 reviewer 因格式问题恒拒，循环就无法收敛。裁决解析需对真实模型输出更鲁棒。
3. **无人值守 = 自动批准 + 可观测 + 预算闭环**，三者缺一不可。本次补上了自动批准与可观测（trace 已自证），但成本护栏仍是空接线。
4. **可观测性是 dogfood 的真正赢家**：上一轮 OPTIMIZE 落地的 `cogent.session`/`llm.stream`/`worktree.*`/`agent.role` span 让本次「为什么没成功」可以被逐 span 追问——这正是埋点的价值。

---

## 6. 复现实方式

```bash
# 1) 准备 .env（含 DEEPSEEK_API_KEY，已 gitignore）
cp .env.example .env && $EDITOR .env

# 2) 构建并运行（脚本内已设 reasoner / COGENT_YES / file exporter / 预算）
go build -o bin/cogent ./cmd/cogent
bash scripts/run-selfloop.sh 2>&1 | tee /tmp/selfloop.log

# 3) 观察
ls -t data/traces/traces-*.jsonl | head -1
jq -r '.Name' <trace> | sort | uniq -c
jq -r 'select(.Name=="cogent.session")|.Attributes[]|"\(.Key)=\(.Value.Value)"' <trace>
bash eval/tasks/finish_reason_selfloop/verify.sh; echo "exit=$?"
```

## 7. 交付物清单

| 文件 | 说明 |
| --- | --- |
| `eval/tasks/finish_reason_selfloop/task.txt` | goal intent |
| `eval/tasks/finish_reason_selfloop/verify.sh` | 客观判据（即时材料化验收测试，初始必失败） |
| `scripts/run-selfloop.sh` | 运行编排（reasoner + worktree + 预算 + file exporter + COGENT_YES） |
| `cmd/cogent/prompter.go`（改） | 新增 `yesPrompter` + `newPrompter`（COGENT_YES 自动批准） |
| `cmd/cogent/goal.go`、`loop.go`（改） | 改用 `newPrompter` 支持无人值守 |
| `docs/selfloop-report.md` | 本报告 |

> **一句话总结**：循环机制（worktree 隔离 / maker-reviewer / 预算护栏 / 反馈续跑 / 全链路 trace）端到端跑通且可观测，但因 **reviewer 双闸门在 objective verify 之前持续拒绝**（叠加 reasoner 裁决不合规），目标未达成——这暴露了「主观闸门可短路客观判据」「reviewer 模型选型至关重要」「成本护栏未闭环」三个真实可改进点，比一次「侥幸成功」更有价值。
