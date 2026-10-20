package qa

import (
	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/google/jsonschema-go/jsonschema"
)

type Arguments struct {
	Question string `json:"question" jsonschema:"本次需要回答的具体问题"`
	Context  string `json:"context" jsonschema:"选择general/recent/history/mixed上下文模式"`
}

// Output contains the validated answer and original evidence, without an Agent Run.
type Output struct {
	Answer       ai.Answer        `json:"answer"`
	Basis        string           `json:"basis"`
	Sources      []ai.Message     `json:"sources"`
	HistoryScope *ai.HistoryScope `json:"historyScope,omitempty"`
}

func inputConstraints(s *jsonschema.Schema) {
	s.Properties["question"].MinLength = jsonschema.Ptr(1)
	s.Properties["question"].MaxLength = jsonschema.Ptr(3000)
	s.Properties["context"].Enum = []any{"general", "recent", "history", "mixed"}
}
func outputConstraints(s *jsonschema.Schema) {
	s.Properties["basis"].Enum = []any{"general", "history", "mixed"}
	answer := s.Properties["answer"]
	answer.Properties["status"].Enum = []any{"answered", "insufficient_evidence", "conflicting_evidence"}
	answer.Properties["answer"].MinLength = jsonschema.Ptr(1)
	answer.Properties["answer"].MaxLength = jsonschema.Ptr(30000)
	ids := answer.Properties["sourceMessageIds"]
	ids.Type = "array"
	ids.Types = nil
	ids.UniqueItems = true
}
func present(output Output) tools.Completion {
	return tools.Completion{Answer: &output.Answer, Basis: output.Basis, Sources: output.Sources, HistoryScope: output.HistoryScope}
}
