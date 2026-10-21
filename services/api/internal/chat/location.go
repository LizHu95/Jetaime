package chat

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/location"
	"github.com/jackc/pgx/v5"
)

type ClientContext struct {
	Location *location.Fix `json:"location,omitempty"`
}

// SetLocationConverter is configured at startup, before the store is shared with workers.
func (s *Store) SetLocationConverter(convert location.Converter) { s.convertLocation = convert }

func (s *Store) attachLocation(ctx context.Context, run Run, input *ai.Context) error {
	if input.Request == nil {
		return nil
	}
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT l.location FROM message_client_locations l JOIN messages m ON m.id=l.message_id WHERE m.id=$1 AND m.group_id=$2 AND m.sender_user_id=$3 AND m.seq=$4 AND l.expires_at>now()`, run.TriggerID, run.GroupID, run.RequestedBy, run.ContextEnd).Scan(&raw)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	var fix location.Fix
	input.Request.LocationStatus = "unavailable"
	if json.Unmarshal(raw, &fix) != nil || !fix.Valid(time.Now()) || s.convertLocation == nil {
		return nil
	}
	converted, err := s.convertLocation(ctx, fix)
	if err == nil && converted.Valid(time.Now()) && converted.CoordinateSystem == "gcj02" {
		input.Request.Location = &converted
		input.Request.LocationStatus = "available"
	}
	return nil
}

// CleanLocations bounds retention independently of further messages or AI activity.
func (s *Store) CleanLocations(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		_ = s.pruneLocations(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Store) pruneLocations(ctx context.Context) error {
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := s.pool.Exec(bounded, `DELETE FROM message_client_locations WHERE expires_at<=now()`)
	return err
}
