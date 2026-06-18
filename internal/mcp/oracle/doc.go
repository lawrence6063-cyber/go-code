// Package oracle 是一个独立的 Go 模块（拥有自己的 go.mod），用官方 modelcontextprotocol/go-sdk
// 作为 MCP 协议的“离线一致性对照基准（oracle）”：它实现 internal/mcp/mcpconform 定义的同一套
// Client 抽象，并跑同一套 RunSuite 断言。
//
// 为何独立成模块：把官方 SDK 这个较重的依赖完全隔离在子模块里，主模块 go.mod/go.sum 保持零新增依赖、
// 生产二进制不引入 SDK。注意 build tag 并不能达到此效果——go mod tidy 会扫描所有 build tag，
// 仍会把 SDK 写进主 go.mod；唯有独立模块能真正隔离。
//
// SDK 在此仅作“裁判”验证我们的手写协议实现，而非运行期兜底。
package oracle
