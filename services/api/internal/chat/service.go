package chat

import (
	"context"
	"errors"
	"github.com/LizHu95/Jetaime/services/api/internal/assistant"
	"github.com/LizHu95/Jetaime/services/api/internal/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/telemetry"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

type Service struct {
	store            *Store
	model            llm.ChatModel
	modelName        string
	tokens           int
	timeout          time.Duration
	contextInjector  assistant.ContextInjector
	mainInstructions assistant.MainInstructionProvider
}

type ServiceOption func(*Service)

func WithMainInstructions(provider assistant.MainInstructionProvider) ServiceOption {
	return func(service *Service) { service.mainInstructions = provider }
}

func WithDefaultContextInjector(inject assistant.ContextInjector) ServiceOption {
	return func(service *Service) {
		if inject != nil {
			service.contextInjector = inject
		}
	}
}

func NewService(store *Store, model llm.ChatModel, name string, tokens int, timeout time.Duration, options ...ServiceOption) *Service {
	service := &Service{store: store, model: model, modelName: name, tokens: tokens, timeout: timeout, contextInjector: assistant.InjectDefaultContext}
	for _, option := range options {
		option(service)
	}
	return service
}

var aiMention = regexp.MustCompile(`(?i)(^|[\s，。！？,:：!?])@AI(\s|$|[，。！？,:：!?]|\p{Han})`)

var extractCommand = regexp.MustCompile(`(?i)^\s*@AI\s+/extract(\s|$)`)

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
	input.MentionsAI = input.MentionsAI || aiMention.MatchString(input.Content)
	if input.AgentKind == "" {
		input.AgentKind = "qa"
		if input.MentionsAI {
			input.AgentKind = "auto"
		}
	}
	if input.AgentKind != "auto" && input.AgentKind != "help" && input.AgentKind != "qa" && input.AgentKind != "extract" {
		return Message{}, ErrInvalid
	}
	if input.MentionsAI && extractCommand.MatchString(input.Content) {
		input.AgentKind = "extract"
	}
	if !input.MentionsAI && input.AgentKind != "qa" {
		return Message{}, ErrInvalid
	}
	if input.MentionsAI && input.AgentKind != "extract" && len(input.Content) > 3000 {
		return Message{}, ErrInvalid
	}
	if !input.MentionsAI {
		return s.store.Send(ctx, group, actor, input, s.model != nil)
	}
	ctx, span := telemetry.Start(ctx, "chat.ai", "CHAIN", attribute.String("session.id", group), attribute.String("user.id", actor))
	telemetry.Content(span, "langfuse.observation.input", input.Content)
	message, err := s.store.Send(ctx, group, actor, input, s.model != nil)
	var traceErr error
	if err != nil {
		traceErr = errors.New("enqueue failed")
	}
	telemetry.End(span, traceErr)
	return message, err
}
