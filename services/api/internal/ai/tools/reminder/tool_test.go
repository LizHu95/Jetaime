package remindertool

import (
	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"testing"
	"time"
)

func TestDefaultClockCannotBeInvented(t *testing.T) {
	for _, tc := range []struct {
		text, clock string
		want        bool
	}{
		{"明天", "12:00", false}, {"提醒我吃饭", "12:00", false}, {"中午12点", "12:00", true}, {"下午三点半", "15:30", true}, {"10分钟后", "12:10", true}, {"明天晚上", "12:00", false}, {"12:30", "12:30", true},
		{"15:30", "15:00", false}, {"下午三点半", "15:00", false}, {"12:30", "12:00", false}, {"8点", "08:00", false}, {"下午8点", "20:00", true}, {"08:00", "08:00", true}, {"十五分钟后", "12:15", true}, {"半小时后", "12:30", true}, {"下午3点45分", "15:45", true}, {"下午3:30", "15:30", true},
	} {
		at := time.Date(2026, 10, 20, 12, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*3600))
		input := ai.Context{ActorID: "a", Request: &ai.RequestContext{MessageID: "m", SentAt: at}, Messages: []ai.Message{{ID: "m", SenderID: "a", Text: tc.text, SentAt: at}}}
		if got := hasExplicitTime(input, tc.clock); got != tc.want {
			t.Fatalf("%s: %v", tc.text, got)
		}
	}
}
