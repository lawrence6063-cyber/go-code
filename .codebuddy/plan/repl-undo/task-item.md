# 实施计划

- [ ] 1. 扩展 Engine 接口，新增 Undo 方法与 TurnSnapshot 机制
   - 在 `internal/engine/engine.go` 的 `Engine` 接口中新增 `Undo(ctx context.Context) (*UndoResult, error)` 方法
   - 定义 `UndoResult` 结构体，包含：被撤销的轮次摘要（`Summary string`）、是否有文件改动（`HasFileChanges bool`）、撤销的消息数量（`RemovedCount int`）
   - 在 `engine` 结构体中新增 `turnSnapshots []turnSnapshot` 字段，记录每轮开始前的消息切片长度（`msgIndex int`）
   - _需求：3.1、3.2、3.3_

- [ ] 2. 实现 Engine.Undo 核心逻辑（消息历史回退）
   - 在 `internal/engine/undo.go` 新建文件，实现 `engine.Undo` 方法
   - 逻辑：从 `turnSnapshots` 弹出最后一个快照，将 `e.msgs` 截断到该快照记录的 `msgIndex` 位置
   - 边界处理：`turnSnapshots` 为空时返回 `ErrNothingToUndo` 错误
   - 生成轮次摘要：取被移除的第一条 user 消息的前 50 字符作为 `Summary`
   - _需求：3.1、3.3、5.1_

- [ ] 3. 在 Run 方法中记录 TurnSnapshot
   - 修改 `internal/engine/engine.go` 的 `Run` 方法，在追加 user 消息前将当前 `len(e.msgs)` 压入 `turnSnapshots`
   - 确保 Resume 后的首轮也正确记录快照（在 Resume 方法中注入 continue 消息前记录）
   - _需求：2.1、5.2_

- [ ] 4. 实现 Git 快照管理器（Snapshotter）
   - 在 `internal/engine/snapshot.go` 新建文件，定义 `Snapshotter` 接口：`Take(ctx) (string, error)` 记录快照、`Restore(ctx, id string) error` 恢复快照、`IsGitRepo() bool` 检测
   - 实现 `gitSnapshotter`：`Take` 使用 `git stash create` 创建无名 stash 对象（不影响 stash 列表），记录返回的 SHA；若工作区无改动则记录空字符串
   - `Restore`：若 SHA 非空则 `git stash apply <sha>`；若为空则 `git checkout -- . && git clean -fd`
   - `IsGitRepo`：执行 `git rev-parse --is-inside-work-tree` 判断
   - _需求：2.1、2.2、2.3、2.4_

- [ ] 5. 将 Snapshotter 集成到 Engine 的 Undo 流程
   - 在 `engine.Deps` 中新增可选的 `Snapshotter` 依赖字段
   - 修改 `engine.Run`：在记录 turnSnapshot 时同步调用 `Snapshotter.Take`，将 SHA 存入 `turnSnapshot`
   - 修改 `engine.Undo`：调用 `Snapshotter.Restore` 恢复工作区；恢复失败时仅打印错误但仍回退消息（解耦）
   - 在 `buildEngine`（`cmd/cogent/commands.go`）中装配 `gitSnapshotter` 实例注入 Deps
   - _需求：2.2、2.3、5.3、5.4_

- [ ] 6. 实现 Session 持久化中的 Undo 事件
   - 在 `internal/engine/undo.go` 的 `Undo` 方法末尾，向 session transcript 追加类型为 `"undo"` 的事件
   - Payload 包含被撤销消息对应的事件 UUID 列表（需在 turnSnapshot 中额外记录每轮起始的 `lastUUID`）
   - 修改 `session_event.go` 中的 `rebuildMessages`：遇到 `"undo"` 事件时，从结果中排除 payload 中列出的 UUID 对应的消息
   - _需求：4.1、4.2、4.3_

- [ ] 7. 在 REPL inputLoop 中集成 /undo 命令
   - 修改 `cmd/cogent/commands.go` 的 `inputLoop` 函数：在 `exit`/`quit` 判断后新增 `/undo` 分支
   - `/undo` 分支逻辑：调用 `eng.Undo(ctx)`，根据返回的 `UndoResult` 打印撤销摘要
   - 处理 `ErrNothingToUndo`：打印"没有可撤销的轮次"并 continue
   - 处理 `Snapshotter` 不可用（非 git 仓库）：打印"当前目录不是 git 仓库，无法恢复工作区文件，仅回退对话历史"
   - 修改 REPL 欢迎信息：在 `runREPL` 中追加 `/undo` 命令提示
   - _需求：1.1、1.3、1.4、1.5、6.1、6.2、6.3_

- [ ] 8. 为 Undo 功能编写单元测试
   - 在 `internal/engine/undo_test.go` 中测试：正常 undo 回退消息、连续多次 undo、undo 到底后返回错误
   - 在 `internal/engine/snapshot_test.go` 中测试 `gitSnapshotter`（需 git init 临时目录）：Take/Restore 正确性、非 git 目录检测
   - 在 `internal/engine/session_event_test.go` 中测试 `rebuildMessages` 对 undo 事件的处理
   - _需求：3.1、3.3、4.2、5.1_

- [ ] 9. 集成测试：端到端验证 /undo 命令
   - 在 `cmd/cogent/commands_test.go` 或新建 `cmd/cogent/undo_test.go` 中编写集成测试
   - 模拟多轮对话后执行 undo，验证 engine 消息历史正确截断
   - 模拟 resume 后 undo 事件被正确重建（排除已撤销消息）
   - _需求：5.1、5.2、4.2_
