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
