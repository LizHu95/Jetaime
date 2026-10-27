package memory

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func memoryRecorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	previous := otel.GetTracerProvider()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { otel.SetTracerProvider(previous); _ = provider.Shutdown(context.Background()) })
	return recorder
}
func spanAttributes(s sdktrace.ReadOnlySpan) map[string]attribute.Value {
	out := map[string]attribute.Value{}
	for _, a := range s.Attributes() {
		out[string(a.Key)] = a.Value
	}
	return out
}
func TestEmbeddingTraceDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name, input, body, reason string
		status                    int
		capture                   bool
	}{
		{"success", "private text", `{"data":[{"index":0,"embedding":[1,2,3]}],"usage":{"prompt_tokens":4,"total_tokens":4}}`, "", 200, true},
		{"content_disabled", "private text", `{"data":[{"index":0,"embedding":[1,2,3]}]}`, "", 200, false},
		{"empty", " ", "", "empty_input", 200, false},
		{"too_large", strings.Repeat("a", 33), "", "input_too_large", 200, false},
		{"http_error", "private text", "secret provider error", "http_error", 429, false},
		{"invalid_json", "private text", "secret provider error", "invalid_response", 200, false},
		{"shape", "private text", `{"data":[]}`, "invalid_response_shape", 200, false},
		{"dimension", "private text", `{"data":[{"index":0,"embedding":[1]}]}`, "dimension_mismatch", 200, false},
		{"zero", "private text", `{"data":[{"index":0,"embedding":[0,0,0]}]}`, "zero_vector", 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := memoryRecorder(t)
			if tc.capture {
				t.Setenv("JETAIME_TRACE_CAPTURE_CONTENT", "true")
			} else {
				t.Setenv("JETAIME_TRACE_CAPTURE_CONTENT", "false")
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			c := &embeddingClient{config: EmbeddingConfig{BaseURL: server.URL, Model: "test", Dimension: 3, MaxInputBytes: 32}, client: server.Client()}
			_, err := c.Embed(embeddingContext(context.Background(), "search_query"), tc.input)
			spans := r.Ended()
			if len(spans) != 1 {
				t.Fatalf("spans: %d", len(spans))
			}
			s := spans[0]
			a := spanAttributes(s)
			if a["memory.failure_reason"].AsString() != tc.reason || (err != nil) != (tc.reason != "") {
				t.Fatalf("reason=%v err=%v", a["memory.failure_reason"], err)
			}
			if a["memory.input_bytes"].AsInt64() != int64(len(tc.input)) || a["memory.embedding_purpose"].AsString() != "search_query" {
				t.Fatal(a)
			}
			if tc.reason != "" && s.Status().Code != codes.Error {
				t.Fatal(s.Status())
			}
			_, captured := a["input.value"]
			if captured != tc.capture {
				t.Fatal("content capture gate ignored")
			}
			for key, value := range a {
				if strings.Contains(value.AsString(), "secret provider") || strings.Contains(key, "embedding.vector") {
					t.Fatal("unexpected content", key)
				}
			}
			if tc.name == "success" && (a["llm.token_count.total"].AsInt64() != 4 || a["llm.token_count.prompt"].AsInt64() != 4 || a["memory.actual_embedding_dimension"].AsInt64() != 3 || a["http.response.status_code"].AsInt64() != 200) {
				t.Fatal(a)
			}
		})
	}
}
func TestCandidateValidationTrace(t *testing.T) {
	for _, capture := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[capture], func(t *testing.T) {
			r := memoryRecorder(t)
			t.Setenv("JETAIME_TRACE_CAPTURE_CONTENT", map[bool]string{false: "false", true: "true"}[capture])
			b := Batch{From: 4, To: 5, Messages: []Message{{ID: "source", SenderID: "user", Seq: 5}}}
			c := Candidate{Kind: "fact", Subject: "shared", EventKey: "private key", Content: "private candidate", TemporalScope: "enduring", SourceIDs: []string{"missing"}}
			err := traceValidateCandidates(context.Background(), b, []Candidate{c}, "before_save_extraction")
			if !errors.Is(err, ErrInvalid) {
				t.Fatal(err)
			}
			if err := traceValidateCandidates(context.Background(), b, nil, "before_apply_mutations"); err != nil {
				t.Fatal(err)
			}
			spans := r.Ended()
			if len(spans) != 2 {
				t.Fatal(len(spans))
			}
			a := spanAttributes(spans[0])
			index, ok := a["memory.failed_candidate_index"]
			if !ok || index.AsInt64() != 0 || a["memory.failure_reason"].AsString() != "source_not_in_batch" || a["memory.validation_stage"].AsString() != "before_save_extraction" || a["memory.from_seq"].AsInt64() != 4 || a["memory.message_count"].AsInt64() != 1 {
				t.Fatal(a)
			}
			for _, s := range spans {
				a := spanAttributes(s)
				_, present := a["input.value"]
				if present != capture {
					t.Fatal("capture gate ignored")
				}
				if strings.Contains(s.Status().Description, "private") {
					t.Fatal("content leaked in status")
				}
			}
			a = spanAttributes(spans[1])
			if !a["memory.succeeded"].AsBool() || a["memory.validation_stage"].AsString() != "before_apply_mutations" {
				t.Fatal(a)
			}
		})
	}
}
func TestVectorFailureReasons(t *testing.T) {
	for _, tc := range []struct {
		v      []float32
		reason string
	}{{[]float32{1}, "dimension_mismatch"}, {[]float32{0, 0, 0}, "zero_vector"}, {[]float32{float32(math.NaN()), 0, 0}, "non_finite_vector"}, {[]float32{float32(math.Inf(1)), 0, 0}, "non_finite_vector"}} {
		err := validateVector(tc.v, 3)
		if !errors.Is(err, ErrInvalid) || !strings.HasSuffix(err.Error(), tc.reason) {
			t.Fatal(err)
		}
	}
}
