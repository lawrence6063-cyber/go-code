package tui

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/alaindong/cogent/internal/engine"
	"github.com/alaindong/cogent/internal/tui/completion"
)

// handleSlashCommand 路由并执行一条斜杠命令（line 已去除首尾空白，且以 / 开头）。
// 未知命令给出提示。/exit 由 inputLoop 直接处理以退出循环，故此处不含。
func handleSlashCommand(ctx context.Context, eng engine.Engine, line string) {
	cmd := line
	if i := strings.IndexByte(line, ' '); i >= 0 {
		cmd = line[:i]
	}
	switch cmd {
	case "/undo":
		handleUndo(ctx, eng)
	case "/help":
		printSlashHelp()
	case "/model":
		fmt.Printf("cogent> model: %s\n", slashModelName())
	case "/clear", "/compact":
		fmt.Printf("cogent> %s 暂未实现\n", cmd)
	default:
		fmt.Printf("cogent> 未知命令 %s（输入 /help 查看可用命令）\n", cmd)
	}
}

// printSlashHelp 打印内建斜杠命令清单（复用补全注册表，保持单一事实来源）。
func printSlashHelp() {
	fmt.Println("cogent> 可用命令：")
	for _, c := range completion.NewCommandProvider().Filter("/", 0) {
		fmt.Printf("  %-10s %s\n", c.Name, c.Desc)
	}
}

// slashModelName 返回当前模型名（COGENT_MODEL），未设置时回退 "default"。
func slashModelName() string {
	if m := strings.TrimSpace(os.Getenv("COGENT_MODEL")); m != "" {
		return m
	}
	return "default"
}
