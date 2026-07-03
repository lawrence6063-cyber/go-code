package tui

import (
	"context"
	"errors"
	"io"
	"os"
	"unicode/utf8"
)

// decodeKey 从字节源解码一个按键事件（处理控制字符、ESC 转义序列与多字节 UTF-8）。
func decodeKey(ctx context.Context, src byteSource) (keyEvent, error) {
	b, err := src.readByte(ctx)
	if err != nil {
		return keyEvent{}, err
	}
	switch {
	case b == 0x1b:
		return decodeEscape(ctx, src)
	case b == '\r' || b == '\n':
		return ev(keyEnter), nil
	case b == '\t':
		return ev(keyTab), nil
	case b == 0x7f || b == 0x08:
		return ev(keyBackspace), nil
	case b == 0x03:
		return ev(keyCtrlC), nil
	case b == 0x04:
		return ev(keyCtrlD), nil
	case b == 0x01:
		return ev(keyHome), nil
	case b == 0x05:
		return ev(keyEnd), nil
	case b == 0x07:
		return ev(keyCtrlG), nil
	case b == 0x10: // Ctrl-P
		return ev(keyUp), nil
	case b == 0x0e: // Ctrl-N
		return ev(keyDown), nil
	case b == 0x12:
		return ev(keyCtrlR), nil
	case b == 0x15:
		return ev(keyCtrlU), nil
	case b < 0x20:
		return ev(keyUnknown), nil
	case b < 0x80:
		return runeEvent(rune(b)), nil
	default:
		return decodeUTF8(ctx, src, b)
	}
}

// decodeEscape 解码 ESC 引导的转义序列（方向键/Home/End 等）；孤立 ESC 返回 keyEsc。
func decodeEscape(ctx context.Context, src byteSource) (keyEvent, error) {
	lead, ok := src.readByteNow()
	if !ok {
		return ev(keyEsc), nil
	}
	if lead != '[' && lead != 'O' {
		src.unread(lead)
		return ev(keyEsc), nil
	}
	c, err := src.readByte(ctx)
	if err != nil {
		return ev(keyEsc), nil
	}
	if c >= '0' && c <= '9' {
		return decodeNumericEscape(ctx, src, c), nil
	}
	return ev(escapeFinalToKey(c)), nil
}

// escapeFinalToKey 把 ESC[<letter> 的末字母映射为按键。
func escapeFinalToKey(c byte) keyType {
	switch c {
	case 'A':
		return keyUp
	case 'B':
		return keyDown
	case 'C':
		return keyRight
	case 'D':
		return keyLeft
	case 'H':
		return keyHome
	case 'F':
		return keyEnd
	default:
		return keyUnknown
	}
}

// decodeNumericEscape 解析形如 ESC[<digits>~ 的序列（Home/End/Delete 等），读到 '~' 结束。
func decodeNumericEscape(ctx context.Context, src byteSource, first byte) keyEvent {
	seq := []byte{first}
	for {
		n, err := src.readByte(ctx)
		if err != nil || n == '~' {
			break
		}
		seq = append(seq, n)
	}
	switch string(seq) {
	case "1", "7":
		return ev(keyHome)
	case "4", "8":
		return ev(keyEnd)
	default:
		return ev(keyUnknown)
	}
}

// decodeUTF8 读取多字节 UTF-8 rune 的后续字节并解码为字符事件。
func decodeUTF8(ctx context.Context, src byteSource, first byte) (keyEvent, error) {
	n := utf8ByteLen(first)
	buf := make([]byte, 1, 4)
	buf[0] = first
	for i := 1; i < n; i++ {
		b, err := src.readByte(ctx)
		if err != nil {
			return ev(keyUnknown), nil
		}
		buf = append(buf, b)
	}
	r, _ := utf8.DecodeRune(buf)
	if r == utf8.RuneError {
		return ev(keyUnknown), nil
	}
	return runeEvent(r), nil
}

// utf8ByteLen 由 UTF-8 首字节推断该 rune 的字节长度。
func utf8ByteLen(b byte) int {
	switch {
	case b&0xE0 == 0xC0:
		return 2
	case b&0xF0 == 0xE0:
		return 3
	case b&0xF8 == 0xF0:
		return 4
	default:
		return 1
	}
}

// byteSource 抽象逐字节输入：readByte 阻塞直到字节或 ctx 取消，readByteNow 单次轮询，unread 回退一字节。
type byteSource interface {
	readByte(ctx context.Context) (byte, error)
	readByteNow() (byte, bool)
	unread(b byte)
}

// rawInput 是基于 *os.File 的 byteSource：内部缓冲 + 一字节回退，靠终端 VTIME 轮询实现可取消阻塞读。
type rawInput struct {
	f     *os.File
	buf   [256]byte
	n, i  int
	ungot int // <0 表示无回退字节
}

// newRawInput 构造读终端 f 的字节源。
func newRawInput(f *os.File) *rawInput { return &rawInput{f: f, ungot: -1} }

// unread 回退一个字节，供 ESC 序列判定失败时归还。
func (r *rawInput) unread(b byte) { r.ungot = int(b) }

// fill 从终端读取一批字节；返回 got=false 表示 VTIME 超时（无数据，正常轮询）。
// 注意：VMIN=0/VTIME>0 下，无数据时底层 read(2) 返回 0 字节，Go 的 os.File.Read
// 会将 n==0 转为 io.EOF——这不是真正的文件结束，只是 VTIME 超时，需特判忽略。
func (r *rawInput) fill() (got bool, err error) {
	m, err := r.f.Read(r.buf[:])
	if m > 0 {
		r.n, r.i = m, 0
		return true, nil
	}
	// m==0: VTIME 超时或真 EOF。对终端而言 VMIN=0/VTIME>0 返回 0 是正常的"暂无数据"，
	// Go 附带的 io.EOF 应忽略；只有非 EOF 的真实错误（如 EIO/设备断开）才向上冒泡。
	if err == io.EOF || err == nil {
		return false, nil
	}
	return false, err
}

// readByte 阻塞取下一字节；ctx 取消或读到 EOF 时返回错误。
func (r *rawInput) readByte(ctx context.Context) (byte, error) {
	if r.ungot >= 0 {
		b := byte(r.ungot)
		r.ungot = -1
		return b, nil
	}
	for {
		if r.i < r.n {
			b := r.buf[r.i]
			r.i++
			return b, nil
		}
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if _, err := r.fill(); err != nil {
			return 0, err
		}
	}
}

// readByteNow 单次轮询取字节：无缓冲且本次轮询无数据时返回 ok=false（用于孤立 ESC 判定）。
func (r *rawInput) readByteNow() (byte, bool) {
	if r.ungot >= 0 {
		b := byte(r.ungot)
		r.ungot = -1
		return b, true
	}
	if r.i < r.n {
		b := r.buf[r.i]
		r.i++
		return b, true
	}
	if got, err := r.fill(); err != nil || !got {
		return 0, false
	}
	b := r.buf[r.i]
	r.i++
	return b, true
}

// 确保 rawInput 满足 byteSource（编译期校验）。
var _ byteSource = (*rawInput)(nil)

// errNotTerminal 在非终端 fd 上尝试进入 raw 模式时返回。
var errNotTerminal = errors.New("not a terminal")
