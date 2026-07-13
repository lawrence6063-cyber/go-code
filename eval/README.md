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

### polyglot 套件（多语言，接入模式 B / 无 Docker，E4-2）

`--dataset=polyglot` 接入 aider [`polyglot-benchmark`](https://github.com/Aider-AI/polyglot-benchmark)
（六语言 Exercism 练习），一步点亮「多语言 + 反馈收敛」维度。数据集不随本仓库分发，需自行 clone
后用 `--polyglot-dir` 指向其根目录（Adapter 只读取、不联网拉取）。

```bash
# 先 clone 数据集（一次性）
git clone https://github.com/Aider-AI/polyglot-benchmark /path/to/polyglot-benchmark

# 看会跑哪些练习（校验数据集与筛选，不建副本、不跑 agent）
cogent eval run --dataset=polyglot --polyglot-dir=/path/to/polyglot-benchmark --dry-run

# 取子集跑分：只跑 go、每语言限 20 个练习、便宜模型
cogent eval run --dataset=polyglot --polyglot-dir=/path/to/polyglot-benchmark \
  --language=go --limit=20 --model=deepseek-chat

# 只跑指定练习
cogent eval run --dataset=polyglot --polyglot-dir=/path/to/polyglot-benchmark --exercise=two-fer,leap
```

polyglot 关键点：
- **工作区副本排除 `.meta/`**：`.meta/` 含 `example` 参考解，绝不复制进 agent 工作区（gold 不喂给 agent）。
- **工作区目录命名为 slug**：cpp 的 Exercism `CMakeLists.txt` 由目录名推导工程名/源文件名，故副本目录必须叫 slug（非 `workspace`）。
- **测试文件 pristine 防篡改**：判定前用数据集源目录的 test + editor 文件覆盖工作区副本，agent 只能改解题文件、
  改不动被判定实际执行的测试（verifier independence）。判定脚本 `verify.sh` 生成在工作区外，agent 够不到。
- 每练习 `capabilities=[convergence]`、`source=polyglot`，报告按语言/维度分组聚合。

#### polyglot 环境准备（六语言工具链）

Adapter 只做「加载 + 隔离 + 判定」，实际测试由宿主工具链执行。六语言各自的测试命令与依赖：

| 语言 | 测试命令 | 宿主依赖 |
| --- | --- | --- |
| go | `go test ./...` | go |
| python | `python3 -m pytest -q *_test.py` | python3 + `pip install pytest` |
| rust | `cargo test --quiet` | rust（`brew install rust` 或 rustup） |
| javascript | `npm install && npm test`（jest） | node + npm |
| java | `gradle test`（**系统 gradle**，非 `./gradlew`） | JDK + `brew install gradle` |
| cpp | `cmake -B build … && ./build/<slug>` | cmake + C++ 编译器 |

两个环境注意点（受限网络下的坑）：
- **java 用系统 gradle 而非 `./gradlew`**：练习自带的 wrapper 会从 `services.gradle.org` 拉取上百 MB 发行版，
  受限网络易被重置。系统 gradle 直接构建（junit5 依赖走 mavenCentral，体积小）。若系统 gradle 为 9.x，需一个
  `~/.gradle/init.gradle` 为所有 java 工程注入 JUnit Platform launcher（8.x 时代的 build.gradle 依赖其自动提供）：
  ```groovy
  allprojects { plugins.withId('java') { dependencies { testRuntimeOnly 'org.junit.platform:junit-platform-launcher' } } }
  ```
- java 运行需 `JAVA_HOME` 与 `PATH` 指向 JDK（如 `export JAVA_HOME=/opt/homebrew/opt/openjdk`）。

#### polyglot 可解性自证（oracle selfcheck，不花 LLM）

`internal/eval/adapter/polyglot/TestOracleSolutionsPass` 是受 `POLYGLOT_DIR` 门控的集成测试：用数据集自带的
参考解（example）代替 agent 产出，走**真实 adapter + verifier 代码路径**跑通每语言测试，证明数据集加载、
工作区隔离、六语言命令、pristine 全部正确（对标可解性双校验的 polyglot 版）。缺工具链的语言自动跳过。

```bash
export POLYGLOT_DIR=~/.cache/cogent-eval/polyglot-benchmark   # clone 的数据集
export POLYGLOT_ORACLE_LIMIT=1                                # 每语言取样数（默认 1）
export JAVA_HOME=/opt/homebrew/opt/openjdk; export PATH="$JAVA_HOME/bin:$PATH"
GOTOOLCHAIN=auto go test ./internal/eval/adapter/polyglot -run TestOracleSolutionsPass -v -timeout 40m
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
**E4-2**（`polyglot.Adapter`，多语言，接入模式 B）已落地到
[`internal/eval/adapter/polyglot/`](../internal/eval/adapter/polyglot/)：数据集加载 + 工作区隔离（排除 `.meta/`、目录命名为 slug）
+ 测试 pristine 防篡改 + 六语言测试命令 + CLI `--dataset=polyglot` + 单测；并经 `TestOracleSolutionsPass` 用参考解
走真实代码路径**六语言（go/python/rust/js/java/cpp）端到端全绿**验证。
运行期 Docker 任务（E1-5）与其余主流基准 Adapter（E3 SWE-bench / E4-1 Terminal-Bench）见 spec §8 排期。
