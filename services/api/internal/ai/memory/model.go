package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/LizHu95/Jetaime/services/api/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"io"
	"strings"

	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
)

type ModelExtractor struct {
	Model  llm.ChatModel
	Config llm.Config
}

func (m ModelExtractor) call(ctx context.Context, prompt, schema string, input any, out any) error {
	payload, err := json.Marshal(input)
	if err != nil {
		return err
	}
	req := llm.Request{Model: m.Config.Model, Messages: []llm.Message{{Role: "system", Content: prompt + "\nJSON Schema:\n" + schema}, {Role: "user", Content: string(payload)}}, Format: json.RawMessage(schema), Options: llm.Options{NumCtx: m.Config.ContextTokens, NumPredict: 4096}}
	if err = llm.CheckContextBudget(req, m.Config.ContextTokens); err != nil {
		return err
	}
	result, err := m.Model.Call(ctx, req)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(result.Content))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(out); err != nil {
		return fmt.Errorf("%w: invalid memory JSON", ErrInvalid)
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return ErrInvalid
	}
	return nil
}
func (m ModelExtractor) Extract(ctx context.Context, b Batch) (candidates []Candidate, err error) {
	ctx, span := telemetry.Start(ctx, "memory.extract", "CHAIN")
	defer func() { telemetry.End(span, err) }()
	var out struct {
		Candidates []Candidate `json:"candidates"`
	}
	err = m.call(ctx, extractPrompt, candidateSchema, b, &out)
	if err != nil {
		return nil, err
	}
	if out.Candidates == nil {
		return nil, fmt.Errorf("%w: missing_candidates", ErrInvalid)
	}
	sources := map[string]Message{}
	for _, message := range b.Messages {
		sources[message.ID] = message
	}
	candidates = make([]Candidate, 0, len(out.Candidates))
	for _, candidate := range out.Candidates {
		contextOnly := len(candidate.SourceIDs) > 0
		for _, id := range candidate.SourceIDs {
			source, known := sources[id]
			if !known || source.SenderID == "" || source.Seq > b.From {
				contextOnly = false
				break
			}
		}
		// Known historical overlap is context, not a new memory. Unknown or invalid
		// evidence remains in the result so the strict validator still rejects it.
		if !contextOnly {
			candidates = append(candidates, candidate)
		}
	}
	span.SetAttributes(attribute.Int("memory.candidate_count", len(candidates)), attribute.Int("memory.ignored_context_only", len(out.Candidates)-len(candidates)))
	return candidates, nil
}

func (m ModelExtractor) Reconcile(ctx context.Context, c Candidate, related []Document) (Decision, error) {
	var out Decision
	err := m.call(ctx, reconcilePrompt, decisionSchema, map[string]any{"candidate": c, "related": related}, &out)
	return out, err
}
