// 本文件解析练习的 .meta/config.json（EVAL_SPEC §5.2）：Exercism 用它把练习内文件按用途
// 分类为 solution（待实现，喂给 agent）/ test（判定用，需 pristine 防篡改）/ example（参考解，绝不泄露）。
package polyglot

import (
	"encoding/json"
	"fmt"
	"os"
)

// metaConfig 是 .meta/config.json 中评测关心的子集：files 分类。
type metaConfig struct {
	Files struct {
		Solution []string `json:"solution"` // 学生需实现的文件（喂给 agent 的编辑目标）
		Test     []string `json:"test"`     // 验证实现的测试文件（判定用，pristine 恢复）
		Editor   []string `json:"editor"`   // 编辑器只读辅助文件（如 go 的 cases_test.go，测试依赖，也需 pristine 恢复）
		Example  []string `json:"example"`  // 参考实现（绝不复制进工作区；仅 oracle 自检用）
	} `json:"files"`
}

// readMetaConfig 读取并解析练习目录下的 .meta/config.json。
func readMetaConfig(path string) (metaConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return metaConfig{}, fmt.Errorf("read config.json: %w", err)
	}
	var c metaConfig
	if err := json.Unmarshal(data, &c); err != nil {
		return metaConfig{}, fmt.Errorf("parse config.json: %w", err)
	}
	return c, nil
}
