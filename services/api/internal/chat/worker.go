package chat

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/assistant"
	"github.com/google/uuid"
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
func (s *Store) AgentContext(ctx context.Context, r Run, budget int) (assistant.Context, string, error) {
	state, err := s.State(ctx, r.GroupID, r.RequestedBy)
	if err != nil {
		return assistant.Context{}, "", err
	}
	input := assistant.Context{ActorID: r.RequestedBy, GroupID: r.GroupID, Users: []assistant.User{}, Messages: []assistant.Message{}, Tasks: []assistant.Task{}}
	for _, user := range state.Members {
		input.Users = append(input.Users, assistant.User{ID: user.ID, Nickname: user.Nickname})
	}
	input.Users = append(input.Users, assistant.User{ID: r.AssistantID, Nickname: "AI · " + state.AssistantName})
	rows, err := s.pool.Query(ctx, messageSelect+`JOIN chat_groups g ON g.id=x.group_id WHERE x.group_id=$1 AND x.seq<=$2 AND g.status='active' ORDER BY x.seq`, r.GroupID, r.ContextEnd)
	if err != nil {
		return input, "", err
	}
	defer rows.Close()
	bytes := 0
	question := ""
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return input, "", err
		}
		bytes += len(m.Content) + 256
		if bytes > budget {
			return input, "", ErrContext
		}
		sender := r.AssistantID
		if m.SenderID != nil {
			sender = *m.SenderID
		}
		input.Messages = append(input.Messages, assistant.Message{ID: m.ID, GroupID: m.GroupID, SenderID: sender, SentAt: m.CreatedAt.In(time.FixedZone("Asia/Shanghai", 8*3600)), Text: m.Content})
		if m.ID == r.TriggerID {
			question = questionText(m.Content)
		}
	}
	if err := rows.Err(); err != nil {
		return input, "", err
	}
	if question == "" {
		question = "请依据本群对话提供帮助。"
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
		if _, err = tx.Exec(ctx, `INSERT INTO messages(id,group_id,seq,sender_type,sender_assistant_id,content,reply_to_id,agent_kind,agent_result) VALUES($1,$2,$3,'assistant',$4,$5,$6,$7,$8)`, id, run.GroupID, seq, run.AssistantID, answer, run.TriggerID, run.Kind, firstResult(result)); err != nil {
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
	call, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	input, question, err := s.store.AgentContext(call, run, s.tokens)
	code := ""
	answer := ""
	var result *assistant.Result
	if errors.Is(err, ErrContext) {
		code = "context_too_large"
	} else if err != nil {
		code = "context_unavailable"
	} else {
		agent, e := assistant.NewAgent(s.model, s.modelName, s.tokens)
		if e != nil {
			code = "agent_unavailable"
		} else {
			output, e := agent.Execute(call, input, run.Kind, question)
			if e != nil {
				log.Printf("chat agent run=%s kind=%s failed: %v", run.ID, run.Kind, e)
				code = "agent_failed"
				if errors.Is(e, assistant.ErrBudget) {
					code = "context_too_large"
				}
				for _, step := range output.Steps {
					if step.Status == "failed" && step.Name == "model_call" {
						code = "model_failed"
					}
					if step.Status == "failed" && step.Name == "validate_result" {
						code = "invalid_model_response"
					}
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
	if err := s.store.Complete(finish, run, answer, code, result); err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrConflict) {
		log.Printf("chat agent run=%s completion failed", run.ID)
	}
}

func firstResult(results []*assistant.Result) *assistant.Result {
	if len(results) == 0 {
		return nil
	}
	return results[0]
}
