package decisions

import "time"

// DateRange uses inclusive local calendar dates in YYYY-MM-DD format.
// A holiday name such as "National Day" must be resolved to explicit dates/year
// and confirmed by the user; it does not specify an official holiday schedule.
type DateRange struct {
	StartDate string `json:"startDate"`
	EndDate   string `json:"endDate"`
}

// TimeWindow is an interval of actual instants serialized with UTC offsets.
// A validator must check start < end and interpret calendar dates in TimeZone.
type TimeWindow struct {
	StartAt time.Time `json:"startAt"`
	EndAt   time.Time `json:"endAt"`
}

type Pace string

const (
	PaceRelaxed  Pace = "relaxed"
	PaceBalanced Pace = "balanced"
	PacePacked   Pace = "packed"
)

// PlanningConditions is reserved for day_plan and trip_plan, absent for select.
// The parent budget applies to the entire plan, not independently to each day.
// Location strings express requested places, never verified coordinates.
type PlanningConditions struct {
	Dates        DateRange   `json:"dates"`
	TimeZone     string      `json:"timeZone"` // IANA name, e.g. Asia/Shanghai.
	Window       *TimeWindow `json:"window,omitempty"`
	Origin       string      `json:"origin,omitempty"`
	Destinations []string    `json:"destinations"`
	Pace         Pace        `json:"pace"`
}

// Itinerary is one complete option containing ordered days and steps, not a
// list of alternative notes. This type does not implement a planning engine.
type Itinerary struct {
	TimeZone           string    `json:"timeZone"`
	Days               []PlanDay `json:"days"`
	EstimatedCostCents *int64    `json:"estimatedCostCents,omitempty"`
}

type PlanDay struct {
	Date  string     `json:"date"` // Local YYYY-MM-DD in the itinerary timezone.
	Items []PlanItem `json:"items"`
}

type PlanItemKind string

const (
	PlanItemNote     PlanItemKind = "note"
	PlanItemBreak    PlanItemKind = "break"
	PlanItemTransfer PlanItemKind = "transfer"
)

// PlanItem is a step within an option. Note steps must reference an authorized
// candidate; breaks/transfers have no NoteID and must not invent merchant facts.
// Nil window/cost means unknown; estimates are not bookings or live routing.
// ItemID is service-assigned and unique within the option, reserved for later
// item-level edits/feedback; initial Feedback continues to target whole options.
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
