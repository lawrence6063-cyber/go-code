// Package mcp 实现一个最小的 MCP（Model Context Protocol）stdio 客户端：通过 os/exec 拉起外部
// server 子进程，以换行分隔的 JSON-RPC 2.0 完成 initialize 握手、tools/list 与 tools/call，并把远端
// 工具以 mcp__<server>__<tool> 命名、fail-closed 融入工具池（内建优先去重）。
//
// 选择自实现而非引入官方 go-sdk：零新增第三方依赖、保持生产二进制精简，契合“手写核心”的定位；
// 官方 SDK 仅作离线协议一致性对照基准（oracle），隔离在带独立 go.mod 的 internal/mcp/oracle 子模块，
// 主模块 go.mod/go.sum 保持不变。
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// maxFrameBytes 限制单条 JSON-RPC 消息的最大字节数，防御异常超长行耗尽内存。
const maxFrameBytes = 8 << 20 // 8 MiB

// rpcError 是 JSON-RPC 2.0 的错误对象。
type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Error 实现 error 接口。
func (e *rpcError) Error() string { return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message) }

// frame 是一条入站消息，可能是响应（含 Result/Error）或服务端发起的请求/通知（含 Method）。
type frame struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

// rpcRequest 是一条出站请求（带 id，期待响应）。
type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// rpcNotification 是一条出站通知（无 id，不期待响应）。
type rpcNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// transport 承载与单个 server 子进程之间的换行分隔 JSON-RPC 双工通信：
// 单 readLoop goroutine 按 id 把响应分发给等待中的 call；写入加锁串行化。
type transport struct {
	stdin   io.WriteCloser
	writeMu sync.Mutex
	nextID  atomic.Int64

	pendMu  sync.Mutex
	pending map[int64]chan frame

	done    chan struct{} // readLoop 退出后关闭
	errMu   sync.Mutex
	readErr error
}

// newTransport 基于 server 的 stdout/stdin 启动传输层并拉起读循环。
func newTransport(stdout io.Reader, stdin io.WriteCloser) *transport {
	t := &transport{
		stdin:   stdin,
		pending: make(map[int64]chan frame),
		done:    make(chan struct{}),
	}
	go t.readLoop(stdout)
	return t
}

// readLoop 单遍扫描 server 输出，把响应按 id 投递给等待者；坏行跳过，结束时记录读错误。
func (t *transport) readLoop(stdout io.Reader) {
	defer close(t.done)
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), maxFrameBytes)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var f frame
		if err := json.Unmarshal(line, &f); err != nil {
			continue // 跳过非法行，不毒化整条连接
		}
		if f.ID == nil {
			continue // 服务端通知/请求：最小客户端不处理
		}
		t.deliver(*f.ID, f)
	}
	err := sc.Err()
	if err == nil {
		err = io.EOF
	}
	t.setReadErr(err)
}

// deliver 把响应投递给对应 id 的等待者（缓冲 channel，非阻塞）。
func (t *transport) deliver(id int64, f frame) {
	t.pendMu.Lock()
	ch, ok := t.pending[id]
	if ok {
		delete(t.pending, id)
	}
	t.pendMu.Unlock()
	if ok {
		ch <- f
	}
}

// call 发起一次请求并等待响应；ctx 取消或连接关闭时及时返回。
func (t *transport) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := t.nextID.Add(1)
	ch := make(chan frame, 1)
	t.pendMu.Lock()
	t.pending[id] = ch
	t.pendMu.Unlock()
	defer func() {
		t.pendMu.Lock()
		delete(t.pending, id)
		t.pendMu.Unlock()
	}()

	if err := t.write(rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-t.done:
		return nil, fmt.Errorf("mcp connection closed: %w", t.getReadErr())
	case f := <-ch:
		if f.Error != nil {
			return nil, f.Error
		}
		return f.Result, nil
	}
}

// notify 发送一条通知（不等待响应）。
func (t *transport) notify(method string, params any) error {
	return t.write(rpcNotification{JSONRPC: "2.0", Method: method, Params: params})
}

// write 将消息编码为单行 JSON 并以换行结尾原子写出（MCP 规定消息内不含换行）。
func (t *transport) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal rpc: %w", err)
	}
	b = append(b, '\n')
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if _, err := t.stdin.Write(b); err != nil {
		return fmt.Errorf("write rpc: %w", err)
	}
	return nil
}

// closeStdin 关闭写端，向 server 发出 EOF 以请求其优雅退出。
func (t *transport) closeStdin() error { return t.stdin.Close() }

// waitDone 阻塞直至 readLoop 退出（调用方须先确保 server 进程已结束以触发 stdout EOF）。
func (t *transport) waitDone() { <-t.done }

// setReadErr 记录读循环的终止原因。
func (t *transport) setReadErr(err error) {
	t.errMu.Lock()
	t.readErr = err
	t.errMu.Unlock()
}

// getReadErr 返回读循环的终止原因。
func (t *transport) getReadErr() error {
	t.errMu.Lock()
	defer t.errMu.Unlock()
	return t.readErr
}
