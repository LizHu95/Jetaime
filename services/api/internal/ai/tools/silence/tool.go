// Package silence lets proactive execution abstain after checking evidence.
package silence

import (
	"context"

	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
)

type Arguments struct {
	Reason string `json:"reason"`
}

func New() *tools.Typed[Arguments, Arguments] {
	return tools.NewTyped(tools.Metadata{Name: "remain_silent", Description: "仅主动介入时可用：查证后无需发言，静默结束，不创建群消息。", Kind: "main", Terminal: true, ReadOnly: true, Idempotent: true},
		func(_ context.Context, scope tools.Scope, args Arguments) (Arguments, error) {
			if scope.Context.Request == nil || scope.Context.Request.TriggerMode != "proactive" {
				return Arguments{}, tools.InvalidArguments("silence_not_allowed", "明确请求需要正常回复。")
			}
			return args, nil
		}, tools.Options[Arguments]{Present: func(Arguments) tools.Completion { return tools.Completion{Silent: true} }})
}
