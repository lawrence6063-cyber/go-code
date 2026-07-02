//go:build linux

package main

import "golang.org/x/sys/unix"

// termiosGetReq/termiosSetReq 是 linux 上读写 termios 的 ioctl 请求号。
const (
	termiosGetReq = unix.TCGETS
	termiosSetReq = unix.TCSETS
)
