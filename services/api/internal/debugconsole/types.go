package debugconsole

import "github.com/LizHu95/Jetaime/services/api/internal/ai"

type Group struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type GroupMember struct {
	GroupID string `json:"groupId"`
	UserID  string `json:"userId"`
}
type Fixture struct {
	Users    []ai.User     `json:"users"`
	Groups   []Group       `json:"groups"`
	Members  []GroupMember `json:"members"`
	Messages []ai.Message  `json:"messages"`
}
type diskState struct {
	Messages []ai.Message `json:"messages"`
	Tasks    []ai.Task    `json:"tasks"`
}
