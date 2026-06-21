// Package orchestrate 负责把模型一次返回的多个工具调用编排成"并发批 + 串行批"再调度执行。
// 连续的并发安全（只读）工具合并为同一并发批并行执行，其余工具各自独占串行批；
// 并发结果按请求顺序合并，保证 tool_use↔tool_result 配对不乱、零写竞态、可被 ctx 中断。
//
// 职责边界：本包只承担"分批 + 并发调度"这一可独立单测的核心，单个工具的实际执行
// （档位校验、事件发送、未知工具规范化、tool.call span）通过 RunFunc 回调注入，
// 复用 engine 既有逻辑。依赖方向：engine → orchestrate → {tool, types, observe, errgroup}，
// 本包不反向依赖 engine，也不感知 StreamEvent。
package orchestrate

import (
	"context"
	"fmt"

	"golang.org/x/sync/errgroup"

	"github.com/alaindong/cogent/internal/observe"
	"github.com/alaindong/cogent/internal/tool"
	"github.com/alaindong/cogent/internal/types"
)

// Batch 是一组将被一起调度的工具调用。Concurrent 为 true 时批内并行执行，否则串行（通常仅 1 个）。
type Batch struct {
	Concurrent bool                 // 是否并发执行批内工具
	Blocks     []types.ToolUseBlock // 本批的工具调用，按模型请求顺序排列
}

// RunFunc 执行单个工具调用并返回规范化后的 tool_result 消息（错误已被规整为 IsError 结果，不返回 Go error）。
type RunFunc func(ctx context.Context, block types.ToolUseBlock) types.Message

// PartitionBatches 把工具调用序列切分为并发/串行批：向前扫描，连续的并发安全工具合并进同一并发批，
// 其余工具各自独占一个串行批。pool 为 nil 或工具未知/非并发安全时一律按串行处理（fail-closed，最坏退化为全串行）。
func PartitionBatches(blocks []types.ToolUseBlock, pool tool.Pool) []Batch {
	var batches []Batch
	var concurrent []types.ToolUseBlock
	flush := func() {
		if len(concurrent) == 0 {
			return
		}
		batches = append(batches, Batch{Concurrent: true, Blocks: concurrent})
		concurrent = nil
	}
	for _, b := range blocks {
		if isConcurrencySafe(b, pool) {
			concurrent = append(concurrent, b)
			continue
		}
		flush()
		batches = append(batches, Batch{Concurrent: false, Blocks: []types.ToolUseBlock{b}})
	}
	flush()
	return batches
}

// isConcurrencySafe 报告某工具调用是否可并发执行；pool 为 nil 或工具未知时返回 false（fail-closed）。
func isConcurrencySafe(block types.ToolUseBlock, pool tool.Pool) bool {
	if pool == nil {
		return false
	}
	t, ok := pool.Get(block.Name)
	if !ok {
		return false
	}
	return t.IsConcurrencySafe(block.Input)
}

// Run 逐批执行并按请求顺序返回全部 tool_result 消息。每批起一个 tool.batch span。
// 并发批用 errgroup + 索引槽位写法并行执行（无锁、保序）；串行批逐个执行。
// ctx 取消时跳过尚未执行的块且不产出零值 Message，以免破坏 function calling 配对。
func Run(ctx context.Context, batches []Batch, run RunFunc, tracer observe.Tracer) []types.Message {
	total := 0
	for _, b := range batches {
		total += len(b.Blocks)
	}
	results := make([]types.Message, 0, total)
	for _, b := range batches {
		if ctx.Err() != nil {
			break
		}
		results = append(results, runBatch(ctx, b, run, tracer)...)
	}
	return results
}

// runBatch 执行单个批：起 tool.batch span，按 Concurrent 选择并发或串行执行。
func runBatch(ctx context.Context, b Batch, run RunFunc, tracer observe.Tracer) []types.Message {
	ctx, end := tracer.Start(ctx, "tool.batch",
		observe.Attr{Key: "batch.concurrent", Value: b.Concurrent},
		observe.Attr{Key: "batch.size", Value: len(b.Blocks)},
	)
	var msgs []types.Message
	if b.Concurrent && len(b.Blocks) > 1 {
		msgs = runConcurrent(ctx, b.Blocks, run)
	} else {
		msgs = runSerial(ctx, b.Blocks, run)
	}
	end(nil)
	return msgs
}

// runSerial 串行执行一批工具调用；ctx 取消时提前停止并仅返回已完成的结果。
func runSerial(ctx context.Context, blocks []types.ToolUseBlock, run RunFunc) []types.Message {
	out := make([]types.Message, 0, len(blocks))
	for i := range blocks {
		if ctx.Err() != nil {
			break
		}
		out = append(out, safeRun(ctx, blocks[i], run))
	}
	return out
}

// runConcurrent 并发执行一批工具调用：每个调用写入自身索引槽位（无锁、无 append 竞态、保请求序），
// errgroup 仅承担 ctx 取消传播与 Wait（工具错误已被规整为 IsError 结果，单个失败不取消兄弟）。
// 取消时跳过未执行块，合并时滤掉空槽位以保持 function calling 配对完整。
func runConcurrent(ctx context.Context, blocks []types.ToolUseBlock, run RunFunc) []types.Message {
	slots := make([]types.Message, len(blocks))
	done := make([]bool, len(blocks))
	g, gctx := errgroup.WithContext(ctx)
	for i := range blocks {
		g.Go(func() error {
			if gctx.Err() != nil {
				return gctx.Err()
			}
			slots[i] = safeRun(gctx, blocks[i], run)
			done[i] = true
			return nil
		})
	}
	_ = g.Wait()
	out := make([]types.Message, 0, len(blocks))
	for i := range slots {
		if done[i] {
			out = append(out, slots[i])
		}
	}
	return out
}

// safeRun 在工具执行 goroutine 顶层兜底 panic：单个工具（含 MCP 外部工具）panic 时不击穿进程，
// 而是规整为配对完整的 tool_result(IsError) 让模型把它当普通工具失败自我修正（OPTIMIZE_SPEC R3）。
// recover 仅用于此处兜底，返回 interface{} 不假设其为 error。
func safeRun(ctx context.Context, block types.ToolUseBlock, run RunFunc) (msg types.Message) {
	defer func() {
		if v := recover(); v != nil {
			msg = types.Message{
				Role:      types.RoleTool,
				Text:      fmt.Sprintf("tool panicked: %v", v),
				ToolUseID: block.ID,
				ToolName:  block.Name,
			}
		}
	}()
	return run(ctx, block)
}
