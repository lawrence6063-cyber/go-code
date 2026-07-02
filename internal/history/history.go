// Package history 提供交互式行编辑器的命令历史存储：
// 支持纯内存历史与工作区落盘历史，并为 reverse-i-search 提供自末尾的反向查找。
package history

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// maxEntries 是历史保留的最大条数，超出时丢弃最早的条目，防止历史文件无限膨胀。
const maxEntries = 1000

// 历史落盘的目录与文件权限（控制面私有数据，仅当前用户可读写）。
const (
	dirPerm  = 0o700
	filePerm = 0o600
)

// Store 是命令历史存储：内存维护有序条目（最早在前、最新在后），
// 可选地把新增条目追加落盘到工作区控制面。并发安全。
type Store struct {
	mu      sync.Mutex
	entries []string
	path    string // 落盘文件路径；为空表示纯内存历史
}

// NewMemory 构造一个不落盘的纯内存历史（用于测试与非工作区场景）。
func NewMemory() *Store {
	return &Store{}
}

// New 构造以 workRoot 控制面为落盘位置的历史；workRoot 为空或加载失败时退化为内存历史。
func New(workRoot string) *Store {
	if strings.TrimSpace(workRoot) == "" {
		return NewMemory()
	}
	s := &Store{path: filepath.Join(workRoot, ".cogent", "history")}
	s.load()
	return s
}

// load 从落盘文件读取历史条目（文件不存在视为空历史）。
func (s *Store) load() {
	f, err := os.Open(s.path)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()

	var entries []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if line := sc.Text(); line != "" {
			entries = append(entries, line)
		}
	}
	if len(entries) > maxEntries {
		entries = entries[len(entries)-maxEntries:]
	}
	s.entries = entries
}

// Len 返回当前历史条目数。
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

// At 返回自末尾计第 idx 条历史（idx=0 为最近一条）；越界时 ok=false。
func (s *Store) At(idx int) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if idx < 0 || idx >= len(s.entries) {
		return "", false
	}
	return s.entries[len(s.entries)-1-idx], true
}

// Append 追加一条历史：忽略空白行与紧邻的重复行，随后尽力落盘。
func (s *Store) Append(line string) {
	if strings.TrimSpace(line) == "" {
		return
	}
	s.mu.Lock()
	if n := len(s.entries); n > 0 && s.entries[n-1] == line {
		s.mu.Unlock()
		return
	}
	s.entries = append(s.entries, line)
	if len(s.entries) > maxEntries {
		s.entries = s.entries[len(s.entries)-maxEntries:]
	}
	path := s.path
	s.mu.Unlock()

	if path != "" {
		persist(path, line)
	}
}

// SearchBackward 从自末尾第 from 条起向更早查找首个包含 query 的历史条目，
// 返回命中行与其自末尾索引；未命中时 ok=false。
func (s *Store) SearchBackward(query string, from int) (string, int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if from < 0 {
		from = 0
	}
	for i := from; i < len(s.entries); i++ {
		line := s.entries[len(s.entries)-1-i]
		if strings.Contains(line, query) {
			return line, i, true
		}
	}
	return "", 0, false
}

// persist 把单条历史追加落盘（尽力而为，失败时静默）；首次写入前创建控制面目录。
func persist(path, line string) {
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, filePerm)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = f.WriteString(line + "\n")
}
