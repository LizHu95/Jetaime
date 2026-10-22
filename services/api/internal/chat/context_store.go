package chat

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	aicontext "github.com/LizHu95/Jetaime/services/api/internal/ai/context"
)

func (s *Store) AgentContext(ctx context.Context, r Run, budget int, injectors ...aicontext.ContextInjector) (ai.Context, string, error) {
	return s.loadAgentContext(ctx, r, budget, true, injectors...)
}

// ExtractionContext loads tool evidence independently of the persisted request mode.
func (s *Store) ExtractionContext(ctx context.Context, r Run, budget int) (ai.Context, error) {
	input, _, err := s.loadAgentContext(ctx, r, budget, false)
	return input, err
}

func (s *Store) loadAgentContext(ctx context.Context, r Run, budget int, windowed bool, injectors ...aicontext.ContextInjector) (ai.Context, string, error) {
	state, err := s.State(ctx, r.GroupID, r.RequestedBy)
	if err != nil {
		return ai.Context{}, "", err
	}
	input := ai.Context{ActorID: r.RequestedBy, GroupID: r.GroupID, Users: []ai.User{}, Messages: []ai.Message{}, Tasks: []ai.Task{}}
	for _, user := range state.Members {
		input.Users = append(input.Users, ai.User{ID: user.ID, Nickname: user.Nickname})
	}
	input.Users = append(input.Users, ai.User{ID: r.AssistantID, Nickname: "AI · " + state.AssistantName})
	recentLimit := aicontext.DefaultRecentMessages + 1 // Trigger plus bounded background for every question.
	if r.TriggerMode == "proactive" {
		recentLimit = ai.InterventionMessageLimit
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
	var trigger ai.Message
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
		addRecentPlaces(&input, m.AgentResult)
		sender := r.AssistantID
		if m.SenderID != nil {
			sender = *m.SenderID
		}
		input.Messages = append(input.Messages, ai.Message{SenderType: m.SenderType, SenderName: m.SenderName, ReplyToID: m.ReplyToID, ID: m.ID, GroupID: m.GroupID, SenderID: sender, SentAt: m.CreatedAt.In(time.FixedZone("Asia/Shanghai", 8*3600)), Text: m.Content})
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
		input.Request = &ai.RequestContext{MessageID: trigger.ID, SenderID: trigger.SenderID, SentAt: trigger.SentAt, CurrentTime: time.Now().In(time.FixedZone("Asia/Shanghai", 8*3600)), Timezone: "Asia/Shanghai"}
	}
	if windowed {
		if len(input.Messages) == 0 || input.Messages[0].ID != r.TriggerID {
			return input, "", ErrNotFound
		}
		for i, j := 0, len(input.Messages)-1; i < j; i, j = i+1, j-1 {
			input.Messages[i], input.Messages[j] = input.Messages[j], input.Messages[i]
		}
		input.HistoryScope = &ai.HistoryScope{Policy: "default-context-v1", EarlierMessagesOmitted: earlierOmitted}
		if r.TriggerMode == "proactive" {
			input.HistoryScope.Policy = "proactive-recent-30-v1"
			input.Request.JobID = r.ID
			input.Request.JobAttempt = r.Attempt
			input.Request.TriggerMode = "proactive"
			if err := s.attachLocation(ctx, r, &input); err != nil {
				return input, "", err
			}
			// Recognition receives the exact window, without the default recent-message injector.
			// Oversized windows fail closed at the model budget check, rather than silently dropping evidence.
			return input, question, ai.ValidateContext(input)
		}

		request := aicontext.ContextRequest{Snapshot: input, TriggerID: r.TriggerID, Question: question, Tokens: budget, Now: input.Request.CurrentTime, MaxSeq: r.ContextEnd}
		if replyTo != nil {
			reference, e := scanMessage(s.pool.QueryRow(ctx, messageSelect+`JOIN chat_groups g ON g.id=x.group_id WHERE x.group_id=$1 AND x.id=$2 AND x.seq<=$3 AND g.status='active'`, r.GroupID, *replyTo, r.ContextEnd))
			if e != nil {
				return input, "", e
			}
			sender := r.AssistantID
			if reference.SenderID != nil {
				sender = *reference.SenderID
			}
			addRecentPlaces(&input, reference.AgentResult)
			request.Snapshot.RecentPlaces = input.RecentPlaces
			request.References = []ai.Message{{ID: reference.ID, GroupID: reference.GroupID, SenderID: sender, SentAt: reference.CreatedAt.In(time.FixedZone("Asia/Shanghai", 8*3600)), Text: reference.Content}}
		}
		inject := aicontext.ContextInjector(aicontext.InjectDefaultContext)
		if len(injectors) > 0 && injectors[0] != nil {
			inject = injectors[0]
		}
		input, err = inject(ctx, request)
		if err == nil {
			err = aicontext.CheckInitialContext(input, request)
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
		if errors.Is(err, ai.ErrContext) {
			return input, "", ErrContext
		}
		if err != nil {
			return input, "", err
		}
		log.Printf("chat context run=%s policy=%s messages=%d earlier_omitted=%t context_tokens=%d", r.ID, input.HistoryScope.Policy, len(input.Messages), input.HistoryScope.EarlierMessagesOmitted, budget)
	}
	if input.Request != nil {
		input.Request.JobID = r.ID
		input.Request.JobAttempt = r.Attempt
	}
	if windowed {
		if err := s.attachLocation(ctx, r, &input); err != nil {
			return input, "", err
		}
	}
	return input, question, nil
}

// Bounded, newest-first display history; candidate searches never enter this list.
// Rows are already authorized and fenced at the original trigger sequence.
func addRecentPlaces(input *ai.Context, result *ai.Result) {
	if result == nil {
		return
	}
	for _, card := range result.Cards {
		if card.Type != "place" || card.Place == nil || ai.ValidateCards([]ai.Card{card}) != nil {
			continue
		}
		seen := false
		for _, old := range input.RecentPlaces {
			if old.ID == card.ID {
				seen = true
				break
			}
		}
		if !seen && len(input.RecentPlaces) < 20 {
			input.RecentPlaces = append(input.RecentPlaces, ai.RecommendedPlace{ID: card.ID, Name: card.Place.Name})
		}
	}
}
