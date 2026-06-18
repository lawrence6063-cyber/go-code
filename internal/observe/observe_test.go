package observe

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/goleak"
)

// TestMain 在包级别断言无 goroutine 泄漏：BatchSpanProcessor / PeriodicReader 须随 Shutdown 退出。
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// spanDump 是 stdouttrace 导出的 span JSON 的最小投影，仅取断言所需字段。
type spanDump struct {
	Name        string `json:"Name"`
	SpanContext struct {
		TraceID string `json:"TraceID"`
		SpanID  string `json:"SpanID"`
	} `json:"SpanContext"`
	Parent struct {
		TraceID string `json:"TraceID"`
		SpanID  string `json:"SpanID"`
	} `json:"Parent"`
	Status struct {
		Code string `json:"Code"`
	} `json:"Status"`
	Attributes []struct {
		Key   string `json:"Key"`
		Value struct {
			Value any `json:"Value"`
		} `json:"Value"`
	} `json:"Attributes"`
}

func TestNew_DisabledReturnsNoop(t *testing.T) {
	prov, err := New(Config{Enabled: false})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, ok := prov.(noopProvider); !ok {
		t.Errorf("disabled provider type = %T, want noopProvider", prov)
	}
	// no-op 调用零开销且不 panic。
	ctx, end := prov.Tracer().Start(context.Background(), "x")
	end(nil)
	prov.Meter().Count("c", 1)
	if ctx == nil {
		t.Error("Start returned nil ctx")
	}
	if err := prov.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
}

func TestNew_NoneExporterReturnsNoop(t *testing.T) {
	prov, err := New(Config{Enabled: true, Exporter: "none"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, ok := prov.(noopProvider); !ok {
		t.Errorf("none exporter type = %T, want noopProvider", prov)
	}
}

func TestNew_UnknownExporter(t *testing.T) {
	if _, err := New(Config{Enabled: true, Exporter: "bogus"}); err == nil {
		t.Error("expected error for unknown exporter, got nil")
	}
}

func TestFileExporter_SpanTree(t *testing.T) {
	dir := t.TempDir()
	prov, err := New(Config{Enabled: true, Exporter: "file", TraceDir: dir, SampleRatio: 1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	tr := prov.Tracer()
	pctx, endParent := tr.Start(context.Background(), "parent", Attr{Key: "k", Value: "v"})
	_, endChild := tr.Start(pctx, "child")
	endChild(errors.New("boom")) // 子 span 标记错误
	endParent(nil)

	prov.Meter().Count("cogent.tool.calls", 1, Attr{Key: "tool.name", Value: "read_file"})
	prov.Meter().Record("cogent.react.steps", 3)

	if err := prov.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	spans := readSpans(t, dir)
	parent := findSpan(t, spans, "parent")
	child := findSpan(t, spans, "child")

	if parent.SpanContext.TraceID != child.SpanContext.TraceID {
		t.Errorf("trace_id mismatch: parent=%s child=%s", parent.SpanContext.TraceID, child.SpanContext.TraceID)
	}
	if child.Parent.SpanID != parent.SpanContext.SpanID {
		t.Errorf("child.parent_span = %s, want parent span %s", child.Parent.SpanID, parent.SpanContext.SpanID)
	}
	if child.Status.Code != "Error" {
		t.Errorf("child status = %s, want Error", child.Status.Code)
	}
	if !hasAttr(parent, "k", "v") {
		t.Errorf("parent missing attr k=v: %+v", parent.Attributes)
	}
}

// readSpans 解码 traceDir 下的 traces-*.jsonl（可能含多个串联 JSON 值）为 span 列表。
func readSpans(t *testing.T, dir string) []spanDump {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "traces-*.jsonl"))
	if err != nil || len(matches) == 0 {
		t.Fatalf("no trace file found in %s (err=%v)", dir, err)
	}
	f, err := os.Open(matches[0])
	if err != nil {
		t.Fatalf("open trace file: %v", err)
	}
	defer func() { _ = f.Close() }()

	var spans []spanDump
	dec := json.NewDecoder(f)
	for {
		var s spanDump
		if derr := dec.Decode(&s); errors.Is(derr, io.EOF) {
			break
		} else if derr != nil {
			t.Fatalf("decode span: %v", derr)
		}
		spans = append(spans, s)
	}
	if len(spans) == 0 {
		t.Fatal("no spans decoded")
	}
	return spans
}

func findSpan(t *testing.T, spans []spanDump, name string) spanDump {
	t.Helper()
	for _, s := range spans {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("span %q not found among %d spans", name, len(spans))
	return spanDump{}
}

func hasAttr(s spanDump, key, want string) bool {
	for _, a := range s.Attributes {
		if a.Key == key {
			if v, ok := a.Value.Value.(string); ok && v == want {
				return true
			}
		}
	}
	return false
}

func TestToKeyValue(t *testing.T) {
	tests := []struct {
		name    string
		in      Attr
		wantKey string
		wantStr string
	}{
		{"string", Attr{Key: "s", Value: "x"}, "s", "x"},
		{"bool", Attr{Key: "b", Value: true}, "b", "true"},
		{"int", Attr{Key: "i", Value: 7}, "i", "7"},
		{"int64", Attr{Key: "i64", Value: int64(9)}, "i64", "9"},
		{"float64", Attr{Key: "f", Value: 1.5}, "f", "1.5"},
		{"fallback", Attr{Key: "x", Value: struct{ A int }{A: 1}}, "x", "{1}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kv := toKeyValue(tt.in)
			if string(kv.Key) != tt.wantKey {
				t.Errorf("key = %q, want %q", kv.Key, tt.wantKey)
			}
			if got := kv.Value.Emit(); got != tt.wantStr {
				t.Errorf("value = %q, want %q", got, tt.wantStr)
			}
		})
	}
}
