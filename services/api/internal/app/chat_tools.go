package app

import (
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/LizHu95/Jetaime/services/api/internal/chat"
	"github.com/LizHu95/Jetaime/services/api/internal/reminder"
)

// chatExecutionScope adapts tool protocol to backend-owned chat authorization.
func chatExecutionScope(scope tools.Scope) chat.ExecutionScope {
	out := chat.ExecutionScope{GroupID: scope.Context.GroupID, ActorID: scope.Context.ActorID, ContextEndSeq: scope.ContextEndSeq}
	if r := scope.Context.Request; r != nil {
		out.SenderID, out.MessageID, out.JobID, out.JobAttempt = r.SenderID, r.MessageID, r.JobID, r.JobAttempt
	}
	return out
}

func reminderOperation(scope tools.Scope) (reminder.Operation, error) {
	return chat.ReminderOperation(chatExecutionScope(scope))
}
