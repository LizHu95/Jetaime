package chat

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/location"
	"github.com/google/uuid"
)

func TestLocationPrivateBoundedAndSenderScoped(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	fix := location.Fix{Longitude: 121.5, Latitude: 31.3, AccuracyMeters: 50, CapturedAt: time.Now().Add(-time.Minute), CoordinateSystem: "wgs84"}
	f.store.SetLocationConverter(func(_ context.Context, in location.Fix) (location.Fix, error) {
		in.Longitude = 121.504
		in.CoordinateSystem = "gcj02"
		return in, nil
	})
	input := SendInput{Content: "@AI 附近吃什么", ClientID: uuid.NewString(), ClientContext: &ClientContext{Location: &fix}}
	message, err := f.service.Send(ctx, f.group, f.users[0].ID, input)
	if err != nil {
		t.Fatal(err)
	}
	var retainedFromReceipt bool
	if err = f.store.pool.QueryRow(ctx, `SELECT expires_at > now()+interval '4 minutes 30 seconds' FROM message_client_locations WHERE message_id=$1`, message.ID).Scan(&retainedFromReceipt); err != nil || !retainedFromReceipt {
		t.Fatal("storage retention was tied to the position cache age", err)
	}
	runs, err := f.service.ListRuns(ctx, f.group, f.users[0].ID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs: %v %v", runs, err)
	}
	run := runs[0]
	snapshot, _, err := f.store.AgentContext(ctx, run, 32000)
	if err != nil || snapshot.Request.Location == nil || snapshot.Request.Location.Longitude != 121.504 {
		t.Fatalf("location missing: %+v %v", snapshot.Request, err)
	}
	for _, actor := range f.users[:2] {
		page, e := f.service.ListMessages(ctx, f.group, actor.ID, 0, 0, 20)
		if e != nil {
			t.Fatal(e)
		}
		raw, _ := json.Marshal(page)
		if strings.Contains(string(raw), "longitude") || strings.Contains(string(raw), "121.5") {
			t.Fatal("coordinates broadcast in message API")
		}
	}
	// A request constructed for another member cannot load the sender's private position.
	run.RequestedBy = f.users[1].ID
	snapshot, _, err = f.store.AgentContext(ctx, run, 32000)
	if err == nil {
		t.Fatal("another member loaded a private request")
	}
	// New requests never inherit a previous message's location.
	_, err = f.service.Send(ctx, f.group, f.users[1].ID, SendInput{Content: "@AI 怎么去", ClientID: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	runs, err = f.service.ListRuns(ctx, f.group, f.users[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _, err = f.store.AgentContext(ctx, runs[0], 32000)
	if err != nil || snapshot.Request.Location != nil {
		t.Fatal("previous position reused")
	}
	f.store.SetLocationConverter(func(context.Context, location.Fix) (location.Fix, error) {
		return location.Fix{}, errors.New("conversion unavailable")
	})
	run.RequestedBy = f.users[0].ID
	snapshot, _, err = f.store.AgentContext(ctx, run, 32000)
	if err != nil || snapshot.Request.Location != nil || snapshot.Request.LocationStatus != "unavailable" {
		t.Fatal("conversion failure did not degrade safely")
	}
	// Expiry is checked again when the durable job is loaded, not only at ingestion.
	if _, err = f.store.pool.Exec(ctx, `UPDATE message_client_locations SET expires_at=now()-interval '1 second' WHERE message_id=$1`, message.ID); err != nil {
		t.Fatal(err)
	}
	run.RequestedBy = f.users[0].ID
	snapshot, _, err = f.store.AgentContext(ctx, run, 32000)
	if err != nil || snapshot.Request.Location != nil {
		t.Fatal("expired location reused")
	}
	// The cleanup loop also runs once immediately on startup.
	if err = f.store.pruneLocations(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = f.store.pool.QueryRow(ctx, `SELECT count(*) FROM message_client_locations`).Scan(&count); err != nil || count != 0 {
		t.Fatal("expired location retained", err)
	}
}

func TestOrdinaryMessageLocationIsPrivateAndBoundToProactiveTrigger(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if _, err := f.service.UpdateSettings(ctx, f.group, f.users[0].ID, true); err != nil {
		t.Fatal(err)
	}
	f.store.SetLocationConverter(func(_ context.Context, in location.Fix) (location.Fix, error) {
		in.CoordinateSystem = "gcj02"
		return in, nil
	})
	fix := location.Fix{Longitude: 121.5, Latitude: 31.3, AccuracyMeters: 30, CapturedAt: time.Now(), CoordinateSystem: "wgs84"}
	ordinary, err := f.service.Send(ctx, f.group, f.users[0].ID, SendInput{Content: "今天晚饭吃什么", ClientID: uuid.NewString(), ClientContext: &ClientContext{Location: &fix}})
	if err != nil || ordinary.MentionsAI {
		t.Fatal("ordinary message failed", err)
	}
	var count int
	if err = f.store.pool.QueryRow(ctx, `SELECT count(*) FROM message_client_locations WHERE message_id=$1`, ordinary.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("ordinary location not persisted", err)
	}
	raw, _ := json.Marshal(ordinary)
	if strings.Contains(string(raw), "longitude") {
		t.Fatal("private location exposed in message")
	}
	dueProactive(t, f)
	run, err := f.store.Claim(ctx, time.Minute)
	if err != nil || run.TriggerID != ordinary.ID || run.RequestedBy != f.users[0].ID {
		t.Fatal("incorrect proactive trigger", err)
	}
	snapshot, _, err := f.store.AgentContext(ctx, run, 32768)
	if err != nil || snapshot.Request.Location == nil || snapshot.Request.LocationStatus != "available" {
		t.Fatal("trigger location missing", err)
	}
	// A peer's later trigger cannot inherit the first sender's private position.
	peer, err := f.service.Send(ctx, f.group, f.users[1].ID, SendInput{Content: "明天再决定", ClientID: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	dueProactive(t, f)
	next, err := f.store.Claim(ctx, time.Minute)
	if err != nil || next.TriggerID != peer.ID {
		t.Fatal("peer proactive trigger missing", err)
	}
	snapshot, _, err = f.store.AgentContext(ctx, next, 32768)
	if err != nil || snapshot.Request.Location != nil {
		t.Fatal("previous member's position inherited", err)
	}
}
