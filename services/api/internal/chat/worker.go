package chat

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/assistant"
	"github.com/LizHu95/Jetaime/services/api/internal/telemetry"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
)

// Claim locks the group first, as Send/Read/Close do. A live running lease
// excludes the entire group; expired attempts are recovered before new work.
func (s *Store) Claim(ctx context.Context, lease time.Duration) (Run, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Run{}, err
	}
	defer rollback(tx)
	var group string
	err = tx.QueryRow(ctx, `SELECT g.id::text FROM chat_groups g WHERE g.status='active'
 AND EXISTS(SELECT 1 FROM agent_runs r WHERE r.group_id=g.id AND (r.status='queued' OR (r.status='running' AND r.lease_expires_at<=now())))
 AND NOT EXISTS(SELECT 1 FROM agent_runs r WHERE r.group_id=g.id AND r.status='running' AND r.lease_expires_at>now())
 ORDER BY (SELECT min(context_end_seq) FROM agent_runs r WHERE r.group_id=g.id AND r.status IN ('queued','running'))
 FOR UPDATE OF g SKIP LOCKED LIMIT 1`).Scan(&group)
	if err != nil {
		return Run{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE agent_runs SET status='queued',lease_expires_at=NULL WHERE group_id=$1 AND status='running' AND lease_expires_at<=now()`, group); err != nil {
		return Run{}, err
	}
	var id string
	if err = tx.QueryRow(ctx, `SELECT id::text FROM agent_runs WHERE group_id=$1 AND status='queued' ORDER BY context_end_seq,id LIMIT 1 FOR UPDATE`, group).Scan(&id); err != nil {
		return Run{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE agent_runs SET status='running',attempt_count=attempt_count+1,started_at=now(),lease_expires_at=now()+$2*interval '1 second',error_code=NULL WHERE id=$1`, id, int64(lease/time.Second)); err != nil {
		return Run{}, err
	}
	r, err := scanRun(tx.QueryRow(ctx, runSelect+`WHERE id=$1`, id))
	if err != nil {
		return Run{}, err
	}
	if err = notify(ctx, tx, Event{Type: "ai", GroupID: group}); err != nil {
		return Run{}, err
	}
	return r, tx.Commit(ctx)
}
func (s *Store) AgentContext(ctx context.Context, r Run, budget int, injectors ...assistant.ContextInjector) (assistant.Context, string, error) {
	state, err := s.State(ctx, r.GroupID, r.RequestedBy)
	if err != nil {
		return assistant.Context{}, "", err
	}
	input := assistant.Context{ActorID: r.RequestedBy, GroupID: r.GroupID, Users: []assistant.User{}, Messages: []assistant.Message{}, Tasks: []assistant.Task{}}
	for _, user := range state.Members {
		input.Users = append(input.Users, assistant.User{ID: user.ID, Nickname: user.Nickname})
	}
	input.Users = append(input.Users, assistant.User{ID: r.AssistantID, Nickname: "AI · " + state.AssistantName})
	windowed := r.Kind != "extract"
	recentLimit := 200
	if r.Kind == "auto" {
		recentLimit = assistant.DefaultRecentMessages + 1 // Current request plus recent background.
	}
	if r.Kind == "help" {
		recentLimit = 40
	}
	order := ` ORDER BY x.seq`
	if windowed {
		// Fetch one extra row to detect omitted history. Keep database reads
		// bounded independently of how long the group has been active.
		order = fmt.Sprintf(" ORDER BY x.seq DESC LIMIT %d", recentLimit+1)
	}
	rows, err := s.pool.Query(ctx, messageSelect+`JOIN chat_groups g ON g.id=x.group_id WHERE x.group_id=$1 AND x.seq<=$2 AND g.status='active'`+order, r.GroupID, r.ContextEnd)
	if err != nil {
		return input, "", err
	}
	defer rows.Close()
	bytes := 0
	question := ""
	earlierOmitted := false
	var trigger assistant.Message
	var replyTo *string
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return input, "", err
		}
		if windowed && len(input.Messages) == recentLimit {
			earlierOmitted = true
			break
		}
		bytes += len(m.Content) + 256
		if !windowed && bytes > budget {
			return input, "", ErrContext
		}
		sender := r.AssistantID
		if m.SenderID != nil {
			sender = *m.SenderID
		}
		input.Messages = append(input.Messages, assistant.Message{ID: m.ID, GroupID: m.GroupID, SenderID: sender, SentAt: m.CreatedAt.In(time.FixedZone("Asia/Shanghai", 8*3600)), Text: m.Content})
		if m.ID == r.TriggerID {
			question = questionText(m.Content)
			trigger = input.Messages[len(input.Messages)-1]
			replyTo = m.ReplyToID
		}
	}
	if err := rows.Err(); err != nil {
		return input, "", err
	}
	rows.Close()
	if question == "" {
		question = "请依据本群对话提供帮助。"
	}
	if trigger.ID != "" {
		input.Request = &assistant.RequestContext{MessageID: trigger.ID, SenderID: trigger.SenderID, SentAt: trigger.SentAt, CurrentTime: time.Now().In(time.FixedZone("Asia/Shanghai", 8*3600)), Timezone: "Asia/Shanghai"}
	}
	if windowed {
		if len(input.Messages) == 0 || input.Messages[0].ID != r.TriggerID {
			return input, "", ErrNotFound
		}
		for i, j := 0, len(input.Messages)-1; i < j; i, j = i+1, j-1 {
			input.Messages[i], input.Messages[j] = input.Messages[j], input.Messages[i]
		}
		switch r.Kind {
		case "auto":
			input.HistoryScope = &assistant.HistoryScope{Policy: "default-context-v1", EarlierMessagesOmitted: earlierOmitted}
			request := assistant.ContextRequest{Snapshot: input, TriggerID: r.TriggerID, Question: question, Tokens: budget, Now: input.Request.CurrentTime}
			if replyTo != nil {
				reference, e := scanMessage(s.pool.QueryRow(ctx, messageSelect+`JOIN chat_groups g ON g.id=x.group_id WHERE x.group_id=$1 AND x.id=$2 AND x.seq<=$3 AND g.status='active'`, r.GroupID, *replyTo, r.ContextEnd))
				if e != nil {
					return input, "", e
				}
				sender := r.AssistantID
				if reference.SenderID != nil {
					sender = *reference.SenderID
				}
				request.References = []assistant.Message{{ID: reference.ID, GroupID: reference.GroupID, SenderID: sender, SentAt: reference.CreatedAt.In(time.FixedZone("Asia/Shanghai", 8*3600)), Text: reference.Content}}
			}
			inject := assistant.ContextInjector(assistant.InjectDefaultContext)
			if len(injectors) > 0 && injectors[0] != nil {
				inject = injectors[0]
			}
			input, err = inject(ctx, request)
			if err == nil {
				err = assistant.CheckInitialContext(input, request)
			}
			if err == nil {
				// Keep all future providers behind the same database authorization
				// and original-trigger fence, even if they load additional history.
				ids := make([]string, 0, len(input.Messages))
				for _, message := range input.Messages {
					ids = append(ids, message.ID)
				}
				var authorized int
				err = s.pool.QueryRow(ctx, `SELECT count(*) FROM messages x JOIN chat_groups g ON g.id=x.group_id WHERE x.group_id=$1 AND x.seq<=$2 AND x.id=ANY($3::uuid[]) AND g.status='active'`, r.GroupID, r.ContextEnd, ids).Scan(&authorized)
				if err == nil && authorized != len(ids) {
					err = ErrNotFound
				}
			}
		case "help":
			input, err = assistant.RecentHelpContext(input, question, budget, earlierOmitted)
		default:
			input, err = assistant.RecentQAContext(input, question, budget, earlierOmitted)
		}
		if errors.Is(err, assistant.ErrBudget) {
			return input, "", ErrContext
		}
		if err != nil {
			return input, "", err
		}
		log.Printf("chat context run=%s policy=%s messages=%d earlier_omitted=%t context_tokens=%d", r.ID, input.HistoryScope.Policy, len(input.Messages), input.HistoryScope.EarlierMessagesOmitted, budget)
	}
	return input, question, nil
}

// Complete uses attempt_count as a fencing token. A late worker cannot write
// after recovery, retry, group closure, or a successful earlier completion.
func (s *Store) Complete(ctx context.Context, run Run, answer, code string, result ...*assistant.Result) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err = lockGroup(ctx, tx, run.GroupID, run.RequestedBy); err != nil {
		return err
	}
	var valid bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs WHERE id=$1 AND status='running' AND attempt_count=$2 AND lease_expires_at>now())`, run.ID, run.Attempt).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return ErrConflict
	}
	var responseID *string
	status := "failed"
	if code == "" {
		status = "succeeded"
		id := uuid.NewString()
		responseID = &id
		var seq int64
		if err = tx.QueryRow(ctx, `UPDATE chat_groups SET last_message_seq=last_message_seq+1,last_message_at=now() WHERE id=$1 RETURNING last_message_seq`, run.GroupID).Scan(&seq); err != nil {
			return err
		}
		responseKind := run.Kind
		if value := firstResult(result); value != nil {
			responseKind = value.Kind
		}
		if _, err = tx.Exec(ctx, `INSERT INTO messages(id,group_id,seq,sender_type,sender_assistant_id,content,reply_to_id,agent_kind,agent_result) VALUES($1,$2,$3,'assistant',$4,$5,$6,$7,$8)`, id, run.GroupID, seq, run.AssistantID, answer, run.TriggerID, responseKind, firstResult(result)); err != nil {
			return err
		}
	}
	var errorCode *string
	if code != "" {
		errorCode = &code
	}
	if _, err = tx.Exec(ctx, `UPDATE agent_runs SET status=$2,response_message_id=$3,error_code=$4,finished_at=now(),lease_expires_at=NULL WHERE id=$1`, run.ID, status, responseID, errorCode); err != nil {
		return err
	}
	eventType := "ai"
	if responseID != nil {
		eventType = "message"
	}
	if err = notify(ctx, tx, Event{Type: eventType, GroupID: run.GroupID}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Service) Work(ctx context.Context) {
	if s.model == nil {
		return
	}
	for ctx.Err() == nil {
		run, err := s.store.Claim(ctx, s.timeout+30*time.Second)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(500 * time.Millisecond):
			}
			continue
		}
		s.execute(ctx, run)
	}
}
func (s *Service) execute(ctx context.Context, run Run) {
	ctx = telemetry.Restore(ctx, run.TraceParent, run.TraceState)
	ctx, span := telemetry.Start(ctx, "chat.ai.execute", "CHAIN", attribute.String("langfuse.trace.name", "chat.ai"), attribute.String("user.id", run.RequestedBy), attribute.String("chat.run_id", run.ID), attribute.String("session.id", run.GroupID), attribute.String("chat.trigger_message_id", run.TriggerID), attribute.Int("chat.attempt", run.Attempt))
	if !run.QueuedAt.IsZero() {
		span.SetAttributes(attribute.Int64("langfuse.observation.metadata.queue_wait_ms", time.Since(run.QueuedAt).Milliseconds()))
	}
	var traceErr error
	defer func() { telemetry.End(span, traceErr) }()
	call, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	contextCall, contextSpan := telemetry.Start(call, "chat.build_context", "CHAIN")
	input, question, err := s.store.AgentContext(contextCall, run, s.tokens, s.contextInjector)
	var contextErr error
	if err != nil {
		contextErr = errors.New("context unavailable")
	}
	telemetry.End(contextSpan, contextErr)
	code := ""
	answer := ""
	var result *assistant.Result
	if errors.Is(err, ErrContext) {
		code = "context_too_large"
	} else if err != nil {
		code = "context_unavailable"
	} else {
		agent, e := assistant.NewAgent(s.model, s.modelName, s.tokens, assistant.WithMainInstructions(s.mainInstructions))
		if e != nil {
			code = "agent_unavailable"
		} else {
			var output assistant.Run
			if run.Kind == "auto" {
				output, e = agent.ExecuteMain(call, input, question, func(ctx context.Context) (assistant.Context, error) {
					childRun := run
					childRun.Kind = "extract"
					childInput, _, err := s.store.AgentContext(ctx, childRun, s.tokens)
					return childInput, err
				})
			} else {
				output, e = agent.Execute(call, input, run.Kind, question)
			}
			if e != nil {
				log.Printf("chat agent run=%s kind=%s failed: %v", run.ID, run.Kind, e)
				code = "agent_failed"
				for _, step := range output.Steps {
					if step.Status == "failed" && step.Name == "model_call" {
						code = "model_failed"
					}
					if step.Status == "failed" && step.Name == "validate_result" {
						code = "invalid_model_response"
					}
				}
				if errors.Is(e, assistant.ErrMainDecision) {
					code = "invalid_model_response"
				}
				if errors.Is(e, assistant.ErrBudget) || errors.Is(e, ErrContext) {
					code = "context_too_large"
				}
			} else {
				value, text, e := assistant.ResultFromRun(output)
				if e != nil {
					code = "invalid_model_response"
				} else {
					result = &value
					answer = text
				}
			}
		}
	}
	// Completion must outlive the provider timeout, but not an entire server
	// shutdown. Persist failures with a separate bounded context for recovery.
	finish, cancelFinish := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancelFinish()
	telemetry.Content(span, "langfuse.observation.output", answer)
	if code != "" {
		traceErr = errors.New(code)
	}
	finish, persistSpan := telemetry.Start(finish, "chat.save_reply", "CHAIN")
	persistErr := s.store.Complete(finish, run, answer, code, result)
	var saveErr error
	if persistErr != nil {
		saveErr = errors.New("completion failed")
		traceErr = saveErr
		if !errors.Is(persistErr, ErrNotFound) && !errors.Is(persistErr, ErrConflict) {
			log.Printf("chat agent run=%s completion failed", run.ID)
		}
	}
	telemetry.End(persistSpan, saveErr)
	log.Printf("chat agent run=%s trace_id=%s status=%s", run.ID, span.SpanContext().TraceID(), runStatus(code, persistErr))
}

func firstResult(results []*assistant.Result) *assistant.Result {
	if len(results) == 0 {
		return nil
	}
	return results[0]
}

func runStatus(code string, err error) string {
	if code != "" || err != nil {
		return "failed"
	}
	return "succeeded"
}
