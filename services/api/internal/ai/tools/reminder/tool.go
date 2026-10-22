// Package remindertool adapts LLM calls to the reminder domain API.
package remindertool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	aicontext "github.com/LizHu95/Jetaime/services/api/internal/ai/context"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/LizHu95/Jetaime/services/api/internal/reminder"
)

type API interface {
	Create(context.Context, reminder.Scope, reminder.CreateInput, reminder.Operation) (reminder.Result, error)
	Update(context.Context, reminder.Scope, string, reminder.UpdateInput, reminder.Operation) (reminder.Result, error)
	CancelDraft(context.Context, reminder.Scope, reminder.Operation) (reminder.Result, error)
	Draft(context.Context, reminder.Scope) (*reminder.Reminder, error)
}
type Tool struct {
	*tools.Typed[Arguments, Output]
	Service   API
	Operation func(tools.Scope) (reminder.Operation, error)
}

func New(service API, operation func(tools.Scope) (reminder.Operation, error)) *Tool {
	t := &Tool{Service: service, Operation: operation}
	t.Typed = tools.NewTyped(tools.Metadata{
		Name:        "set_reminder",
		Description: description,
		Version:     "reminder-v5-source-feedback",
		Kind:        "main",
		Terminal:    true,
		Idempotent:  true,
	}, t.Execute, tools.Options[Output]{
		Input:   inputConstraints,
		Output:  outputConstraints,
		Present: present,
	})
	return t
}

func (t *Tool) Execute(ctx context.Context, scope tools.Scope, args Arguments) (Output, error) {
	if scope.Context.Request == nil || t.Operation == nil {
		return Output{}, ai.ErrInvalidDecision
	}
	sources, err := reminderSources(scope.Context, args.SourceMessageIDs)
	if err != nil {
		return Output{}, err
	}
	op, err := t.Operation(scope)
	if err != nil {
		return Output{}, err
	}
	actor := reminder.Scope{GroupID: scope.Context.GroupID, UserID: scope.Context.ActorID}
	var result reminder.Result
	switch args.Action {
	case "save":
		draft := scope.Context.ReminderDraft
		if args.Time != "" && !hasExplicitTime(scope.Context, args.Time, sources...) {
			return Output{}, tools.InvalidArguments("time_evidence_missing", "time 缺少明确的原文依据。请在 sourceMessageIds 引用相关时间原文；用户确实未提供时刻时，time 应传空字符串。")
		}
		if args.ReminderID == "" && draft != nil {
			args.ReminderID = draft.ID
			args.Version = draft.Version
		}
		if args.ReminderID == "" {
			result, err = t.Service.Create(ctx, actor, reminder.CreateInput{Title: args.Title, Date: args.Date, Time: args.Time}, op)
		} else {
			patch := reminder.UpdateInput{Version: args.Version}
			if args.Title != "" {
				patch.Title = &args.Title
			}
			if args.Date != "" {
				patch.Date = &args.Date
			}
			if args.Time != "" {
				patch.Time = &args.Time
			}
			result, err = t.Service.Update(ctx, actor, args.ReminderID, patch, op)
		}
	case "cancel":
		if args.ReminderID == "" {
			result, err = t.Service.CancelDraft(ctx, actor, op)
		} else {
			result, err = t.Service.Update(ctx, actor, args.ReminderID, reminder.UpdateInput{Status: reminder.Cancelled, Version: args.Version}, op)
		}
	case "unsupported":
		result = reminder.Result{Status: "unsupported", MissingFields: []string{}}
	default:
		return Output{}, ai.ErrInvalidDecision
	}
	if err != nil {
		if errors.Is(err, reminder.ErrInvalid) {
			return Output{}, tools.InvalidArguments("invalid_reminder", "提醒标题需为1到200个字符，date需为合法日期，time需为24小时时刻；请修正参数。")
		}
		if errors.Is(err, reminder.ErrConflict) {
			return Output{}, ai.ErrConflict
		}
		if errors.Is(err, reminder.ErrNotFound) {
			return Output{}, ai.ErrNotFound
		}
		return Output{}, err
	}
	return Output{Result: result, SourceMessageIDs: sources}, nil
}
func reply(result reminder.Result) string {
	if result.LegacyAnswer != "" {
		return result.LegacyAnswer
	}
	switch result.Status {
	case "unsupported":
		return "目前支持一次设置一条一次性提醒。请告诉我一件要提醒的事，以及具体日期和时间。"
	case "cancelled":
		return "已取消这次提醒设置。"
	case "no_active_draft":
		return "当前没有待补充的提醒。"
	case "needs_clarification":
		if result.Reason == "time_in_past" {
			return "这个时间已经过去了，请告诉我一个未来的日期和具体时间。"
		}
		if result.Reminder == nil {
			return "请补充提醒的日期和具体时间。"
		}
		r := result.Reminder
		if r.Date == "" {
			if r.Time != "" {
				return fmt.Sprintf("你想哪天 %s 提醒你「%s」？", r.Time, r.Title)
			}
			return "你想哪天、几点提醒你「" + r.Title + "」？"
		}
		return fmt.Sprintf("%s 几点提醒你「%s」？请说明上午或下午。", r.Date, r.Title)
	case "created", "updated":
		if result.Reminder != nil && result.Reminder.DueAt != nil {
			verb := "创建"
			if result.Status == "updated" {
				verb = "更新"
			}
			return fmt.Sprintf("提醒已%s：%s（北京时间）提醒你「%s」。仅提醒一次。", verb, result.Reminder.DueAt.In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("2006年1月2日 15:04"), result.Reminder.Title)
		}
	}
	return "提醒操作已完成。"
}
func (t *Tool) Inject(ctx context.Context, request aicontext.ContextRequest) (ai.Context, error) {
	input, err := aicontext.InjectDefaultContext(ctx, request)
	if err != nil {
		return input, err
	}
	draft, err := t.Service.Draft(ctx, reminder.Scope{GroupID: input.GroupID, UserID: input.ActorID})
	if err == nil && draft != nil {
		input.ReminderDraft = &ai.ReminderDraft{ID: draft.ID, Version: draft.Version, Title: draft.Title, Date: draft.Date, Time: draft.Time}
	}
	return input, err
}

// A model may resolve an explicit time, but cannot invent a default clock time.
// Prior draft times have already passed this check on the original message.
var clockExpression = regexp.MustCompile(`([0-9零〇一二两三四五六七八九十百]+\s*(点|时|[:：][0-9]{2})|([0-9零〇一二两三四五六七八九十百]+|半)\s*(分钟|小时|秒钟|秒)\s*(后|以后)|正午|午夜|零点)`)

func hasExplicitTime(input ai.Context, clock string, sources ...string) bool {
	if input.ReminderDraft != nil && input.ReminderDraft.Time == clock {
		return true
	}
	if input.Request == nil {
		return false
	}
	selected := map[string]bool{input.Request.MessageID: true}
	for _, id := range sources {
		selected[id] = true
	}
	for _, m := range input.Messages {
		if selected[m.ID] && m.SenderID == input.ActorID && m.GroupID == input.GroupID && !m.SentAt.After(input.Request.SentAt) && clockExpression.MatchString(m.Text) {
			return true
		}
	}
	return false
}

// A historical clock is eligible only when the model cites its original message.
// Do not borrow an arbitrary time from unrelated background or another member.
func reminderSources(input ai.Context, cited []string) ([]string, error) {
	if input.Request == nil {
		return nil, ai.ErrInvalidDecision
	}
	eligible := make(map[string]bool, len(input.Messages))
	allowed := []string{}
	for _, m := range input.Messages {
		if m.SenderID == input.ActorID && m.GroupID == input.GroupID && !m.SentAt.After(input.Request.SentAt) {
			eligible[m.ID] = true
			allowed = append(allowed, m.ID)
		}
	}
	invalid := []string{}
	for _, id := range cited {
		if !eligible[id] {
			invalid = append(invalid, id)
		}
	}
	if len(invalid) > 0 {
		rejected, _ := json.Marshal(invalid)
		choices, _ := json.Marshal(allowed)
		return nil, tools.InvalidArguments("invalid_sources", fmt.Sprintf("sourceMessageIds 中这些 ID 不可用：%s。请移除它们，不能引用 AI 助手、其他用户、缺失或晚于当前请求的消息。当前可用的真人消息 ID 为：%s。只选择与本次提醒相关的原文；补全草稿时，已有标题和字段可直接沿用 reminderDraft，无需引用 AI 的追问。", rejected, choices))
	}
	ids := []string{input.Request.MessageID}
	selected := map[string]bool{input.Request.MessageID: true}
	for _, id := range cited {
		if !selected[id] {
			ids = append(ids, id)
			selected[id] = true
		}
	}
	return ids, nil
}
