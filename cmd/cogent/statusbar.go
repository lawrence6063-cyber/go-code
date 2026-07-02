package main

import (
	"fmt"
	"strings"
)

// statusBar 是常驻状态栏：在每次输入提示前展示当前模型、累计 token 用量与估算成本。
// 数据来源为装饰 observe.Meter 的 costMeter（拦截 cogent.tokens 计数得到）。
type statusBar struct {
	meter *costMeter
	model string
}

// newStatusBar 构造绑定到成本计量器的状态栏；model 为空时回退为 "default"。
func newStatusBar(meter *costMeter, model string) *statusBar {
	if strings.TrimSpace(model) == "" {
		model = "default"
	}
	return &statusBar{meter: meter, model: model}
}

// render 返回一行状态栏文本（暗色 ANSI 修饰，末尾换行）；meter 为空时仅显示模型名。
func (b *statusBar) render() string {
	if b.meter == nil {
		return fmt.Sprintf("\x1b[2m[%s]\x1b[0m\n", b.model)
	}
	in, out := b.meter.Tokens()
	return fmt.Sprintf("\x1b[2m[%s | tok in:%d out:%d | $%.4f]\x1b[0m\n",
		b.model, in, out, b.meter.SpentUSD())
}
