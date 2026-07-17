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
| `runtime_build_fix` | easy | runtime | 构建：`go build` 成功 + 产物存在 + `go test` |
| `runtime_http_serve` | medium | runtime | 跑服务：`httptest` 起真实回环 HTTP 服务，断言状态码+响应体 |
| `runtime_perf_optimize` | medium | runtime | 性能：大输入时间预算判定（O(n²)→O(n)） |
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

### 夜间跑分（非门禁质量基线，E5-2）

`scripts/eval-nightly.sh` + `.github/workflows/eval-nightly.yml` 提供**非门禁**的夜间跑分：构建 cogent →
`eval run` native → 有基线则 `eval compare`（退化仅告警不阻断），定位为「质量基线体检」而非 CI 门禁
（承接 DEV_SPEC §9.2）。**密钥仅走 `DEEPSEEK_API_KEY` 环境变量**（本地 export / `~/.cogent/config.env` /
CI secret），脚本与工作流均不硬编码密钥。

```bash
# 本地手动跑（key 仅走 env，不落盘）
DEEPSEEK_API_KEY=sk-... COGENT_MODEL=deepseek-chat bash scripts/eval-nightly.sh

# 只跑某维度子集、指定基线做回归对比
EVAL_ARGS="--capability=convergence" EVAL_BASELINE=eval/doc/nightly-baseline.json \
  DEEPSEEK_API_KEY=sk-... bash scripts/eval-nightly.sh
```

- 环境变量：`COGENT_MODEL`（默认 deepseek-chat）、`COGENT_LLM_BASE_URL`（默认 deepseek 端点）、
  `EVAL_ARGS`（透传 `eval run`）、`EVAL_CONCURRENCY`（默认 3）、`EVAL_BASELINE`（compare 基线，缺则跳过）、
  `EVAL_STRICT=1`（改为透传 `eval run` 退出码；默认非门禁 0 退出）。
- 报告落 `eval-artifacts/nightly/<ts>/`（gitignore）；GitHub Actions 每日定时 + 手动触发，`continue-on-error`
  非门禁，上传报告 artifact。首次跑无基线会提示 `cp report.json <baseline>` 播种基线。

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

### swebench 套件（跨仓库困难任务，接入模式 A/B，E3-1）

`--dataset=swebench` 接入 [SWE-bench](https://www.swebench.com)（真实 GitHub issue 修复），覆盖「跨仓库 /
架构级困难任务 + 探索」维度。数据集与仓库均由用户预先准备（Adapter 只读取、不联网拉取）：

- **数据集**：从 HuggingFace 导出 `SWE-bench_Lite` 为 JSONL（每行一条 instance），`--swebench-file` 指向它。
  取 Lite 小子集即可（个人项目最小可行路径，`--limit=20`）。
- **仓库镜像**：把 instance 引用的仓库 clone 到本地目录，`--swebench-repos` 指向其根；命名用
  `<repos>/<org>__<name>`（SWE-bench 惯例）或 `<repos>/<org>/<name>`。

两种接入模式（EVAL_SPEC §5.2.1）本 Adapter 均支持：

| 模式 | 判定归属 | 说明 |
| --- | --- | --- |
| **A 官方判定**（推荐） | 官方 `sb-cli` 云端 / `run_evaluation` 本地 Docker | 跑完自动导出 `predictions.jsonl` 到 artifact 目录，交官方判定——免在本仓复刻各仓库测试环境，结果与官方口径一致 |
| **B Adapter 接回** | 本地 `instanceVerifier` | 瞬态套隐藏 `test_patch` 跑 FAIL_TO_PASS/PASS_TO_PASS 判定，吃到 loop 过程指标；需各仓库测试环境可用（真实 SWE-bench 多需 Docker） |

```bash
# 先看会跑哪些样本（校验数据集与筛选，不建副本、不跑 agent）
cogent eval run --dataset=swebench --swebench-file=/path/to/lite.jsonl --dry-run

# 取子集跑分（模式 B 本地判定 + 自动导出 predictions.jsonl 供模式 A）
cogent eval run --dataset=swebench \
  --swebench-file=/path/to/lite.jsonl --swebench-repos=/path/to/repos \
  --limit=20 --model=deepseek-chat --max-iterations=8 --max-cost=1

# 只跑指定 instance
cogent eval run --dataset=swebench --swebench-file=/path/to/lite.jsonl \
  --swebench-repos=/path/to/repos --id=django__django-11099
```

swebench 关键点：
- **工作区隔离**：每条样本从本地镜像 clone 到独立副本并检出 base 提交，绝不污染仓库镜像。
- **gold patch 绝不喂 agent**；意图刻意不透露 FAIL_TO_PASS / test_patch（隐藏判定测试），避免面向测试作弊。
- **隐藏测试 pristine**：判定前把 `test_patch` 触及文件还原到 base、瞬态 `git apply` 隐藏测试、判完反向还原，
  agent 全程看不到隐藏测试、也改不动被实际执行的判定测试（verifier independence）。
- **Mode A 导出**：`predictions.jsonl` 抽 `git diff base` 作 model_patch 并排除测试文件路径，交官方判定。
- **SWE-bench scaffold（默认启用）**：`intent()` 在 issue 文本后注入针对真实 issue 修复常见失败模式的工作指引——
  ①定位优先（先 grep/find_files/read_file 找到根因文件再改）②最小单文件改动（禁散弹式跨文件改动）
  ③禁改测试/vendored/配置(setup.py、pyproject.toml…)/changelog ④禁盲装依赖或跑测试自证（工作区无依赖，空耗迭代）
  ⑤补丁卫生（无调试残留、diff 仅含必要行）。用 `COGENT_SWEBENCH_SCAFFOLD=0` 关闭回退最小意图，便于 A/B 对比裸 agent：
  ```bash
  # 裸 agent 基线（关闭 scaffold）
  COGENT_SWEBENCH_SCAFFOLD=0 cogent eval run --dataset=swebench --swebench-file=... --swebench-repos=... --limit=20
  # scaffold（默认）——对比 Resolved@1 提升
  cogent eval run --dataset=swebench --swebench-file=... --swebench-repos=... --limit=20
  ```
  一键闭环脚本见 [`scripts/eval-swebench-modeA.sh`](../scripts/eval-swebench-modeA.sh)（cogent 产补丁 → 官方 Docker 判定 → Resolved@1）。

#### swebench 可解性自证（fixture oracle，不花 LLM / 无 Docker）

`internal/eval/adapter/swebench/TestAdapterOracleFixture` 用自包含的合成 git 仓库（缺陷源码 + 隐藏
`test_patch` + gold 参考解），走**真实 Adapter + instanceVerifier 代码路径**，在无 Docker / 无网络下验证：
数据集加载、仓库镜像解析、工作区 clone/checkout 隔离、隐藏测试瞬态判定（修复前 FAIL_TO_PASS 失败、gold
修复后通过）、判完还原、Mode A model_patch 抽取全部正确（对标 polyglot 的 `TestOracleSolutionsPass`）。
含 **go 与 python（pytest，SWE-bench 主语言）双语言变体**，缺对应 runtime 的变体自动跳过。

```bash
GOTOOLCHAIN=auto go test ./internal/eval/adapter/swebench -run TestAdapterOracleFixture -v
```

备好真实数据集 + 本地仓库镜像后，可用官方 gold patch 对真实样本自证（`SWEBENCH_ORACLE_LIMIT` 控取样数）——
真实 Python 仓库测试多需其依赖环境（Docker）：

```bash
export SWEBENCH_FILE=/path/to/lite.jsonl SWEBENCH_REPOS=/path/to/repos SWEBENCH_ORACLE_LIMIT=1
GOTOOLCHAIN=auto go test ./internal/eval/adapter/swebench -run TestOracleGoldPatchPasses -v -timeout 40m
```

### terminalbench 套件（运行期/跨领域困难任务，接入模式 B，E4-1）

`--dataset=terminalbench` 接入 [Terminal-Bench](https://www.tbench.ai)（跨 SWE/ML/安全/数据科学的困难任务，
「指令 + 测试脚本 `run-tests.sh` + oracle 参考解 `solution.sh`」三件套）。数据集由用户自行 clone，
用 `--terminalbench-dir` 指向其根目录（含 `tasks/<id>/` 或直接是任务目录）。

Terminal-Bench 任务本质是 **Docker 环境**（每任务 `Dockerfile` + 容器内依赖）：

| 模式 | 判定归属 | 说明 |
| --- | --- | --- |
| **B Adapter 接回**（本 Adapter） | 本地 `taskVerifier` 跑 `run-tests.sh` | 吃到 loop 过程指标；**仅适用于纯文件系统/脚本类任务**，依赖容器内包/服务的任务无法本地判定 |
| **A 官方判定** | 官方 Harbor（`tb` CLI，需 Docker） | 把 cogent 注册为 Harbor 的 agent，容器内判定；结果可比榜单 |

```bash
# 看会跑哪些任务（校验数据集与筛选，不建副本、不跑 agent）
cogent eval run --dataset=terminalbench --terminalbench-dir=/path/to/terminal-bench --dry-run

# 取子集跑分（--capability 复用为 tag 筛选，--difficulty 按难度）
cogent eval run --dataset=terminalbench --terminalbench-dir=/path/to/terminal-bench \
  --limit=10 --model=deepseek-chat
```

terminalbench 关键点：
- **工作区隔离 + 隐藏测试**：工作区副本排除 `run-tests.sh`/`tests/`/`solution.sh`/`Dockerfile`/`task.yaml`；
  判定时把 `run-tests.sh` + `tests/` 瞬态注入、跑完移除，agent 全程看不到判定测试、也改不动（verifier independence）。
- **oracle 参考解 `solution.sh` 绝不喂 agent**；意图刻意不透露隐藏测试。
- 每任务 `capabilities=[runtime, exploration]+tags`、`source=terminalbench`、难度缺省按 `hard`。

#### terminalbench 可解性自证（fixture oracle，不花 LLM / 无 Docker）

`internal/eval/adapter/terminalbench/TestAdapterOracleFixture` 用一个自包含、Docker-free 的合成任务
（指令 + 隐藏 `run-tests.sh`/`tests/` + oracle `solution.sh`），走**真实 Adapter + taskVerifier 代码路径**，
在无 Docker/网络下验证：数据集扫描与 task.yaml 解析、工作区隔离、隐藏测试瞬态注入判定（修复前失败、
oracle 应用后通过）、判完移除。

```bash
GOTOOLCHAIN=auto go test ./internal/eval/adapter/terminalbench -run TestAdapterOracleFixture -v
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
**E3-1**（`swebench.Adapter`，跨仓库困难任务，接入模式 A/B）已代码级落地到
[`internal/eval/adapter/swebench/`](../internal/eval/adapter/swebench/)：JSONL 数据集加载 + 本地仓库镜像 clone/checkout 工作区隔离
+ 隐藏 `test_patch` 瞬态判定（pristine）+ Mode A `predictions.jsonl` 导出 + CLI `--dataset=swebench` + 单测；并经
`TestAdapterOracleFixture` 用合成 git 仓库走真实 adapter+verifier 路径在无 Docker/网络下验证。
**E4-1**（`terminalbench.Adapter`，运行期/跨领域困难任务，接入模式 B）已代码级落地到
[`internal/eval/adapter/terminalbench/`](../internal/eval/adapter/terminalbench/)：task.yaml 块标量解析 + 工作区隔离（排除隐藏测试/参考解）
+ 隐藏测试（`run-tests.sh`/`tests/`）瞬态注入判完移除 + CLI `--dataset=terminalbench` + 单测；并经
`TestAdapterOracleFixture` 用合成 Docker-free 任务走真实 adapter+verifier 路径验证。
运行期 native 任务（E1-5）已落地；E5 规模化/夜间 CI 见 spec §8 排期。
