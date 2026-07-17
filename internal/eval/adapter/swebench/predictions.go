// 本文件实现 SWE-bench 官方判定（接入模式 A，EVAL_SPEC §5.2.1/§5.2.3）所需的 predictions 导出：
// 把 agent 在各工作区产出的补丁收集为官方 predictions.jsonl 格式，交 sb-cli 云端 / run_evaluation
// 本地 Docker 判定——免在本仓复刻各仓库测试环境，结果与官方口径一致、可对外引用。
package swebench

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// Prediction 是 SWE-bench 官方 predictions.jsonl 的一行（字段名对齐官方 harness）。
type Prediction struct {
	InstanceID      string `json:"instance_id"`        // 样本标识
	ModelNameOrPath string `json:"model_name_or_path"` // 产出补丁的模型 / agent 标识
	ModelPatch      string `json:"model_patch"`        // agent 产出的 unified diff（相对 base 提交）
}

// ModelPatch 返回工作区相对 base 提交的补丁（agent 的修改），并排除隐藏判定测试触及的文件——
// 官方 harness 会自行叠加 test_patch，模型补丁不应重复携带测试改动（否则可能冲突）。
func ModelPatch(ctx context.Context, workRoot, base, testPatch string) (string, error) {
	args := []string{"diff", base, "--"}
	args = append(args, ".")
	for _, p := range patchPaths(testPatch) {
		args = append(args, ":(exclude)"+p)
	}
	res, err := runGit(ctx, workRoot, args...)
	if err != nil {
		return "", err
	}
	if res.exitCode != 0 {
		return "", fmt.Errorf("git diff failed: %s", oneLine(res.stderr))
	}
	return res.stdout, nil
}

// WritePredictions 把一组 Prediction 序列化为 JSONL（每行一条）写入 w。
func WritePredictions(w io.Writer, preds []Prediction) error {
	enc := json.NewEncoder(w)
	for i := range preds {
		if err := enc.Encode(preds[i]); err != nil {
			return fmt.Errorf("encode prediction %s: %w", preds[i].InstanceID, err)
		}
	}
	return nil
}

// CollectPrediction 为一条已跑完的样本抽取模型补丁，组装成一条 Prediction。
// modelName 为产出补丁的模型/agent 标识（空则用占位符）。
func CollectPrediction(ctx context.Context, inst Instance, workRoot, modelName string) (Prediction, error) {
	dctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	patch, err := ModelPatch(dctx, workRoot, inst.BaseCommit, inst.TestPatch)
	if err != nil {
		return Prediction{}, err
	}
	if strings.TrimSpace(modelName) == "" {
		modelName = "cogent"
	}
	return Prediction{InstanceID: inst.InstanceID, ModelNameOrPath: modelName, ModelPatch: patch}, nil
}
