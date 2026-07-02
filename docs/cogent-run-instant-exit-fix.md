# `cogent run` 一秒退出 —— 根因分析与修复

## 1. 现象

在终端执行 `cogent run` 后，打印完 banner、状态栏和 `you>` 提示符，**未等任何输入就立刻退回 shell**：

```
[default | tok in:0 out:0 | $0.0000]

you>

alaindong  ~
❯
```

退出码为 0（“正常退出”），且没有任何错误提示，因此极具迷惑性。

---

## 2. 排查过程与证据

### 2.1 定位到输入循环

`cmd/cogent/commands.go` 的 `inputLoop` 打印 `you>` 后调用 `in.next(ctx)` 读取一行：

```go
fmt.Print("\nyou> ")
line, ok := in.next(ctx)
if !ok {
    fmt.Println()
    return ctx.Err() // ctx 未取消时返回 nil → REPL 正常结束 → 进程退出码 0
}
```

TTY 场景下 `in.next` 会走 `lineEditor.readLine`（raw 模式行编辑器）。只要它返回 `ok=false`，整个 REPL 就会被当成“输入结束（EOF）”而退出。

### 2.2 用真实 pty 复现（带 debug 日志）

REPL 只有在 **TTY** 下才走 raw editor 路径，普通管道无法复现，因此用 `expect` 提供真实伪终端，并开 `COGENT_LOG_LEVEL=debug`。关键日志：

```
level=DEBUG msg="lineeditor: enter raw failed" err="inappropriate ioctl for device"
>>> child EXITED WITHOUT input   <-- BUG 复现
```

`enterRaw` 失败，`readLine` 直接返回 `"", false`。

### 2.3 用最小程序坐实底层原因

单独写了一个只调 ioctl 的探测程序，在同一 pty 下运行，结果：

| 调用 | 结果 |
| --- | --- |
| `IoctlGetTermios(TIOCGETA)`（读 termios） | **成功** |
| `IoctlSetTermios(TIOCSETA)`（写 termios，即进入 raw） | **失败：`inappropriate ioctl for device` (ENOTTY)** |
| 官方 `golang.org/x/term.MakeRaw` | **同样失败** |
| 常量值 | `TIOCGETA=0x40487413`、`TIOCSETA=0x80487414`（darwin/arm64 标准值，正确） |

再用系统 **go1.22.5** 编译同样的探测程序对照，`TIOCSETA` **依旧失败**。

结论（排除法）：
- ❌ 不是常量写错（值正确）
- ❌ 不是我们手写 `enterRaw` 的实现问题（官方 `x/term.MakeRaw` 也失败）
- ❌ 不是 go1.26 运行时回归（go1.22 / go1.26 表现一致）
- ✅ 是**运行环境的 TTY 特性**：该伪终端允许读 termios（`TIOCGETA`），却拒绝写 termios（`TIOCSETA`）进入 raw 模式

---

## 3. 根因链

1. 该终端/pty 上 `TIOCSETA`（进入 raw 模式）被内核拒绝 `ENOTTY`。
2. `isTerminalFD` 只用 `TIOCGETA`（读）判定“是不是终端” → 返回 `true` → 选择 raw 交互式行编辑器路径。
3. `lineEditor.readLine` 里 `enterRaw` 失败（或进入后首个按键即底层 I/O 错误）时返回 `"", false`。
4. `inputLoop` 把 `ok=false` 当作 **EOF** → `return ctx.Err()`（此时为 `nil`）→ 进程以退出码 0 正常结束。
5. 失败信息只记在 `slog.Debug`（默认 INFO 级别不可见）→ 用户只看到“秒退”，没有任何原因。

> 核心缺陷：`readLine` 用单一的 `false` 混淆了两种完全不同的情况——**“用户主动退出（Ctrl-C/EOF）”** 与 **“环境不支持 raw 模式”**，后者被误判成 EOF 导致直接退出。

---

## 4. 修复方案（治本，与具体环境无关）

思路：**raw 模式不可用时优雅降级为逐行读取，而不是退出 REPL。** 这也是所有成熟 CLI（vim、fzf、readline）的通用做法——应用层无法强行让内核支持某个 ioctl，正确姿势是探测失败即降级。

### 4.1 `cmd/cogent/lineeditor.go`：`readLine` 改为三态返回

```go
// 返回三态：
//   ok=true                 成功读到整行
//   ok=false, usable=true   用户主动结束(Ctrl-C/空行Ctrl-D)或 ctx 取消 —— 调用方应退出
//   ok=false, usable=false  raw 交互不可用(进不去 raw / 首个按键即 I/O 错误) —— 调用方应回退逐行读
func (le *lineEditor) readLine(ctx context.Context) (line string, ok bool, usable bool) {
    restore, err := enterRaw(le.in.Fd())
    if err != nil {
        slog.Debug("lineeditor: enter raw failed", "err", err)
        return "", false, false // 进不去 raw：不可用，请回退
    }
    defer func() { _ = restore() }()

    // ... 绘制、解码按键 ...
    readAny := false
    for {
        drawLine(le.out, core)
        k, derr := decodeKey(ctx, src)
        if derr != nil {
            finishLine(le.out, core)
            // 首个按键就 I/O 失败且非用户主动取消 → 判定环境不可用，回退（尚未消费输入，安全）
            if !readAny && ctx.Err() == nil {
                return "", false, false
            }
            return "", false, true // 用户 EOF 或 ctx 取消：正常结束
        }
        readAny = true
        // ... 提交/中断处理，均返回 usable=true ...
    }
}
```

### 4.2 `cmd/cogent/prompter.go`：`inputReader` 支持懒回退

```go
type inputReader struct {
    lines  <-chan string // 非 TTY / 已回退：后台 Scanner 行来源
    editor *lineEditor   // TTY：交互式行编辑器（nil 表示走逐行读取）
    tty    *os.File      // TTY 源；raw 不可用时据此回退逐行读
}

func (ir *inputReader) next(ctx context.Context) (string, bool) {
    if ir.editor != nil {
        line, ok, usable := ir.editor.readLine(ctx)
        if usable {
            return line, ok
        }
        // raw 不可用：一次性提示 + 懒回退到逐行读取，然后重试本次读取
        fmt.Fprintln(os.Stderr,
            "cogent: 交互式行编辑不可用，回退到逐行输入模式（无 @ 文件补全下拉）")
        ir.fallbackToLines()
    }
    select {
    case <-ctx.Done():
        return "", false
    case line, ok := <-ir.lines:
        return line, ok
    }
}

func (ir *inputReader) fallbackToLines() {
    ir.editor = nil
    src := io.Reader(os.Stdin)
    if ir.tty != nil {
        src = ir.tty
    }
    ir.lines = newInputReader(src).lines // 起后台 Scanner 逐行读
}
```

> 说明：曾尝试过“启动前预探测 raw 是否可用”，但实测发现**预探测能通过、真正读键时却仍失败**（enterRaw 成功但首个 `decodeKey` I/O 错误），所以最终采用“运行时懒回退”，同时覆盖“进不去 raw”和“进去后首个按键 I/O 失败”两种情况，更稳健。

### 涉及文件
- `cmd/cogent/lineeditor.go`：`readLine` 三态化。
- `cmd/cogent/prompter.go`：`inputReader` 增加 `tty` 字段、`next` 懒回退、新增 `fallbackToLines`。

---

## 5. 验证

在**同一个会让 `TIOCSETA` 失败的 pty 环境**下：

```
you>                 # 打印提示后不再秒退
>>> GOOD: alive, waiting for input   # 保持等待输入
>>> GOOD: clean exit after typing exit   # 喂 exit 干净退出
```

- 修复前：`you>` 后立即退出（误判 EOF）。
- 修复后：自动降级为逐行输入，REPL 正常工作；输入 `exit`/`quit` 或 Ctrl-C 正常退出。

**降级模式的代价**：失去 `@` 文件补全下拉、↑↓ 历史、Ctrl-R 反向搜索；但普通多轮对话完全可用。当终端支持 raw 时，仍会启用完整的富交互行编辑器。

---

## 6. 你可以自己这样确认与使用

### 重新构建（软链 `~/bin/cogent` 已指向项目 `bin/cogent`，重建即生效）
```bash
cd /Users/alaindong/Desktop/new_career/resume/ai项目/cogent
export GOTOOLCHAIN=auto
go build -o bin/cogent ./cmd/cogent/
```

### 直接跑
```bash
cogent run
```
现在最坏情况也只是降级为逐行输入，**不会再秒退**。

### 想知道你的终端到底支不支持 raw
```bash
COGENT_LOG_LEVEL=debug cogent run
```
- 若看到 `enter raw failed ... inappropriate ioctl for device` 和“回退到逐行输入模式”提示 → 你这个终端确实拒绝进入 raw（详见下一节）。
- 若没有该日志、且 `@` 能弹出补全下拉 → 你的终端支持 raw，走的是完整交互路径。

---

## 7. 关于 `TIOCSETA` 被拒（环境侧说明）

- 本次 `TIOCSETA` 失败是在**受限执行环境提供的伪终端**里复现到的。**真实的 macOS Terminal.app / iTerm2 一般不会拒绝 `TIOCSETA`**（否则 vim、less、zsh 行编辑都无法工作）。
- 应用层**无法**强行让 `TIOCSETA` 成功——某个 fd 是否支持该 ioctl 由内核与设备类型决定。因此**正确解法就是第 4 节的降级**，而不是“修好 ioctl”。
- 如果你在真实终端里也遇到 `enter raw failed`，常见诱因（供排查）：
  - 在 IDE 的“输出/调试面板”而非“集成终端”里运行；
  - `ssh` 未分配 pty（加 `ssh -t`）、`docker exec` 未加 `-t`；
  - `tmux`/`screen` 配置异常，或 stdin 被重定向/管道接管；
  - 某些非标准终端模拟器。

---

## 8. 遗留（与本问题无关，但建议顺手处理）

`cmd/cogent` 测试包存在 **`stubProvider` 重复定义**：
- `lineeditor_test.go` 定义 `type stubProvider struct { all []string }`
- `history_editor_test.go` 使用 `stubProvider{list: ...}`（字段名 `list`）

二者字段名不一致导致 `go test ./cmd/cogent` **无法编译**（`go build` 生成二进制不受影响）。建议统一为单一定义（保留 `all` 版本，把用到 `list` 的地方改为 `all`，并删除重复的类型/方法），以恢复测试可运行。
