// Package skills 加载磁盘上的可复用技能包（LOOP_SPEC §4.6），落地 DEV_SPEC §2.4 预留的扩展点。
// 技能包是「某类任务怎么做」的固化单元（流程/约定/示例），与 memory（事实/约定，被动注入）正交：
// skills 按相关性「召回」后注入，避免全量撑爆上下文。目录约定 <workRoot>/.cogent/skills/<name>/SKILL.md。
//
// 本包是依赖图的叶子（仅依赖标准库），加载复用 memory 的路径前缀校验与硬截断范式。
// .cogent/skills/ 属控制面，由人维护、模型工具不可写（防注入持久化，LOOP_SPEC §5）。
package skills

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// 技能包目录约定与硬截断常量（量级对齐 memory）。
const (
	ControlDir   = ".cogent"  // 控制面目录
	SkillsSubdir = "skills"   // 技能包子目录
	FileName     = "SKILL.md" // 技能包正文文件名
	MaxBodyBytes = 25000      // 单个技能包正文最大字节
	MaxBodyLines = 400        // 单个技能包正文最大行数
)

// skillNameRe 限定技能名字符集（目录名），既防 Load 时的路径穿越，也保证命名规范。
var skillNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// Skill 是一个磁盘技能包：把「某类任务怎么做」固化为可按需注入的单元。
type Skill struct {
	Name        string // 技能名（目录名），如 "add-rate-limiter"
	Description string // 一行描述，用于相关性召回（List 仅返回此项，不含正文）
	Body        string // SKILL.md 正文：步骤/约定/示例（仅 Load 返回，按上限硬截断）
}

// Loader 发现并加载技能包；与 memory.Loader 同构（先索引、按需召回）。
type Loader interface {
	// List 扫描 <workRoot>/.cogent/skills/*/SKILL.md，仅返回名称 + 描述（轻量索引，不含正文）。
	// 目录缺失/坏目录返回空切片与 nil error（技能是可选增强，不存在不算错误）。
	List(ctx context.Context, workRoot string) ([]Skill, error)
	// Load 按名加载单个技能包正文（含 Body，按上限硬截断），供命中相关性时注入上下文。
	Load(ctx context.Context, workRoot, name string) (Skill, error)
}

// New 构造一个基于本地文件的技能包加载器。
func New() Loader { return &fileLoader{} }

// fileLoader 是 Loader 的本地文件实现。
type fileLoader struct{}

// List 见 Loader 接口说明：逐目录容错，非法名/缺 SKILL.md 的目录跳过。
func (l *fileLoader) List(_ context.Context, workRoot string) ([]Skill, error) {
	dir, err := skillsDir(workRoot)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil // 技能目录缺失=无技能，不算错误
	}
	if err != nil {
		return nil, fmt.Errorf("read skills dir: %w", err)
	}
	var skills []Skill
	for _, e := range entries {
		if !e.IsDir() || !skillNameRe.MatchString(e.Name()) {
			continue // 跳过非目录与非法名（容错）
		}
		path := filepath.Join(dir, e.Name(), FileName)
		desc, ok := readDescription(path)
		if !ok {
			continue // 缺 SKILL.md 的目录跳过
		}
		skills = append(skills, Skill{Name: e.Name(), Description: desc})
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	return skills, nil
}

// Load 见 Loader 接口说明：校验名称后读取正文并硬截断。
func (l *fileLoader) Load(_ context.Context, workRoot, name string) (Skill, error) {
	if !skillNameRe.MatchString(name) {
		return Skill{}, fmt.Errorf("invalid skill name %q", name)
	}
	dir, err := skillsDir(workRoot)
	if err != nil {
		return Skill{}, err
	}
	path := filepath.Join(dir, name, FileName)
	data, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, fmt.Errorf("read skill %q: %w", name, err)
	}
	body := string(data)
	return Skill{
		Name:        name,
		Description: descriptionOf(body),
		Body:        truncate(body),
	}, nil
}

// Relevant 从索引按与 query 的关键词重叠度选出至多 max 个技能（最简召回，不引向量库，
// 延续 v1「文件头清单 + 轻量筛选」取舍）。query 为空时返回前 max 个（稳定顺序）。
func Relevant(skills []Skill, query string, max int) []Skill {
	if max <= 0 || len(skills) == 0 {
		return nil
	}
	terms := tokenize(query)
	type scored struct {
		s     Skill
		score int
		idx   int
	}
	ranked := make([]scored, 0, len(skills))
	for i, sk := range skills {
		hay := tokenize(sk.Name + " " + sk.Description)
		ranked = append(ranked, scored{s: sk, score: overlap(terms, hay), idx: i})
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score // 高分优先
		}
		return ranked[i].idx < ranked[j].idx // 同分稳定按原序
	})
	out := make([]Skill, 0, max)
	for _, r := range ranked {
		if len(terms) > 0 && r.score == 0 {
			break // 有查询词但毫不相关则不强行注入
		}
		out = append(out, r.s)
		if len(out) >= max {
			break
		}
	}
	return out
}

// skillsDir 解析技能目录绝对路径并做前缀校验，防止 .. 穿越出工作根（复用 memory 范式）。
func skillsDir(workRoot string) (string, error) {
	root, err := filepath.Abs(workRoot)
	if err != nil {
		return "", fmt.Errorf("resolve root: %w", err)
	}
	target := filepath.Clean(filepath.Join(root, ControlDir, SkillsSubdir))
	prefix := filepath.Join(root, ControlDir) + string(os.PathSeparator)
	if !strings.HasPrefix(target+string(os.PathSeparator), prefix) {
		return "", errors.New("skills path escapes working directory")
	}
	return target, nil
}

// readDescription 读取技能包正文的描述行（仅扫描开头若干行，避免全量读入做索引）。
func readDescription(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	for i := 0; scanner.Scan() && i < 20; i++ {
		if d := cleanDescLine(scanner.Text()); d != "" {
			return d, true
		}
	}
	return "", true // 文件存在但无描述行：仍视为有效技能（描述空）
}

// descriptionOf 从已读入的正文提取描述行。
func descriptionOf(body string) string {
	for _, line := range strings.Split(body, "\n") {
		if d := cleanDescLine(line); d != "" {
			return d
		}
	}
	return ""
}

// cleanDescLine 把一行整理为描述：去除 Markdown 标题井号与首尾空白；空行返回空串。
func cleanDescLine(line string) string {
	t := strings.TrimSpace(line)
	t = strings.TrimLeft(t, "#")
	return strings.TrimSpace(t)
}

// truncate 按行数与字节双重硬截断取更严格者，保证注入不超量（同 memory 手法）。
func truncate(content string) string {
	byLines := truncateLines(content, MaxBodyLines)
	if len(byLines) > MaxBodyBytes {
		byLines = truncateBytes(byLines, MaxBodyBytes)
	}
	return byLines
}

// truncateLines 保留最多 maxLines 行。
func truncateLines(content string, maxLines int) string {
	scanner := bufio.NewScanner(strings.NewReader(content))
	scanner.Buffer(make([]byte, 0, 64*1024), MaxBodyBytes+1)
	var sb strings.Builder
	count := 0
	for scanner.Scan() {
		if count >= maxLines {
			break
		}
		if count > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(scanner.Text())
		count++
	}
	return sb.String()
}

// truncateBytes 在不切断 UTF-8 字符的前提下截断到至多 maxBytes 字节。
func truncateBytes(content string, maxBytes int) string {
	if len(content) <= maxBytes {
		return content
	}
	cut := maxBytes
	for cut > 0 && content[cut]&0xC0 == 0x80 {
		cut--
	}
	return content[:cut]
}

// tokenize 把文本切成小写词集合（按非字母数字分隔），用于最简关键词召回。
func tokenize(s string) map[string]bool {
	set := make(map[string]bool)
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}) {
		if len(w) >= 2 {
			set[w] = true
		}
	}
	return set
}

// overlap 统计 query 词在 hay 中命中的个数。
func overlap(query, hay map[string]bool) int {
	n := 0
	for w := range query {
		if hay[w] {
			n++
		}
	}
	return n
}
