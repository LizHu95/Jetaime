// Package notification defines invalidation hints. Durable messages stay in chat.
package notification

const Channel = "jetaime_chat_v1"

type Event struct {
	Type    string `json:"type"`
	GroupID string `json:"groupId,omitempty"`
	ActorID string `json:"actorId,omitempty"`
}
