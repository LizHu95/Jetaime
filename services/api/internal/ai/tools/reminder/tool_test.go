package remindertool

import (
	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"testing"
)

func TestDefaultClockCannotBeInvented(t *testing.T) {
	for _, tc := range []struct {
		text string
		want bool
	}{
		{"明天", false}, {"提醒我吃饭", false}, {"中午12点", true}, {"下午三点半", true}, {"10分钟后", true}, {"明天晚上", false}, {"12:30", true},
	} {
		input := ai.Context{ActorID: "a", Request: &ai.RequestContext{MessageID: "m"}, Messages: []ai.Message{{ID: "m", SenderID: "a", Text: tc.text}}}
		if got := hasExplicitTime(input, "12:00"); got != tc.want {
			t.Fatalf("%s: %v", tc.text, got)
		}
	}
}
