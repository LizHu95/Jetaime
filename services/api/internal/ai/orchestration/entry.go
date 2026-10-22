// Package orchestration separates user-initiated requests from passive listening.
package orchestration

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
)

// Entry is registered in the runtime in front of the shared main agent.
// The persisted trigger mode "proactive" denotes AI initiative (passive from
// the user's perspective); mentions and request follow-ups bypass recognition.
type Entry struct {
	main       ai.Agent
	recognizer ai.Recognizer
	evidence   ai.InterventionEvidenceProvider
}

func New(main ai.Agent, recognizer ai.Recognizer, providers ...ai.InterventionEvidenceProvider) (*Entry, error) {
	if main == nil || recognizer == nil {
		return nil, fmt.Errorf("main agent and intervention recognizer are required")
	}
	entry := &Entry{main: main, recognizer: recognizer}
	if len(providers) > 0 {
		entry.evidence = providers[0]
	}
	return entry, nil
}

func (e *Entry) ID() string { return e.main.ID() }

func (e *Entry) Invoke(ctx context.Context, in ai.Invocation) (ai.Run, error) {
	if err := ctx.Err(); err != nil {
		return ai.Run{}, err
	}
	if in.TriggerMode != "proactive" {
		return e.main.Invoke(ctx, in)
	}
	return e.invokePassive(ctx, in)
}

func (e *Entry) invokePassive(ctx context.Context, in ai.Invocation) (ai.Run, error) {
	started := time.Now()
	run := ai.Run{Kind: "main", GroupID: in.Context.GroupID, ActorID: in.Context.ActorID, StartedAt: started, Status: "failed"}
	if err := ai.ValidateContext(in.Context); err != nil {
		return run, err
	}
	if in.Context.Request == nil || len(in.Context.Messages) == 0 || len(in.Context.Messages) > ai.InterventionMessageLimit {
		return run, ai.ErrInvalidDecision
	}
	current := in.Context.Messages[len(in.Context.Messages)-1]
	if current.ID != in.Context.Request.MessageID || current.SenderID != in.Context.ActorID || current.SenderType == "assistant" {
		return run, ai.ErrInvalidDecision
	}
	input := ai.RecognitionInput{TriggerMode: "proactive", CurrentMessageID: current.ID, Messages: in.Context.Messages}
	result, err := e.recognizer.Discover(ctx, input)
	run.Model, run.PromptVersion, run.Steps = result.Model, result.PromptVersion, result.Steps
	run.DurationMS = time.Since(started).Milliseconds()
	if err != nil {
		return run, err
	}
	candidate := result.Decision
	originalCount := len(in.Context.Messages)
	if candidate.Action != "skip" {
		if candidate.TopicContext == nil {
			return run, ai.ErrInvalidDecision
		}
		topic := candidate.TopicContext
		selected := map[string]bool{}
		for _, id := range topic.MessageIDs {
			selected[id] = true
		}
		if !selected[current.ID] {
			return run, ai.ErrInvalidDecision
		}
		messages := []ai.Message{}
		for _, message := range in.Context.Messages {
			if selected[message.ID] {
				messages = append(messages, message)
			}
		}
		in.Context.Messages = messages
		input.Messages = messages
		request := *in.Context.Request
		request.RelatedMessageIDs = nil
		for _, id := range in.Context.Request.RelatedMessageIDs {
			if selected[id] {
				request.RelatedMessageIDs = append(request.RelatedMessageIDs, id)
			}
		}
		request.RelatedMessageIDs = append(request.RelatedMessageIDs, topic.AnchorMessageIDs...)
		in.Context.Request = &request
		places := []ai.RecommendedPlace{}
		for _, place := range in.Context.RecentPlaces {
			if selected[place.SourceMessageID] {
				places = append(places, place)
			}
		}
		in.Context.RecentPlaces = places
		if in.Context.HistoryScope != nil {
			history := *in.Context.HistoryScope
			history.EarlierMessagesOmitted = history.EarlierMessagesOmitted || len(messages) < originalCount
			in.Context.HistoryScope = &history
		}
	}
	decision := ai.Recognition{ShouldIntervene: candidate.Action == "intervene", Reason: candidate.Reason, Breakdowns: candidate.Breakdowns, Intent: candidate.Intent}
	if candidate.Action == "verify" {
		var steps []ai.Step
		var extra []ai.Message
		decision, steps, extra, err = e.verify(ctx, in, input, candidate)
		run.Steps = append(run.Steps, steps...)
		run.DurationMS = time.Since(started).Milliseconds()
		if err != nil {
			return run, err
		}
		in.Context.Messages = append(append([]ai.Message{}, in.Context.Messages...), extra...)
		sort.SliceStable(in.Context.Messages, func(i, j int) bool {
			if in.Context.Messages[i].ID == current.ID {
				return false
			}
			if in.Context.Messages[j].ID == current.ID {
				return true
			}
			return in.Context.Messages[i].SentAt.Before(in.Context.Messages[j].SentAt)
		})
	} else if candidate.Action != "skip" && candidate.Action != "intervene" {
		return run, ai.ErrInvalidDecision
	}
	run.Recognition = &decision
	if !decision.ShouldIntervene {
		run.Silent, run.Status = true, "succeeded"
		return run, nil
	}
	if decision.Intent == nil {
		return run, ai.ErrInvalidDecision
	}
	if err := ctx.Err(); err != nil {
		return run, err
	}
	request := *in.Context.Request
	in.Context.Request = &request
	request.TriggerMode = "proactive"
	request.AllowWrites = decision.Intent.Type == "execute_request"
	// Preserve all recognition evidence when the main agent selects its smaller
	// execution context. Copy the request so the recognition snapshot stays intact.
	ids := append(append([]string{}, request.RelatedMessageIDs...), decision.Intent.EvidenceMessageIDs...)
	for _, breakdown := range decision.Breakdowns {
		ids = append(ids, breakdown.EvidenceMessageIDs...)
	}
	request.RelatedMessageIDs = nil
	seen := map[string]bool{}
	for _, id := range ids {
		if !seen[id] {
			request.RelatedMessageIDs = append(request.RelatedMessageIDs, id)
			seen[id] = true
		}
	}
	in.Mode = "main"
	in.Question = "当前话题：" + candidate.TopicContext.Topic + "。主动介入目标：" + decision.Intent.Objective + "。依据对话简短、中立地帮助大家，不展示诊断标签或内部推理，不重复已解决的问题。"
	output, err := e.main.Invoke(ctx, in)
	output.StartedAt = started
	output.DurationMS = time.Since(started).Milliseconds()
	output.Recognition = &decision
	output.Steps = append(run.Steps, output.Steps...)
	return output, err
}

// verify performs exactly one bounded read and at most one final decision stage.
func (e *Entry) verify(ctx context.Context, in ai.Invocation, input ai.RecognitionInput, candidate ai.InterventionCandidate) (ai.Recognition, []ai.Step, []ai.Message, error) {
	silent := ai.Recognition{Reason: "核验能力不可用，无法确认疑点", Breakdowns: []ai.Breakdown{}}
	if candidate.Verification == nil {
		return silent, nil, nil, ai.ErrInvalidDecision
	}
	if err := ctx.Err(); err != nil {
		return silent, nil, nil, err
	}
	if e.evidence == nil || in.ContextEndSeq <= 0 {
		step := ai.Step{Name: "verify_intervention_evidence", StartedAt: time.Now(), Status: "succeeded", Output: map[string]any{"available": false}}
		tools.TraceStep(ctx, step)
		return silent, []ai.Step{step}, nil, nil
	}
	// Supply the provider only the trusted group/actor scope and original window.
	snapshot := ai.Context{GroupID: in.Context.GroupID, ActorID: in.Context.ActorID, Users: append([]ai.User{}, in.Context.Users...), Messages: append([]ai.Message{}, in.Context.Messages...)}
	question := *candidate.Verification
	question.EvidenceMessageIDs = append([]string{}, question.EvidenceMessageIDs...)
	request := ai.InterventionEvidenceRequest{Context: snapshot, ContextEndSeq: in.ContextEndSeq, Question: question}
	started := time.Now()
	lookup, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	evidence, err := e.evidence(lookup, request)
	step := ai.Step{Name: "verify_intervention_evidence", StartedAt: started, DurationMS: time.Since(started).Milliseconds(), Status: "failed", Input: question}
	if err == nil {
		err = lookup.Err()
	}
	var extra []ai.Message
	if err == nil {
		extra, err = checkedEvidence(in.Context, evidence)
	}
	if err != nil {
		step.Error = err.Error()
		tools.TraceStep(ctx, step)
		return silent, []ai.Step{step}, nil, err
	}
	evidence.Messages = extra
	step.Status, step.Output = "succeeded", evidence
	tools.TraceStep(ctx, step)
	steps := []ai.Step{step}
	if len(extra) == 0 {
		silent.Reason = "未找到可补充的历史原文，无法确认疑点"
		return silent, steps, nil, nil
	}
	result, err := e.recognizer.Decide(ctx, ai.VerificationInput{Conversation: input, Candidate: candidate, Evidence: evidence})
	steps = append(steps, result.Steps...)
	return result.Decision, steps, extra, err
}

func checkedEvidence(snapshot ai.Context, evidence ai.InterventionEvidence) ([]ai.Message, error) {
	if len(evidence.Messages) > ai.MaxInterventionEvidenceMessages || evidence.Coverage != "partial" {
		return nil, ai.ErrInvalidDecision
	}
	existing := map[string]ai.Message{}
	for _, message := range snapshot.Messages {
		existing[message.ID] = message
	}
	current := snapshot.Messages[len(snapshot.Messages)-1]
	extra := []ai.Message{}
	seen := map[string]bool{}
	for _, message := range evidence.Messages {
		if seen[message.ID] || message.SentAt.After(current.SentAt) {
			return nil, ai.ErrInvalidDecision
		}
		seen[message.ID] = true
		if original, ok := existing[message.ID]; ok {
			if original.GroupID != message.GroupID || original.SenderID != message.SenderID || original.Text != message.Text || !original.SentAt.Equal(message.SentAt) {
				return nil, ai.ErrInvalidDecision
			}
			continue
		}
		extra = append(extra, message)
	}
	snapshot.Messages = append(append([]ai.Message{}, snapshot.Messages...), extra...)
	if err := ai.ValidateContext(snapshot); err != nil {
		return nil, err
	}
	return extra, nil
}
