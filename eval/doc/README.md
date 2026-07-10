# eval/doc — 评测报告归档

本目录存放 cogent 评测的**正式报告**（人读、可引用、随基线累积）。
原始跑分产物（`report.md`/`report.json` + 工作区副本 + trace）在 `eval-artifacts/`（已 gitignore，可重生成）；
本目录只保留经整理、值得留存的报告。

## 报告索引

| 日期 | 报告 | 评测集 | 模型 | 结果 |
| --- | --- | --- | --- | --- |
| 2026-07-10 | [baseline-2026-07-10.md](baseline-2026-07-10.md) | native（9 任务） | deepseek-v4-pro | 9/9（100%），六维度全绿 |

## 复现

见各报告的「复现命令」小节；统一入口为 `cogent eval run` / `cogent eval compare`（EVAL_SPEC §6）。
