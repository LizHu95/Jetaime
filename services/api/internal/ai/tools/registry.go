// Package tools defines typed, in-process capabilities available to the main Agent.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
)

// Definition is the model-visible contract. Identity, permissions and execution
// policy are owned by the backend and are not model arguments.
type Definition struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"inputSchema"`
	OutputSchema json.RawMessage `json:"outputSchema"`
	Annotations  Annotations     `json:"annotations"`
}
type Annotations struct {
	ReadOnly   bool `json:"readOnlyHint"`
	Idempotent bool `json:"idempotentHint"`
}
type Metadata struct {
	Name, Description, Version, Kind, Model string
	ReadOnly, Idempotent, Terminal          bool
	Timeout                                 time.Duration
}
type Call struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// Scope is backend-owned; it is never decoded from model arguments.
type Scope struct {
	ExcludedCards  map[string]bool    // Invocation-local exclusions activated by a replacement search.
	Cards          map[string]ai.Card // Invocation-local, populated only by successful tools.
	Context        ai.Context
	ContextEndSeq  int64
	Question       string
	LoadExtraction ai.ExtractContextLoader
}

// Completion is an optional presentation of a successful business result.
// Tools never construct the Agent's execution Run.
type Completion struct {
	Cards        []ai.Card
	Silent       bool
	Answer       *ai.Answer
	Suggestions  []ai.Suggestion
	Sources      []ai.Message
	Basis        string
	HistoryScope *ai.HistoryScope
}
type Execution struct {
	StartedAt                  time.Time
	DurationMS                 int64
	Steps                      []ai.Step
	ModelIdentity              *llm.Identity
	Kind, Model, PromptVersion string
}

// Result contains validated JSON, presentation, and a separate execution record.
// Data uses JSON at this heterogeneous registry boundary, never an unchecked any.
type Result struct {
	Data       json.RawMessage
	Terminal   bool
	Completion Completion
	Execution  Execution
}

// Tool implementations are provided by NewTyped. Embedding Typed allows a
// capability to expose additional backend methods, such as a context injector.
type Tool interface {
	Definition() Definition
	Invoke(context.Context, Scope, json.RawMessage) (Result, error)
	contractError() error
}
type Registry struct{ entries map[string]Tool }

func New(entries ...Tool) (*Registry, error) {
	r := &Registry{entries: map[string]Tool{}}
	for _, t := range entries {
		if t == nil || reflect.ValueOf(t).Kind() == reflect.Pointer && reflect.ValueOf(t).IsNil() {
			return nil, fmt.Errorf("nil tool")
		}
		if err := t.contractError(); err != nil {
			return nil, err
		}
		d := t.Definition()
		if r.entries[d.Name] != nil {
			return nil, fmt.Errorf("duplicate tool %q", d.Name)
		}
		r.entries[d.Name] = t
	}
	return r, nil
}
func (r *Registry) Definitions() []Definition {
	out := make([]Definition, 0, len(r.entries))
	for _, t := range r.entries {
		out = append(out, t.Definition())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
func (r *Registry) Execute(ctx context.Context, s Scope, call Call) (Result, error) {
	t, ok := r.entries[call.Name]
	if !ok {
		return Result{}, InvalidArguments("unknown_tool", "选择已注册的工具名称。")
	}
	if request := s.Context.Request; request != nil && request.TriggerMode == "proactive" && !request.AllowWrites && !t.Definition().Annotations.ReadOnly {
		return Result{}, InvalidArguments("explicit_request_required", "主动介入不能自行执行写操作；需要当前用户明确向 AI 提出操作请求。")
	}
	return t.Invoke(ctx, s, call.Arguments)
}
func (r *Registry) Decode(content string) (Call, error) {
	var c Call
	if ai.DecodeJSON([]byte(content), &c) != nil {
		return c, InvalidArguments("invalid_call", "返回仅包含 name 和 arguments 的工具调用对象。")
	}
	if r.entries[c.Name] == nil {
		return c, InvalidArguments("unknown_tool", "选择已注册的工具名称。")
	}
	return c, nil
}
