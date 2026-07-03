package tui

import (
	"encoding/json"
	"strings"

	"github.com/alaindong/cogent/internal/tui/render"
)

// maxSummaryArg 是单行工具摘要中参数部分的最大显示列宽（超出以省略号截断）。
const maxSummaryArg = 60

// Summarize 按工具类型把入参 JSON 归纳为一行关键参数摘要，供工具调用折叠展示与 HITL 复用。
// 解析失败或未知工具时回退为工具名（可附带可识别的关键字段），保证不 panic。
func Summarize(name string, input json.RawMessage) string {
	args := parseArgs(input)
	if arg := summaryArg(name, args); arg != "" {
		return name + " " + render.Truncate(arg, maxSummaryArg)
	}
	return name
}

// summaryArg 依工具类型抽取最能表达"这次调用在做什么"的关键参数字符串；未知工具返回空串。
func summaryArg(name string, args map[string]string) string {
	switch name {
	case "read_file", "write_file", "edit_file", "list_dir", "find_files":
		return firstNonEmpty(args, "path", "file_path", "filePath", "target_directory", "pattern")
	case "grep", "search_content", "codebase_search":
		return firstNonEmpty(args, "pattern", "query", "regex")
	case "bash", "execute_command":
		return firstNonEmpty(args, "command", "cmd")
	case "task":
		return firstNonEmpty(args, "description", "prompt")
	default:
		// 未知工具：尽力挑一个常见的可读字段作提示。
		return firstNonEmpty(args, "path", "file_path", "command", "pattern", "query", "description")
	}
}

// parseArgs 把工具入参 JSON 浅解析为「字段名 → 字符串值」映射；解析失败返回空映射。
// 仅收敛字符串型顶层字段（工具关键参数几乎都是字符串），其余类型忽略。
func parseArgs(input json.RawMessage) map[string]string {
	out := map[string]string{}
	if len(input) == 0 {
		return out
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(input, &raw); err != nil {
		return out
	}
	for k, v := range raw {
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			out[k] = strings.TrimSpace(s)
		}
	}
	return out
}

// firstNonEmpty 按给定优先级返回第一个非空字段值；都为空时返回空串。
func firstNonEmpty(args map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := args[k]; v != "" {
			return v
		}
	}
	return ""
}
