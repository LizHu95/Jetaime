package intervention

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
	a, err := New(model, config.Model, config.ContextTokens)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, previous, current string
		action                  string
		intent                  string
	}{
		{"explicit reminder request", "周五早上8:00去第四人民医院体检", "帮我创建提醒", "intervene", "execute_request"},
		{"ordinary chat", "今天好冷", "是啊，穿厚一点", "skip", ""},
		{"no visible budget baseline", "这家餐厅人均500元", "看起来不错，先放进候选吧", "skip", ""},
		{"constraint already resolved", "这次聚餐人均预算300元，这家要500元，超预算了", "那就排除这家，找300以内的，我正在查", "skip", ""},
		{"constraint explicitly revised", "这次聚餐原定人均300元，不过大家已同意改成500元", "那这家500元的可以放进候选，我再比较一下", "skip", ""},
		{"different activity constraint", "上次公司聚餐人均预算300元，这次朋友生日的预算还没定", "我先看看人均500元的餐厅，再跟朋友商量", "skip", ""},
		{"no participation evidence", "周末一起散步吧", "我看看周末天气再定", "skip", ""},
		{"member request is not AI authorization", "我明天需要去医院", "我自己在手机上设个提醒，先设8点", "skip", ""},
		{"verify previous agreement", "这次周末聚餐的预算按之前商量的来", "准备订人均500元这家，就照之前的预算安排吧", "verify", ""},
		{"visible unresolved constraint", "这次聚餐大家已经确认人均最多300元，预算不变", "我准备直接订人均500元这家，就按原预算安排", "intervene", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := ai.RecognitionInput{TriggerMode: "proactive", CurrentMessageID: "11111111-1111-4111-8111-111111111111", Messages: []ai.Message{
				{ID: "22222222-2222-4222-8222-222222222222", SenderID: "alice", SenderName: "测试成员", SenderType: "user", Text: tc.previous},
				{ID: "11111111-1111-4111-8111-111111111111", SenderID: "alice", SenderName: "测试成员", SenderType: "user", Text: tc.current},
			}}
			result, err := a.Discover(context.Background(), input)
			decision, steps := result.Decision, result.Steps
			if err != nil {
				t.Fatalf("recognition failed: %v", err)
			}
			if decision.Action != tc.action || tc.action == "intervene" && (decision.Intent == nil || tc.intent != "" && decision.Intent.Type != tc.intent) {
				t.Fatalf("unexpected recognition: %+v", decision)
			}
			t.Logf("model=%s valid=true action=%s attempts=%d", config.Model, decision.Action, len(steps))
		})
	}
	for _, continuation := range []bool{false, true} {
		name := "new trip topic after yesterday food"
		if continuation {
			name = "explicit continuation of yesterday food"
		}
		t.Run(name, func(t *testing.T) {
			base := time.Date(2026, 10, 22, 21, 32, 0, 0, time.FixedZone("Asia/Shanghai", 8*3600))
			messages := []ai.Message{
				{ID: "food", SenderID: "alice", SenderType: "user", Text: "昨天想找附近宵夜，推荐餐厅", SentAt: base.Add(-24 * time.Hour)},
				{ID: "trip", SenderID: "alice", SenderType: "user", Text: "下周一20点去成都出差", SentAt: base.Add(-10 * time.Minute)},
				{ID: "peer", SenderID: "bob", SenderType: "user", Text: "那我要一起去成都吗", SentAt: base.Add(-9 * time.Minute)},
				{ID: "cost", SenderID: "alice", SenderType: "user", Text: "你去要花钱，我公司报销", SentAt: base.Add(-8 * time.Minute)},
				{ID: "current", SenderID: "alice", SenderType: "user", Text: "AI帮我对比权衡下是否去成都同行", SentAt: base},
			}
			if continuation {
				messages[4].Text = "AI接着昨天的宵夜推荐，帮我对比餐厅"
			}
			result, err := a.Discover(context.Background(), ai.RecognitionInput{TriggerMode: "proactive", CurrentMessageID: "current", Messages: messages})
			if err != nil {
				t.Fatal(err)
			}
			topic := result.Decision.TopicContext
			if result.Decision.Action != "intervene" || topic == nil {
				t.Fatal("explicit request lost current topic", result.Decision)
			}
			selected, anchors := map[string]bool{}, map[string]bool{}
			for _, id := range topic.MessageIDs {
				selected[id] = true
			}
			for _, id := range topic.AnchorMessageIDs {
				anchors[id] = true
			}
			if continuation {
				if !selected["food"] || !anchors["food"] || selected["trip"] {
					t.Fatal("explicit cross-day continuation misclassified", topic)
				}
			} else if selected["food"] || !anchors["trip"] {
				t.Fatal("old food leaked or trip anchor missing", topic)
			}
		})
	}

	t.Run("implicit continuation across days", func(t *testing.T) {
		base := time.Date(2026, 10, 22, 21, 32, 0, 0, time.FixedZone("Asia/Shanghai", 8*3600))
		messages := []ai.Message{
			{ID: "food", SenderID: "alice", SenderType: "user", Text: "晚上吃面吧", SentAt: base.Add(-25 * time.Hour)},
			{ID: "trip", SenderID: "alice", SenderType: "user", Text: "下周一去成都出差，可以同行，不过你要自费", SentAt: base.Add(-24 * time.Hour)},
			{ID: "current", SenderID: "bob", SenderType: "user", Text: "AI那我要同行吗，我下周安排还不确定，而且要学习和照顾猫咪", SentAt: base},
		}
		result, err := a.Discover(context.Background(), ai.RecognitionInput{TriggerMode: "proactive", CurrentMessageID: "current", Messages: messages})
		if err != nil {
			t.Fatal(err)
		}
		topic := result.Decision.TopicContext
		if topic == nil {
			t.Fatal("implicit follow-up lost topic", result.Decision)
		}
		anchors, selected := map[string]bool{}, map[string]bool{}
		for _, id := range topic.MessageIDs {
			selected[id] = true
		}
		for _, id := range topic.AnchorMessageIDs {
			anchors[id] = true
		}
		if !anchors["trip"] || selected["food"] {
			t.Fatal("date overrode semantic continuation", topic)
		}
	})

}
