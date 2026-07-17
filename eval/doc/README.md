# eval/doc — 评测报告归档

本目录存放 cogent 评测的**正式报告**（人读、可引用、随基线累积）。
原始跑分产物（`report.md`/`report.json` + 工作区副本 + trace）在 `eval-artifacts/`（已 gitignore，可重生成）；
本目录只保留经整理、值得留存的报告。

## 报告索引

| 日期 | 报告 | 评测集 | 模型 | 结果 |
| --- | --- | --- | --- | --- |
| 2026-07-14 | [swebench-scaffold-plan.md](swebench-scaffold-plan.md) | SWE-bench Lite（scaffold A/B 30 例 + 方案） | deepseek-v4-pro | baseline 60% vs prompt-scaffold 60%（Δ0，补丁聚焦度 2.1→1.2 文件）；方案：test-time scaling（多候选+复现/回归筛选+投票）让 scaffold 有效 |
| 2026-07-13 | [baseline-2026-07-13.md](baseline-2026-07-13.md) | 全套件（native + 4 Adapter） | — / deepseek-v4-pro | harness 9/9 + 四 Adapter 自证全绿；规模 628 样本；agent 基线引用 |
| 2026-07-10 | [baseline-2026-07-10.md](baseline-2026-07-10.md) | native（9 任务） | deepseek-v4-pro | 9/9（100%），六维度全绿 |

## 复现

见各报告的「复现命令」小节；统一入口为 `cogent eval run` / `cogent eval compare`（EVAL_SPEC §6）。
