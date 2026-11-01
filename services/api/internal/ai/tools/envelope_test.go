package tools

import (
	"context"
	"testing"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/google/jsonschema-go/jsonschema"
)

type envelopeInput struct {
	Action string            `json:"action"`
	Count  int               `json:"count"`
	Data   map[string]string `json:"data,omitempty"`
}

func envelopeTool(name string, calls *int) Tool {
	return NewTyped(Metadata{Name: name, Description: "保存"}, func(context.Context, Scope, envelopeInput) (struct{}, error) {
		*calls++
		return struct{}{}, nil
	}, Options[struct{}]{Input: func(s *jsonschema.Schema) {
		s.Properties["action"].Enum = []any{"save"}
		s.Properties["count"].Minimum = jsonschema.Ptr(float64(1))
	}})
}

func TestEnvelopeNormalizationRequiresUniqueCompleteVisibleContract(t *testing.T) {
	for _, tc := range []struct {
		name, raw  string
		hidden     bool
		ambiguous  bool
		want       string
		normalized bool
	}{
		{name: "bare arguments", raw: ` {"action":"save","count":2} `, want: "save_item", normalized: true},
		{name: "ordinary envelope", raw: `{"name":"save_item","arguments":{"action":"save","count":2}}`, want: "save_item"},
		{name: "missing required", raw: `{"action":"save"}`},
		{name: "enum", raw: `{"action":"cancel","count":2}`},
		{name: "bound", raw: `{"action":"save","count":0}`},
		{name: "type", raw: `{"action":"save","count":"2"}`},
		{name: "extra identity", raw: `{"action":"save","count":2,"actorId":"other"}`},
		{name: "duplicate field", raw: `{"action":"save","count":1,"count":2}`},
		{name: "nested duplicate", raw: `{"action":"save","count":2,"data":{"x":"first","x":"last"}}`},
		{name: "partial envelope", raw: `{"arguments":{"action":"save","count":2}}`},
		{name: "mixed envelope", raw: `{"name":"save_item","action":"save","count":2}`},
		{name: "unknown name", raw: `{"name":"unknown","arguments":{"action":"save","count":2}}`},
		{name: "two objects", raw: `{"action":"save","count":2} {}`},
		{name: "invalid JSON", raw: `{"action":"save","count":2`},
		{name: "null", raw: `null`},
		{name: "array", raw: `[{"action":"save","count":2}]`},
		{name: "empty", raw: `{}`},
		{name: "hidden capability", raw: `{"action":"save","count":2}`, hidden: true},
		{name: "ambiguous", raw: `{"action":"save","count":2}`, ambiguous: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			entries := []Tool{envelopeTool("save_item", &calls)}
			if tc.ambiguous {
				entries = append(entries, envelopeTool("save_other", &calls))
			}
			r, err := New(entries...)
			if err != nil {
				t.Fatal(err)
			}
			visible := r.Definitions()
			if tc.hidden {
				visible = nil
			}
			call, normalized, err := r.DecodeWithEnvelope(tc.raw, visible)
			if tc.want == "" {
				if err == nil || normalized || call.Name != "" {
					t.Fatalf("unsafe inference: %+v normalized=%v err=%v", call, normalized, err)
				}
			} else if err != nil || call.Name != tc.want || normalized != tc.normalized {
				t.Fatalf("decode failed: %+v normalized=%v err=%v", call, normalized, err)
			}
			if calls != 0 {
				t.Fatal("decoding executed a tool")
			}
			if normalized && string(call.Arguments) != `{"action":"save","count":2}` {
				t.Fatal("arguments changed")
			}
		})
	}
}

func TestNormalizedWriteStillRequiresAuthorization(t *testing.T) {
	calls := 0
	r, err := New(envelopeTool("save_item", &calls))
	if err != nil {
		t.Fatal(err)
	}
	call, normalized, err := r.DecodeWithEnvelope(`{"action":"save","count":2}`, r.Definitions())
	if err != nil || !normalized {
		t.Fatal("expected local normalization", err)
	}
	scope := contractScope()
	scope.Context.Request = &ai.RequestContext{TriggerMode: "proactive", AllowWrites: false}
	_, err = r.Execute(context.Background(), scope, call)
	failure, ok := Repairable(err)
	if !ok || failure.Code != "explicit_request_required" || calls != 0 {
		t.Fatalf("authorization bypassed: calls=%d err=%v", calls, err)
	}
}
