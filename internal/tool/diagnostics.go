// Package tool 中的 diagnostics.go 实现 diagnostics 工具：跑 gofmt -l/go vet/go build
// 三条固定的验证命令并把结果结构化回流，把"改完代码要不要验证"从"依赖模型自觉去敲
// bash 命令"固化为一个低摩擦、只读语义的显式工具动作。与 bash 的关键区别：入参只有一个
// 受严格字符白名单约束的 path，不接受任意 shell 字符串，从设计上就没有命令注入面。
package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/alaindong/cogent/internal/observe"
	"github.com/alaindong/cogent/internal/permission"
	"github.com/alaindong/cogent/internal/sandbox"
	"github.com/alaindong/cogent/internal/types"
)

// diagnosticsPathPattern 是 path 入参允许的字符集：字母数字与常见路径符号（含 Go 的 "..."
// 通配后缀），拒绝空格/引号/分号/管道/反引号/$ 等一切 shell 元字符——input 从设计上就没有
// 命令注入面，不依赖后续转义兜底。
var diagnosticsPathPattern = regexp.MustCompile(`^[A-Za-z0-9_./-]*$`)

// diagnosticsTool 对工作目录跑固定的格式/静态检查/编译验证；只读语义、可并发、直接放行。
type diagnosticsTool struct {
	Defaults
	sb       sandbox.Sandbox
	workRoot string
	tracer   observe.Tracer
}

// NewDiagnostics 构造 diagnostics 工具，复用已装配的 sandbox 与工作根目录、tracer。
func NewDiagnostics(sb sandbox.Sandbox, workRoot string, tracer observe.Tracer) Tool {
	return &diagnosticsTool{sb: sb, workRoot: workRoot, tracer: tracer}
}

func (t *diagnosticsTool) Name() string { return "diagnostics" }
func (t *diagnosticsTool) Description() string {
	return "Run static verification (gofmt -l, go vet, go build) over the workspace or a " +
		"specific Go package pattern and return a structured pass/fail report per check. " +
		"Use this after editing Go code to verify formatting, vet issues, and compile errors " +
		"without manually crafting bash commands."
}

func (t *diagnosticsTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"optional Go package pattern to check, e.g. './...' (default), '.', or 'internal/tool/...'"}}}`)
}

// IsReadOnly 只读：三条命令均不修改受版本控制的文件（go build 输出被丢弃到 /dev/null）。
func (t *diagnosticsTool) IsReadOnly(json.RawMessage) bool { return true }

// IsConcurrencySafe 只读可并发。
func (t *diagnosticsTool) IsConcurrencySafe(json.RawMessage) bool { return true }

// CheckPermission 只读操作直接放行。
func (t *diagnosticsTool) CheckPermission(context.Context, json.RawMessage) (permission.Decision, error) {
	return permission.Decision{Behavior: permission.BehaviorAllow}, nil
}

// diagnosticsSection 是一条验证命令及其展示标题。
type diagnosticsSection struct {
	title string
	cmd   string
}

// Call 校验 path 后依次跑三条命令，汇总为一份结构化报告；任一命令报告问题即整体 IsError=true
// （信号透明地传给模型，不隐藏问题），但不中断——三条命令总是全部跑完。
func (t *diagnosticsTool) Call(ctx context.Context, input json.RawMessage, _ ProgressSink) (types.ToolResult, error) {
	var in struct {
		Path string `json:"path"`
	}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &in); err != nil {
			return types.ToolResult{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
		}
	}
	pattern, err := validateDiagnosticsPath(t.workRoot, in.Path)
	if err != nil {
		return types.ToolResult{Content: err.Error(), IsError: true}, nil
	}

	ctx, end := t.tracer.Start(ctx, "diagnostics.run", observe.Attr{Key: "path", Value: pattern})
	defer end(nil)

	report, anyIssue := t.runSections(ctx, buildDiagnosticsSections(pattern))
	return types.ToolResult{Content: strings.TrimSpace(report), IsError: anyIssue}, nil
}

// runSections 依次执行每条验证命令并汇总为可读报告；返回是否有任一命令报告了问题。
func (t *diagnosticsTool) runSections(ctx context.Context, sections []diagnosticsSection) (string, bool) {
	var b strings.Builder
	anyIssue := false
	for _, sec := range sections {
		res, execErr := t.sb.Exec(ctx, sec.cmd)
		if execErr != nil {
			fmt.Fprintf(&b, "## %s\nexec error: %v\n\n", sec.title, execErr)
			anyIssue = true
			continue
		}
		out := truncate(mergeOutput(res.Stdout, res.Stderr), maxBashOutput)
		issue := res.ExitCode != 0 || (sec.title == "gofmt -l" && strings.TrimSpace(out) != "")
		if issue {
			anyIssue = true
		}
		status := "ok"
		if issue {
			status = "issues found"
		}
		if out == "" {
			out = "(no output)"
		}
		fmt.Fprintf(&b, "## %s [%s, exit=%d]\n%s\n\n", sec.title, status, res.ExitCode, out)
	}
	return b.String(), anyIssue
}

// validateDiagnosticsPath 校验 path：空则回退默认 "./..."；非空须命中字符白名单，
// 且去掉 Go 的 "/..." 通配后缀后仍须通过 sandbox.ValidatePath 的工作区边界校验。
func validateDiagnosticsPath(workRoot, path string) (string, error) {
	if path == "" {
		return "./...", nil
	}
	if !diagnosticsPathPattern.MatchString(path) {
		return "", fmt.Errorf("path contains disallowed characters: %q", path)
	}
	base := strings.TrimSuffix(path, "/...")
	if _, err := sandbox.ValidatePath(workRoot, base); err != nil {
		return "", err
	}
	return path, nil
}

// buildDiagnosticsSections 为已校验的 pattern 构造三条验证命令；pattern 为默认的
// "./..." 时 gofmt 改用 find+xargs 递归检查全部 .go 文件（gofmt -l 本身不递归目录）。
func buildDiagnosticsSections(pattern string) []diagnosticsSection {
	q := shellQuote(pattern)
	gofmtCmd := "gofmt -l " + q
	if pattern == "./..." {
		gofmtCmd = `find . -name '*.go' -not -path './vendor/*' -not -path './.git/*' -not -path './.cogent/*' | xargs -r gofmt -l`
	}
	return []diagnosticsSection{
		{title: "gofmt -l", cmd: gofmtCmd},
		{title: "go vet", cmd: "go vet " + q},
		{title: "go build", cmd: "go build -o /dev/null " + q},
	}
}

// shellQuote 把 s 包成 POSIX 单引号字面量（转义内部单引号），用于把已经过字符白名单
// 校验的 pattern 安全嵌入 `bash -c` 组合命令；仅用于内部已校验字符串，不作为通用转义工具。
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
