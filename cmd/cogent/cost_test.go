package main

import (
	"math"
	"testing"

	"github.com/alaindong/cogent/internal/observe"
)

// recordingMeter 记录转发到底层的计数/直方图调用，用于断言装饰器的透传行为。
type recordingMeter struct {
	counts  map[string]int64
	records int
}

func newRecordingMeter() *recordingMeter {
	return &recordingMeter{counts: make(map[string]int64)}
}

func (m *recordingMeter) Count(name string, delta int64, _ ...observe.Attr) {
	m.counts[name] += delta
}

func (m *recordingMeter) Record(string, float64, ...observe.Attr) {
	m.records++
}

func tokenCount(kind, model string) []observe.Attr {
	return []observe.Attr{
		{Key: "token.kind", Value: kind},
		{Key: "llm.model", Value: model},
	}
}

func approxEqual(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestCostMeter_AccruesByModelAndKind(t *testing.T) {
	inner := newRecordingMeter()
	cm := newCostMeter(inner)

	// deepseek-reasoner: input 0.55/M, output 2.19/M。
	cm.Count(tokensMetricName, 1_000_000, tokenCount("input", "deepseek-reasoner")...)
	cm.Count(tokensMetricName, 1_000_000, tokenCount("output", "deepseek-reasoner")...)

	want := 0.55 + 2.19
	if got := cm.SpentUSD(); !approxEqual(got, want) {
		t.Errorf("SpentUSD = %v, want %v", got, want)
	}
	// 计数仍应转发到底层。
	if inner.counts[tokensMetricName] != 2_000_000 {
		t.Errorf("forwarded token count = %d, want 2000000", inner.counts[tokensMetricName])
	}
}

func TestCostMeter_UnknownModelUsesFallback(t *testing.T) {
	cm := newCostMeter(newRecordingMeter())
	cm.Count(tokensMetricName, 2_000_000, tokenCount("input", "mystery-model")...)
	want := 2.0 * fallbackModelPrice.inputPerMTok
	if got := cm.SpentUSD(); !approxEqual(got, want) {
		t.Errorf("SpentUSD = %v, want %v (fallback)", got, want)
	}
}

func TestCostMeter_EnvOverridesPrice(t *testing.T) {
	t.Setenv("COGENT_PRICE_DEEPSEEK_REASONER_INPUT", "10")
	t.Setenv("COGENT_PRICE_DEEPSEEK_REASONER_OUTPUT", "20")
	cm := newCostMeter(newRecordingMeter())

	cm.Count(tokensMetricName, 1_000_000, tokenCount("input", "deepseek-reasoner")...)
	cm.Count(tokensMetricName, 1_000_000, tokenCount("output", "deepseek-reasoner")...)

	if got := cm.SpentUSD(); !approxEqual(got, 30) {
		t.Errorf("SpentUSD = %v, want 30 (env override)", got)
	}
}

func TestCostMeter_IgnoresNonTokenMetricsForCost(t *testing.T) {
	inner := newRecordingMeter()
	cm := newCostMeter(inner)

	cm.Count("cogent.loop.outcome", 1)
	cm.Record("cogent.llm.ttft", 42)

	if got := cm.SpentUSD(); got != 0 {
		t.Errorf("SpentUSD = %v, want 0 (non-token metrics must not accrue cost)", got)
	}
	if inner.counts["cogent.loop.outcome"] != 1 || inner.records != 1 {
		t.Error("non-token metrics should still be forwarded to inner meter")
	}
}

func TestCostProvider_MeterIsCostMeter(t *testing.T) {
	prov, err := observe.New(observe.Config{Enabled: false})
	if err != nil {
		t.Fatalf("observe.New: %v", err)
	}
	cp := newCostProvider(prov)
	// 经装饰 provider 的 Meter 计数 token 后，成本应可读取（即便底层是 no-op）。
	cp.Meter().Count(tokensMetricName, 1_000_000, tokenCount("output", "deepseek-chat")...)
	if got := cp.meter.SpentUSD(); !approxEqual(got, defaultModelPrices["deepseek-chat"].outputPerMTok) {
		t.Errorf("SpentUSD = %v, want %v", got, defaultModelPrices["deepseek-chat"].outputPerMTok)
	}
}

func TestModelEnvKey(t *testing.T) {
	if got := modelEnvKey("deepseek-reasoner"); got != "DEEPSEEK_REASONER" {
		t.Errorf("modelEnvKey = %q, want DEEPSEEK_REASONER", got)
	}
}
