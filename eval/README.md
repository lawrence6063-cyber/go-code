# cogent 评测集（eval）

本目录为 cogent 的分层评测集：用一组"自带验证脚本的真实修复任务"来回归 agent 的端到端能力，
并按 [`spec/EVAL_SPEC.md`](../spec/EVAL_SPEC.md) 的六维度（反馈收敛 / 预算护栏 / 独立判定抗注入 /
maker-reviewer / 难度分层 / 运行期）对能力做体检。

判定不依赖 agent 自述"我完成了"，而以 `verify.sh` 的退出码为准（0=通过），防止自我评估虚高。

## 目录结构

```
eval/
├── README.md
├── bin/
│   └── eval_selfcheck.sh   # 可解性双校验：初始必失败 / oracle 必通过（EVAL_SPEC §5.1）
└── tasks/
    └── <task-name>/
        ├── task.yaml       # 机器可读元数据：难度/语言/维度/预算/预期结局
        ├── task.txt        # 自然语言任务描述（喂给 agent 的输入）
        ├── repo/           # 自包含任务的工作区：含缺陷代码与单测（独立 go module）
        ├── verify.sh       # 客观判定脚本，退出码 0 = 通过
        └── oracle/         # 参考解 patch（fix.patch）：应用后 verify.sh 必通过，用于自证可解
```

> 部分"自宿主"任务（改动 cogent 本体，如 `add_find_files_tool` / `session_root_span`）
> 无独立 `repo/`——其工作根为 cogent 仓库根，`task.yaml` 中标注 `workdir: repo-root`。

## task.yaml 字段

| 字段 | 说明 |
| --- | --- |
| `id` | 任务稳定标识 |
| `difficulty` | `easy` \| `medium` \| `hard` |
| `languages` | 语言标签，如 `[go]` |
| `capabilities` | 六维度标签：`convergence`/`budget`/`injection`/`review`/`runtime`/`exploration` |
| `workdir` | `repo`（自包含子工程）\| `repo-root`（改 cogent 本体） |
| `budget` | 覆盖 `loop.Budget` 默认值：`max_iterations`/`max_cost_usd`/`max_wallclock` |
| `expected_outcome` | 期望结局：`achieved` \| `budget_spent` \| `canceled` |
| `verifier` | `script`（退出码）\| `llm_judge`（语义） |
| `timeout` | 单任务超时 |
| `oracle` | 参考解 patch 路径，或 `n/a`（自宿主/无解任务） |
| `solvability_check` | 是否纳入 `eval_selfcheck.sh` 双校验（自包含且可解的任务=`true`） |

## 现有任务

| 任务 | 难度 | 维度 | 判定方式 |
| --- | --- | --- | --- |
| `fix_off_by_one` | easy | convergence | `go test`（闭区间求和） |
| `fix_concurrent_counter` | easy | convergence, runtime | `go test -race`（并发竞态检测） |
| `feedback_convergence` | medium | convergence | `go test`（多失败用例，需 N 轮收敛） |
| `budget_iterations` | medium | budget | 恒失败（无解，压轮数护栏，断言 `budget_spent`） |
| `budget_cost` | medium | budget | 恒失败（无解，压成本护栏，断言 `budget_spent`） |
| `budget_wallclock` | medium | budget | 恒失败（无解，压墙钟护栏，断言 `budget_spent`） |
| `injection_verifier` | medium | injection | `go test`（注释/文档注入载荷，只认退出码） |
| `injection_test_output` | medium | injection | `go test`（测试输出打印假 banner，只认退出码） |
| `review_reject_retry` | medium | review | `go test`（质量点缺失，首审必拒→重做） |
| `add_find_files_tool` | hard | exploration, review | 编译 + 注册校验 + canary + 全量测试（自宿主） |
| `session_root_span` | hard | exploration, runtime | 结构检查 + 注入式 trace 验收（自宿主） |

## 运行单个任务的验证

```bash
bash eval/tasks/fix_off_by_one/verify.sh
```

未修复缺陷时该脚本应失败（退出码非 0），修复后通过（退出码 0）。

## 可解性双校验（selfcheck）

对所有 `solvability_check: true` 的自包含任务，校验"初始态必失败 / oracle 态必通过"，
确保任务确实有待解决、且判定器无 bug（对标 SWE-bench Verified 的无歧义约定）：

```bash
bash eval/bin/eval_selfcheck.sh                 # 校验全部自包含任务
bash eval/bin/eval_selfcheck.sh fix_off_by_one  # 只校验指定任务
```

## 批量跑分（Headless 运行器，E2 已落地）

`cogent eval run` 批量执行 native 评测集：加载任务 → 建工作区副本 → 跑目标循环 → 按判定矩阵
聚合指标 → 产出 `report.md`（人读）+ `report.json`（供 compare 解析）。

```bash
# 先看会跑哪些任务（不建副本、不跑 agent）
cogent eval run --dry-run

# 按维度/难度/语言/id 筛选后跑分
cogent eval run --capability=convergence --out=report.md
cogent eval run --difficulty=easy
cogent eval run --id=fix_off_by_one --max-iterations=6

# 并发跑分（worker 池）
cogent eval run --capability=budget --n-concurrent=4

# 省成本：便宜模型 + 收紧预算
cogent eval run --model=deepseek-chat --max-iterations=8 --max-cost=1

# 回归对比：与上次基线 diff（检出退化时退出码 3）
cogent eval compare --base=baseline.json --head=report.json --fail-on-regress
```

关键点：
- **工作区副本隔离**：每个 case 在 `--artifact-dir`（默认 `./eval-artifacts/<ts>`）下的临时副本上跑，
  绝不污染 `eval/tasks/<name>/repo/` 源；副本刻意排除 `oracle/`（参考解绝不喂给 agent）。
- **判定矩阵**：`expected_outcome: achieved` 判 verify 通过；`budget_spent` 为反向评测——
  期望系统「正确地失败并早停」（撞预算护栏且未超轮）。三个 budget 任务分压轮数/成本/墙钟护栏。
- **归档**：报告落 `report.md`（人读）+ `report.json`（`compare` 解析源）；每个 case 另写 `result.json` 供复盘。
- **并发**：`--n-concurrent>1` 启 worker 池，ctx 取消安全收尾、无 goroutine 泄漏。
- 首版仅支持 `workdir: repo` 的自包含任务；`repo-root`（自宿主）任务待后续在干净 clone/worktree 上跑。

## 手动驱动 agent 跑某个任务（示例）

```bash
cd eval/tasks/fix_off_by_one/repo
cogent run "$(cat ../task.txt)"
cd .. && bash verify.sh   # 校验 agent 的修改是否真正通过
```

## 新增任务约定

1. 在 `tasks/` 下新建任务目录，放入 `task.yaml`、`task.txt`、`verify.sh`，
   自包含任务另放 `repo/`（可独立构建/测试的最小工程）与 `oracle/fix.patch`。
2. `verify.sh` 必须给出客观、可复现的判据，以退出码表达结果（0=通过）。
3. 初始态下 `verify.sh` 应为失败，确保任务"有待解决"，避免空跑通过。
4. 自包含且可解的任务应提供 `oracle/fix.patch` 并置 `solvability_check: true`，
   使 `git apply oracle/fix.patch` 后 `verify.sh` 通过；随后 `bash eval/bin/eval_selfcheck.sh` 应全绿。

## 与 EVAL_SPEC 的关系

本目录已落地 [`EVAL_SPEC.md`](../spec/EVAL_SPEC.md) 的 **E0**（native 格式升级：`task.yaml` + oracle +
可解性双校验）与 **E1** 的自包含种子任务（收敛 / 预算三子任务 / 注入双载荷 / 双角色）。
**E2**（Headless 批量跑分运行器）已落地到 [`internal/eval/`](../internal/eval/)：`native.Adapter` 加载器、
顺序 + 并发 `Runner`、判定矩阵、指标聚合、Markdown/JSON 报告、逐 case 归档、`cogent eval run`/`compare` 子命令。
运行期 Docker 任务（E1-5）与主流基准 Adapter（E3–E5）见 spec §8 排期。
