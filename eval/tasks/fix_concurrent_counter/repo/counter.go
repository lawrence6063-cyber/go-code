// Package counterx 提供一个并发安全的计数器。
package counterx

import "sync"

// Counter 是一个支持并发自增与读取的计数器。
type Counter struct {
	mu sync.Mutex
	n  int
}

// Inc 把计数器加 1。
func (c *Counter) Inc() {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
}

// Value 返回当前计数值。
func (c *Counter) Value() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}
