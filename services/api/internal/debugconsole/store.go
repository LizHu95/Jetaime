package debugconsole

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/google/uuid"
)

type Store struct {
	mu                     sync.RWMutex
	fixture                Fixture
	state                  diskState
	fixturePath, statePath string
}

func OpenStore(fixturePath, statePath string) (*Store, error) {
	s := &Store{fixturePath: fixturePath, statePath: statePath, state: diskState{Messages: []ai.Message{}, Tasks: []ai.Task{}}}
	b, err := os.ReadFile(fixturePath)
	if err != nil {
		return nil, err
	}
	if err = ai.DecodeJSON(b, &s.fixture); err != nil {
		return nil, fmt.Errorf("fixture: %w", err)
	}
	if b, err = os.ReadFile(statePath); err == nil {
		if err = ai.DecodeJSON(b, &s.state); err != nil {
			return nil, fmt.Errorf("state file invalid (not overwritten): %w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err = validate(s.fixture, s.state); err != nil {
		return nil, err
	}
	return s, nil
}
func validate(f Fixture, d diskState) error {
	users := map[string]bool{}
	groups := map[string]bool{}
	members := map[string]bool{}
	messages := map[string]ai.Message{}
	for _, u := range f.Users {
		if strings.TrimSpace(u.ID) == "" || strings.TrimSpace(u.Nickname) == "" || users[u.ID] {
			return fmt.Errorf("invalid or duplicate user")
		}
		users[u.ID] = true
	}
	for _, g := range f.Groups {
		if strings.TrimSpace(g.ID) == "" || strings.TrimSpace(g.Name) == "" || groups[g.ID] {
			return fmt.Errorf("invalid or duplicate group")
		}
		groups[g.ID] = true
	}
	for _, m := range f.Members {
		k := m.GroupID + "\x00" + m.UserID
		if !users[m.UserID] || !groups[m.GroupID] || members[k] {
			return fmt.Errorf("invalid or duplicate membership")
		}
		members[k] = true
	}
	for _, m := range append(append([]ai.Message{}, f.Messages...), d.Messages...) {
		if m.ID == "" || m.SentAt.IsZero() || strings.TrimSpace(m.Text) == "" || !members[m.GroupID+"\x00"+m.SenderID] {
			return fmt.Errorf("invalid message %q", m.ID)
		}
		if _, ok := messages[m.ID]; ok {
			return fmt.Errorf("duplicate message %q", m.ID)
		}
		messages[m.ID] = m
	}
	ids := map[string]bool{}
	keys := map[string]bool{}
	for _, t := range d.Tasks {
		if t.ID == "" || ids[t.ID] || t.ConfirmationKey == "" || keys[t.ConfirmationKey] || strings.TrimSpace(t.Title) == "" || !members[t.GroupID+"\x00"+t.AssigneeID] || (t.Status != "pending" && t.Status != "completed") {
			return fmt.Errorf("invalid task %q", t.ID)
		}
		ids[t.ID] = true
		keys[t.ConfirmationKey] = true
		if err := ai.CheckSources(t.GroupID, t.SourceMessageIDs, messages, true); err != nil {
			return err
		}
	}
	return nil
}
func (s *Store) member(group, actor string) bool {
	for _, m := range s.fixture.Members {
		if m.GroupID == group && m.UserID == actor {
			return true
		}
	}
	return false
}
func (s *Store) contextLocked(group, actor string) (ai.Context, error) {
	if !s.member(group, actor) {
		return ai.Context{}, fmt.Errorf("not a member of this group")
	}
	c := ai.Context{ActorID: actor, GroupID: group, Users: []ai.User{}, Messages: []ai.Message{}, Tasks: []ai.Task{}}
	for _, u := range s.fixture.Users {
		if s.member(group, u.ID) {
			c.Users = append(c.Users, u)
		}
	}
	for _, m := range append(append([]ai.Message{}, s.fixture.Messages...), s.state.Messages...) {
		if m.GroupID == group {
			c.Messages = append(c.Messages, m)
		}
	}
	sort.SliceStable(c.Messages, func(i, j int) bool {
		if c.Messages[i].SentAt.Equal(c.Messages[j].SentAt) {
			return c.Messages[i].ID < c.Messages[j].ID
		}
		return c.Messages[i].SentAt.Before(c.Messages[j].SentAt)
	})
	for _, t := range s.state.Tasks {
		if t.GroupID == group {
			c.Tasks = append(c.Tasks, t)
		}
	}
	return c, nil
}
func (s *Store) Context(group, actor string) (ai.Context, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.contextLocked(group, actor)
}
func (s *Store) Catalog() Fixture {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Fixture{Users: append([]ai.User{}, s.fixture.Users...), Groups: append([]Group{}, s.fixture.Groups...), Members: append([]GroupMember{}, s.fixture.Members...)}
}
func (s *Store) persistLocked(next diskState) error {
	if err := validate(s.fixture, next); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.statePath), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.statePath), ".assistant-state-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err = f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(name, s.statePath); err != nil {
		return err
	}
	s.state = next
	return nil
}
func (s *Store) AppendMessage(group, actor, text string) (ai.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.member(group, actor) {
		return ai.Message{}, fmt.Errorf("not a member of this group")
	}
	text = strings.TrimSpace(text)
	if text == "" || len(text) > 6000 {
		return ai.Message{}, fmt.Errorf("message must be 1–6000 bytes")
	}
	m := ai.Message{ID: "m-" + uuid.NewString(), GroupID: group, SenderID: actor, SentAt: time.Now(), Text: text}
	next := diskState{Messages: append(append([]ai.Message{}, s.state.Messages...), m), Tasks: s.state.Tasks}
	return m, s.persistLocked(next)
}
func (s *Store) Reload() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(s.fixturePath)
	if err != nil {
		return err
	}
	var next Fixture
	if err = ai.DecodeJSON(b, &next); err != nil {
		return err
	}
	if err = validate(next, s.state); err != nil {
		return err
	}
	s.fixture = next
	return nil
}
func (s *Store) FindConfirmation(group, actor, key string) (*ai.Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.member(group, actor) {
		return nil, fmt.Errorf("not a member of this group")
	}
	for _, t := range s.state.Tasks {
		if t.ConfirmationKey == key {
			if t.GroupID != group {
				return nil, fmt.Errorf("confirmation belongs to another group")
			}
			copy := t
			return &copy, nil
		}
	}
	return nil, nil
}
func (s *Store) Confirm(group, actor, key string, suggestion ai.Suggestion, title, assignee string, due *time.Time) (ai.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.member(group, actor) || !s.member(group, assignee) {
		return ai.Task{}, fmt.Errorf("actor and assignee must be group members")
	}
	for _, t := range s.state.Tasks {
		if t.ConfirmationKey == key {
			if t.GroupID != group {
				return ai.Task{}, fmt.Errorf("confirmation belongs to another group")
			}
			return t, nil
		}
	}
	title = strings.TrimSpace(title)
	if title == "" || len(title) > 600 {
		return ai.Task{}, fmt.Errorf("task title must be 1–600 bytes")
	}
	t := ai.Task{ID: "t-" + uuid.NewString(), GroupID: group, Title: title, AssigneeID: assignee, DueAt: due, Status: "pending", SourceMessageIDs: append([]string{}, suggestion.SourceMessageIDs...), ConfirmationKey: key}
	next := diskState{Messages: s.state.Messages, Tasks: append(append([]ai.Task{}, s.state.Tasks...), t)}
	return t, s.persistLocked(next)
}
func (s *Store) Complete(group, actor, id string, done bool) (ai.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.member(group, actor) {
		return ai.Task{}, fmt.Errorf("not a member of this group")
	}
	tasks := append([]ai.Task{}, s.state.Tasks...)
	for i, t := range tasks {
		if t.ID != id {
			continue
		}
		if t.GroupID != group || t.AssigneeID != actor {
			return ai.Task{}, fmt.Errorf("only the assignee may change this task")
		}
		t.Status = "pending"
		if done {
			t.Status = "completed"
		}
		tasks[i] = t
		return t, s.persistLocked(diskState{Messages: s.state.Messages, Tasks: tasks})
	}
	return ai.Task{}, fmt.Errorf("task not found")
}

// ContextFromFixture adapts evaluation data without opening or writing files.
func ContextFromFixture(f Fixture, state json.RawMessage, group, actor string) (ai.Context, error) {
	d := diskState{Messages: []ai.Message{}, Tasks: []ai.Task{}}
	if len(state) > 0 {
		if err := ai.DecodeJSON(state, &d); err != nil {
			return ai.Context{}, err
		}
	}
	if err := validate(f, d); err != nil {
		return ai.Context{}, err
	}
	store := &Store{fixture: f, state: d}
	return store.Context(group, actor)
}
