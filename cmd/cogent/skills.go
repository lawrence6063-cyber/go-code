package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/alaindong/cogent/internal/skills"
)

// maxInjectedSkills 是单次目标循环注入的最多技能包数（按需召回，避免撑爆上下文）。
const maxInjectedSkills = 3

// augmentWithSkills 把与目标意图相关的 SKILL.md 正文按需拼接到意图中（LOOP_SPEC §4.6 注入点）。
// 不改 engine 固定 system prompt：技能正文以任务侧文本注入（与 reviewer rubric 走 task 同构）。
// 无技能/出错/无相关命中时原样返回（技能是可选增强，缺失不报错）。
func augmentWithSkills(ctx context.Context, workRoot, intent string) string {
	loader := skills.New()
	index, err := loader.List(ctx, workRoot)
	if err != nil || len(index) == 0 {
		return intent
	}
	picked := skills.Relevant(index, intent, maxInjectedSkills)
	if len(picked) == 0 {
		return intent
	}
	var sb strings.Builder
	sb.WriteString(intent)
	sb.WriteString("\n\n---\nReusable project skills that may apply (follow them if relevant):\n")
	for _, p := range picked {
		full, err := loader.Load(ctx, workRoot, p.Name)
		if err != nil {
			continue // 单个技能加载失败：跳过，不阻断
		}
		fmt.Fprintf(&sb, "\n### skill: %s\n%s\n", full.Name, full.Body)
	}
	return sb.String()
}
