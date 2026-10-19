package mainagent

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	aicontext "github.com/LizHu95/Jetaime/services/api/internal/ai/context"
	"github.com/LizHu95/Jetaime/services/api/internal/llm"
)

// Explicit opt-in: ordinary tests never consume real inference quota.
func TestRealModelRouterIntents(t *testing.T) {
	path := os.Getenv("JETAIME_ROUTER_MODEL_CONFIG")
	if path == "" {
		t.Skip("set JETAIME_ROUTER_MODEL_CONFIG for real routing acceptance")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("could not read model profiles")
	}
	var profiles struct {
		Models map[string]llm.Config `json:"models"`
	}
	if json.Unmarshal(data, &profiles) != nil {
		t.Fatal("invalid model profiles")
	}
	profile := os.Getenv("JETAIME_ROUTER_MODEL_PROFILE")
	if profile == "" {
		profile = "cloud-qwen"
	}
	config, ok := profiles.Models[profile]
	if !ok {
		t.Fatal("model profile not found")
	}
	model, err := config.New(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := NewAgent(model, config.Model, config.ContextTokens)
	if err != nil {
		t.Fatal(err)
	}
	store, _, _ := setup(t, `{}`)
	input, _ := store.Context("daily", "lin")
	for _, tc := range []struct{ question, kind string }{
		{"邻居鞋子放在门口很臭，有什么除味方法？", "main"},
		{"帮我想想周末怎么玩", "main"},
		{"我们之前说周末几点出发？", "main"},
		{"刚才是谁说预算200元？", "main"},
		{"从群聊里整理一下还没完成的待办", "extract"},
		{"帮我设置明天八点的提醒", "main"},
	} {
		t.Run(tc.kind+"/"+tc.question, func(t *testing.T) {
			decision, err := agent.ExecuteMain(context.Background(), input, tc.question, func(context.Context) (ai.Context, error) { return input, nil })
			if err != nil || decision.Kind != tc.kind {
				t.Fatalf("route=%s expected=%s error=%v", decision.Kind, tc.kind, err)
			}
		})
	}
	t.Run("help-answer", func(t *testing.T) {
		question := "邻居鞋子放在门口很臭，有什么除味方法？"
		run, err := agent.ExecuteMain(context.Background(), input, question, func(context.Context) (ai.Context, error) {
			return aicontext.RecentHelpContext(input, question, config.ContextTokens, false)
		})
		if err != nil || run.Kind != "main" || run.Answer == nil || run.Answer.Status != "answered" {
			t.Fatal("general advice did not produce a validated answer", err)
		}
	})
}
