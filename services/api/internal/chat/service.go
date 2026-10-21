package chat

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/LizHu95/Jetaime/services/api/internal/telemetry"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
)

type Service struct {
	store     *Store
	aiEnabled bool
}

func NewService(store *Store, aiEnabled bool) *Service {
	return &Service{store: store, aiEnabled: aiEnabled}
}

var aiMention = regexp.MustCompile(`(?i)(^|[\s，。！？,:：!?])@AI(\s|$|[，。！？,:：!?]|\p{Han})`)

func questionText(content string) string {
	indices := aiMention.FindStringSubmatchIndex(content)
	if len(indices) >= 4 {
		start := indices[3]
		content = content[:start] + content[start+3:]
	}
	return strings.TrimSpace(content)
}
func validID(id string) bool {
	parsed, err := uuid.Parse(id)
	return err == nil && parsed.String() == id
}
func (s *Service) Send(ctx context.Context, group, actor string, input SendInput) (Message, error) {
	if !validID(group) || !validID(input.ClientID) || !utf8.ValidString(input.Content) || strings.ContainsRune(input.Content, '\x00') || strings.TrimSpace(input.Content) == "" || utf8.RuneCountInString(input.Content) > 4000 {
		return Message{}, ErrInvalid
	}
	if input.ClientContext != nil && input.ClientContext.Location != nil && !input.ClientContext.Location.Valid(time.Now()) {
		return Message{}, ErrInvalid
	}
	input.MentionsAI = input.MentionsAI || aiMention.MatchString(input.Content)
	if input.AgentKind == "" {
		input.AgentKind = "qa"
		if input.MentionsAI {
			input.AgentKind = "auto"
		}
	}
	if input.AgentKind != "auto" && input.AgentKind != "help" && input.AgentKind != "qa" {
		return Message{}, ErrInvalid
	}
	if !input.MentionsAI && input.AgentKind != "qa" {
		return Message{}, ErrInvalid
	}
	if input.MentionsAI && len(input.Content) > 3000 {
		return Message{}, ErrInvalid
	}
	if !input.MentionsAI {
		return s.store.Send(ctx, group, actor, input, s.aiEnabled)
	}
	ctx, span := telemetry.Start(ctx, "chat.ai.enqueue", "CHAIN", attribute.String("session.id", group), attribute.String("user.id", actor), attribute.String("chat.trigger_mode", "reactive"), attribute.String("chat.trigger_source", "mention"), attribute.Bool("chat.trigger_mentions_ai", true))
	telemetry.Content(span, "input.value", input.Content)
	message, err := s.store.Send(ctx, group, actor, input, s.aiEnabled)
	var traceErr error
	if err != nil {
		traceErr = errors.New("enqueue failed")
	}
	telemetry.End(span, traceErr)
	return message, err
}
