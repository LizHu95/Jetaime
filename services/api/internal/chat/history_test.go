package chat

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestHistoryInvalidInputBeforeDatabase(t *testing.T) {
	service := NewService(nil, true)
	start := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		group, actor string
		fence        int64
		end          time.Time
	}{
		{"bad", uuid.NewString(), 1, start.Add(time.Hour)},
		{uuid.NewString(), "bad", 1, start.Add(time.Hour)},
		{uuid.NewString(), uuid.NewString(), 0, start.Add(time.Hour)},
		{uuid.NewString(), uuid.NewString(), 1, start},
		{uuid.NewString(), uuid.NewString(), 1, start.Add(MaxHistorySpan + time.Second)},
	} {
		_, err := service.HistoryMessages(context.Background(), tc.group, tc.actor, tc.fence, start, tc.end)
		if !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid range or identity reached database", err)
		}
	}
}

func TestPostgresHistoryRangeAuthorizationAndFence(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	start := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	end := start.Add(MaxHistorySpan)
	var messages []Message
	for _, at := range []time.Time{start.Add(-time.Second), start, end.Add(-time.Second), end, start.Add(time.Hour)} {
		m := Message{ID: uuid.NewString(), Seq: int64(len(messages) + 1)}
		if _, err := f.store.pool.Exec(ctx, `INSERT INTO messages(id,group_id,seq,sender_type,sender_user_id,content,client_message_id,created_at)
 VALUES($1,$2,$3,'user',$4,'范围测试',$5,$6)`, m.ID, f.group, m.Seq, f.users[0].ID, uuid.NewString(), at); err != nil {
			t.Fatal(err)
		}
		messages = append(messages, m)
	}
	got, err := f.service.HistoryMessages(ctx, f.group, f.users[0].ID, messages[3].Seq, start, end)
	if err != nil || len(got) != 2 || got[0].ID != messages[1].ID || got[1].ID != messages[2].ID {
		t.Fatal("time boundaries or sequence fence failed", got, err)
	}
	_, err = f.service.HistoryMessages(ctx, f.group, f.users[2].ID, messages[4].Seq, start, end)
	if !errors.Is(err, ErrNotFound) {
		t.Fatal("nonmember read history", err)
	}
	// Reading exactly one sentinel beyond the threshold is sufficient for overflow.
	if _, err = f.store.pool.Exec(ctx, `INSERT INTO messages(id,group_id,seq,sender_type,sender_user_id,content,client_message_id,created_at)
 SELECT gen_random_uuid(),$1,100+i,'user',$2,'消息',gen_random_uuid(),$3 FROM generate_series(1,1002) AS i`, f.group, f.users[0].ID, start); err != nil {
		t.Fatal(err)
	}
	got, err = f.service.HistoryMessages(ctx, f.group, f.users[0].ID, 2000, start, end)
	if err != nil || len(got) != MaxHistoryMessages+1 {
		t.Fatal("query did not enforce sentinel limit", len(got), err)
	}
}
