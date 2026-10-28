package intervention

import (
	_ "embed"
	"encoding/json"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/google/jsonschema-go/jsonschema"
)

const recognitionVersion = "proactive-v10-compact-decisions"

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

var breakdownTypes = []any{"conflict", "uncertainty", "underspecified_plan", "forgotten_constraint", "goal_drift", "discussion_loop", "participation_imbalance", "risky_commitment", "understanding_mismatch", "support_mismatch", "communication_escalation", "pacing_conflict", "repair_blocked"}
var intentTypes = []any{"answer", "clarify", "suggest", "summarize", "remind", "execute_request"}

func recognitionSchema() (*jsonschema.Resolved, json.RawMessage, error) {
	s, err := jsonschema.For[ai.Recognition](nil)
	if err != nil {
		return nil, nil, err
	}
	// Empty branches omit unused fields; domain validation still checks every
	// supplied source, intent and action before execution.
	s.Required = []string{"shouldIntervene", "reason"}
	s.Properties["breakdowns"].Types = nil
	s.Properties["breakdowns"].Type = "array"
	s.Properties["breakdowns"].Items.Properties["type"].Enum = breakdownTypes
	// Pointer fields are represented using a nullable type by jsonschema.For.
	// Final historical verification is read-only; discovery can still identify
	// explicit execution requests through candidateSchema.
	s.Properties["intent"].Properties["type"].Enum = []any{"answer", "clarify", "suggest", "summarize", "remind"}
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
	s.Required = []string{"action", "reason"}
	s.Properties["topicContext"].Properties["continuity"].Enum = []any{"new_topic", "continuation", "uncertain"}
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
