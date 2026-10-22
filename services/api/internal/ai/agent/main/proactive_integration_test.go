package mainagent

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
)

// Opt-in only; recognition uses real inference but never executes any tools.
func TestRealModelProactiveRecognition(t *testing.T) {
	path := os.Getenv("JETAIME_PROACTIVE_MODEL_CONFIG")
	if path == "" {
		t.Skip("set JETAIME_PROACTIVE_MODEL_CONFIG for real recognition acceptance")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("could not read model profiles")
	}
	var profiles struct {
		DefaultProfile string                `json:"default_profile"`
		Models         map[string]llm.Config `json:"models"`
	}
	if json.Unmarshal(data, &profiles) != nil {
		t.Fatal("invalid model profiles")
	}
	profile := os.Getenv("JETAIME_PROACTIVE_MODEL_PROFILE")
	if profile == "" {
		profile = profiles.DefaultProfile
	}
	config, ok := profiles.Models[profile]
	if !ok {
		t.Fatal("model profile not found")
	}
	model, err := config.New(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	a, err := NewAgent(model, config.Model, config.ContextTokens)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, previous, current string
		intervene               bool
		intent                  string
	}{
		{"explicit reminder request", "周五早上8:00去第四人民医院体检", "帮我创建提醒", true, "execute_request"},
		{"ordinary chat", "今天好冷", "是啊，穿厚一点", false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := ai.RecognitionInput{TriggerMode: "proactive", CurrentMessageID: "11111111-1111-4111-8111-111111111111", Messages: []ai.Message{
				{ID: "22222222-2222-4222-8222-222222222222", SenderID: "alice", SenderName: "测试成员", SenderType: "user", Text: tc.previous},
				{ID: "11111111-1111-4111-8111-111111111111", SenderID: "alice", SenderName: "测试成员", SenderType: "user", Text: tc.current},
			}}
			decision, steps, err := a.recognizeIntent(context.Background(), input)
			if err != nil {
				t.Fatalf("recognition failed: %v", err)
			}
			if decision.ShouldIntervene != tc.intervene || tc.intervene && (decision.Intent == nil || decision.Intent.Type != tc.intent) {
				t.Fatalf("unexpected recognition: %+v", decision)
			}
			t.Logf("model=%s valid=true shouldIntervene=%t attempts=%d", config.Model, decision.ShouldIntervene, len(steps))
		})
	}
}
