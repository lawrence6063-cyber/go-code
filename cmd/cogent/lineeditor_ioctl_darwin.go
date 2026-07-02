//go:build darwin

package main

import "golang.org/x/sys/unix"

// termiosGetReq/termiosSetReq 是 darwin 上读写 termios 的 ioctl 请求号。
const (
	termiosGetReq = unix.TIOCGETA
	termiosSetReq = unix.TIOCSETA
)
