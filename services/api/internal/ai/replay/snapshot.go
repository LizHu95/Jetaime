package replay

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/qa"
	"github.com/LizHu95/Jetaime/services/api/internal/chat/history"
	"github.com/google/uuid"
)

type Snapshot struct {
	Messages []Message
	SHA256   string
}

func LoadSnapshot(path string) (*Snapshot, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	hash := sha256.New()
	scanner := bufio.NewScanner(io.TeeReader(f, hash))
	scanner.Buffer(make([]byte, 65536), 4<<20)
	s := &Snapshot{}
	ids := map[string]bool{}
	seqs := map[string]map[int64]bool{}
	for scanner.Scan() {
		var m Message
		if err := json.Unmarshal(scanner.Bytes(), &m); err != nil {
			return nil, fmt.Errorf("invalid snapshot JSON at row %d", len(s.Messages)+1)
		}
		if m.ID == "" || m.GroupID == "" || m.SenderID == "" || m.Seq <= 0 || m.CreatedAt.IsZero() || strings.TrimSpace(m.Content) == "" || (m.SenderType != "user" && m.SenderType != "assistant") || ids[m.ID] {
			return nil, fmt.Errorf("invalid or duplicate snapshot row %d", len(s.Messages)+1)
		}
		if seqs[m.GroupID] == nil {
			seqs[m.GroupID] = map[int64]bool{}
		}
		if seqs[m.GroupID][m.Seq] {
			return nil, fmt.Errorf("duplicate group sequence")
		}
		seqs[m.GroupID][m.Seq] = true
		ids[m.ID] = true
		s.Messages = append(s.Messages, m)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(s.Messages) == 0 {
		return nil, fmt.Errorf("empty snapshot")
	}
	s.SHA256 = hex.EncodeToString(hash.Sum(nil))
	sort.Slice(s.Messages, func(i, j int) bool {
		if s.Messages[i].GroupID == s.Messages[j].GroupID {
			return s.Messages[i].Seq < s.Messages[j].Seq
		}
		return s.Messages[i].GroupID < s.Messages[j].GroupID
	})
	return s, nil
}
func LoadSuite(path string, snapshot *Snapshot) (Suite, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Suite{}, err
	}
	var suite Suite
	if err = ai.DecodeJSON(raw, &suite); err != nil {
		return suite, fmt.Errorf("invalid case file: %w", err)
	}
	if suite.Version == "" || suite.SnapshotSHA256 != snapshot.SHA256 || len(suite.Cases) == 0 {
		return suite, fmt.Errorf("suite requires version, nonempty cases and matching snapshotSha256")
	}
	seen := map[string]bool{}
	for _, c := range suite.Cases {
		if seen[c.ID] {
			return suite, fmt.Errorf("duplicate case ID")
		}
		seen[c.ID] = true
		if _, err := snapshot.View(c); err != nil {
			return suite, fmt.Errorf("case %s: %w", c.ID, err)
		}
	}
	return suite, nil
}

// View is per-case state: membership is inferred only from already-visible
// original messages, not future messages. This is NOT production authorization.
type View struct {
	Case     Case
	Messages []Message
	Input    ai.Context
	Fence    int64
}

func (s *Snapshot) View(c Case) (*View, error) {
	if c.ID == "" || c.Question == "" || len(c.Question) > 3000 || c.CutoffSeq <= 0 || c.CutoffSeq >= 1<<62 || c.At.IsZero() {
		return nil, fmt.Errorf("question, identity, positive cutoff and timestamp are required")
	}
	if c.Timezone == "" {
		c.Timezone = "Asia/Shanghai"
	}
	if _, err := time.LoadLocation(c.Timezone); err != nil {
		return nil, fmt.Errorf("invalid timezone")
	}
	v := &View{Case: c, Fence: c.CutoffSeq + 1}
	users := map[string]ai.User{}
	actor := false
	cutoffExists := false
	byID := map[string]Message{}
	for _, m := range s.Messages {
		if m.GroupID != c.GroupID || m.Seq > c.CutoffSeq {
			continue
		}
		if m.CreatedAt.After(c.At) {
			return nil, fmt.Errorf("visible message is after case timestamp")
		}
		v.Messages = append(v.Messages, m)
		byID[m.ID] = m
		if m.Seq == c.CutoffSeq {
			cutoffExists = true
		}
		name := m.SenderName
		if name == "" {
			name = m.SenderID
		}
		users[m.SenderID] = ai.User{ID: m.SenderID, Nickname: name}
		if m.SenderID == c.ActorID && m.SenderType == "user" {
			actor = true
		}
	}
	if !actor || !cutoffExists {
		return nil, fmt.Errorf("actor or cutoff not present in visible group history")
	}
	for _, id := range append(append([]string{}, c.RetrievedMessageIDs...), c.Expected.RequiredSourceIDs...) {
		if _, ok := byID[id]; !ok {
			return nil, fmt.Errorf("retrieval/reference ID is outside visible group history")
		}
	}
	if c.ReplyToID != nil {
		if _, ok := byID[*c.ReplyToID]; !ok {
			return nil, fmt.Errorf("reply target is outside visible history")
		}
	}
	for _, f := range c.MapFixtures {
		if !strings.HasPrefix(f.Tool, "maps_") || !json.Valid(f.Arguments) || !json.Valid(f.Response) {
			return nil, fmt.Errorf("invalid map fixture")
		}
	}
	trigger := Message{ID: uuid.NewSHA1(uuid.NameSpaceURL, []byte("jetaime-eval:"+c.ID)).String(), GroupID: c.GroupID, Seq: v.Fence, SenderType: "user", SenderID: c.ActorID, SenderName: users[c.ActorID].Nickname, Content: c.Question, CreatedAt: c.At, ReplyToID: c.ReplyToID}
	if _, exists := byID[trigger.ID]; exists {
		return nil, fmt.Errorf("synthetic message ID collision")
	}
	v.Messages = append(v.Messages, trigger)
	v.Input = ai.Context{GroupID: c.GroupID, ActorID: c.ActorID, Tasks: []ai.Task{}, Request: &ai.RequestContext{MessageID: trigger.ID, SenderID: c.ActorID, SentAt: c.At, CurrentTime: c.At, Timezone: c.Timezone, TriggerMode: "reactive", AllowWrites: true}}
	for _, u := range users {
		v.Input.Users = append(v.Input.Users, u)
	}
	sort.Slice(v.Input.Users, func(i, j int) bool { return v.Input.Users[i].ID < v.Input.Users[j].ID })
	start := max(0, len(v.Messages)-30)
	for i, m := range v.Messages {
		referenced := c.ReplyToID != nil && m.ID == *c.ReplyToID
		if (i >= start && m.SenderType == "user") || referenced {
			v.Input.Messages = append(v.Input.Messages, m.original())
		}
	}
	if c.ReplyToID != nil {
		v.Input.Request.RelatedMessageIDs = []string{*c.ReplyToID}
	}
	v.Input.HistoryScope = &ai.HistoryScope{Policy: "snapshot-replay-v1", EarlierMessagesOmitted: len(v.Input.Messages) < len(v.Messages)}
	if err := ai.ValidateContext(v.Input); err != nil {
		return nil, err
	}
	return v, nil
}
func (v *View) authorize(group, actor string, fence int64) error {
	if group != v.Case.GroupID || actor != v.Case.ActorID || fence != v.Fence {
		return fmt.Errorf("replay scope mismatch")
	}
	return nil
}
func (v *View) RecentMessages(_ context.Context, group, actor string, fence int64, limit int) ([]history.Message, error) {
	if err := v.authorize(group, actor, fence); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 30 {
		return nil, fmt.Errorf("invalid recent limit")
	}
	out := []history.Message{}
	for _, m := range v.Messages[max(0, len(v.Messages)-limit):] {
		out = append(out, history.Message{ID: m.ID, Seq: m.Seq, SenderID: m.SenderID, SenderName: m.SenderName, SenderType: m.SenderType, SentAt: m.CreatedAt, Text: m.Content})
	}
	return out, nil
}
func (v *View) HistoryMessages(_ context.Context, group, actor string, fence int64, start, end time.Time) ([]ai.Message, error) {
	if err := v.authorize(group, actor, fence); err != nil {
		return nil, err
	}
	out := []ai.Message{}
	for _, m := range v.Messages {
		if !m.CreatedAt.Before(start) && m.CreatedAt.Before(end) {
			out = append(out, m.original())
			if len(out) > 1000 {
				break
			}
		}
	}
	return out, nil
}
func (v *View) QAContext(_ context.Context, r qa.ContextRequest) (ai.Context, error) {
	if err := v.authorize(r.Scope.Context.GroupID, r.Scope.Context.ActorID, r.Scope.ContextEndSeq); err != nil {
		return ai.Context{}, err
	}
	out := r.Scope.Context
	out.Messages = append([]ai.Message{}, out.Messages...)
	known := map[string]bool{}
	for _, m := range out.Messages {
		known[m.ID] = true
	}
	selected := map[string]bool{}
	for _, id := range v.Case.RetrievedMessageIDs {
		selected[id] = true
	}
	for _, m := range v.Messages {
		if selected[m.ID] && !known[m.ID] {
			out.Messages = append(out.Messages, m.original())
			known[m.ID] = true
		}
	}
	sort.SliceStable(out.Messages, func(i, j int) bool { return out.Messages[i].SentAt.Before(out.Messages[j].SentAt) })
	out.HistoryScope = &ai.HistoryScope{Policy: "fixed-retrieval-replay-v1", HistoricalSearch: true, EarlierMessagesOmitted: true, RetrievedMessageIDs: append([]string{}, v.Case.RetrievedMessageIDs...)}
	return out, nil
}
