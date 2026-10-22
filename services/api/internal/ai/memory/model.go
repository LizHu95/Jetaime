package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/LizHu95/Jetaime/services/api/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"io"
	"strconv"
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
	schema := extractionSchema(b)
	var input any = b
	for attempt := 0; attempt < 2; attempt++ {
		var out struct {
			Candidates []Candidate `json:"candidates"`
		}
		if err = m.call(ctx, extractPrompt, schema, input, &out); err != nil {
			return nil, err
		}
		if out.Candidates == nil {
			return nil, fmt.Errorf("%w: missing_candidates", ErrInvalid)
		}
		candidates = normalizeExtractionCandidates(b, out.Candidates)
		span.SetAttributes(attribute.Int("memory.candidate_count", len(candidates)), attribute.Int("memory.ignored_context_only", len(out.Candidates)-len(candidates)), attribute.Int("memory.validation_retries", attempt))
		if err = validateCandidates(b, candidates); err == nil {
			return candidates, nil
		}
		// Retry once with the exact validation failure. Never guess a source ID,
		// drop unsupported evidence, or persist a partially validated extraction.
		input = struct {
			Batch      Batch       `json:"batch"`
			Rejected   []Candidate `json:"rejectedCandidates"`
			Error      string      `json:"validationError"`
			Correction string      `json:"correction"`
		}{b, out.Candidates, err.Error(), "上次结果未通过校验。请重新核对batch.messages，返回完整的修正结果。sourceMessageIds必须逐字复制本批次支持结论的用户消息id，不能使用seq、人物ID或自行生成ID；没有真实来源支持的结论应省略。"}
	}
	return nil, err
}

func extractionSchema(b Batch) string {
	ids := make([]string, 0, len(b.Messages))
	for _, message := range b.Messages {
		if message.SenderID != "" {
			ids = append(ids, message.ID)
		}
	}
	if len(ids) == 0 {
		return candidateSchema
	}
	raw, _ := json.Marshal(ids)
	return strings.Replace(candidateSchema, `"items":{"type":"string"}`, `"items":{"type":"string","enum":`+string(raw)+`}`, 1)
}

func normalizeExtractionCandidates(b Batch, input []Candidate) []Candidate {
	sources := map[string]Message{}
	sequenceIDs := map[string]string{}
	for _, message := range b.Messages {
		sources[message.ID] = message
		sequenceIDs[strconv.FormatInt(message.Seq, 10)] = message.ID
	}
	candidates := make([]Candidate, 0, len(input))
	for _, candidate := range input {
		// Some models return the displayed sequence instead of the UUID. Resolve
		// only an exact reference within this batch; never infer missing evidence.
		candidate.SourceIDs = append([]string(nil), candidate.SourceIDs...)
		for i, id := range candidate.SourceIDs {
			if _, known := sources[id]; !known {
				if canonical, found := sequenceIDs[id]; found {
					candidate.SourceIDs[i] = canonical
				}
			}
		}
		contextOnly := len(candidate.SourceIDs) > 0
		for _, id := range candidate.SourceIDs {
			source, known := sources[id]
			if !known || source.SenderID == "" || source.Seq > b.From {
				contextOnly = false
				break
			}
		}
		// Only known historical overlap can be ignored. Invalid evidence must
		// reach validation so the model can correct it or fail safely.
		if !contextOnly {
			candidates = append(candidates, candidate)
		}
	}
	return candidates
}

func (m ModelExtractor) Reconcile(ctx context.Context, c Candidate, related []Document) (Decision, error) {
	var out Decision
	err := m.call(ctx, reconcilePrompt, decisionSchema, map[string]any{"candidate": c, "related": related}, &out)
	return out, err
}
