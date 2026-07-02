package completion

import (
	"bufio"
	"bytes"
	"context"
	"io/fs"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// defaultLimit 是单次返回候选的默认上限。
const defaultLimit = 15

// cacheTTL 是候选文件清单的缓存有效期：期内复用内存清单，仅在内存做过滤，
// 避免每次击键都 spawn git 或遍历磁盘。
const cacheTTL = 5 * time.Second

// maxWalkFiles 是回退目录遍历的条目上限，防止超大仓库拖慢首次加载。
const maxWalkFiles = 20000

// skippedDirs 是回退遍历时跳过的噪声/控制面目录（与 internal/tool 保持一致语义）。
var skippedDirs = map[string]bool{
	".git":         true,
	".cogent":      true,
	"node_modules": true,
}

// Provider 提供工作区文件候选：首次触发时拉取清单并缓存，Filter 在内存中模糊过滤。
type Provider interface {
	// Filter 返回与 partial 匹配的候选相对路径（已排序、截断到 limit）；limit<=0 时用默认上限。
	Filter(ctx context.Context, partial string, limit int) []string
}

// fileProvider 是基于 git ls-files（优先）/目录遍历（回退）的候选来源实现。
type fileProvider struct {
	workRoot string

	mu       sync.Mutex // 保护以下缓存字段
	paths    []string   // 缓存的候选相对路径清单
	loadedAt time.Time  // 上次加载时间（用于 TTL）
}

// NewProvider 构造基于 workRoot 的文件候选来源。
func NewProvider(workRoot string) Provider {
	return &fileProvider{workRoot: workRoot}
}

// Filter 取候选清单（必要时刷新缓存）并按 partial 做模糊过滤、排序、截断。
func (p *fileProvider) Filter(ctx context.Context, partial string, limit int) []string {
	if limit <= 0 {
		limit = defaultLimit
	}
	paths := p.snapshot(ctx)
	ranked := rankMatches(paths, partial)
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	return ranked
}

// snapshot 返回候选清单：命中 TTL 缓存则直接复用，否则重新加载并回填缓存。
func (p *fileProvider) snapshot(ctx context.Context) []string {
	p.mu.Lock()
	if p.paths != nil && time.Since(p.loadedAt) < cacheTTL {
		cached := p.paths
		p.mu.Unlock()
		return cached
	}
	p.mu.Unlock()

	paths := loadPaths(ctx, p.workRoot)

	p.mu.Lock()
	p.paths = paths
	p.loadedAt = time.Now()
	p.mu.Unlock()
	return paths
}

// loadPaths 优先用 git ls-files（跟踪 + 未跟踪）拉取清单，失败或非 git 仓库时回退目录遍历。
func loadPaths(ctx context.Context, workRoot string) []string {
	if paths, ok := gitListFiles(ctx, workRoot); ok {
		return paths
	}
	return walkFiles(ctx, workRoot)
}

// gitListFiles 用 git ls-files 收集跟踪与未跟踪（排除 gitignore）文件；
// 非 git 仓库或命令失败时返回 ok=false 以触发回退。
func gitListFiles(ctx context.Context, workRoot string) ([]string, bool) {
	tracked, ok := runGit(ctx, workRoot, "ls-files")
	if !ok {
		return nil, false
	}
	others, _ := runGit(ctx, workRoot, "ls-files", "--others", "--exclude-standard")

	seen := make(map[string]struct{}, len(tracked)+len(others))
	out := make([]string, 0, len(tracked)+len(others))
	for _, group := range [][]string{tracked, others} {
		for _, f := range group {
			if f == "" {
				continue
			}
			if _, dup := seen[f]; dup {
				continue
			}
			seen[f] = struct{}{}
			out = append(out, f)
		}
	}
	return out, true
}

// runGit 在 workRoot 执行 git 命令并按行切分 stdout；命令失败返回 ok=false。
func runGit(ctx context.Context, workRoot string, args ...string) ([]string, bool) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = workRoot
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	return splitLines(out), true
}

// splitLines 把命令输出按行切分并去除空行。
func splitLines(out []byte) []string {
	var lines []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if line := sc.Text(); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// walkFiles 遍历 workRoot 收集相对路径（跳过 skippedDirs，带条目上限），字典序返回。
func walkFiles(ctx context.Context, workRoot string) []string {
	var files []string
	_ = filepath.WalkDir(workRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil || ctx.Err() != nil {
			if ctx.Err() != nil {
				return filepath.SkipAll
			}
			return nil
		}
		if d.IsDir() {
			if skippedDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if len(files) >= maxWalkFiles {
			return filepath.SkipAll
		}
		if rel, relErr := filepath.Rel(workRoot, path); relErr == nil {
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(files)
	return files
}

// matchRank 是一次候选匹配的评分：rank 越小优先级越高，用于稳定排序。
type matchRank struct {
	path string
	rank int
}

// 匹配优先级：前缀 < 路径分段前缀 < 子串 < 子序列（值越小越优先）。
const (
	rankPrefix     = 0
	rankBasePrefix = 1
	rankSubstring  = 2
	rankSubseq     = 3
)

// rankMatches 按 partial 对候选做模糊匹配并排序：空 partial 直接返回原序列，
// 否则按匹配等级、再按路径长度、最后按字典序稳定排序。
func rankMatches(paths []string, partial string) []string {
	if partial == "" {
		return append([]string(nil), paths...)
	}
	q := strings.ToLower(partial)
	matched := make([]matchRank, 0, len(paths))
	for _, p := range paths {
		if rank, ok := scorePath(strings.ToLower(p), q); ok {
			matched = append(matched, matchRank{path: p, rank: rank})
		}
	}
	sort.SliceStable(matched, func(i, j int) bool {
		if matched[i].rank != matched[j].rank {
			return matched[i].rank < matched[j].rank
		}
		if len(matched[i].path) != len(matched[j].path) {
			return len(matched[i].path) < len(matched[j].path)
		}
		return matched[i].path < matched[j].path
	})
	out := make([]string, len(matched))
	for i := range matched {
		out[i] = matched[i].path
	}
	return out
}

// scorePath 判定小写路径 p 是否匹配小写查询 q，并返回匹配等级；不匹配返回 ok=false。
func scorePath(p, q string) (int, bool) {
	switch {
	case strings.HasPrefix(p, q):
		return rankPrefix, true
	case strings.HasPrefix(baseName(p), q):
		return rankBasePrefix, true
	case strings.Contains(p, q):
		return rankSubstring, true
	case isSubsequence(q, p):
		return rankSubseq, true
	default:
		return 0, false
	}
}

// baseName 返回路径最后一段（以 / 分隔）。
func baseName(p string) string {
	if idx := strings.LastIndexByte(p, '/'); idx >= 0 {
		return p[idx+1:]
	}
	return p
}

// isSubsequence 判定 q 是否为 s 的子序列（按序但不要求连续）。
func isSubsequence(q, s string) bool {
	if q == "" {
		return true
	}
	qi := 0
	for i := 0; i < len(s) && qi < len(q); i++ {
		if s[i] == q[qi] {
			qi++
		}
	}
	return qi == len(q)
}
