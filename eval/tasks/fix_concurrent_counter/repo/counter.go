// Package counterx 提供一个并发安全的计数器。
package counterx

// Counter 是一个支持并发自增与读取的计数器。
// 当前实现缺少并发保护：Inc 与 Value 直接读写 n，在并发场景下存在数据竞态，
// 会丢失部分自增（-race 也会报告竞态），需补上同步保护。
type Counter struct {
	n int
}

// Inc 把计数器加 1。
func (c *Counter) Inc() {
	c.n++
}

// Value 返回当前计数值。
func (c *Counter) Value() int {
	return c.n
}
