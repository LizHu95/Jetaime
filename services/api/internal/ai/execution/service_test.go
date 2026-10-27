package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/qa"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/recent"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/respond"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/silence"
	"github.com/LizHu95/Jetaime/services/api/internal/chat/history"
)

type disabledExtractionModel struct {
	calls int
}

func (m *disabledExtractionModel) Call(_ context.Context, request llm.Request) (llm.Result, error) {
	m.calls++
	var payload struct {
		Tools []tools.Definition `json:"tools"`
	}
	if err := json.Unmarshal([]byte(request.Messages[1].Content), &payload); err != nil {
		return llm.Result{}, err
	}
	if len(payload.Tools) != 1 || payload.Tools[0].Name != "answer_question" {
		return llm.Result{}, errors.New("unexpected explicit tool catalog")
	}
	return llm.Result{Content: `{"name":"extract_todos","arguments":{"question":"整理待办"}}`}, nil
}

func TestExplicitQAToolsRejectExtraction(t *testing.T) {
	model := &disabledExtractionModel{}
	registry, err := tools.New(qa.New(model, "test", 16384, nil))
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(model, "test", 16384, registry)
	if err != nil {
		t.Fatal(err)
	}
	input := ai.Context{GroupID: "g", ActorID: "u", Users: []ai.User{{ID: "u", Nickname: "用户"}}, Messages: []ai.Message{{ID: "m", GroupID: "g", SenderID: "u", Text: "整理待办", SentAt: time.Now()}}}
	_, err = service.Execute(context.Background(), ai.Invocation{Context: input, Question: "整理待办", LoadExtraction: func(context.Context) (ai.Context, error) {
		t.Fatal("disabled tool loaded extraction context")
		return input, nil
	}})
	if !errors.Is(err, ai.ErrInvalidDecision) || model.calls != 3 {
		t.Fatalf("disabled extraction executed: calls=%d err=%v", model.calls, err)
	}
}

type customCatalogModel struct{ t *testing.T }

func (m customCatalogModel) Call(_ context.Context, request llm.Request) (llm.Result, error) {
	var payload struct {
		Tools []tools.Definition `json:"tools"`
	}
	if err := json.Unmarshal([]byte(request.Messages[1].Content), &payload); err != nil {
		return llm.Result{}, err
	}
	if len(payload.Tools) != 1 || payload.Tools[0].Name != "echo" {
		m.t.Fatalf("execution added an implicit capability: %+v", payload.Tools)
	}
	return llm.Result{Content: `{"name":"echo","arguments":{"text":"ready"}}`}, nil
}

func TestExecutionUsesOnlySuppliedRegistry(t *testing.T) {
	type input struct {
		Text string `json:"text"`
	}
	type output struct {
		Text string `json:"text"`
	}
	tool := tools.NewTyped(tools.Metadata{Name: "echo", Description: "回显", Terminal: true}, func(_ context.Context, _ tools.Scope, in input) (output, error) { return output{Text: in.Text}, nil }, tools.Options[output]{Present: func(out output) tools.Completion {
		return tools.Completion{Answer: &ai.Answer{Status: "answered", Answer: out.Text, SourceMessageIDs: []string{}}}
	}})
	registry, err := tools.New(tool)
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(customCatalogModel{t: t}, "test", 16384, registry)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := ai.Context{GroupID: "g", ActorID: "u", Users: []ai.User{{ID: "u", Nickname: "User"}}, Messages: []ai.Message{{ID: "m", GroupID: "g", SenderID: "u", Text: "echo", SentAt: time.Now()}}}
	run, err := service.Execute(context.Background(), ai.Invocation{Context: snapshot, Question: "echo"})
	if err != nil || run.Answer == nil || run.Answer.Answer != "ready" {
		t.Fatalf("explicit registry not used: %+v %v", run.Answer, err)
	}
	if definitions := registry.Definitions(); len(definitions) != 1 || definitions[0].Name != "echo" {
		t.Fatal("supplied registry mutated")
	}
}

func TestExecutionRequiresExplicitRegistry(t *testing.T) {
	if _, err := New(customCatalogModel{t: t}, "test", 16384, nil); err == nil {
		t.Fatal("missing registry silently replaced with defaults")
	}
}

type entryModel struct {
	outputs  []string
	requests []llm.Request
}

func (m *entryModel) Call(_ context.Context, request llm.Request) (llm.Result, error) {
	m.requests = append(m.requests, request)
	if len(m.outputs) == 0 {
		return llm.Result{}, errors.New("unexpected model call")
	}
	output := m.outputs[0]
	m.outputs = m.outputs[1:]
	return llm.Result{Content: output}, nil
}
func TestExecutionRoutesActiveAndPassiveToSameMain(t *testing.T) {
	const answer = `{"name":"respond","arguments":{"status":"answered","answer":"确认一下地点？","sourceMessageIds":["m"]}}`
	for _, tc := range []struct {
		name, trigger string
		outputs       []string
		silent        bool
	}{
		{name: "active mention", trigger: "mention", outputs: []string{answer}},
		{name: "active followup", outputs: []string{answer}},
		{name: "passive silence", trigger: "proactive", outputs: []string{`{"topicContext":{"topic":"当前测试事项","continuity":"continuation","messageIds":["m"],"anchorMessageIds":["m"]},"action":"skip","verification":null,"reason":"正常聊天","breakdowns":[],"intent":null}`}, silent: true},
		{name: "passive intervention", trigger: "proactive", outputs: []string{`{"topicContext":{"topic":"当前测试事项","continuity":"continuation","messageIds":["m"],"anchorMessageIds":["m"]},"action":"intervene","verification":null,"reason":"地点不明确","breakdowns":[],"intent":{"type":"clarify","objective":"确认地点","evidenceMessageIds":["m"]}}`, answer}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := &entryModel{outputs: append([]string{}, tc.outputs...)}
			registry, err := tools.New(respond.New(), silence.New())
			if err != nil {
				t.Fatal(err)
			}
			service, err := New(model, "test", 16384, registry)
			if err != nil {
				t.Fatal(err)
			}
			snapshot := ai.Context{GroupID: "g", ActorID: "u", Users: []ai.User{{ID: "u", Nickname: "用户"}}, Messages: []ai.Message{{ID: "m", GroupID: "g", SenderID: "u", Text: "去哪里", SentAt: time.Now()}}, Request: &ai.RequestContext{MessageID: "m"}}
			run, err := service.Execute(context.Background(), ai.Invocation{TriggerMode: tc.trigger, Context: snapshot, Question: "去哪里"})
			if err != nil || run.Status != "succeeded" || run.Silent != tc.silent || len(model.requests) != len(tc.outputs) || run.AgentID != "main" || run.InvocationID == "" {
				t.Fatalf("entry wiring failed: run=%+v calls=%d err=%v", run, len(model.requests), err)
			}
			if (run.Recognition != nil) != (tc.trigger == "proactive") {
				t.Fatal("incorrect recognition path", run)
			}
			if !tc.silent && (run.Answer == nil || run.Answer.Answer != "确认一下地点？") {
				t.Fatal("shared main did not respond", run)
			}
		})
	}
}

func TestExecutionVerificationHandoffKeepsHistoricalEvidence(t *testing.T) {
	const discovery = `{"topicContext":{"topic":"当前测试事项","continuity":"continuation","messageIds":["m"],"anchorMessageIds":["m"]},"action":"verify","reason":"需核实此前预算","breakdowns":[],"intent":null,"verification":{"topic":"本次周末聚餐","query":"此前预算及后续修改是什么","evidenceMessageIds":["m"]}}`
	for _, intervene := range []bool{true, false} {
		t.Run(fmt.Sprint("intervene=", intervene), func(t *testing.T) {
			final := `{"shouldIntervene":false,"reason":"预算已经调整，无需重复参与","breakdowns":[],"intent":null}`
			if intervene {
				final = `{"shouldIntervene":true,"reason":"有明确预算和当前提议的原文","breakdowns":[{"type":"forgotten_constraint","evidenceMessageIds":["old","m"]}],"intent":{"type":"clarify","objective":"确认是否要调整本次预算","evidenceMessageIds":["old","m"]}}`
			}
			model := &entryModel{outputs: []string{discovery, final}}
			if intervene {
				model.outputs = append(model.outputs, `{"name":"respond","arguments":{"status":"answered","answer":"此前人均预算是300元，要调整吗？","sourceMessageIds":["old","m"]}}`)
			}
			registry, err := tools.New(respond.New(), silence.New())
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			snapshot := ai.Context{GroupID: "g", ActorID: "u", Users: []ai.User{{ID: "u", Nickname: "用户"}}, Messages: []ai.Message{{ID: "m", GroupID: "g", SenderID: "u", Text: "准备订500元这家，预算按之前的来", SentAt: now}}, Request: &ai.RequestContext{MessageID: "m", SenderID: "u", SentAt: now}}
			reads := 0
			provider := func(_ context.Context, request ai.InterventionEvidenceRequest) (ai.InterventionEvidence, error) {
				reads++
				if request.Context.GroupID != "g" || request.Context.ActorID != "u" || request.ContextEndSeq != 42 || request.Question.Topic != "本次周末聚餐" {
					t.Fatal("untrusted verification scope", request)
				}
				return ai.InterventionEvidence{Coverage: "partial", Messages: []ai.Message{{ID: "old", GroupID: "g", SenderID: "u", Text: "本次聚餐人均最多300元", SentAt: now.Add(-time.Hour)}}}, nil
			}
			service, err := New(model, "test", 32768, registry, evidenceProviderFunc(provider))
			if err != nil {
				t.Fatal(err)
			}
			run, err := service.Execute(context.Background(), ai.Invocation{TriggerMode: "proactive", ContextEndSeq: 42, Context: snapshot})
			if err != nil || run.Recognition == nil || run.Silent == intervene || reads != 1 || len(model.outputs) != 0 {
				t.Fatalf("verification pipeline failed: %+v err=%v reads=%d", run, err, reads)
			}
			if run.Steps[0].Name != "discover_intervention" || run.Steps[1].Name != "verify_intervention_evidence" || run.Steps[2].Name != "decide_intervention" {
				t.Fatal("missing staged trace", run.Steps)
			}
			if !intervene {
				return
			}
			var execution struct {
				Context ai.Context `json:"context"`
			}
			if err := json.Unmarshal([]byte(model.requests[2].Messages[1].Content), &execution); err != nil {
				t.Fatal(err)
			}
			if len(execution.Context.Messages) != 2 || execution.Context.Messages[0].ID != "old" || len(execution.Context.Request.RelatedMessageIDs) != 2 {
				t.Fatal("historical evidence lost at main handoff", execution)
			}
			if len(snapshot.Messages) != 1 || len(snapshot.Request.RelatedMessageIDs) != 0 {
				t.Fatal("recognition snapshot mutated")
			}
		})
	}
}

func TestPassiveTopicScopePreservesTripAnchorAndSeparatesOldPlaces(t *testing.T) {
	for _, tc := range []struct {
		name         string
		continuation bool
		oldAge       time.Duration
	}{
		{"different topics across days", false, 24 * time.Hour},
		{"different topics within one day", false, time.Hour},
		{"explicit continuation across days", true, 24 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(2026, 10, 22, 21, 32, 0, 0, time.FixedZone("Asia/Shanghai", 8*3600))
			messages := []ai.Message{
				{ID: "food", GroupID: "g", SenderID: "u", SenderType: "user", Text: "昨天推荐的宵夜餐厅", SentAt: now.Add(-tc.oldAge)},
				{ID: "trip", GroupID: "g", SenderID: "u", SenderType: "user", Text: "下周一去成都出差，是否自费同行", SentAt: now.Add(-10 * time.Minute)},
			}
			ids := []string{"trip"}
			for i := 0; i < 8; i++ {
				id := fmt.Sprint("short-", i)
				messages = append(messages, ai.Message{ID: id, GroupID: "g", SenderID: "u", Text: "公司报销，同行自费，再权衡一下", SentAt: now.Add(time.Duration(i-8) * time.Second)})
				ids = append(ids, id)
			}
			messages = append(messages, ai.Message{ID: "current", GroupID: "g", SenderID: "u", Text: "帮我对比权衡下", SentAt: now})
			ids = append(ids, "current")
			topic := &ai.TopicContext{Topic: "去成都出差是否自费同行", Continuity: "new_topic", MessageIDs: ids, AnchorMessageIDs: []string{"trip", "current"}}
			if tc.continuation {
				topic.Topic, topic.Continuity = "继续昨天宵夜推荐", "continuation"
				topic.MessageIDs = []string{"food", "current"}
				topic.AnchorMessageIDs = []string{"food", "current"}
				messages[len(messages)-1].Text = "接着昨天的宵夜推荐，帮我对比权衡下"
			}
			candidate := ai.InterventionCandidate{TopicContext: topic, Action: "intervene", Reason: "当前作者明确请求权衡", Breakdowns: []ai.Breakdown{}, Intent: &ai.InterventionIntent{Type: "suggest", Objective: "比较是否同行", EvidenceMessageIDs: []string{"current"}}}
			discovery, err := json.Marshal(candidate)
			if err != nil {
				t.Fatal(err)
			}
			model := &entryModel{outputs: []string{string(discovery), `{"name":"remain_silent","arguments":{"reason":"暂不补充"}}`}}
			registry, _ := tools.New(silence.New())
			service, err := New(model, "test", 32768, registry)
			if err != nil {
				t.Fatal(err)
			}
			input := ai.Context{GroupID: "g", ActorID: "u", Users: []ai.User{{ID: "u", Nickname: "用户"}}, Messages: messages, Request: &ai.RequestContext{MessageID: "current", SentAt: now}, RecentPlaces: []ai.RecommendedPlace{{ID: "old-place", Name: "旧餐厅", SourceMessageID: "food"}}}
			run, err := service.Execute(context.Background(), ai.Invocation{TriggerMode: "proactive", Context: input})
			if err != nil || !run.Silent || len(model.requests) != 2 {
				t.Fatalf("topic handoff failed: %v", err)
			}
			var payload struct {
				Context  ai.Context `json:"context"`
				Question string     `json:"question"`
			}
			if err := json.Unmarshal([]byte(model.requests[1].Messages[1].Content), &payload); err != nil {
				t.Fatal(err)
			}
			got := map[string]bool{}
			for _, message := range payload.Context.Messages {
				got[message.ID] = true
			}
			if tc.continuation {
				if !got["food"] || got["trip"] || len(payload.Context.RecentPlaces) != 1 {
					t.Fatal("cross-day continuation was incorrectly cut", payload)
				}
			} else if !got["trip"] || got["food"] || len(payload.Context.RecentPlaces) != 0 {
				t.Fatal("trip anchor dropped or unrelated restaurant leaked", payload)
			}
			if len(input.Messages) != 11 || len(input.RecentPlaces) != 1 || len(input.Request.RelatedMessageIDs) != 0 {
				t.Fatal("original snapshot mutated")
			}
		})
	}
}

type followupReader struct {
	t        *testing.T
	messages []history.Message
	calls    int
}

func (r *followupReader) RecentMessages(_ context.Context, group, actor string, end int64, limit int) ([]history.Message, error) {
	r.calls++
	if group != "g" || actor != "u" || end != 42 || limit != 30 {
		r.t.Fatal("follow-up scope changed")
	}
	return r.messages, nil
}
func TestActiveFollowupReadsOriginalTripBeforeResponding(t *testing.T) {
	now := time.Now()
	for _, question := range []string{"我要去吗", "我考虑的是不确定下周有没有事，其次要学习，还有家里有猫咪"} {
		t.Run(question, func(t *testing.T) {
			reader := &followupReader{t: t, messages: []history.Message{
				{ID: "trip", Seq: 10, SenderID: "other", SenderType: "user", Text: "下周一去成都出差，我公司报销，你同行需自费", SentAt: now.Add(-24 * time.Hour)},
				{ID: "concerns", Seq: 11, SenderID: "u", SenderType: "user", Text: "我下周日程没确定，还要学习和照顾猫咪", SentAt: now.Add(-time.Hour)},
			}}
			model := &entryModel{outputs: []string{
				`{"name":"read_recent_messages","arguments":{}}`,
				`{"name":"respond","arguments":{"status":"answered","answer":"目前更倾向先暂缓确认同行。先确认下周安排、学习时间和猫咪照护，再决定是否自费去成都。","sourceMessageIds":["trip","concerns","current"]}}`,
			}}
			registry, err := tools.New(recent.New(reader), respond.New())
			if err != nil {
				t.Fatal(err)
			}
			service, err := New(model, "test", 32768, registry)
			if err != nil {
				t.Fatal(err)
			}
			input := ai.Context{GroupID: "g", ActorID: "u", Users: []ai.User{{ID: "u", Nickname: "提问者"}, {ID: "other", Nickname: "出差者"}}, Messages: []ai.Message{{ID: "current", GroupID: "g", SenderID: "u", Text: question, SentAt: now}}, Request: &ai.RequestContext{MessageID: "current", SenderID: "u", SentAt: now}}
			run, err := service.Execute(context.Background(), ai.Invocation{TriggerMode: "mention", ContextEndSeq: 42, Context: input, Question: question})
			if err != nil || run.Answer == nil || reader.calls != 1 || len(model.requests) != 2 {
				t.Fatalf("follow-up recovery failed: %+v %v", run, err)
			}
			var payload struct {
				Context ai.Context `json:"context"`
			}
			if err := json.Unmarshal([]byte(model.requests[1].Messages[1].Content), &payload); err != nil {
				t.Fatal(err)
			}
			if len(payload.Context.Messages) != 3 || payload.Context.Messages[0].ID != "trip" || payload.Context.Messages[2].ID != "current" {
				t.Fatal("trip evidence missing or chronology lost", payload)
			}
			if strings.Count(model.requests[1].Messages[1].Content, "下周一去成都出差") != 1 {
				t.Fatal("duplicated original evidence")
			}
			if len(input.Messages) != 1 {
				t.Fatal("original request mutated")
			}
		})
	}
}
