// Package tools defines in-process capabilities available to the main Agent.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

type Definition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}
type Call struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// Scope is backend-owned; it is never decoded from model arguments.
type Scope struct {
	Context        ai.Context
	ContextEndSeq  int64
	Question       string
	LegacyMode     string
	LoadExtraction ai.ExtractContextLoader
}
type Result struct {
	Run      ai.Run
	Terminal bool
	Data     any
}
type Tool interface {
	Definition() Definition
	Invoke(context.Context, Scope, json.RawMessage) (Result, error)
}
type Func struct {
	Spec    Definition
	Execute func(context.Context, Scope, json.RawMessage) (Result, error)
}

func (f Func) Definition() Definition { return f.Spec }
func (f Func) Invoke(ctx context.Context, s Scope, args json.RawMessage) (Result, error) {
	return f.Execute(ctx, s, args)
}

type Registry struct{ entries map[string]Tool }

func New(entries ...Tool) (*Registry, error) {
	r := &Registry{entries: map[string]Tool{}}
	for _, t := range entries {
		if t == nil {
			return nil, fmt.Errorf("nil tool")
		}
		d := t.Definition()
		if d.Name == "" || d.Description == "" || !json.Valid(d.Parameters) || r.entries[d.Name] != nil {
			return nil, fmt.Errorf("invalid or duplicate tool")
		}
		r.entries[d.Name] = t
	}
	return r, nil
}
func (r *Registry) Definitions() []Definition {
	out := []Definition{}
	for _, t := range r.entries {
		out = append(out, t.Definition())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
func (r *Registry) Execute(ctx context.Context, s Scope, call Call) (out Result, err error) {
	t, ok := r.entries[call.Name]
	if !ok || !json.Valid(call.Arguments) || string(call.Arguments) == "null" {
		return out, ai.ErrInvalidDecision
	}
	if err = ai.ValidateContext(s.Context); err != nil {
		return out, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	ctx, span := telemetry.Start(ctx, "ai.tool.execute", "TOOL", attribute.String("tool.name", call.Name), attribute.String("session.id", s.Context.GroupID), attribute.Int64("tool.context_end_seq", s.ContextEndSeq))
	defer func() { telemetry.End(span, err) }()
	return t.Invoke(ctx, s, call.Arguments)
}
func (r *Registry) Decode(content string) (Call, error) {
	var c Call
	if ai.DecodeJSON([]byte(content), &c) != nil || r.entries[c.Name] == nil || !json.Valid(c.Arguments) || string(c.Arguments) == "null" {
		return c, ai.ErrInvalidDecision
	}
	return c, nil
}

type legacyTool interface {
	LegacyArguments(Scope) (json.RawMessage, bool)
}

func (r *Registry) ExecuteLegacy(ctx context.Context, s Scope) (Result, error) {
	for name, t := range r.entries {
		if legacy, ok := t.(legacyTool); ok {
			if raw, matched := legacy.LegacyArguments(s); matched {
				return r.Execute(ctx, s, Call{Name: name, Arguments: raw})
			}
		}
	}
	return Result{}, fmt.Errorf("unsupported capability %q", s.LegacyMode)
}
