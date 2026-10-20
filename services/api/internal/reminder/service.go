package reminder

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

type Service struct {
	store *Store
	now   func() time.Time
}

func NewService(store *Store) *Service { return &Service{store: store, now: time.Now} }

func (s *Service) Create(ctx context.Context, scope Scope, input CreateInput, op Operation) (Result, error) {
	return s.store.mutate(ctx, scope, op, func(tx *transaction) (Result, error) {
		// Expired drafts no longer consume the member's active-draft slot.
		if err := tx.expireDraft(ctx); err != nil {
			return Result{}, err
		}
		r := Reminder{ID: uuid.NewString(), UserID: scope.UserID, Title: input.Title, Date: input.Date, Time: input.Time, Timezone: "Asia/Shanghai", Version: 1, UpdatedAt: s.now()}
		result, err := prepare(&r, s.now())
		if err != nil {
			return Result{}, err
		}
		if r.Status == Draft {
			if draft, err := tx.draft(ctx); err != nil {
				return Result{}, err
			} else if draft != nil {
				return Result{}, ErrConflict
			}
		}
		if err = tx.insert(ctx, r, op.SourceMessageID); err != nil {
			return Result{}, err
		}
		return result, nil
	})
}

func (s *Service) Update(ctx context.Context, scope Scope, id string, input UpdateInput, op Operation) (Result, error) {
	if !validID(id) {
		return Result{}, ErrInvalid
	}
	return s.store.mutate(ctx, scope, op, func(tx *transaction) (Result, error) {
		r, err := tx.get(ctx, id)
		if err != nil {
			return Result{}, err
		}
		if r.UserID != scope.UserID {
			return Result{}, ErrNotFound
		}
		if input.Status == Cancelled && r.Status == Cancelled {
			return outcome(r, "cancelled"), nil
		}
		if input.Version != r.Version {
			return Result{}, ErrConflict
		}
		if r.Status == Sent || r.Status == Cancelled {
			return Result{}, ErrConflict
		}
		if input.Status != "" && input.Status != Pending && input.Status != Cancelled {
			return Result{}, ErrInvalid
		}
		if input.Title != nil {
			r.Title = *input.Title
		}
		if input.Date != nil {
			r.Date = *input.Date
		}
		if input.Time != nil {
			r.Time = *input.Time
		}
		previousStatus := r.Status
		var result Result
		if input.Status == Cancelled {
			r.Status = Cancelled
			result = outcome(r, "cancelled")
		} else {
			result, err = prepare(&r, s.now())
			if err != nil {
				return Result{}, err
			}
			// A confirmed reminder cannot become an incomplete draft by accident.
			if (input.Status == Pending || previousStatus == Pending) && r.Status != Pending {
				return Result{}, ErrInvalid
			}
			if r.Status == Pending && previousStatus == Pending {
				result.Status = "updated"
			}
		}
		r.Version++
		r.UpdatedAt = s.now()
		result.Reminder = &r
		if err = tx.save(ctx, r); err != nil {
			return Result{}, err
		}
		return result, nil
	})
}
func (s *Service) Draft(ctx context.Context, scope Scope) (*Reminder, error) {
	return s.store.Draft(ctx, scope)
}
func (s *Service) CancelDraft(ctx context.Context, scope Scope, op Operation) (Result, error) {
	return s.store.mutate(ctx, scope, op, func(tx *transaction) (Result, error) {
		r, err := tx.draft(ctx)
		if err != nil {
			return Result{}, err
		}
		if r == nil {
			return Result{Status: "no_active_draft", MissingFields: []string{}}, nil
		}
		r.Status = Cancelled
		r.Version++
		r.UpdatedAt = s.now()
		if err = tx.save(ctx, *r); err != nil {
			return Result{}, err
		}
		return outcome(*r, "cancelled"), nil
	})
}
func (s *Service) List(ctx context.Context, scope Scope) ([]Reminder, error) {
	return s.store.List(ctx, scope)
}
func (s *Service) Cancel(ctx context.Context, scope Scope, id string) error {
	// The legacy cancel endpoint is idempotent without requiring a version.
	_, err := s.store.mutate(ctx, scope, Operation{ID: uuid.NewString()}, func(tx *transaction) (Result, error) {
		r, err := tx.get(ctx, id)
		if err != nil {
			return Result{}, err
		}
		if r.UserID != scope.UserID {
			return Result{}, ErrNotFound
		}
		if r.Status == Sent {
			return Result{}, ErrNotFound
		}
		if r.Status == Cancelled {
			return outcome(r, "cancelled"), nil
		}
		r.Status = Cancelled
		r.Version++
		r.UpdatedAt = s.now()
		if err = tx.save(ctx, r); err != nil {
			return Result{}, err
		}
		return outcome(r, "cancelled"), nil
	})
	return err
}
func validID(id string) bool { v, err := uuid.Parse(id); return err == nil && v.String() == id }
func outcome(r Reminder, status string) Result {
	return Result{Status: status, ReminderID: r.ID, Reminder: &r, MissingFields: []string{}}
}
func prepare(r *Reminder, now time.Time) (Result, error) {
	if strings.TrimSpace(r.Title) == "" || utf8.RuneCountInString(r.Title) > 200 || strings.ContainsRune(r.Title, '\x00') {
		return Result{}, ErrInvalid
	}
	if r.Date != "" {
		if _, err := time.Parse("2006-01-02", r.Date); err != nil {
			return Result{}, ErrInvalid
		}
	}
	if r.Time != "" {
		if _, err := time.Parse("15:04", r.Time); err != nil {
			return Result{}, ErrInvalid
		}
	}
	r.Status = Draft
	r.DueAt = nil
	result := outcome(*r, "needs_clarification")
	if r.Date == "" {
		result.MissingFields = append(result.MissingFields, "date")
	}
	if r.Time == "" {
		result.MissingFields = append(result.MissingFields, "time")
	}
	if len(result.MissingFields) > 0 {
		return result, nil
	}
	due, err := time.ParseInLocation("2006-01-02 15:04", r.Date+" "+r.Time, zone)
	if err != nil {
		return Result{}, ErrInvalid
	}
	if !due.After(now) {
		result.Reason = "time_in_past"
		result.MissingFields = []string{"date", "time"}
		return result, nil
	}
	r.Status = Pending
	r.DueAt = &due
	return outcome(*r, "created"), nil
}
