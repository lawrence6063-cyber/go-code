package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/alaindong/cogent/internal/config"
)

// newInitCmd 构造 init 子命令：交互式引导首次配置，写入 ~/.cogent/config.env。
// 让全局安装的 cogent 在任意目录都能读到密钥，体验对齐 Claude Code 的首次登录。
func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "首次配置：交互式写入 ~/.cogent/config.env（API 密钥、模型等）",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runInit()
		},
	}
}

// runInit 交互式收集配置并落盘。
func runInit() error {
	dir, _ := config.UserConfigDir()
	cfgPath, _ := config.UserConfigFile()
	fmt.Printf("cogent init — 配置将写入 %s\n\n", cfgPath)

	// 读取已有配置作为默认值（回车保留现状）。
	existing := readExisting(cfgPath)

	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	ask := func(prompt, def string) string {
		if def != "" {
			fmt.Printf("%s [回车保留 %s]: ", prompt, maskKey(def))
		} else {
			fmt.Printf("%s: ", prompt)
		}
		if !sc.Scan() {
			os.Exit(0)
		}
		v := strings.TrimSpace(sc.Text())
		if v == "" {
			return def
		}
		return v
	}

	apiKey := ask("DEEPSEEK_API_KEY（必填）", existing["DEEPSEEK_API_KEY"])
	if apiKey == "" {
		return fmt.Errorf("DEEPSEEK_API_KEY 不能为空，请重跑 cogent init")
	}
	baseURL := ask("COGENT_LLM_BASE_URL（回车用默认 https://api.deepseek.com/v1）",
		or(existing["COGENT_LLM_BASE_URL"], "https://api.deepseek.com/v1"))
	model := ask("COGENT_MODEL（回车用默认 deepseek-chat）",
		or(existing["COGENT_MODEL"], "deepseek-chat"))

	entries := map[string]string{
		"DEEPSEEK_API_KEY":     apiKey,
		"COGENT_LLM_BASE_URL": baseURL,
		"COGENT_MODEL":        model,
	}
	if err := config.SaveFile(entries); err != nil {
		return fmt.Errorf("写入配置失败: %w", err)
	}
	_ = dir
	fmt.Printf("\n已写入 %s（权限 0o600）。\n", cfgPath)
	fmt.Println("现在可以在任意目录运行 `cogent run` 进入交互式对话。")
	return nil
}

// readExisting 读取已存在的配置文件为 map（容错）。
func readExisting(path string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if idx := strings.IndexByte(line, '='); idx > 0 {
			k := strings.TrimSpace(line[:idx])
			v := strings.TrimSpace(line[idx+1:])
			if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
				v = v[1 : len(v)-1]
			}
			out[k] = v
		}
	}
	return out
}

// or 返回第一个非空字符串。
func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// maskKey 对密钥做部分脱敏，仅显示前4后4。
func maskKey(s string) string {
	if len(s) <= 8 {
		return strings.Repeat("*", len(s))
	}
	return s[:4] + strings.Repeat("*", len(s)-8) + s[len(s)-4:]
}
