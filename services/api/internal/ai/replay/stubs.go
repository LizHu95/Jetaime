package replay

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/LizHu95/Jetaime/services/api/internal/reminder"
	"github.com/google/uuid"
)

// fixtureCaller has no HTTP client or fallback. Missing recordings are errors,
// never empty successful responses and never requests to the live map service.
type fixtureCaller struct {
	fixtures []MapFixture
	misses   int
}

func (f *fixtureCaller) Call(_ context.Context, name string, args any) (json.RawMessage, error) {
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	var value any
	if err = json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	canonical, _ := json.Marshal(value)
	for _, fixture := range f.fixtures {
		var want any
		if json.Unmarshal(fixture.Arguments, &want) != nil {
			continue
		}
		encoded, _ := json.Marshal(want)
		if fixture.Tool == name && string(encoded) == string(canonical) {
			return append(json.RawMessage{}, fixture.Response...), nil
		}
	}
	f.misses++
	return nil, fmt.Errorf("no recorded response for map tool %s", name)
}

// memoryReminders is a limited test double, NOT a second business service.
// Replay reports always label these operations as simulated. No delivery,
// database, worker, existing reminders or cross-case drafts are supported.
type memoryReminders struct {
	group, actor string
	now          time.Time
	saved        *reminder.Reminder
	operations   int
}

func (m *memoryReminders) check(s reminder.Scope) error {
	if s.GroupID != m.group || s.UserID != m.actor {
		return reminder.ErrNotFound
	}
	return nil
}
func (m *memoryReminders) Draft(_ context.Context, s reminder.Scope) (*reminder.Reminder, error) {
	if err := m.check(s); err != nil {
		return nil, err
	}
	if m.saved == nil || m.saved.Status != reminder.Draft {
		return nil, nil
	}
	r := *m.saved
	return &r, nil
}
func (m *memoryReminders) Create(_ context.Context, s reminder.Scope, in reminder.CreateInput, _ reminder.Operation) (reminder.Result, error) {
	if err := m.check(s); err != nil {
		return reminder.Result{}, err
	}
	if m.saved != nil {
		return reminder.Result{}, reminder.ErrConflict
	}
	r := reminder.Reminder{ID: uuid.NewSHA1(uuid.NameSpaceURL, []byte(m.group+m.actor+"replay-reminder")).String(), UserID: s.UserID, Title: in.Title, Date: in.Date, Time: in.Time, Timezone: "Asia/Shanghai", Version: 1, UpdatedAt: m.now}
	return m.save(r, "created")
}
func (m *memoryReminders) Update(_ context.Context, s reminder.Scope, id string, in reminder.UpdateInput, _ reminder.Operation) (reminder.Result, error) {
	if err := m.check(s); err != nil {
		return reminder.Result{}, err
	}
	if m.saved == nil || m.saved.ID != id {
		return reminder.Result{}, reminder.ErrNotFound
	}
	if m.saved.Version != in.Version {
		return reminder.Result{}, reminder.ErrConflict
	}
	r := *m.saved
	r.Version++
	if in.Title != nil {
		r.Title = *in.Title
	}
	if in.Date != nil {
		r.Date = *in.Date
	}
	if in.Time != nil {
		r.Time = *in.Time
	}
	if in.Status == reminder.Cancelled {
		r.Status = reminder.Cancelled
		m.saved = &r
		m.operations++
		return reminder.Result{Status: "cancelled", ReminderID: r.ID, Reminder: &r, MissingFields: []string{}}, nil
	}
	return m.save(r, "updated")
}
func (m *memoryReminders) CancelDraft(ctx context.Context, s reminder.Scope, op reminder.Operation) (reminder.Result, error) {
	r, err := m.Draft(ctx, s)
	if err != nil {
		return reminder.Result{}, err
	}
	if r == nil {
		return reminder.Result{Status: "no_active_draft", MissingFields: []string{}}, nil
	}
	return m.Update(ctx, s, r.ID, reminder.UpdateInput{Version: r.Version, Status: reminder.Cancelled}, op)
}
func (m *memoryReminders) save(r reminder.Reminder, status string) (reminder.Result, error) {
	if strings.TrimSpace(r.Title) == "" || utf8.RuneCountInString(r.Title) > 200 || strings.ContainsRune(r.Title, '\x00') {
		return reminder.Result{}, reminder.ErrInvalid
	}
	if r.Date != "" {
		if _, err := time.Parse("2006-01-02", r.Date); err != nil {
			return reminder.Result{}, reminder.ErrInvalid
		}
	}
	if r.Time != "" {
		if _, err := time.Parse("15:04", r.Time); err != nil {
			return reminder.Result{}, reminder.ErrInvalid
		}
	}
	r.Status = reminder.Draft
	r.DueAt = nil
	result := reminder.Result{Status: "needs_clarification", ReminderID: r.ID, MissingFields: []string{}}
	if r.Date == "" {
		result.MissingFields = append(result.MissingFields, "date")
	}
	if r.Time == "" {
		result.MissingFields = append(result.MissingFields, "time")
	}
	if len(result.MissingFields) == 0 {
		zone := time.FixedZone("Asia/Shanghai", 8*3600)
		due, err := time.ParseInLocation("2006-01-02 15:04", r.Date+" "+r.Time, zone)
		if err != nil {
			return reminder.Result{}, reminder.ErrInvalid
		}
		if !due.After(m.now) {
			result.Reason = "time_in_past"
			result.MissingFields = []string{"date", "time"}
		} else {
			r.Status = reminder.Pending
			r.DueAt = &due
			result.Status = status
		}
	}
	m.saved = &r
	m.operations++
	copy := r
	result.Reminder = &copy
	return result, nil
}
func reminderOperation(scope tools.Scope) (reminder.Operation, error) {
	if scope.Context.Request == nil {
		return reminder.Operation{}, ai.ErrInvalidDecision
	}
	return reminder.Operation{ID: scope.Context.Request.MessageID, SourceMessageID: scope.Context.Request.MessageID}, nil
}
