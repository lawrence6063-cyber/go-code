// Package config 负责加载用户级配置文件（~/.cogent/config.env）。
//
// 设计目标：让全局安装的 cogent 二进制在任意目录都能读到密钥与接入配置，
// 不再依赖项目内 .env。加载语义：文件提供默认值，已存在的环境变量优先
// （项目 .env 或 inline export 仍能覆盖用户级配置）。
//
// 文件格式：简单的 KEY=VALUE 行，支持 # 注释与引号值，与 .env 兼容，
// 不引入第三方依赖（无 godotenv）。
package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// UserConfigDir 返回用户级配置目录 ~/.cogent。
func UserConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cogent"), nil
}

// UserConfigFile 返回用户级配置文件路径 ~/.cogent/config.env。
func UserConfigFile() (string, error) {
	d, err := UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "config.env"), nil
}

// Load 把 ~/.cogent/config.env 中的 KEY=VALUE 注入 os.Environ。
// 仅当环境变量未设置时才写入（env 优先于文件），返回实际加载的条目数。
// 文件不存在时不报错（返回 0, nil），由调用方决定是否引导首次配置。
func Load() (int, error) {
	path, err := UserConfigFile()
	if err != nil {
		return 0, err
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	n := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := parseKV(line)
		if !ok {
			continue
		}
		if os.Getenv(key) == "" {
			_ = os.Setenv(key, val)
			n++
		}
	}
	if err := sc.Err(); err != nil {
		return n, fmt.Errorf("scan %s: %w", path, err)
	}
	return n, nil
}

// EnsureDir 创建 ~/.cogent 目录（权限 0o700），已存在时为 no-op。
func EnsureDir() error {
	d, err := UserConfigDir()
	if err != nil {
		return err
	}
	return os.MkdirAll(d, 0o700)
}

// SaveFile 以 0o600 权限写入 KEY=VALUE 形式的配置到 ~/.cogent/config.env。
// 已存在的文件会被覆盖。entries 保持传入顺序。
func SaveFile(entries map[string]string) error {
	if err := EnsureDir(); err != nil {
		return err
	}
	path, err := UserConfigFile()
	if err != nil {
		return err
	}
	var b strings.Builder
	for k, v := range entries {
		b.WriteString(k)
		b.WriteString("=")
		if needsQuote(v) {
			b.WriteString("\"")
			b.WriteString(strings.ReplaceAll(v, "\"", "\\\""))
			b.WriteString("\"")
		} else {
			b.WriteString(v)
		}
		b.WriteString("\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

// parseKV 解析一行 KEY=VALUE，支持可选引号包裹的值。
func parseKV(line string) (key, val string, ok bool) {
	idx := strings.IndexByte(line, '=')
	if idx <= 0 {
		return "", "", false
	}
	key = strings.TrimSpace(line[:idx])
	val = strings.TrimSpace(line[idx+1:])
	if len(val) >= 2 {
		if (val[0] == '"' && val[len(val)-1] == '"') ||
			(val[0] == '\'' && val[len(val)-1] == '\'') {
			val = val[1 : len(val)-1]
			if val[0] == '"' {
				val = strings.ReplaceAll(val, "\\\"", "\"")
			}
		}
	}
	return key, val, true
}

func needsQuote(v string) bool {
	if v == "" {
		return false
	}
	return strings.ContainsAny(v, " \t#\"'")
}
