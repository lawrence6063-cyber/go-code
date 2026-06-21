// Package session 以 append-only JSONL 事件流持久化会话，支持中断后 resume 重建。
// 设计理念（DEV_SPEC §6.5）：写入路径做简单（顺序 append、崩溃安全），
// 把复杂性压到恢复路径（Load 去重 + 链路修复）。session 仅依赖 types 与 secret（纯标准库脱敏叶子包），
// 不反向依赖任何业务包。
package session

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/alaindong/cogent/internal/secret"
)

// ErrSessionNotFound 表示 resume 时找不到对应会话的 transcript 文件。
var ErrSessionNotFound = errors.New("session not found")

// 存储相关常量。
const (
	dirPerms     = 0o700    // transcript 目录权限（仅属主可访问）
	filePerms    = 0o600    // transcript 文件权限（仅属主可读写）
	fileSuffix   = ".jsonl" // transcript 文件后缀
	maxLineBytes = 8 << 20  // Load 单行扫描上限（8MiB，容忍超长工具结果）
)

// Event 是 transcript 中的一条 append-only 记录。
type Event struct {
	UUID       string          `json:"uuid"`        // 事件唯一标识，用于去重
	ParentUUID string          `json:"parent_uuid"` // 父事件 UUID，串成会话链
	Type       string          `json:"type"`        // user/assistant/tool_result/summary/meta
	Payload    json.RawMessage `json:"payload"`     // 事件载荷（通常为序列化后的消息）
	Timestamp  int64           `json:"ts"`          // 事件时间戳（Unix 纳秒）
}

// Store 以 append-only JSONL 持久化会话事件，支持 resume 重放。
type Store interface {
	// Append 把一条事件序列化为单行 JSON 追加写入 transcript。
	Append(ctx context.Context, sessionID string, e Event) error
	// Load 逐行解析 transcript 重建事件列表（按 UUID 去重保序）；文件缺失返回 ErrSessionNotFound。
	Load(ctx context.Context, sessionID string) ([]Event, error)
	// Resolve 返回某会话 transcript 的文件路径。
	Resolve(sessionID string) string
}

// jsonlStore 是 Store 的 JSONL 文件实现，所有 transcript 落在同一 dataDir 下。
type jsonlStore struct {
	dataDir string
}

// NewStore 构造一个以 dataDir 为根的 JSONL 会话存储。
func NewStore(dataDir string) Store {
	return &jsonlStore{dataDir: dataDir}
}

// Append 见 Store 接口说明：落盘前对 Payload 做密钥脱敏，再以 O_APPEND 追加单行 JSON。
func (s *jsonlStore) Append(ctx context.Context, sessionID string, e Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := s.safePath(sessionID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dataDir, dirPerms); err != nil {
		return fmt.Errorf("mkdir data dir: %w", err)
	}
	e.Payload = redactSecrets(e.Payload)
	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, filePerms)
	if err != nil {
		return fmt.Errorf("open transcript: %w", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("append event: %w", err)
	}
	return nil
}

// Load 见 Store 接口说明：单遍扫描，按 UUID 去重并保留首次出现顺序。
func (s *jsonlStore) Load(ctx context.Context, sessionID string) ([]Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := s.safePath(sessionID)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrSessionNotFound, sessionID)
	}
	if err != nil {
		return nil, fmt.Errorf("open transcript: %w", err)
	}
	defer func() { _ = f.Close() }()
	return scanEvents(f)
}

// Resolve 见 Store 接口说明：sessionID 非法时回退为目录本身（调用方不应据此读写）。
func (s *jsonlStore) Resolve(sessionID string) string {
	path, err := s.safePath(sessionID)
	if err != nil {
		return s.dataDir
	}
	return path
}

// scanEvents 逐行解析 JSONL 为事件列表，按 UUID 去重保序；忽略空行与坏行（容错恢复）。
func scanEvents(f *os.File) ([]Event, error) {
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64<<10), maxLineBytes)
	var events []Event
	seen := make(map[string]bool)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			continue // 容错：跳过损坏行，尽量恢复其余历史
		}
		if e.UUID != "" && seen[e.UUID] {
			continue
		}
		seen[e.UUID] = true
		events = append(events, e)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan transcript: %w", err)
	}
	return events, nil
}

// idCharset 是 sessionID 允许的字符集合，用于防止路径穿越与非法文件名。
var idCharset = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// safePath 校验 sessionID 合法后拼出 transcript 文件路径，防止 ../ 穿越。
func (s *jsonlStore) safePath(sessionID string) (string, error) {
	if sessionID == "" || !idCharset.MatchString(sessionID) {
		return "", fmt.Errorf("invalid session id %q", sessionID)
	}
	return filepath.Join(s.dataDir, sessionID+fileSuffix), nil
}

// NewSessionID 生成带时间前缀的会话 ID（可读 + 唯一），仅用安全字符。
func NewSessionID() string {
	var buf [6]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return time.Now().UTC().Format("20060102-150405")
	}
	return time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(buf[:])
}

// redactSecrets 在落盘前对 Payload 做密钥脱敏，避免密钥泄露进 transcript（§7.5）。
// 规则统一委托 internal/secret 包，与 observe 入 trace 共用同一套脱敏规则。
func redactSecrets(payload json.RawMessage) json.RawMessage {
	if len(payload) == 0 {
		return payload
	}
	return secret.Redact(payload)
}
