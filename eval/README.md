# cogent 评测集（eval）

本目录为 cogent 的轻量评测集脚手架：用一组"自带验证脚本的真实修复任务"来回归 agent 的端到端能力
（读文件 → 定位缺陷 → 编辑 → 自查）。Phase 3 仅落地任务结构与样例，批量跑分的 Headless 运行器留待 Phase 8。

## 目录结构

```
eval/
├── README.md
└── tasks/
    └── <task-name>/
        ├── repo/        # 任务工作区：含待修复代码与单元测试（独立 go module）
        ├── task.txt     # 自然语言任务描述（喂给 agent 的输入）
        └── verify.sh    # 客观验证脚本，退出码 0 = 通过，非 0 = 失败
```

每个任务自包含：`repo/` 是 agent 的工作根目录，`task.txt` 是任务输入，`verify.sh` 给出客观判据。
判定不依赖 agent 自述"我完成了"，而以 `verify.sh` 的退出码为准（防止自我评估虚高）。

## 运行单个任务的验证

```bash
bash eval/tasks/fix_off_by_one/verify.sh
```

未修复缺陷时该脚本应失败（退出码非 0），修复后通过（退出码 0）。

## 手动驱动 agent 跑某个任务（示例）

```bash
cd eval/tasks/fix_off_by_one/repo
cogent run "$(cat ../task.txt)"
cd .. && bash verify.sh   # 校验 agent 的修改是否真正通过
```

## 现有任务

| 任务 | 能力点 | 验证方式 |
| --- | --- | --- |
| `fix_off_by_one` | 定位并修复经典 off-by-one 循环边界缺陷 | `go test`（闭区间求和用例） |
| `implement_fizzbuzz` | 从未实现的桩补全经典 FizzBuzz 规则 | `go test`（含 3/5/15 倍数边界用例） |

## 新增任务约定

1. 在 `tasks/` 下新建任务目录，放入 `repo/`、`task.txt`、`verify.sh`。
2. `repo/` 应是一个可独立构建/测试的最小工程（如带自己的 `go.mod`）。
3. `verify.sh` 必须给出客观、可复现的判据，且以退出码表达结果（0=通过）。
4. 初始状态下 `verify.sh` 应为失败，确保任务"有待解决"，避免空跑通过。
