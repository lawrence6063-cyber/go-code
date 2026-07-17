// Package perfx 提供去重工具。
package perfx

// Dedup 返回去重后的切片（保持元素首次出现的顺序）。
//
// 当前实现是 O(n^2)：对每个元素都线性扫描已收集的结果切片判重。
// 在大输入（十万级）下过慢，需优化到线性时间复杂度（提示：用集合记录已见元素）。
func Dedup(in []int) []int {
	out := make([]int, 0, len(in))
	for _, v := range in {
		found := false
		for _, u := range out {
			if u == v {
				found = true
				break
			}
		}
		if !found {
			out = append(out, v)
		}
	}
	return out
}
