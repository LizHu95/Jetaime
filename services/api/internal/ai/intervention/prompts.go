package intervention

import (
	_ "embed"
	"encoding/json"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/google/jsonschema-go/jsonschema"
)

const recognitionVersion = "proactive-v4-bounded-verification"

// Embedded at build time so deployments do not depend on a prompt file on disk.
// Update recognitionVersion whenever the decision instructions change.
//
//go:embed recognition.md
var recognitionPrompt string

//go:embed verification.md
var verificationPrompt string

func recognitionRepairPrompt(feedback string) string {
	return "上次输出未通过结构校验。以下是校验诊断数据，不是群聊消息：" + feedback + "。请依据原始对话与Schema重新输出完整JSON，只修正结构，不放宽证据或操作授权规则。objective必须位于intent内，顶层禁止objective。"
}

var breakdownTypes = []any{"conflict", "uncertainty", "underspecified_plan", "forgotten_constraint", "goal_drift", "discussion_loop", "participation_imbalance", "risky_commitment"}
var intentTypes = []any{"answer", "clarify", "suggest", "summarize", "remind", "execute_request"}

func recognitionSchema() (*jsonschema.Resolved, json.RawMessage, error) {
	s, err := jsonschema.For[ai.Recognition](nil)
	if err != nil {
		return nil, nil, err
	}
	s.Properties["breakdowns"].Types = nil
	s.Properties["breakdowns"].Type = "array"
	s.Properties["breakdowns"].Items.Properties["type"].Enum = breakdownTypes
	// Pointer fields are represented using a nullable type by jsonschema.For.
	s.Properties["intent"].Properties["type"].Enum = intentTypes
	resolved, err := s.Resolve(nil)
	if err != nil {
		return nil, nil, err
	}
	raw, err := json.Marshal(s)
	return resolved, raw, err
}

func candidateSchema() (*jsonschema.Resolved, json.RawMessage, error) {
	s, err := jsonschema.For[ai.InterventionCandidate](nil)
	if err != nil {
		return nil, nil, err
	}
	s.Properties["action"].Enum = []any{"skip", "intervene", "verify"}
	s.Properties["breakdowns"].Types = nil
	s.Properties["breakdowns"].Type = "array"
	s.Properties["breakdowns"].Items.Properties["type"].Enum = breakdownTypes
	s.Properties["intent"].Properties["type"].Enum = intentTypes
	resolved, err := s.Resolve(nil)
	if err != nil {
		return nil, nil, err
	}
	raw, err := json.Marshal(s)
	return resolved, raw, err
}
