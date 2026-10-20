// Package reminder manages one-time reminders independently of any AI adapter.
package reminder

import (
	"errors"
	"time"
)

var (
	ErrInvalid  = errors.New("invalid reminder input")
	ErrNotFound = errors.New("reminder or group not found")
	ErrConflict = errors.New("reminder version or operation conflict")
)

const (
	Draft     = "draft"
	Pending   = "pending"
	Sent      = "sent"
	Cancelled = "cancelled"
)

var zone = time.FixedZone("Asia/Shanghai", 8*3600)

type Scope struct {
	GroupID string
	UserID  string
}
type Reminder struct {
	ID        string     `json:"id"`
	UserID    string     `json:"userId"`
	UserName  string     `json:"userName"`
	Title     string     `json:"title"`
	Date      string     `json:"date"`
	Time      string     `json:"time"`
	DueAt     *time.Time `json:"dueAt"`
	Timezone  string     `json:"timezone"`
	Status    string     `json:"status"`
	Version   int64      `json:"version"`
	UpdatedAt time.Time  `json:"updatedAt"`
	SentAt    *time.Time `json:"sentAt,omitempty"`
}
type CreateInput struct {
	Title string `json:"title"`
	Date  string `json:"date"`
	Time  string `json:"time"`
}

// Pointer fields distinguish omitted values from explicitly cleared fields.
type UpdateInput struct {
	Title   *string `json:"title,omitempty"`
	Date    *string `json:"date,omitempty"`
	Time    *string `json:"time,omitempty"`
	Status  string  `json:"status,omitempty"`
	Version int64   `json:"version"`
}
type Result struct {
	Status        string    `json:"status"`
	ReminderID    string    `json:"reminderId,omitempty"`
	Reminder      *Reminder `json:"reminder,omitempty"`
	MissingFields []string  `json:"missingFields"`
	Reason        string    `json:"reason,omitempty"`
	// Only populated when replaying operations migrated from the old format.
	LegacyAnswer string `json:"legacyAnswer,omitempty"`
}
