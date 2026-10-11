package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIsolatedWorkflowUsesRealImplementation(t *testing.T) {
	// This fixture is confined to this protocol test.
	data := []byte(`{"users":[{"id":"lin","nickname":"测试用户"}],"groups":[{"id":"daily","name":"测试群"}],"members":[{"groupId":"daily","userId":"lin"}],"messages":[{"id":"m2","groupId":"daily","senderId":"lin","sentAt":"2026-10-10T10:00:00+08:00","text":"测试消息"}]}`)
	var input request
	if err := json.Unmarshal(data, &input.Fixture); err != nil {
		t.Fatal(err)
	}
	input.GroupID = "daily"
	input.ActorID = "lin"
	input.Kind = "qa"
	input.Question = "测试问题？"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"done": true, "message": map[string]string{"content": `{"status":"answered","answer":"测试消息","sourceMessageIds":["m2"]}`}})
	}))
	defer server.Close()
	for i := 0; i < 2; i++ {
		output, err := execute(context.Background(), input, server.URL, "test", time.Second, 8192)
		if err != nil || output.Error != "" || output.Run.Status != "succeeded" {
			t.Fatalf("%+v %v", output, err)
		}
		for _, step := range output.Run.Steps {
			if step.StartedAt.IsZero() || step.DurationNS < 0 {
				t.Fatal("missing measured timing")
			}
		}
	}
	input.ActorID = "chen"
	output, err := execute(context.Background(), input, server.URL, "test", time.Second, 8192)
	if err != nil || output.Error == "" {
		t.Fatal("unauthorized access not recorded")
	}
}
