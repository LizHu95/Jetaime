package decisions

import "time"

// DateRange 使用 YYYY-MM-DD 格式的当地日期，包含开始和结束日。
// “国庆”等称呼需先明确年份和日期并由用户确认，不能直接当作放假日历。
type DateRange struct {
	StartDate string `json:"startDate"`
	EndDate   string `json:"endDate"`
}

// TimeWindow 表示带 UTC 偏移的实际时间区间。
// 未来规划校验需保证开始早于结束，并按 TimeZone 解读当地日期。
type TimeWindow struct {
	StartAt time.Time `json:"startAt"`
	EndAt   time.Time `json:"endAt"`
}

// Pace 表示用户期望的安排节奏：轻松、适中或紧凑。
type Pace string

const (
	PaceRelaxed  Pace = "relaxed"
	PaceBalanced Pace = "balanced"
	PacePacked   Pace = "packed"
)

// PlanningConditions 预留给一天安排与旅行规划，本期 select 不接受该字段。
// Conditions 的预算限制作用于整个计划；地点文本尚不代表核实过的坐标。
type PlanningConditions struct {
	Dates        DateRange   `json:"dates"`
	TimeZone     string      `json:"timeZone"` // IANA 时区名，例如 Asia/Shanghai。
	Window       *TimeWindow `json:"window,omitempty"`
	Origin       string      `json:"origin,omitempty"`
	Destinations []string    `json:"destinations"`
	Pace         Pace        `json:"pace"`
}

// Itinerary 是一个完整方案，内部按日期排列各个步骤。
// 当前仅预留结构，尚未实现规划引擎。
type Itinerary struct {
	TimeZone           string    `json:"timeZone"`
	Days               []PlanDay `json:"days"`
	EstimatedCostCents *int64    `json:"estimatedCostCents,omitempty"`
}

// PlanDay 保存计划中某一天的有序步骤。
type PlanDay struct {
	Date  string     `json:"date"` // 按计划时区解读的当地日期，格式 YYYY-MM-DD。
	Items []PlanItem `json:"items"`
}

type PlanItemKind string

const (
	PlanItemNote     PlanItemKind = "note"
	PlanItemBreak    PlanItemKind = "break"
	PlanItemTransfer PlanItemKind = "transfer"
)

// PlanItem 是方案内的一个步骤：笔记步骤需引用有权限的候选，休息与交通无 NoteID。
// 时间或费用为 nil 表示未知；估算不代表已经预订或获得实时路线。
// ItemID 预留由服务端分配，用于未来步骤级编辑；本期反馈仍针对整个 Option。
type PlanItem struct {
	ItemID             string       `json:"itemId"`
	Kind               PlanItemKind `json:"kind"`
	NoteID             string       `json:"noteId,omitempty"`
	Title              string       `json:"title"`
	Location           string       `json:"location,omitempty"`
	Window             *TimeWindow  `json:"window,omitempty"`
	EstimatedCostCents *int64       `json:"estimatedCostCents,omitempty"`
	Unknowns           []string     `json:"unknowns"`
}
