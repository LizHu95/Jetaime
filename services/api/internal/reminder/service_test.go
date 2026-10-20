package reminder

import (
	"reflect"
	"testing"
	"time"
)

func TestPrepareLifecycle(t *testing.T) {
	now := time.Date(2026, 10, 20, 20, 0, 0, 0, zone)
	for _, tc := range []struct {
		name, date, clock, status, reason string
		missing                           []string
	}{
		{"no time", "", "", Draft, "", []string{"date", "time"}},
		{"date only", "2026-10-21", "", Draft, "", []string{"time"}},
		{"clock only", "", "12:00", Draft, "", []string{"date"}},
		{"past", "2026-10-20", "12:00", Draft, "time_in_past", []string{"date", "time"}},
		{"complete", "2026-10-21", "12:00", Pending, "", []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := Reminder{ID: "id", Title: "吃饭", Date: tc.date, Time: tc.clock}
			result, err := prepare(&r, now)
			if err != nil || r.Status != tc.status || result.Reason != tc.reason || !reflect.DeepEqual(result.MissingFields, tc.missing) {
				t.Fatalf("%+v %+v %v", r, result, err)
			}
			if tc.status == Pending && (r.DueAt == nil || r.DueAt.Format(time.RFC3339) != "2026-10-21T12:00:00+08:00") {
				t.Fatal("timezone", r.DueAt)
			}
		})
	}
	for _, r := range []Reminder{{Title: ""}, {Title: "吃饭", Date: "2026-02-30"}, {Title: "吃饭", Time: "25:00"}} {
		if _, err := prepare(&r, now); err == nil {
			t.Fatal("invalid input accepted", r)
		}
	}
}
