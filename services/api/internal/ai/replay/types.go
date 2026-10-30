// Package replay runs the real main Agent against local, immutable message
// snapshots. It never constructs a business database, HTTP server or worker.
package replay

import (
	"encoding/json"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
)

type Message struct {
	ID         string    `json:"id"`
	GroupID    string    `json:"groupId"`
	Seq        int64     `json:"seq"`
	SenderType string    `json:"senderType"`
	SenderID   string    `json:"senderId"`
	SenderName string    `json:"senderName"`
	Content    string    `json:"content"`
	MentionsAI bool      `json:"mentionsAI"`
	ReplyToID  *string   `json:"replyToId"`
	CreatedAt  time.Time `json:"createdAt"`
}

func (m Message) original() ai.Message {
	return ai.Message{ID: m.ID, GroupID: m.GroupID, SenderID: m.SenderID, SenderName: m.SenderName, SenderType: m.SenderType, Text: m.Content, SentAt: m.CreatedAt, ReplyToID: m.ReplyToID}
}

type Suite struct {
	Version        string `json:"version"`
	SnapshotSHA256 string `json:"snapshotSha256"`
	Cases          []Case `json:"cases"`
}

// A case introduces a synthetic question AFTER cutoffSeq. The original snapshot
// remains unchanged, and later real messages (including old AI answers) are hidden.
type Case struct {
	ID                  string       `json:"id"`
	GroupID             string       `json:"groupId"`
	ActorID             string       `json:"actorId"`
	CutoffSeq           int64        `json:"cutoffSeq"`
	At                  time.Time    `json:"at"`
	Timezone            string       `json:"timezone"`
	Question            string       `json:"question"`
	ReplyToID           *string      `json:"replyToId,omitempty"`
	RetrievedMessageIDs []string     `json:"retrievedMessageIds"`
	MapFixtures         []MapFixture `json:"mapFixtures"`
	Reviewed            bool         `json:"reviewed"`
	Expected            Expected     `json:"expected"`
}
type MapFixture struct {
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
	Response  json.RawMessage `json:"response"`
}
type Expected struct {
	Reference         string   `json:"reference,omitempty"`
	AnswerContainsAll []string `json:"answerContainsAll,omitempty"`
	RequiredSourceIDs []string `json:"requiredSourceIds,omitempty"`
	RequiredTools     []string `json:"requiredTools,omitempty"`
	ForbiddenTools    []string `json:"forbiddenTools,omitempty"`
}
type ModelCall struct {
	Model             string `json:"model"`
	InputByteEstimate int    `json:"inputByteEstimate"`
	PromptTokens      *int   `json:"promptTokens"`
	CompletionTokens  *int   `json:"completionTokens"`
	DurationMS        int64  `json:"durationMs"`
	Error             bool   `json:"error"`
}
type Metrics struct {
	ModelCalls                  []ModelCall `json:"modelCalls"`
	FirstInputByteEstimate      int         `json:"firstInputByteEstimate"`
	CumulativeInputByteEstimate int         `json:"cumulativeInputByteEstimate"`
	PromptTokens                *int        `json:"promptTokens"`
	CompletionTokens            *int        `json:"completionTokens"`
	ToolCalls                   []string    `json:"toolCalls"`
	SearchCalls                 int         `json:"searchCalls"`
	DurationMS                  int64       `json:"durationMs"`
}
type Result struct {
	CaseID                      string          `json:"caseId"`
	Repetition                  int             `json:"repetition"`
	Status                      string          `json:"status"`
	Error                       string          `json:"error,omitempty"`
	TraceID                     string          `json:"traceId,omitempty"`
	Answer                      *ai.Answer      `json:"answer,omitempty"`
	Run                         ai.Run          `json:"run"`
	Metrics                     Metrics         `json:"metrics"`
	Scores                      map[string]bool `json:"scores"`
	SimulatedReminderOperations int             `json:"simulatedReminderOperations"`
	MapFixtureMisses            int             `json:"mapFixtureMisses"`
}
