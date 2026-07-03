//go:build !darwin && !linux

package tui

// isTerminalFD 在不支持的平台上一律返回 false，使输入退回非 TTY 逐行读取。
func isTerminalFD(uintptr) bool { return false }

// enterRaw 在不支持的平台上不可用；调用方据此退回逐行读取。
func enterRaw(uintptr) (func() error, error) { return nil, errNotTerminal }
