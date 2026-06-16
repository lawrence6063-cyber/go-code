# cogent · 自主编码 Agent 运行时（Go）

> 代号 `cogent`（Coding aGENT in Go）｜一个能在真实代码库里自主干活的生产级 Agent Runtime。

`cogent` 给定自然语言任务（"修复这个 bug / 给函数加单测 / 重构 X 为 Y"），自主完成
**探索代码 → 制定计划 → 调用工具读写文件、执行命令 → 跑测试验证 → 失败自我修正**，
全过程流式可见、可中断、可恢复。

## 快速开始（Phase 2：能动手改代码）

当前已交付：DeepSeek LLM 接入 + 工具化 ReAct 主循环 + function calling + 权限三态与人在环（HITL）+ 交互式 REPL。
cogent 现在能在真实仓库里读文件、检索、改文件、跑命令，并把结果回流给模型自我验证。

```bash
# 1. 配置密钥（仅从环境变量读取，严禁硬编码）
export DEEPSEEK_API_KEY=sk-xxxx
# 可选：自定义 OpenAI 兼容 BaseURL（默认 https://api.deepseek.com/v1）
export COGENT_LLM_BASE_URL=https://api.deepseek.com/v1
# 可选：指定模型（默认 deepseek-chat）
export COGENT_MODEL=deepseek-chat

# 2. 进入交互式对话（默认 auto 档位，可自主读写）
go run ./cmd/cogent run
# 或携带首轮任务
go run ./cmd/cogent run "给 internal/foo 的 Bar 函数加一行日志"

# 运行档位：plan（只读探索+产计划）/ ask（只读问答）/ auto（默认，可写）
go run ./cmd/cogent run --mode=plan "梳理一下项目结构并给出改造计划"
```

进入 REPL 后：在 `you>` 输入任务，模型自主调用工具完成；输入 `exit`/`quit` 或按 `Ctrl-C` 退出。

### 内建工具

| 工具 | 作用 | 只读/并发安全 | 权限 |
| --- | --- | --- | --- |
| `read_file` | 读取工作区文件 | 是 | 自动放行 |
| `list_dir` | 列出目录条目 | 是 | 自动放行 |
| `grep` | 正则检索文件内容 | 是 | 自动放行 |
| `write_file` | 创建/覆盖文件 | 否 | ask（HITL）|
| `edit_file` | 按旧串精确替换 | 否 | ask（HITL）|
| `bash` | 执行 shell 命令（最小安全版）| 否 | ask；危险命令直接拒 |

### 人在环（HITL）

写/执行类工具默认在中断点暂停征询：

```
[permission] tool "write_file" requests execution:
  input: {"path":"main.go","content":"..."}
  approve / edit / reject? [a/e/r]
```

- `a` 批准原样执行；`e` 输入修正后的 JSON 入参再执行；`r` 拒绝并可附指引让模型改道。

### 安全边界（Phase 2）

- 所有文件操作经路径校验，`../` 越界被拒；`.cogent/`、`.git/` 等控制面写入被禁止。
- `bash` 拦截破坏性命令（`rm -rf /`、`curl ... | sh`、fork bomb 等），默认 ask。
- 密钥仅来自环境变量；完整命令沙箱与并发分批调度将在 Phase 3 落地。

## 开发

```bash
go build ./...
go test -race ./...   # 单元 + 组件测试（含 goroutine 泄漏检测）
```


