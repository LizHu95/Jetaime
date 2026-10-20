package qa

import (
	"context"
	"errors"
	"fmt"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	aicontext "github.com/LizHu95/Jetaime/services/api/internal/ai/context"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/LizHu95/Jetaime/services/api/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// HybridContextInjector retrieves before the first model call, using a backend
// owned sequence fence. The base injector still owns request and reminder context.
func HybridContextInjector(search Searcher, reader MessageReader, base aicontext.ContextInjector) aicontext.ContextInjector {
	if base == nil {
		base = aicontext.InjectDefaultContext
	}
	provider := HistoryContext(search, reader)
	return func(ctx context.Context, request aicontext.ContextRequest) (ai.Context, error) {
		input, err := base(ctx, request)
		if err != nil {
			return input, err
		}
		if request.MaxSeq <= 0 {
			return input, fmt.Errorf("initial history requires a trusted sequence fence")
		}
		ctx, span := telemetry.Start(ctx, "chat.hybrid_retrieval", "RETRIEVER", attribute.String("retrieval.mode", "hybrid"))
		telemetry.Content(span, "input.value", request.Question)
		var failure error
		defer func() { telemetry.End(span, failure) }()
		fail := func(err error) (ai.Context, error) {
			failure = errors.New("hybrid retrieval failed")
			return input, err
		}
		enriched, err := provider(ctx, ContextRequest{Scope: tools.Scope{Context: input, ContextEndSeq: request.MaxSeq, Question: request.Question}, Arguments: Arguments{Question: request.Question}, Tokens: request.Tokens})
		if err != nil {
			return fail(err)
		}
		enriched, err = FitContext(enriched, request.Question, request.Tokens)
		if err != nil {
			return fail(err)
		}
		required := map[string]bool{request.TriggerID: true}
		for _, reference := range request.References {
			required[reference.ID] = true
		}
		// QA and initial routing have different prompt budgets. Keep the trigger and
		// explicit references intact when trimming optional evidence for the latter.
		for {
			err = aicontext.CheckInitialContext(enriched, request)
			if err == nil {
				break
			}
			if !errors.Is(err, ai.ErrContext) {
				return fail(err)
			}
			remove := aicontext.OptionalMessageToDrop(enriched, required)
			if remove < 0 {
				return fail(err)
			}
			enriched.Messages = append(enriched.Messages[:remove], enriched.Messages[remove+1:]...)
		}
		enriched.HistoryScope.Policy = "main-hybrid-v1"
		span.SetAttributes(attribute.Int("retrieval.context_messages", len(enriched.Messages)))
		return enriched, nil
	}
}
