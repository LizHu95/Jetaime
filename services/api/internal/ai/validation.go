package ai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

func DecodeJSON(data []byte, dst any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return fmt.Errorf("expected exactly one JSON value")
	}
	return nil
}
func CheckSources(group string, ids []string, messages map[string]Message, required bool) error {
	if required && len(ids) == 0 {
		return fmt.Errorf("source messages required")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		m, ok := messages[id]
		if !ok || m.GroupID != group || seen[id] {
			return fmt.Errorf("invalid, duplicate or unauthorized source %q", id)
		}
		seen[id] = true
	}
	return nil
}
func ValidateContext(c Context) error {
	if strings.TrimSpace(c.GroupID) == "" || strings.TrimSpace(c.ActorID) == "" {
		return fmt.Errorf("agent requires group and actor")
	}
	users := map[string]bool{}
	for _, u := range c.Users {
		if strings.TrimSpace(u.ID) == "" || strings.TrimSpace(u.Nickname) == "" || users[u.ID] {
			return fmt.Errorf("invalid or duplicate user")
		}
		users[u.ID] = true
	}
	if !users[c.ActorID] {
		return fmt.Errorf("actor is not in the snapshot")
	}
	messages := map[string]Message{}
	for _, m := range c.Messages {
		if m.GroupID != c.GroupID || m.ID == "" || m.SentAt.IsZero() || strings.TrimSpace(m.Text) == "" || !users[m.SenderID] {
			return fmt.Errorf("invalid or unauthorized message %q", m.ID)
		}
		if _, ok := messages[m.ID]; ok {
			return fmt.Errorf("duplicate message %q", m.ID)
		}
		messages[m.ID] = m
	}
	ids, keys := map[string]bool{}, map[string]bool{}
	for _, t := range c.Tasks {
		if t.GroupID != c.GroupID || t.ID == "" || ids[t.ID] || t.ConfirmationKey == "" || keys[t.ConfirmationKey] || strings.TrimSpace(t.Title) == "" || !users[t.AssigneeID] || (t.Status != "pending" && t.Status != "completed") {
			return fmt.Errorf("invalid task %q", t.ID)
		}
		ids[t.ID] = true
		keys[t.ConfirmationKey] = true
		if err := CheckSources(c.GroupID, t.SourceMessageIDs, messages, true); err != nil {
			return err
		}
	}
	return nil
}
