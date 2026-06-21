// Package progress 维护跨 run 的自治循环待办看板（LOOP_SPEC §4.4）。
// 它与 session（单任务恢复）、memory（长期约定）职责正交：progress 追踪「跨多个目标
// 循环 run 的待办进度」，是 Loop 的「仓库记忆」。落盘为人类可读的 Markdown 表格，
// 路径限定在控制面 .cogent/ 之内，只许经本包受控通道写入（DEV_SPEC §7.4 控制面写禁止）。
//
// 本包是依赖图的叶子，仅依赖标准库，便于单测。
package progress

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// 看板落盘位置与权限常量（与 memory 落盘范式一致）。
const (
	BoardDir  = ".cogent"     // 控制面目录
	BoardFile = "progress.md" // 看板文件名
	dirPerms  = 0o700         // 目录权限
	filePerms = 0o600         // 文件权限
)

// Status 是一个待办项的状态。
type Status int

// 待办项状态枚举。
const (
	StatusTodo    Status = iota // 待做
	StatusDoing                 // 进行中
	StatusDone                  // 已完成（经 Verifier 确认）
	StatusBlocked               // 阻塞（需人介入）
)

// String 返回状态的稳定字符串（用于 Markdown 落盘与解析）。
func (s Status) String() string {
	switch s {
	case StatusDoing:
		return "doing"
	case StatusDone:
		return "done"
	case StatusBlocked:
		return "blocked"
	default:
		return "todo"
	}
}

// ParseStatus 把字符串解析为 Status；未知值 fail-safe 归为 todo。
func ParseStatus(s string) Status {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "doing":
		return StatusDoing
	case "done":
		return StatusDone
	case "blocked":
		return StatusBlocked
	default:
		return StatusTodo
	}
}

// Item 是看板中的一个待办项。
type Item struct {
	ID      string // 稳定标识，便于跨 run 更新同一项
	Title   string // 一句话标题
	Status  Status // 当前状态
	Note    string // 最近一次循环的归因（如撞预算原因、阻塞详情）
	Updated int64  // 最后更新时间戳（Unix 秒）
}

// Board 是 progress.md 看板的读写抽象；落盘为人类可读、可手改、可 diff 的 Markdown 表格。
type Board interface {
	// Load 读取 <workRoot>/.cogent/progress.md 并解析为待办项；文件不存在返回空看板与 nil error。
	Load(ctx context.Context, workRoot string) ([]Item, error)
	// Upsert 新增或更新一个待办项（按 ID 匹配）后整体回写。
	Upsert(ctx context.Context, workRoot string, item Item) error
}

// NewBoard 构造一个基于本地文件的看板读写器。
func NewBoard() Board { return &fileBoard{} }

// fileBoard 是 Board 的本地文件实现。
type fileBoard struct{}

// Load 见 Board 接口说明：逐行容错解析 Markdown 表格，坏行跳过。
func (b *fileBoard) Load(_ context.Context, workRoot string) ([]Item, error) {
	path, err := boardPath(workRoot)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil // 看板缺失=空看板，不算错误
	}
	if err != nil {
		return nil, fmt.Errorf("open progress: %w", err)
	}
	defer func() { _ = f.Close() }()

	var items []Item
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		if item, ok := parseRow(scanner.Text()); ok {
			items = append(items, item)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan progress: %w", err)
	}
	return items, nil
}

// Upsert 见 Board 接口说明：按 ID 更新或新增，Updated 为零时填当前时间，然后整体回写。
func (b *fileBoard) Upsert(ctx context.Context, workRoot string, item Item) error {
	if strings.TrimSpace(item.ID) == "" {
		return errors.New("empty item id")
	}
	if item.Updated == 0 {
		item.Updated = time.Now().Unix()
	}
	items, err := b.Load(ctx, workRoot)
	if err != nil {
		return err
	}
	items = upsertItem(items, item)
	return b.write(workRoot, items)
}

// write 把全部待办项整体回写为 Markdown 表格（原子性以单次 WriteFile 近似）。
func (b *fileBoard) write(workRoot string, items []Item) error {
	path, err := boardPath(workRoot)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), dirPerms); err != nil {
		return fmt.Errorf("create progress dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(render(items)), filePerms); err != nil {
		return fmt.Errorf("write progress: %w", err)
	}
	return nil
}

// upsertItem 按 ID 替换既有项或追加新项。
func upsertItem(items []Item, item Item) []Item {
	for i := range items {
		if items[i].ID == item.ID {
			items[i] = item
			return items
		}
	}
	return append(items, item)
}

// render 把待办项渲染为带表头的 Markdown 表格。
func render(items []Item) string {
	var sb strings.Builder
	sb.WriteString("# cogent progress\n\n")
	sb.WriteString("| ID | Status | Title | Note | Updated |\n")
	sb.WriteString("| --- | --- | --- | --- | --- |\n")
	for _, it := range items {
		fmt.Fprintf(&sb, "| %s | %s | %s | %s | %d |\n",
			cell(it.ID), it.Status.String(), cell(it.Title), cell(it.Note), it.Updated)
	}
	return sb.String()
}

// parseRow 把一行 Markdown 表格数据行解析为 Item；非数据行/坏行返回 ok=false（容错跳过）。
func parseRow(line string) (Item, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "|") {
		return Item{}, false
	}
	parts := splitRow(line)
	if len(parts) < 5 {
		return Item{}, false
	}
	id := parts[0]
	if id == "" || id == "ID" || strings.HasPrefix(id, "---") {
		return Item{}, false // 表头或分隔行
	}
	updated, _ := strconv.ParseInt(parts[4], 10, 64)
	return Item{
		ID:      id,
		Status:  ParseStatus(parts[1]),
		Title:   parts[2],
		Note:    parts[3],
		Updated: updated,
	}, true
}

// splitRow 去掉首尾竖线后按竖线切分并 trim 每个单元格。
func splitRow(line string) []string {
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	raw := strings.Split(line, "|")
	cells := make([]string, len(raw))
	for i, c := range raw {
		cells[i] = strings.TrimSpace(c)
	}
	return cells
}

// cell 清洗单元格内容：去除会破坏表格结构的竖线与换行（看板字段以可读为先）。
func cell(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "|", "/")
	return strings.TrimSpace(s)
}

// boardPath 解析看板绝对路径并做前缀校验，防止 .. 穿越出工作根。
func boardPath(workRoot string) (string, error) {
	root, err := filepath.Abs(workRoot)
	if err != nil {
		return "", fmt.Errorf("resolve root: %w", err)
	}
	target := filepath.Clean(filepath.Join(root, BoardDir, BoardFile))
	prefix := filepath.Join(root, BoardDir) + string(os.PathSeparator)
	if !strings.HasPrefix(target, prefix) {
		return "", errors.New("progress path escapes working directory")
	}
	return target, nil
}
