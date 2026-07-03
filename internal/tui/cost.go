package tui

import (
	"context"
	"strings"
	"sync"

	"github.com/alaindong/cogent/internal/loop"
	"github.com/alaindong/cogent/internal/observe"
)

// tokensMetricName 是 engine 上抛 token 用量的计数器名（与 internal/engine 约定一致）。
const tokensMetricName = "cogent.tokens"

// modelPrice 是单个模型的 token 单价（美元/百万 token），区分输入与输出。
type modelPrice struct {
	inputPerMTok  float64 // 输入（prompt）token 单价，美元/1M
	outputPerMTok float64 // 输出（completion）token 单价，美元/1M
}

// defaultModelPrices 是常用 DeepSeek 模型的缺省单价（美元/百万 token），可经环境变量覆盖。
// 仅作护栏估算用途，非精确账单；真实单价请以提供方公布为准并经 COGENT_PRICE_* 覆盖。
// 注：deepseek-chat/deepseek-reasoner 为旧别名（2026-07-24 退役，分别路由到 V4-Flash 非思考/思考模式），
// 单价沿用历史值仅作保守护栏；新接入请用 deepseek-v4-pro / deepseek-v4-flash 显式模型 ID。
var defaultModelPrices = map[string]modelPrice{
	"deepseek-chat":     {inputPerMTok: 0.27, outputPerMTok: 1.10},
	"deepseek-reasoner": {inputPerMTok: 0.55, outputPerMTok: 2.19},
	"deepseek-v4-flash": {inputPerMTok: 0.14, outputPerMTok: 0.28},
	"deepseek-v4-pro":   {inputPerMTok: 1.74, outputPerMTok: 3.48},
}

// fallbackModelPrice 是未知模型且无环境变量覆盖时的保守单价（宁可高估，利于成本护栏早停）。
var fallbackModelPrice = modelPrice{inputPerMTok: 0.50, outputPerMTok: 1.50}

// costMeter 装饰 observe.Meter：拦截 cogent.tokens 计数，按模型单价累计美元成本，
// 其余指标原样转发。同时实现 loop.CostMeter（SpentUSD），驱动外层循环的 --max-cost 护栏。
// 线程安全：引擎多 goroutine 写入、loop 读取累计值。
type costMeter struct {
	inner  observe.Meter
	mu     sync.Mutex
	spent  float64
	inTok  int64 // 累计输入（prompt）token 数
	outTok int64 // 累计输出（completion）token 数
}

// newCostMeter 构造一个装饰 inner 的成本计量器。
func newCostMeter(inner observe.Meter) *costMeter {
	return &costMeter{inner: inner}
}

// Count 见 observe.Meter 接口说明：对 cogent.tokens 按属性中的模型与 token.kind 累计成本，再转发。
func (m *costMeter) Count(name string, delta int64, attrs ...observe.Attr) {
	if name == tokensMetricName && delta > 0 {
		m.accrue(delta, attrs)
	}
	m.inner.Count(name, delta, attrs...)
}

// Record 见 observe.Meter 接口说明：成本计量不消费直方图，原样转发。
func (m *costMeter) Record(name string, value float64, attrs ...observe.Attr) {
	m.inner.Record(name, value, attrs...)
}

// SpentUSD 见 loop.CostMeter 接口说明：返回至今累计的估算成本（美元）。
func (m *costMeter) SpentUSD() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.spent
}

// Tokens 返回至今累计的输入与输出 token 数（供状态栏展示）。
func (m *costMeter) Tokens() (in, out int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.inTok, m.outTok
}

// accrue 根据 token 数与属性（token.kind/llm.model）累加成本与分类 token 计数。
func (m *costMeter) accrue(tokens int64, attrs []observe.Attr) {
	kind, model := tokenAttrs(attrs)
	price := priceFor(model)
	perMTok := price.inputPerMTok
	if kind == "output" {
		perMTok = price.outputPerMTok
	}
	usd := float64(tokens) / 1e6 * perMTok

	m.mu.Lock()
	m.spent += usd
	if kind == "output" {
		m.outTok += tokens
	} else {
		m.inTok += tokens
	}
	m.mu.Unlock()
}

// tokenAttrs 从属性集中提取 token.kind 与 llm.model（缺失时返回空串）。
func tokenAttrs(attrs []observe.Attr) (kind, model string) {
	for _, a := range attrs {
		switch a.Key {
		case "token.kind":
			kind = attrString(a.Value)
		case "llm.model":
			model = attrString(a.Value)
		}
	}
	return kind, model
}

// attrString 把属性值收敛为字符串（仅处理本模块产生的字符串型属性）。
func attrString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// priceFor 返回模型的单价：优先环境变量覆盖，其次缺省表，最后保守回退价。
func priceFor(model string) modelPrice {
	base, ok := defaultModelPrices[strings.ToLower(strings.TrimSpace(model))]
	if !ok {
		base = fallbackModelPrice
	}
	if in, ok := envPrice(model, "INPUT"); ok {
		base.inputPerMTok = in
	}
	if out, ok := envPrice(model, "OUTPUT"); ok {
		base.outputPerMTok = out
	}
	return base
}

// envPrice 读取 COGENT_PRICE_<MODELKEY>_<KIND>（美元/百万 token）覆盖单价；未设或非法时 ok=false。
// MODELKEY 由模型名大写并把非字母数字字符替换为下划线得到（如 deepseek-reasoner → DEEPSEEK_REASONER）。
func envPrice(model, kind string) (float64, bool) {
	key := "COGENT_PRICE_" + modelEnvKey(model) + "_" + kind
	v := envFloat(key, -1)
	if v < 0 {
		return 0, false
	}
	return v, true
}

// modelEnvKey 把模型名规整为环境变量片段：大写 + 非字母数字替换为下划线。
func modelEnvKey(model string) string {
	var sb strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(model)) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			sb.WriteRune(r)
		} else {
			sb.WriteRune('_')
		}
	}
	return sb.String()
}

// CostProvider 装饰 observe.Provider，使 Meter() 返回成本计量器（Tracer/Shutdown 原样转发）。
type CostProvider struct {
	inner observe.Provider
	meter *costMeter
}

// NewCostProvider 包装 inner，返回装饰后的 Provider 与可读取累计成本的计量器。
func NewCostProvider(inner observe.Provider) CostProvider {
	return CostProvider{inner: inner, meter: newCostMeter(inner.Meter())}
}

// Tracer 见 observe.Provider 接口说明：转发底层 Tracer。
func (p CostProvider) Tracer() observe.Tracer { return p.inner.Tracer() }

// Meter 见 observe.Provider 接口说明：返回成本计量器（拦截 token 计数）。
func (p CostProvider) Meter() observe.Meter { return p.meter }

// Shutdown 见 observe.Provider 接口说明：转发底层 Shutdown。
func (p CostProvider) Shutdown(ctx context.Context) error { return p.inner.Shutdown(ctx) }

// CostMeter 返回累计成本计量器（实现 loop.CostMeter），供外层循环 --max-cost 护栏接入。
func (p CostProvider) CostMeter() loop.CostMeter { return p.meter }
