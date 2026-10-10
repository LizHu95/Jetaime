// Package decisions 定义决策能力的业务类型与流程，供 CLI 和后续 HTTP 接口共用。
package decisions

import (
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/memory"
	"github.com/LizHu95/Jetaime/services/api/internal/notes"
)

// Task 表示要完成的任务，与笔记类型独立：餐厅、活动等笔记都可以用于 select。
// 本期只支持 select；一天安排和旅行规划仅预留类型，调用模型前就会被拒绝。
type Task string

const (
	TaskSelect   Task = "select"
	TaskDayPlan  Task = "day_plan"
	TaskTripPlan Task = "trip_plan"
)

const MaxSelectRecommendations = 3

// Conditions 表示经过整理、由用户确认的本次条件。
// 数值使用指针以区分“没有限制”（nil）与“明确限制为零”。
type Conditions struct {
	NoteTypes          []notes.Type        `json:"noteTypes"`
	BudgetMaxCents     *int64              `json:"budgetMaxCents"`     // 全体参与者的总预算，单位为人民币分。
	DurationMaxMinutes *int                `json:"durationMaxMinutes"` // 总耗时上限，单位为分钟，不按人数相乘。
	HardConstraints    []string            `json:"hardConstraints"`    // 必须满足的条件，不能用偏好得分抵消。
	SoftPreferences    []string            `json:"softPreferences"`    // 满足硬约束之后，用于排序和解释的偏好。
	Planning           *PlanningConditions `json:"planning,omitempty"`
}

// Request 表示用户意图；候选笔记由服务端按权限读取，不由客户端指定。
// HTTP 接口应从可信登录身份获得 RequesterID；当前 CLI 使用模拟身份。
type Request struct {
	SpaceID     string     `json:"spaceId"`
	RequesterID string     `json:"requesterId"`
	Task        Task       `json:"task"`
	Query       string     `json:"query"`
	Conditions  Conditions `json:"conditions"`
	SessionID   string     `json:"sessionId,omitempty"` // 内部续轮流程使用；Generate 新建会话时必须为空。
}

// Participant 由空间成员与当前空间获准使用的记忆组装。
// 适用的硬约束必须完整加载，不能因向量检索只返回 Top K 而遗漏。
type Participant struct {
	UserID           string          `json:"userId"`
	HardConstraints  []memory.Memory `json:"hardConstraints"`
	RelevantMemories []memory.Memory `json:"relevantMemories"`
}

// Context 是服务端组装的决策上下文，不是数据库实体。
// Candidates 携带用于事实检查、推荐解释与结果校验的笔记内容。
type Context struct {
	Task          Task                   `json:"task"`
	Query         string                 `json:"query"`
	Conditions    Conditions             `json:"conditions"`
	Participants  []Participant          `json:"participants"`
	Candidates    []notes.Note           `json:"candidates"`
	VerifiedFacts map[string]NumericFact `json:"verifiedFacts,omitempty"`
}

// NumericFact 是事实检查器提供的有来源数值，不包含模型推断。
type NumericFact struct {
	Source             string `json:"source"`
	PerPersonCostCents *int64 `json:"perPersonCostCents"`
	TotalCostCents     *int64 `json:"totalCostCents"`
	People             int    `json:"people"`
	DurationMinutes    *int   `json:"durationMinutes"`
}

// ParticipantMatch 解释一个方案为何适合某位参与者；每位参与者都需要有说明。
type ParticipantMatch struct {
	UserID      string `json:"userId"`
	Explanation string `json:"explanation"`
}

// Option 是一轮决策中的一个备选方案；多个 Option 表示不同选择。
// OptionID 由服务端生成，在当前决策内唯一，供后续反馈引用。
// 内容只能二选一：select 使用 Selection，未来规划使用 Itinerary。
type Option struct {
	OptionID           string             `json:"optionId"`
	Title              string             `json:"title"`
	Selection          *Selection         `json:"selection,omitempty"`
	Itinerary          *Itinerary         `json:"itinerary,omitempty"`
	Reason             string             `json:"reason"`
	ParticipantMatches []ParticipantMatch `json:"participantMatches"`
	Unknowns           []string           `json:"unknowns"`
}

// Selection 是本期的选择结果，引用一条有访问权限的候选笔记。
type Selection struct {
	NoteID string `json:"noteId"`
}

// Outcome 是业务结论：可推荐、没有候选、约束冲突或信息不足。
type Outcome string

const (
	OutcomeRecommended        Outcome = "recommended"
	OutcomeNoCandidates       Outcome = "no_candidates"
	OutcomeConstraintConflict Outcome = "constraint_conflict"
	OutcomeInsufficientInfo   Outcome = "insufficient_information"
)

// Result 保存本轮决策结论；select 最多返回三个备选方案。
// 服务端需校验内容类型、引用 ID 和硬约束；模型或网络故障直接返回 error。
// 未来规划的步骤数量和方案上限由对应流程单独定义。
type Result struct {
	Outcome     Outcome  `json:"outcome"`
	Options     []Option `json:"options"`
	Explanation string   `json:"explanation"`
}

type FeedbackAction string

const (
	FeedbackAdopt       FeedbackAction = "adopt"
	FeedbackReject      FeedbackAction = "reject"
	FeedbackChangeBatch FeedbackAction = "change_batch"
)

// Feedback 是某轮决策上的操作事件，不会直接变成长久记忆。
// 采纳和拒绝必须引用该轮的 OptionID；换一批不指定 OptionID。
// 拒绝只影响当前会话的候选排除，不代表永久不喜欢这条笔记。
type Feedback struct {
	ID         string         `json:"id"` // 同一操作重试应复用 ID，避免重复应用反馈。
	DecisionID string         `json:"decisionId"`
	UserID     string         `json:"userId"`
	Action     FeedbackAction `json:"action"`
	OptionID   string         `json:"optionId,omitempty"`
	Reason     string         `json:"reason,omitempty"`
	CreatedAt  time.Time      `json:"createdAt"`
}

// Decision 保存一轮决策的历史快照，笔记后来变化也不会改写当时的内容。
// 默认不保存全部私密记忆或完整模型提示词。
type Decision struct {
	ID             string       `json:"id"`
	SessionID      string       `json:"sessionId"`
	SpaceID        string       `json:"spaceId"`
	RequesterID    string       `json:"requesterId"`
	Task           Task         `json:"task"`
	ParticipantIDs []string     `json:"participantIds"`
	Query          string       `json:"query"`
	Conditions     Conditions   `json:"conditions"`
	CandidateNotes []notes.Note `json:"candidateNotes"` // 当时有权限、通过类型与排除过滤的完整候选快照。
	Result         Result       `json:"result"`
	Feedback       []Feedback   `json:"feedback"`
	CreatedAt      time.Time    `json:"createdAt"`
}

// Session 管理同一发起人、空间和任务下的临时多轮选择状态。
// 每次换一批都会生成独立 Decision；会话状态不会自动变成长期记忆。
type Session struct {
	ID                 string     `json:"id"`
	SpaceID            string     `json:"spaceId"`
	RequesterID        string     `json:"requesterId"`
	Task               Task       `json:"task"`
	Query              string     `json:"query"`
	Conditions         Conditions `json:"conditions"`
	RecommendedNoteIDs []string   `json:"recommendedNoteIds"`         // 曾推荐过的笔记，换批时优先选择尚未推荐的内容。
	ExcludedNoteIDs    []string   `json:"excludedNoteIds"`            // 本会话已拒绝的笔记，可通过重新采纳解除排除。
	LatestDecisionID   string     `json:"latestDecisionId,omitempty"` // 最新一批，只允许从这一批发起换批。
	AdoptedOption      *OptionRef `json:"adoptedOption,omitempty"`    // 当前采纳，可来自之前的某一批。
	ExpiresAt          time.Time  `json:"expiresAt"`
}

// OptionRef 定位会话跨批次的当前采纳；Decision.AdoptedOptionID 只解读单批历史。
type OptionRef struct {
	DecisionID string `json:"decisionId"`
	OptionID   string `json:"optionId"`
	// 缓存笔记 ID：在旧批次拒绝同一笔记时，也能清除当前采纳。
	NoteID string `json:"noteId"`
}
