package engine

import (
	"context"
	"testing"

	"github.com/alaindong/cogent/internal/llm"
	"github.com/alaindong/cogent/internal/observe"
)

// TestEnvTemperature 覆盖 COGENT_TEMPERATURE 的解析归一化（缺省/合法/非法/越界）。
func TestEnvTemperature(t *testing.T) {
	cases := []struct {
		name string
		val  string
		want float32
	}{
		{name: "empty returns provider default 0", val: "", want: 0},
		{name: "valid mid", val: "0.7", want: 0.7},
		{name: "zero explicit", val: "0", want: 0},
		{name: "upper bound 2", val: "2", want: 2},
		{name: "non-numeric falls back to 0", val: "hot", want: 0},
		{name: "negative out of range", val: "-0.1", want: 0},
		{name: "above range", val: "2.5", want: 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(temperatureEnvVar, c.val)
			if got := envTemperature(); got != c.want {
				t.Errorf("envTemperature()=%v, want %v", got, c.want)
			}
		})
	}
}

// TestEngine_TemperaturePropagates 断言设置 COGENT_TEMPERATURE 后请求携带该温度；
// 未设置时保持 0（提供方默认，行为不变）。
func TestEngine_TemperaturePropagates(t *testing.T) {
	t.Setenv(temperatureEnvVar, "0.7")
	f := &fakeLLM{turns: [][]llm.Delta{{{Text: "ok"}}}}
	eng := newTestEngine(t, f)
	events, err := eng.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	collect(t, events)
	if len(f.gotTemps) == 0 {
		t.Fatal("no llm call recorded")
	}
	if f.gotTemps[0] != 0.7 {
		t.Errorf("request Temperature=%v, want 0.7", f.gotTemps[0])
	}
}

// TestEngine_TemperatureDefaultUnchanged 断言未设置该 env 时温度为 0（与现状一致）。
func TestEngine_TemperatureDefaultUnchanged(t *testing.T) {
	t.Setenv(temperatureEnvVar, "")
	prov, err := observe.New(observe.Config{Enabled: false})
	if err != nil {
		t.Fatalf("observe.New: %v", err)
	}
	f := &fakeLLM{turns: [][]llm.Delta{{{Text: "ok"}}}}
	eng, err := New(Deps{LLM: f, Observe: prov, Model: "test-model"})
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}
	events, err := eng.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	collect(t, events)
	if len(f.gotTemps) == 0 || f.gotTemps[0] != 0 {
		t.Errorf("default request Temperature=%v, want 0", f.gotTemps)
	}
}
