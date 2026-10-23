package chat

import (
	"context"
	"errors"
	"testing"

	"github.com/LizHu95/Jetaime/services/api/internal/chat/history"
	"github.com/LizHu95/Jetaime/services/api/internal/collaboration"
	"github.com/google/uuid"
)

func TestOriginalMessagesRejectInvalidBoundsBeforeDatabase(t *testing.T) {
	s := NewService(nil, false)
	ctx := context.Background()
	group, actor := uuid.NewString(), uuid.NewString()
	for _, limit := range []int{0, 81} {
		if _, err := s.RecentMessages(ctx, group, actor, 1, limit); !errors.Is(err, ErrInvalid) {
			t.Fatal(err)
		}
	}
	if _, err := s.RecentMessages(ctx, group, actor, 0, 30); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := s.RecentMessages(ctx, "invalid", actor, 1, 30); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := s.SourceMessages(ctx, group, actor, 1, []string{"invalid"}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := s.SourceMessages(ctx, group, actor, 0, nil); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}

func TestOriginalMessagesFenceMembershipAndClosedGroup(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	originals := make([]Message, 0, 8)
	for i := 0; i < 8; i++ {
		message, err := f.service.Send(ctx, f.group, f.users[i%2].ID, SendInput{Content: "original", ClientID: uuid.NewString()})
		if err != nil {
			t.Fatal(err)
		}
		originals = append(originals, message)
	}
	fence := originals[4].Seq
	check := func(got []history.Message, err error, first, last int) {
		t.Helper()
		if err != nil || len(got) != last-first+1 {
			t.Fatalf("messages=%+v err=%v", got, err)
		}
		for i, message := range got {
			expected := originals[first+i]
			if message.ID != expected.ID || message.Seq != expected.Seq || message.Seq > fence || message.SenderType != "user" || message.SenderID != *expected.SenderID {
				t.Fatalf("original changed or escaped fence: %+v", message)
			}
		}
	}
	got, err := f.service.RecentMessages(ctx, f.group, f.users[0].ID, fence, 3)
	check(got, err, 2, 4)
	got, err = f.service.SourceMessages(ctx, f.group, f.users[0].ID, fence, []string{originals[3].ID, originals[3].ID})
	check(got, err, 1, 4)
	friends := collaboration.NewService(collaboration.NewPostgresStore(f.store.pool))
	request, err := friends.Send(ctx, f.users[0].ID, "Carol")
	if err != nil {
		t.Fatal(err)
	}
	request, err = friends.Resolve(ctx, f.users[2].ID, request.ID, "accept")
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := f.service.Send(ctx, *request.GroupID, f.users[0].ID, SendInput{Content: "other group", ClientID: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	for _, ids := range [][]string{{originals[7].ID}, {foreign.ID}, {uuid.NewString()}} {
		got, err = f.service.SourceMessages(ctx, f.group, f.users[0].ID, fence, ids)
		if err != nil || len(got) != 0 {
			t.Fatalf("out-of-scope source returned evidence: %+v %v", got, err)
		}
	}
	if _, err := f.service.RecentMessages(ctx, f.group, f.users[2].ID, fence, 3); !errors.Is(err, ErrNotFound) {
		t.Fatal("nonmember read originals", err)
	}
	if _, err := f.service.SourceMessages(ctx, f.group, f.users[2].ID, fence, nil); !errors.Is(err, ErrNotFound) {
		t.Fatal("empty source read bypassed authorization", err)
	}
	if err := f.service.CloseGroup(ctx, f.group, f.users[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.RecentMessages(ctx, f.group, f.users[0].ID, fence, 3); !errors.Is(err, ErrNotFound) {
		t.Fatal("closed group read", err)
	}
	if _, err := f.service.SourceMessages(ctx, f.group, f.users[0].ID, fence, []string{originals[3].ID}); !errors.Is(err, ErrNotFound) {
		t.Fatal("closed group source read", err)
	}
}
