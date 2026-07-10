// Package adapter 把评测样本（native 任务或外部基准）统一映射为 cogent 的目标循环
// 输入（EVAL_SPEC §5.2）。它是评测层依赖图的叶子之一：仅依赖内核的 loop / verify 与标准库，
// 绝不被内核反向 import（守 DEV_SPEC §4.4「依赖只能向内」、EVAL_SPEC §5.3 不破环）。
//
// 每个 Adapter 负责：定位数据集、准备工作区、把单条样本转成一个 Case；跑分逻辑由上层
// Headless 运行器（package eval）统一承载，Adapter 只产出「可被执行的样本」。
package adapter

import (
	"context"
	"time"

	"github.com/alaindong/cogent/internal/loop"
)

// Case 是一条被测样本：喂给目标循环的输入 + 判定归属 + 分组标签 + 判 Pass 所需的期望结局。
// 独立判定器随 Goal.Verifier 注入（执行体无法篡改判定，守独立判定不变量）。
type Case struct {
	ID              string        // 样本稳定标识（跨基准全局唯一，如 "native/fix_off_by_one"）
	Goal            loop.Goal     // 自然语言意图 + WorkRoot + Verifier + Budget
	Meta            Meta          // 难度 / 语言 / 维度标签，用于分组聚合
	ExpectedOutcome string        // 期望结局：achieved | budget_spent | canceled（判 Pass 见 eval §判定矩阵）
	Timeout         time.Duration // 单样本墙钟硬上限（独立于 Budget.MaxWallClock，运行器兜底 kill；<=0 不限）
}

// Meta 承载样本的分组标签（与 native task.yaml 对齐），供运行器按维度聚合指标。
type Meta struct {
	Difficulty   string   // easy | medium | hard
	Languages    []string // go | python | rust | ...
	Capabilities []string // convergence | budget | injection | review | runtime | exploration
	Source       string   // native | swebench | terminalbench | polyglot
}

// Adapter 把一个评测数据集转换为可迭代的被测样本集合。
type Adapter interface {
	// Name 返回数据集名（用于报告分组与 CLI 选择）。
	Name() string
	// Prepare 校验数据集与所需依赖；缺依赖时返回明确错误（fail-fast）。
	Prepare(ctx context.Context) error
	// Cases 产出被测样本；ctx 取消即停止产出并释放资源（无泄漏）。
	Cases(ctx context.Context) ([]Case, error)
}
