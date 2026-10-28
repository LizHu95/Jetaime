package llm

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

const compactInstructions = "\nparticipants集中定义消息作者的senderName和senderType；短ID用于原样引用，日期和时区沿用sentAt。"

// compactRequest changes only structured chat data at the provider boundary.
// Business contexts, tool authorization and persisted sources keep their IDs.
func compactRequest(in Request) (Request, map[string]string) {
	if in.compacted {
		return in, nil
	}
	original := in
	in.Messages = append([]Message(nil), in.Messages...)
	payloads := make([]any, len(in.Messages))
	c := &messageCodec{aliases: map[string]string{}, originals: map[string]string{}, reserved: map[string]bool{}}
	for i, m := range in.Messages {
		if m.Role == "system" {
			continue
		}
		var v any
		if decodeCompactJSON(m.Content, &v) == nil {
			payloads[i] = v
			c.reserve(v)
		}
	}
	for _, v := range payloads {
		c.collect(v)
	}
	if len(c.participants) == 0 {
		return in, nil
	}
	for i, v := range payloads {
		if v == nil {
			continue
		}
		c.rewrite(v)
		// Keep identity definitions beside each data payload, outside chat text.
		if root, ok := v.(map[string]any); ok {
			root["participants"] = c.participants
		}
		b, err := json.Marshal(v)
		if err == nil {
			in.Messages[i].Content = string(b)
		}
	}
	for i, m := range in.Messages {
		if m.Role == "system" {
			in.Messages[i].Content += compactInstructions
			break
		}
	}
	before, after := 0, 0
	for i := range in.Messages {
		before += len(original.Messages[i].Content)
		after += len(in.Messages[i].Content)
	}
	if after >= before {
		return original, nil
	}
	in.compacted = true
	return in, c.originals
}

type messageCodec struct {
	aliases, originals map[string]string
	reserved           map[string]bool
	participants       []map[string]any
}

func (c *messageCodec) reserve(v any) {
	switch x := v.(type) {
	case map[string]any:
		for k, v := range x {
			if k == "text" || k == "tools" || k == "inputSchema" {
				continue
			}
			c.reserve(v)
		}
	case []any:
		for _, v := range x {
			c.reserve(v)
		}
	case string:
		c.reserved[x] = true
	}
}

func (c *messageCodec) alias(id, prefix string) string {
	if _, err := uuid.Parse(id); err != nil {
		return id
	}
	if a, ok := c.aliases[id]; ok {
		return a
	}
	for n := 1; ; n++ {
		a := fmt.Sprintf("%s%d", prefix, n)
		if c.reserved[a] {
			continue
		}
		c.reserved[a] = true
		c.aliases[id] = a
		c.originals[a] = id
		return a
	}
}

func chatMessage(m map[string]any) bool {
	_, text := m["text"].(string)
	_, id := m["id"].(string)
	_, sender := m["senderId"].(string)
	_, at := m["sentAt"].(string)
	return text && id && sender && at
}

// Traversal is deterministic so repeated budgets and provider attempts use the
// same aliases. Schemas and arbitrary message text are never traversed.
func walkData(v any, visit func(map[string]any)) {
	switch x := v.(type) {
	case map[string]any:
		visit(x)
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if k != "text" && k != "tools" && k != "inputSchema" && k != "participants" {
				walkData(x[k], visit)
			}
		}
	case []any:
		for _, v := range x {
			walkData(v, visit)
		}
	}
}

func (c *messageCodec) collect(v any) {
	walkData(v, func(m map[string]any) {
		if !chatMessage(m) {
			return
		}
		c.alias(m["id"].(string), "m")
		sender := m["senderId"].(string)
		alias := c.alias(sender, "u")
		for _, p := range c.participants {
			if p["id"] == alias {
				return
			}
		}
		p := map[string]any{"id": alias}
		for _, k := range []string{"senderName", "senderType"} {
			if s, ok := m[k].(string); ok && s != "" {
				p[k] = s
			}
		}
		c.participants = append(c.participants, p)
	})
}

func referenceKey(k string) bool {
	switch k {
	case "messageId", "currentMessageId", "replyToId", "sourceMessageId", "messageIds", "sourceMessageIds", "evidenceMessageIds", "anchorMessageIds", "relatedMessageIds", "senderId", "actorId", "assigneeId":
		return true
	}
	return false
}

func mapReference(v any, lookup map[string]string) any {
	switch x := v.(type) {
	case string:
		if replacement, ok := lookup[x]; ok {
			return replacement
		}
	case []any:
		for i, v := range x {
			x[i] = mapReference(v, lookup)
		}
	}
	return v
}

func (c *messageCodec) rewrite(v any) {
	walkData(v, func(m map[string]any) {
		message := chatMessage(m)
		if message {
			m["id"] = mapReference(m["id"], c.aliases)
			delete(m, "groupId")
			for _, p := range c.participants {
				if p["id"] == mapReference(m["senderId"], c.aliases) {
					for _, k := range []string{"senderName", "senderType"} {
						if m[k] == p[k] {
							delete(m, k)
						}
					}
				}
			}
			if at, err := time.Parse(time.RFC3339Nano, m["sentAt"].(string)); err == nil {
				m["sentAt"] = at.Format(time.RFC3339)
			}
		}
		for k, v := range m {
			if referenceKey(k) {
				m[k] = mapReference(v, c.aliases)
			}
		}
		// Context users already have their identities in participants. Retain users
		// absent from messages, e.g. an assignee, with the same stable identity.
		if users, ok := m["users"].([]any); ok {
			remaining := make([]any, 0, len(users))
			for _, v := range users {
				u, ok := v.(map[string]any)
				if !ok {
					remaining = append(remaining, v)
					continue
				}
				u["id"] = mapReference(u["id"], c.aliases)
				duplicate := false
				for _, p := range c.participants {
					if p["id"] == u["id"] && p["senderName"] == u["nickname"] {
						duplicate = true
						break
					}
				}
				if !duplicate {
					remaining = append(remaining, u)
				}
			}
			if len(remaining) == 0 {
				delete(m, "users")
			} else {
				m["users"] = remaining
			}
		}
	})
}

func restoreReferences(content string, originals map[string]string) string {
	if len(originals) == 0 {
		return content
	}
	var v any
	if decodeCompactJSON(content, &v) != nil {
		return content
	}
	walkData(v, func(m map[string]any) {
		for k, v := range m {
			if referenceKey(k) {
				m[k] = mapReference(v, originals)
			}
		}
	})
	b, err := json.Marshal(v)
	if err != nil {
		return content
	}
	return string(b)
}

// UseNumber preserves int64 tool versions and other numeric payloads exactly.
func decodeCompactJSON(content string, value *any) error {
	if !json.Valid([]byte(content)) {
		return fmt.Errorf("invalid structured content")
	}
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.UseNumber()
	return decoder.Decode(value)
}
