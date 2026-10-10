package debugui

import (
	"bytes"
	"context"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/LizHu95/Jetaime/services/api/internal/demo"
	"github.com/LizHu95/Jetaime/services/api/internal/telemetry"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

// 真实 OTLP 接收端验证：页面与 Phoenix 收到相同 span，连续操作不关闭 exporter。
func TestPageAndPhoenixShareSpans(t *testing.T) {
	var mu sync.Mutex
	var exported []*tracepb.Span
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/traces" {
			t.Error(r.URL.Path)
		}
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		batch := new(collector.ExportTraceServiceRequest)
		if err := proto.Unmarshal(data, batch); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		for _, resource := range batch.ResourceSpans {
			found := false
			for _, a := range resource.Resource.Attributes {
				if a.Key == "openinference.project.name" && a.Value.GetStringValue() == "page-test" {
					found = true
				}
			}
			if !found {
				t.Error("missing Phoenix project")
			}
			for _, scope := range resource.ScopeSpans {
				exported = append(exported, scope.Spans...)
			}
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	defer receiver.Close()
	s := newServer(t, Config{Trace: telemetry.Config{Enabled: true, Endpoint: receiver.URL + "/v1/traces", Project: "page-test"}})
	first := generate(t, s, "normal", "ollama")
	if err := s.tracerProvider.ForceFlush(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 已导出一批后继续执行失败反馈与新生成；共享 exporter 必须仍可使用。
	failed := decode(t, call(t, s, "POST", "decisions/"+first.Decision.ID+"/feedback", input{ActorID: demo.UserA, Action: "reject"}))
	if failed.Error == "" || failed.Trace == nil {
		t.Fatal("missing failed trace")
	}
	second := generate(t, s, "normal", "ollama")
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	wantCount := 0
	for _, local := range []*Trace{first.Trace, failed.Trace, second.Trace} {
		wantCount += len(local.Stages)
		spans := map[string]*tracepb.Span{}
		for _, span := range exported {
			if hex.EncodeToString(span.TraceId) == local.OTelTraceID {
				spans[hex.EncodeToString(span.SpanId)] = span
			}
		}
		if len(spans) != len(local.Stages) {
			t.Fatalf("page/Phoenix stage mismatch: %d/%d", len(local.Stages), len(spans))
		}
		for _, stage := range local.Stages {
			span := spans[stage.ID]
			if span == nil || span.Name != stage.Name {
				t.Fatal("span identity mismatch", stage.Name)
			}
			if len(span.ParentSpanId) > 0 && hex.EncodeToString(span.ParentSpanId) != stage.ParentID {
				t.Fatal("parent mismatch")
			}
			if stage.Error != "" && span.Status.Code != tracepb.Status_STATUS_CODE_ERROR {
				t.Fatal("failure not exported")
			}
			if strings.HasPrefix(stage.Name, "debug.") {
				attrs := map[string]string{}
				for _, a := range span.Attributes {
					attrs[a.Key] = a.Value.GetStringValue()
				}
				if attrs["debug.trace_id"] != local.ID || attrs["input.value"] == "" || attrs["output.value"] == "" {
					t.Fatal("missing correlation/input/output")
				}
			}
		}
		if w := call(t, s, "GET", "traces/"+local.ID, nil); w.Code != 200 {
			t.Fatal("local trace not retained")
		}
	}
	if len(exported) != wantCount {
		t.Fatal("duplicate/missing exported spans")
	}
}

func TestOfflinePhoenixPreservesPage(t *testing.T) {
	var warnings bytes.Buffer
	s := newServer(t, Config{Trace: telemetry.Config{Enabled: true, Endpoint: "http://127.0.0.1:1/v1/traces", Project: "offline", Warnings: &warnings}})
	out := generate(t, s, "normal", "ollama")
	if out.Decision == nil || len(out.Trace.Stages) == 0 {
		t.Fatal("missing page result")
	}
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(warnings.String(), "Phoenix 上报失败") {
		t.Fatal("missing export warning")
	}
}

// 即使两个请求的 span 交错结束，也只能进入各自页面记录。
func TestRecorderRoutesOverlappingOperations(t *testing.T) {
	p := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(&recordingRouter{}))
	defer func() { _ = p.Shutdown(context.Background()) }()
	a, ra := record(context.Background(), p)
	b, rb := record(context.Background(), p)
	a, sa := telemetry.Start(a, "a", "CHAIN", nil)
	b, sb := telemetry.Start(b, "b", "CHAIN", nil)
	var wg sync.WaitGroup
	for _, ctx := range []context.Context{a, b} {
		wg.Add(1)
		go func(ctx context.Context) {
			defer wg.Done()
			_, child := telemetry.Start(ctx, "child", "CHAIN", nil)
			telemetry.Finish(child, nil)
		}(ctx)
	}
	wg.Wait()
	telemetry.Finish(sb, nil)
	telemetry.Finish(sa, nil)
	for _, group := range []struct {
		rec          *recorder
		name, parent string
	}{{ra, "a", sa.SpanContext().SpanID().String()}, {rb, "b", sb.SpanContext().SpanID().String()}} {
		stages := group.rec.snapshot()
		if len(stages) != 2 || stages[0].Name != group.name || stages[1].ParentID != group.parent {
			t.Fatal("recorders mixed", stages)
		}
	}
}
