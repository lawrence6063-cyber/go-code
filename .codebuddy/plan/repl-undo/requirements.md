# 需求文档：REPL 一键回退（Undo）

## 引言

当前 cogent 的 REPL 交互模式（`cogent run`）缺少内建的"一键撤销上一轮"能力。用户在 agent 执行了一轮改动后如果不满意，只能手动执行 `git checkout -- . && git clean -fd` 来恢复工作区。这对于非 git 专家用户不友好，且无法同时回退 engine 内部的消息历史（导致模型仍"记得"已被撤销的改动）。

本功能旨在为 REPL 模式提供一个内建的 `/undo` 命令，实现：
1. **工作区回退**：撤销上一轮 agent 对文件系统的改动（git tracked + untracked）
2. **消息历史回退**：从 engine 的消息列表中移除上一轮的 user/assistant/tool_result 消息
3. **会话持久化同步**：在 transcript 中记录 undo 事件，使 resume 时能正确重建回退后的状态

设计原则：
- 复用已有的 `gitDiscarder` 机制（`git checkout -- . && git clean -fd`）作为工作区回退的底层实现
- 最小侵入：不改变 engine 核心的 `Run`/`step` 逻辑，通过新增接口方法实现
- 安全优先：undo 操作本身需要确认（防误触），且只能回退最近一轮

## 需求

### 需求 1：REPL 内建 /undo 命令

**用户故事：** 作为一名使用 cogent REPL 的开发者，我希望在 agent 执行了一轮改动后能输入 `/undo` 命令一键撤销，以便快速恢复到上一轮对话前的工作区状态，而不需要手动执行 git 命令。

#### 验收标准

1. WHEN 用户在 REPL 提示符下输入 `/undo` THEN 系统 SHALL 提示用户确认是否撤销上一轮改动（显示将要撤销的轮次摘要）
2. WHEN 用户确认撤销 AND 上一轮存在文件改动 THEN 系统 SHALL 执行 `git checkout -- . && git clean -fd` 恢复工作区到上一轮执行前的状态
3. WHEN 用户确认撤销 THEN 系统 SHALL 从 engine 消息历史中移除上一轮的 user 消息、assistant 回复及所有 tool_result 消息
4. WHEN 用户输入 `/undo` 但当前没有可撤销的轮次（如刚启动或已经撤销过） THEN 系统 SHALL 显示提示信息"没有可撤销的轮次"并继续等待输入
5. WHEN 用户取消撤销（输入 n/no） THEN 系统 SHALL 不做任何改动并继续等待输入

### 需求 2：基于 Git 快照的工作区回退

**用户故事：** 作为一名开发者，我希望 undo 能精确地只撤销上一轮 agent 引入的文件改动，以便我自己在上一轮之前做的改动不会丢失。

#### 验收标准

1. WHEN 每轮 agent 执行开始前 THEN 系统 SHALL 记录当前工作区的 git 状态快照（通过 `git stash create` 或记录 HEAD commit + diff 状态）
2. WHEN undo 执行时 THEN 系统 SHALL 恢复到该轮执行前记录的快照状态，而非简单地丢弃所有未提交改动
3. IF 工作区在 agent 执行前就有未提交改动 THEN 系统 SHALL 在 undo 后保留这些预先存在的改动
4. IF 工作区不在 git 仓库中 THEN 系统 SHALL 在用户输入 `/undo` 时提示"当前目录不是 git 仓库，无法使用 undo 功能"

### 需求 3：Engine 消息历史回退

**用户故事：** 作为一名开发者，我希望 undo 后模型不再"记得"已撤销的改动，以便下一轮对话时模型基于正确的工作区状态进行推理。

#### 验收标准

1. WHEN undo 成功执行 THEN 系统 SHALL 从 engine 内部的 `msgs []types.Message` 中移除上一轮的所有消息（最后一条 user 消息及其后的所有 assistant/tool_result 消息）
2. WHEN undo 后用户发起新的对话 THEN engine SHALL 基于回退后的消息历史（不含已撤销轮次）调用 LLM
3. IF 消息历史中只剩 system prompt（没有任何用户轮次） THEN 系统 SHALL 拒绝 undo 并提示"没有可撤销的轮次"

### 需求 4：会话持久化中的 Undo 事件

**用户故事：** 作为一名开发者，我希望 undo 操作被记录到 session transcript 中，以便 `cogent resume` 时能正确重建回退后的状态。

#### 验收标准

1. WHEN undo 成功执行 THEN 系统 SHALL 向 session transcript 追加一条类型为 `undo` 的事件，payload 中包含被撤销的消息 UUID 列表
2. WHEN resume 重建消息时遇到 `undo` 事件 THEN 系统 SHALL 从重建结果中排除被标记为已撤销的消息
3. WHEN undo 事件落盘失败 THEN 系统 SHALL 仅告警（与现有 session 容错策略一致），不阻断 undo 操作本身的执行

### 需求 5：连续 Undo 与边界处理

**用户故事：** 作为一名开发者，我希望能连续多次 undo 回退多轮，以便在多轮尝试都不满意时能回到更早的状态。

#### 验收标准

1. WHEN 用户连续输入多次 `/undo` THEN 系统 SHALL 逐轮回退（每次回退一轮），直到没有可撤销的轮次
2. WHEN 用户在 undo 后输入新的对话内容 THEN 系统 SHALL 正常执行新轮次（undo 后的新轮次可以被后续的 undo 撤销）
3. IF 某轮 agent 未产生任何文件改动（纯对话） THEN undo SHALL 仅回退消息历史，跳过工作区恢复步骤
4. WHEN 工作区恢复失败（如 git 命令执行出错） THEN 系统 SHALL 显示错误信息，但仍然回退消息历史（消息回退与文件回退解耦，避免状态不一致扩大）

### 需求 6：用户体验与提示

**用户故事：** 作为一名开发者，我希望 undo 功能有清晰的使用提示和反馈，以便我知道什么被撤销了、当前状态如何。

#### 验收标准

1. WHEN REPL 启动时 THEN 系统 SHALL 在欢迎信息中提示 `/undo` 命令的存在（如 "type '/undo' to undo last turn"）
2. WHEN undo 成功执行 THEN 系统 SHALL 显示撤销摘要（如 "已撤销上一轮：[轮次摘要前50字符]，工作区已恢复"）
3. WHEN undo 仅回退了消息但跳过了工作区恢复 THEN 系统 SHALL 明确告知"已撤销对话历史（本轮无文件改动）"
