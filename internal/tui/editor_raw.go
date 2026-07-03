//go:build darwin || linux

package tui

import "golang.org/x/sys/unix"

// isTerminalFD 报告 fd 是否为终端（能取到 termios 即视为终端）。
func isTerminalFD(fd uintptr) bool {
	_, err := unix.IoctlGetTermios(int(fd), termiosGetReq)
	return err == nil
}

// enterRaw 将终端置为原始模式（关闭回显/规范/信号，OPOST 关，VMIN=0/VTIME=1 支持可取消轮询），
// 返回用于恢复原状态的闭包。
func enterRaw(fd uintptr) (func() error, error) {
	old, err := unix.IoctlGetTermios(int(fd), termiosGetReq)
	if err != nil {
		return nil, err
	}
	raw := *old
	raw.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP |
		unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	raw.Oflag &^= unix.OPOST
	raw.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	raw.Cflag &^= unix.CSIZE | unix.PARENB
	raw.Cflag |= unix.CS8
	raw.Cc[unix.VMIN] = 0
	raw.Cc[unix.VTIME] = 1
	if err := unix.IoctlSetTermios(int(fd), termiosSetReq, &raw); err != nil {
		return nil, err
	}
	fdInt := int(fd)
	return func() error { return unix.IoctlSetTermios(fdInt, termiosSetReq, old) }, nil
}
