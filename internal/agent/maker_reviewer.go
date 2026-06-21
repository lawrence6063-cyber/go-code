// Package agent 中的 maker_reviewer.go 实现 Loop 的「制造-审查分离」双角色（LOOP_SPEC §4.2）：
// maker（可写实现者，ModeAuto + 可写工具池）负责改代码，reviewer（只读审查者，ModeAsk +
// 只读工具池）负责审，二者用各自独立的子 Engine（独立上下文），实现「不批改自己的作业」。
// reviewer 通过才落盘，未通过则回滚 maker 改动并把意见反馈给上层带反馈续跑。
package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/alaindong/cogent/internal/engine"
	"github.com/alaindong/cogent/internal/observe"
	"github.com/alaindong/cogent/internal/types"
)

// Role 区分双角色子 Agent 的职责，决定其工具池档位与 span 名。
type Role int

// 双角色枚举。
const (
	RoleMaker    Role = iota // 实现者：可写工具池 + ModeAuto，负责改代码
	RoleReviewer             // 审查者：只读工具池 + ModeAsk，绝不改代码（fail-closed）
)

// spanName 返回角色对应的 trace span 名（§6.1 跨边界传播）。
func (r Role) spanName() string {
	if r == RoleReviewer {
		return "agent.reviewer"
	}
	return "agent.maker"
}

// String 返回角色的可读名（maker/reviewer），用于 span 属性。
func (r Role) String() string {
	if r == RoleReviewer {
		return "reviewer"
	}
	return "maker"
}

// ReviewVerdict 是审查者的结构化裁决（独立于 maker，不批改自己作业）。
type ReviewVerdict struct {
	Approved bool   // 是否通过；fail-closed：解析失败/异常一律视为未通过
	Feedback string // 未通过时给 maker 的修改意见；通过时为空
}

// PipelineResult 汇总一次双角色流水线的产出。
type PipelineResult struct {
	MakerSummary string        // maker 本轮改动摘要
	Verdict      ReviewVerdict // reviewer 裁决
}

// Pipeline 编排「maker 实现 → reviewer 审查 → 通过才落盘」的双角色流水线。
type Pipeline interface {
	// Iterate 让 maker 在工作区实现 task，再由独立 reviewer 审查，返回摘要与裁决。
	Iterate(ctx context.Context, task string) (PipelineResult, error)
}

// Discarder 在审查未通过时丢弃 maker 的本轮改动（「通过才落盘」的 diff 暂存版）。
// 典型实现经沙箱跑白名单 git 命令回滚工作区；可为 nil（不回滚，留 L4 升级为 worktree）。
type Discarder interface {
	// Discard 丢弃工作区中未通过审查的改动。
	Discard(ctx context.Context) error
}

// MakerReviewer 用两套子 Engine 模板依赖实现 Pipeline。maker 应配可写池，
// reviewer 应配只读池；二者可指向不同模型（fast vs strong），成本花在质量闸门刀刃上。
type MakerReviewer struct {
	maker           engine.Deps
	reviewer        engine.Deps
	discarder       Discarder
	tracer          observe.Tracer
	meter           observe.Meter
	maxSummaryBytes int
}

// NewMakerReviewer 用两套依赖构造双角色流水线；discarder 为 nil 时不回滚未通过的改动。
func NewMakerReviewer(maker, reviewer engine.Deps, discarder Discarder) *MakerReviewer {
	mr := &MakerReviewer{
		maker:           maker,
		reviewer:        reviewer,
		discarder:       discarder,
		maxSummaryBytes: defaultMaxSummaryBytes,
	}
	prov := maker.Observe
	if prov == nil {
		prov, _ = observe.New(observe.Config{Enabled: false})
	}
	mr.tracer = prov.Tracer()
	mr.meter = prov.Meter()
	return mr
}

// Iterate 见 Pipeline 接口说明：maker 实现 → reviewer 独立审查 → 解析裁决 →
// 未通过则回滚改动。reviewer 用独立子 Engine 与（可选）不同模型，保证审查独立于实现。
func (m *MakerReviewer) Iterate(ctx context.Context, task string) (PipelineResult, error) {
	makerSummary, err := m.runRole(ctx, RoleMaker, m.maker, task)
	if err != nil {
		return PipelineResult{}, fmt.Errorf("maker: %w", err)
	}
	reviewOut, err := m.runRole(ctx, RoleReviewer, m.reviewer, reviewPrompt(task, makerSummary))
	if err != nil {
		return PipelineResult{}, fmt.Errorf("reviewer: %w", err)
	}
	verdict := parseVerdict(reviewOut)
	m.meter.Count("cogent.review.verdict", 1, observe.Attr{Key: "review.approved", Value: verdict.Approved})
	if !verdict.Approved {
		m.discard(ctx)
	}
	return PipelineResult{MakerSummary: makerSummary, Verdict: verdict}, nil
}

// runRole 用模板依赖新建一个隔离子 Engine 执行一轮任务并收敛输出为摘要。
// 强制 Session=nil 保证上下文隔离；起角色对应的 span 使子节点经 ctx 自动挂接（§8.2）。
func (m *MakerReviewer) runRole(ctx context.Context, role Role, deps engine.Deps, task string) (string, error) {
	deps.Session = nil
	deps.SessionID = ""
	eng, err := engine.New(deps)
	if err != nil {
		return "", fmt.Errorf("build %s engine: %w", role.spanName(), err)
	}
	sctx, end := m.tracer.Start(ctx, role.spanName(), observe.Attr{Key: "agent.role", Value: role.String()})
	events, err := eng.Run(sctx, task)
	if err != nil {
		end(err)
		return "", fmt.Errorf("run %s: %w", role.spanName(), err)
	}
	summary := collectText(events, m.maxSummaryBytes)
	end(nil, observe.Attr{Key: "summary.bytes", Value: len(summary)})
	return summary, nil
}

// discard 在审查未通过时回滚 maker 改动；无 discarder 则不操作，回滚失败仅告警不阻断续跑。
func (m *MakerReviewer) discard(ctx context.Context) {
	if m.discarder == nil {
		return
	}
	if err := m.discarder.Discard(ctx); err != nil {
		slog.Warn("discard rejected changes failed", "err", err)
	}
}

// reviewPrompt 把评审 rubric 拼进任务 prompt（engine 的 system prompt 为固定 const，
// 不可注入；故评审指令走任务侧）。reviewer 以只读档位读代码后据此输出裁决。
// 要求在最后单独一行输出结构化裁决 `VERDICT: APPROVED|REJECTED`，使裁决解析对思维链型
// 模型（先推理后结论）更鲁棒，同时保持 fail-closed（无明确 APPROVED 标记即判拒）。
func reviewPrompt(task, makerSummary string) string {
	var sb strings.Builder
	sb.WriteString("You are an independent code reviewer. Do NOT modify files; only read and inspect.\n\n")
	sb.WriteString("Task that was implemented:\n")
	sb.WriteString(task)
	sb.WriteString("\n\nThe implementer reported:\n")
	sb.WriteString(makerSummary)
	sb.WriteString("\n\nInspect the actual repository changes and judge whether the task is correctly and safely done. ")
	sb.WriteString("You may reason step by step first. ")
	sb.WriteString("Then, on the FINAL line, output your verdict in exactly this form:\n")
	sb.WriteString("VERDICT: APPROVED   (if the change is correct and safe)\n")
	sb.WriteString("VERDICT: REJECTED   (otherwise, and explain above what must be fixed)")
	return sb.String()
}

// parseVerdict 解析 reviewer 输出的裁决；fail-closed：仅当全文出现明确的 APPROVED 裁决标记
// 才判为通过，其余（含 REJECTED、无裁决、空输出、解析失败）一律未通过并把全文作为反馈。
// 不再只看首行：思维链型模型常先输出推理过程，故全文扫描结构化裁决标记并取最终裁决为准。
func parseVerdict(out string) ReviewVerdict {
	if verdictApproved(out) {
		return ReviewVerdict{Approved: true}
	}
	feedback := strings.TrimSpace(out)
	if feedback == "" {
		feedback = "reviewer returned no verdict"
	}
	return ReviewVerdict{Approved: false, Feedback: feedback}
}

// verdictApproved 全文扫描裁决标记，返回最终裁决是否为通过（fail-closed：无明确 APPROVED 标记即 false）。
// 优先采用结构化的 `VERDICT: APPROVED|REJECTED`（取最后一个）；缺失时回退到独立成行的
// APPROVED/REJECTED（取最后一个），以兼容未严格遵循格式的模型输出。
func verdictApproved(out string) bool {
	lines := strings.Split(out, "\n")
	if v, ok := lastStructuredVerdict(lines); ok {
		return v
	}
	v, ok := lastBareVerdict(lines)
	return ok && v
}

// lastStructuredVerdict 返回最后一个 `VERDICT:` 标记的裁决（true=通过）；无此标记时 ok=false。
func lastStructuredVerdict(lines []string) (approved, ok bool) {
	for _, raw := range lines {
		line := normalizeVerdictLine(raw)
		if !strings.HasPrefix(line, "VERDICT:") {
			continue
		}
		v := strings.TrimSpace(strings.TrimPrefix(line, "VERDICT:"))
		switch {
		case strings.HasPrefix(v, "APPROVED"):
			approved, ok = true, true
		case strings.HasPrefix(v, "REJECTED"):
			approved, ok = false, true
		}
	}
	return approved, ok
}

// lastBareVerdict 返回最后一个独立成行的 APPROVED/REJECTED 裁决（true=通过）；无则 ok=false。
func lastBareVerdict(lines []string) (approved, ok bool) {
	for _, raw := range lines {
		line := normalizeVerdictLine(raw)
		switch {
		case strings.HasPrefix(line, "APPROVED"):
			approved, ok = true, true
		case strings.HasPrefix(line, "REJECTED"):
			approved, ok = false, true
		}
	}
	return approved, ok
}

// normalizeVerdictLine 归一化裁决行：去首尾空白、转大写、剥离 markdown 列表/引用/强调前缀，
// 便于稳健匹配裁决标记。
func normalizeVerdictLine(s string) string {
	line := strings.ToUpper(strings.TrimSpace(s))
	return strings.TrimSpace(strings.TrimLeft(line, "#*->` \t"))
}

// collectText 排空事件流并把文本累积、错误附注为摘要（UTF-8 边界截断到上限）。
// 完整 range 至 channel 关闭可保子 Engine goroutine 不泄漏。
func collectText(events <-chan types.StreamEvent, maxBytes int) string {
	var sb strings.Builder
	for ev := range events {
		switch ev.Type {
		case types.EventText:
			sb.WriteString(ev.Text)
		case types.EventError:
			if ev.Err != nil {
				sb.WriteString("\n[error] " + ev.Err.Error())
			}
		}
	}
	return truncateSummary(sb.String(), maxBytes)
}
