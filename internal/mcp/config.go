package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// configRelPath 是 MCP server 配置相对工作根目录的路径。
const configRelPath = ".cogent/mcp.json"

// fileConfig 对应 .cogent/mcp.json 的顶层结构（沿用社区惯例的 mcpServers 键）。
type fileConfig struct {
	MCPServers map[string]serverEntry `json:"mcpServers"`
}

// serverEntry 是配置文件中单个 server 的条目。
type serverEntry struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

// LoadConfig 读取 <workRoot>/.cogent/mcp.json 并解析为校验过的 server 配置列表。
// 文件不存在时返回 (nil, nil)——MCP 是可插拔增强，缺省不报错、不影响 cogent 独立运行。
func LoadConfig(workRoot string) ([]ServerConfig, error) {
	path := filepath.Join(workRoot, configRelPath)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read mcp config: %w", err)
	}
	var fc fileConfig
	if err := json.Unmarshal(data, &fc); err != nil {
		return nil, fmt.Errorf("parse mcp config %s: %w", path, err)
	}
	return toServerConfigs(fc)
}

// toServerConfigs 把文件条目转为有序、已校验的 ServerConfig 列表（按名排序保证确定性）。
func toServerConfigs(fc fileConfig) ([]ServerConfig, error) {
	cfgs := make([]ServerConfig, 0, len(fc.MCPServers))
	for name, e := range fc.MCPServers {
		cfg := ServerConfig{Name: name, Command: e.Command, Args: e.Args, Env: e.Env}
		if err := cfg.validate(); err != nil {
			return nil, fmt.Errorf("mcp server %q: %w", name, err)
		}
		cfgs = append(cfgs, cfg)
	}
	sort.Slice(cfgs, func(i, j int) bool { return cfgs[i].Name < cfgs[j].Name })
	return cfgs, nil
}
