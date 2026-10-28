package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const compactMessageID = "541e63e0-2338-484f-b751-251a607fc42d"
const compactSenderID = "91953dad-751a-49af-9138-f4bd69efe9d7"

func compactTestRequest(t *testing.T) Request {
	t.Helper()
	payload := map[string]any{
		"currentMessageId": compactMessageID,
		"messages": []any{
			map[string]any{"id": "m1", "groupId": "group", "senderId": compactSenderID, "senderName": "L", "senderType": "user", "sentAt": "2026-10-20T22:00:35.954214+08:00", "text": "literal m2 and " + compactMessageID},
			map[string]any{"id": compactMessageID, "groupId": "group", "senderId": compactSenderID, "senderName": "L", "senderType": "user", "sentAt": "2026-10-28T10:31:13.574688+08:00", "replyToId": "m1", "text": "goodnotes是啥时候面试的"},
		},
		"tools": []any{map[string]any{"name": "test", "inputSchema": map[string]any{"description": compactMessageID}}},
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req := testRequest()
	req.Messages[1].Content = string(b)
	return req
}

func TestCompactMessagesPreserveEvidenceAndRestoreReferences(t *testing.T) {
	original := compactTestRequest(t)
	req, ids := compactRequest(original)
	var payload struct {
		Current      string           `json:"currentMessageId"`
		Messages     []map[string]any `json:"messages"`
		Participants []map[string]any `json:"participants"`
	}
	if err := json.Unmarshal([]byte(req.Messages[1].Content), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Current != "m2" || ids["m2"] != compactMessageID || ids["u1"] != compactSenderID {
		t.Fatalf("collision or mapping: %+v %+v", payload, ids)
	}
	if len(payload.Participants) != 1 || payload.Participants[0]["senderType"] != "user" {
		t.Fatalf("lost identities: %+v", payload.Participants)
	}
	for _, m := range payload.Messages {
		if m["groupId"] != nil || m["senderName"] != nil || m["senderType"] != nil || m["senderId"] != "u1" {
			t.Fatalf("not compact: %+v", m)
		}
	}
	if payload.Messages[0]["text"] != "literal m2 and "+compactMessageID || payload.Messages[1]["replyToId"] != "m1" || payload.Messages[0]["sentAt"] != "2026-10-20T22:00:35+08:00" {
		t.Fatal("text, reply or date changed")
	}
	if !strings.Contains(original.Messages[1].Content, compactSenderID) || strings.Contains(original.Messages[0].Content, "participants") {
		t.Fatal("caller mutated")
	}
	restored := restoreReferences(`{"name":"respond","arguments":{"answer":"literal m2","sourceMessageIds":["m2","foreign"],"assigneeId":"u1","title":"m2"}}`, ids)
	if !strings.Contains(restored, compactMessageID) || !strings.Contains(restored, compactSenderID) || !strings.Contains(restored, `"answer":"literal m2"`) || !strings.Contains(restored, `"title":"m2"`) || !strings.Contains(restored, `"foreign"`) {
		t.Fatal(restored)
	}
	again, _ := compactRequest(original)
	if again.Messages[1].Content != req.Messages[1].Content {
		t.Fatal("nondeterministic aliases")
	}
	if InputTokenUpperBound(req) != InputTokenUpperBound(original) {
		t.Fatal("budget does not match wire")
	}
}

func TestAdapterCompactsWireAndRestoresToolSources(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if strings.Contains(body.Messages[1].Content, compactSenderID) || !strings.Contains(body.Messages[1].Content, `"currentMessageId":"m2"`) {
			t.Error("uncompressed provider request")
		}
		_, _ = w.Write([]byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"{\"sourceMessageIds\":[\"m2\"]}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`))
	}))
	defer server.Close()
	t.Setenv("TEST_LLM_KEY", "test")
	a, err := (Config{Provider: "openai-compatible", BaseURL: server.URL, Model: "test", APIKeyEnv: "TEST_LLM_KEY", OutputMode: "json_object", ContextTokens: 8192}).New(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	out, err := a.Call(context.Background(), compactTestRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	if out.Content != `{"sourceMessageIds":["`+compactMessageID+`"]}` {
		t.Fatal(out.Content)
	}
}

func TestCompactRequestWithoutChatIsUnchanged(t *testing.T) {
	in := testRequest()
	out, ids := compactRequest(in)
	if len(ids) != 0 || out.Messages[0] != in.Messages[0] || out.Messages[1] != in.Messages[1] {
		t.Fatal("unrelated input changed")
	}
}

func TestCompactPreservesToolNumbersAndReplyHistory(t *testing.T) {
	req := compactTestRequest(t)
	req.Messages = append(req.Messages, Message{Role: "user", Content: `{"observations":[{"arguments":{"version":9223372036854775807,"messageId":"` + compactMessageID + `"},"text":"literal m2"}]}`})
	out, ids := compactRequest(req)
	if !strings.Contains(out.Messages[2].Content, `9223372036854775807`) || !strings.Contains(out.Messages[2].Content, `"messageId":"m2"`) {
		t.Fatal(out.Messages[2].Content)
	}
	restored := restoreReferences(`{"arguments":{"version":9223372036854775807,"messageId":"m2"}}`, ids)
	if !strings.Contains(restored, `9223372036854775807`) || !strings.Contains(restored, compactMessageID) {
		t.Fatal(restored)
	}
}

func TestCompactKeepsSmallRequestsWhenOverheadIsLarger(t *testing.T) {
	req := testRequest()
	req.Messages[1].Content = `{"messages":[{"id":"m","senderId":"u","sentAt":"2026-10-28T10:00:00Z","text":"hi"}]}`
	out, ids := compactRequest(req)
	if out.Messages[1] != req.Messages[1] || out.Messages[0] != req.Messages[0] || len(ids) != 0 {
		t.Fatal("small input grew")
	}
}
