# MEMORY · cogent 项目长期记忆

## 项目概况
- cogent：用 Go 编写的自主编码 Agent 运行时（DEV_SPEC.md 为设计蓝本，13 节，以 Claude Code 泄露源码静态分析为参照）。
- 定位：求职项目，需"可追问、可深挖"，强调 Go 工程纵深（并发/进程/可观测/安全）。
- 主语言 Go；与作者 Python 项目一 RAG-MCP-Server 通过 MCP 互补（cogent 是 client）。

## 关键约定（务必遵守）
- **go module 路径**：`github.com/alaindong/cogent`（已定，影响所有 import 前缀）。
- **Go 版本**：go.mod 现要求 **go 1.26**（2026-07-02 更新，此前为 1.23）。本机系统 brew 仅 go1.22.5，构建/测试用 `export GOTOOLCHAIN=auto` 让其按 go.mod 自动下载 go1.26.0（已验证可用）。goimports 已装；golangci-lint 未装。
- **架构不变量**：`internal/types` 是最内层共享类型层，不依赖任何业务包；engine 依赖接口经 Deps 注入；fail-closed 默认。
- **对 spec 的合理收敛**：`ToolResult` 定义在 `types` 包（而非 §5.4 的 tool 包），以守住"types 不依赖业务包"，tool 包后续引用 `types.ToolResult`。
- 严格按 Phase 分阶段交付，不提前实现后续 Phase 内容。
- Go 规范：error 末位且必处理、禁 panic 作常规错误流、导出符号带注释、函数<80行、嵌套<4层、参数≤5、import 分组。

## 环境坑（重要，已解决）
- 系统 brew Go 1.22.5 产物运行时报 `missing LC_UUID load command`（dyld abort trap）——1.22.5 内部链接器在较新 macOS 上的已知 bug，Go 1.23+ 修复。`go1.23.10 download` 等由 1.22.5 编译的启动器也无法运行（鸡生蛋）。
- **解决方案（已落地）**：下载官方预编译 tarball `https://go.dev/dl/go1.23.10.darwin-arm64.tar.gz` 解压到 `/tmp/goroot1231/go`，构建运行用 `export PATH=/tmp/goroot1231/go/bin:$PATH; export GOTOOLCHAIN=local`。该 Go 1.23.10 构建产物运行正常。
- go.mod 已设 `go 1.23`。后续构建/运行务必用 Go 1.23+（避免系统 1.22.5）。
- 1.22.5 仍可用于 `go build`/`gofmt`/`go vet`（编译期校验不受影响），仅运行产物有问题。

## polyglot 评测环境（本机已就绪，跨会话稳定）
- 数据集：`~/.cache/cogent-eval/polyglot-benchmark`（aider polyglot-benchmark，225 题 6 语言）。
- **SWE-bench Lite 已下（含仓库镜像，全量就绪）**：`~/.cache/cogent-eval/swebench/lite.jsonl`（300 条，HF datasets-server rows API 分页拉取，非 parquet）+ **仓库镜像 `~/.cache/cogent-eval/swebench/repos/<org>__<name>`（12 个 git --mirror bare 全历史，共 4.1G，300 条实例全覆盖）**。跑分：`--swebench-file=.../lite.jsonl --swebench-repos=.../repos`。cloneAt(clone --no-hardlinks + checkout base) 冒烟已验证。（注意：模式 B 真跑测试仍需各 Python 仓库的依赖环境/Docker；镜像只解决 clone/checkout。）
- **Terminal-Bench 已下**：`git clone --depth1` 到 `~/.cache/cogent-eval/terminal-bench`，任务在 **`original-tasks/`**（241 个，非旧版 `tasks/`）；`--terminalbench-dir` 要指 `.../terminal-bench/original-tasks`。
- **Docker 判定环境已就绪（跨会话稳定）**：引擎用 **Colima**（`colima start --cpu 4 --memory 8 --disk 100 --vm-type=vz --vz-rosetta`，rosetta 加速 x86），**socket=`unix://$HOME/.colima/default/docker.sock`**（python docker SDK 需 `export DOCKER_HOST=` 该值，CLI 靠 context colima）。空闲 `colima stop` 省资源。两官方 harness 各独立 venv（在 `~/.cache/cogent-eval/`）：`swe-venv`(py3.14)=swebench4.1.0；`tb-venv`(**py3.12**,`brew python@3.12`)=terminal-bench0.2.18（**tb 的 typer 与 py3.14 不兼容，必须 3.12；且勿与 swebench 混装会互降依赖**）。已各用 gold/oracle 端到端验证判定链路通（SWE-bench flask resolved 1/1；TB accelerate-maximal-square 100%）。`~/.docker/config.json` 需去掉 Desktop 残留 `credsStore:desktop`、加 `cliPluginsExtraDirs`。
- **后台长任务坑（务必记）**：execute_command 工具 300s 超时会连带杀后台进程（setsid/disown 都救不了）。唯一可存活模式：任务写 `/tmp/x.sh` → `nohup bash /tmp/x.sh >/dev/null 2>&1 &`（脚本内重定向 log），单独轮询 log。macOS 无 `timeout`（用 gtimeout）；`colima status` 偶发阻塞（用 docker info 探活）。
- 工具链已装：go1.26 / python3.14+pytest / node（brew，勿升级触发 llhttp 崩，崩了 `brew reinstall node`）/ cargo（brew rust）/ openjdk26 / cmake / gradle9.6.1。
- java 运行需：`export JAVA_HOME=/opt/homebrew/opt/openjdk; export PATH="$JAVA_HOME/bin:$PATH"`。且已写 `~/.gradle/init.gradle` 为 java 工程注入 `junit-platform-launcher`（gradle9 兼容 8.x 时代 build.gradle 必需）。
- polyglot 用**系统 gradle** 而非练习自带 `./gradlew`（wrapper 拉发行版会被网络重置）。
- 六语言 harness 正确性验证：`POLYGLOT_DIR=... GOTOOLCHAIN=auto go test ./internal/eval/adapter/polyglot -run TestOracleSolutionsPass -v`（用参考解跑通，不花 LLM）。
- 勿把 `/opt/homebrew/bin` 前置到 PATH：会用 python@3.14 顶掉装了 pytest 的默认 python3。
- **LLM 端点（重要）**：cogent 用 `COGENT_LLM_BASE_URL` 指定端点，**不设会默认打到 OpenAI 端点**（deepseek key 报 401、每轮 LLM 调用失败、$0 成本空转到 budget）。跑 deepseek 必须设 `COGENT_LLM_BASE_URL=https://api.deepseek.com/v1`。deepseek-v4-pro/deepseek-chat 在官方端点均可用。

## SWE-bench Scaffold（test-time scaling，SCAFFOLD_SPEC 已全量实现 2026-07-14）
- 目标：best-of-N 采样 + 可执行信号筛选把 Resolved@1 抬过采样噪声（强模型 prompt-scaffold 已榨干）。
- **落点**（守依赖方向，评测层不被内核 import）：
  - 温度旋钮（唯一内核触点）：`internal/engine` 读 `COGENT_TEMPERATURE` 透传 `llm.Request.Temperature`，越界/未设=0=provider 默认（行为不变）。
  - Selector 纯 Go：`internal/eval/scaffold/`（`Select`/`NormalizeDiff`/`RankFiles`/`LoadArtifacts`），CLI `cogent eval scaffold-select`。
  - Localizer：`internal/eval/adapter/swebench/localize.go`，env `COGENT_SWEBENCH_LOCALIZE`（默认关）。
  - Sampler：`scripts/eval-swebench-scaffold.sh`；A/B 三档：`scripts/eval-swebench-scaffold-ab.sh`；复现/回归信号：`eval/scaffold/select_by_tests.py`。
- **合规红线**：选择信号绝不碰 `test_patch/FAIL_TO_PASS/PASS_TO_PASS`（Python harness 加载后即删这些键）；回归自导出、复现 LLM 自造。
- 关键 env：`COGENT_SWEBENCH_NBEST`(5) `SAMPLE_TEMP`(0.7) `ENABLE_TESTS`(0) `COGENT_SWEBENCH_REPRO_M`(5)。runtime A/B 数值待真跑填 eval/doc。
- **py3.14 坑**：本机 python 已 3.14，swebench oracle 的 `TestAdapterOracleFixture` 因 pytest assertion rewrite 用了被移除的 `ast.Str` 而失败（环境问题，非代码问题；要跑该测试需 py3.12 或兼容版 pytest）。
- **全量跑磁盘坑（2026-07-16 教训）**：SWE-bench Lite 全量 N=3 跑到一半 **host Data 卷写满**（Docker 判定镜像 + `/var/folders/.../T/cogent-eval` 工作区副本累积），s3–s6 在 `git clone` 工作区即 `No space left on device` 失败。**只完成 s1+s2 = 98 实例：Resolved@1=73.5%（72/98）**，但该子集是 astropy+django 为主、缺 sympy/scikit-learn/sphinx（最难的一大块），**偏乐观、不代表全 Lite**。教训：driver 应「每片判定后 `docker image prune` + 删该片工作区」防累积撑爆；续跑前先腾到 ≥80–100G。driver 幂等（有报告的片自动跳过），腾空间后重跑可只补 s3–s6。
- **contextmgr 压缩缺 Model 导致 400（2026-07-16 修复）**：`contextmgr.summarize` 构造的 `llm.Request` 没设 `Model`，上下文压缩时用空模型名调用 DeepSeek → `400 ... you passed .`（非致命 WARN，压缩静默失败）。**已修**：`Compact`/`summarize` 增加 `model string` 显式参数，engine 传 `e.model`；含 lastReq.Model 断言的回归测试。注意 `scripts/eval-swebench-scaffold.sh` 每片开跑前都 `go build`，故源码修复后**后续分片自动生效、无需重启**（前提：改动必须先本地 `go build ./...` 通过，否则 build 失败会让 driver 崩）。
- **SWE-bench Lite 全量 N=3 纯投票 最终结果（2026-07-17 完成）**：6 片全判定完成，**Resolved@1=64.1%（184/287）**，若按 300 分母（13 个空补丁未进判定计未解决）=61.3%。deepseek-v4-pro，best-of-3+去重投票，无测试信号，error=0。分片：s1=75.5% s2=71.4% s3=70.8% s4=49.0%(matplotlib短板) s5=52.2% s6(sympy)=65.2%。结论：73.5% 是 astropy+django 偏乐观子集，全量补齐难仓库后回落到 64.1%——这是可对外引用的全 Lite 值。相对 60% 单发基线方向正向但**非同组对照**，要严谨证明需在同 287 实例跑单发 baseline。结果文档 `eval/doc/swebench-lite-full-n3-result-20260716.md`。遗留：13 个实例 agent 未产出补丁；s6 summarize 有条 `I/O operation on closed file` 小瑕疵（不影响判定）。
