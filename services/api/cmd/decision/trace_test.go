package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

// 真实 OTLP HTTP exporter 对接测试接收端，验证短 CLI 退出前确实导出。
func TestCLIExportsWorkflowAndModelFailures(t *testing.T) {
	var mu sync.Mutex
	var batches []*collector.ExportTraceServiceRequest
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/traces" || r.Header.Get("Content-Type") != "application/x-protobuf" {
			t.Errorf("unexpected OTLP request: %s", r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		batch := new(collector.ExportTraceServiceRequest)
		if err := proto.Unmarshal(body, batch); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		batches = append(batches, batch)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	defer receiver.Close()
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewEncoder(w).Encode(map[string]any{"done": true, "message": map[string]string{"role": "assistant", "content": "invalid model result"}, "prompt_eval_count": 123, "eval_count": 12}); err != nil {
			t.Error(err)
		}
	}))
	defer model.Close()
	var out bytes.Buffer
	args := []string{"-trace", "-trace-endpoint", receiver.URL + "/v1/traces", "-trace-project", "test-jetaime", "-once"}
	if err := run(context.Background(), args, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	args = append(args, "-provider", "ollama", "-ollama-url", model.URL)
	if err := run(context.Background(), args, strings.NewReader(""), &out); err == nil {
		t.Fatal("invalid model output accepted")
	}
	mu.Lock()
	defer mu.Unlock()
	names := map[string]*tracepb.Span{}
	var roots []*tracepb.Span
	for _, batch := range batches {
		for _, resource := range batch.ResourceSpans {
			projectFound := false
			for _, a := range resource.Resource.Attributes {
				if a.Key == "openinference.project.name" && a.Value.GetStringValue() == "test-jetaime" {
					projectFound = true
				}
			}
			if !projectFound {
				t.Fatal("Phoenix project not configured")
			}
			for _, scope := range resource.ScopeSpans {
				for _, span := range scope.Spans {
					names[span.Name] = span
					if span.Name == "decision.generate" {
						roots = append(roots, span)
					}
				}
			}
		}
	}
	for _, name := range []string{"decision.generate", "prepare_session", "build_context", "evaluate_candidates", "generate_result", "validate_result", "commit", "ollama.generate"} {
		if names[name] == nil {
			t.Fatalf("missing %s", name)
		}
	}
	if len(roots) != 2 || roots[0].Status.Code != tracepb.Status_STATUS_CODE_OK || roots[1].Status.Code != tracepb.Status_STATUS_CODE_ERROR {
		t.Fatal("successful/failed roots not exported")
	}
	attrs := map[string]string{}
	for _, a := range roots[0].Attributes {
		attrs[a.Key] = a.Value.GetStringValue()
	}
	if len(attrs["fixture.sha256"]) != 64 || attrs["decision.id"] == "" {
		t.Fatal("missing data version or decision ID")
	}
	llm := names["ollama.generate"]
	if len(llm.ParentSpanId) == 0 || !bytes.Equal(llm.TraceId, roots[1].TraceId) || llm.Status.Code != tracepb.Status_STATUS_CODE_ERROR {
		t.Fatal("model span not attached to failed generation")
	}
	modelAttrs := map[string]string{}
	var promptTokens int64
	for _, a := range llm.Attributes {
		modelAttrs[a.Key] = a.Value.GetStringValue()
		if a.Key == "llm.token_count.prompt" {
			promptTokens = a.Value.GetIntValue()
		}
		if a.Key == "input.value" {
			input := a.Value.GetStringValue()
			if !strings.Contains(input, "messages") || strings.Contains(input, `\"id\":\"restaurant-20\"`) {
				t.Fatal("missing prompt or private candidate logged")
			}
		}
	}
	if modelAttrs["openinference.span.kind"] != "LLM" || modelAttrs["llm.model_name"] != "qwen3.5:9b" || !strings.Contains(modelAttrs["output.value"], "invalid model result") || promptTokens != 123 {
		t.Fatal("missing model/raw response/token attributes")
	}
}

func TestUnavailablePhoenixDoesNotFailDecision(t *testing.T) {
	var out bytes.Buffer
	if err := run(context.Background(), []string{"-trace", "-trace-endpoint", "http://127.0.0.1:1/v1/traces", "-once"}, strings.NewReader(""), &out); err != nil {
		t.Fatal("telemetry changed business result", err)
	}
	if !strings.Contains(out.String(), "recommended") {
		t.Fatal(out.String())
	}
}
