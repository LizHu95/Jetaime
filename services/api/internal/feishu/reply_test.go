package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func TestReplyCard(t *testing.T) {
	var requests []*larkim.ReplyMessageReqBody
	code := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Tt-Logid", "reply-log-id")
		if r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal" {
			fmt.Fprint(w, `{"code":0,"tenant_access_token":"test-token","expire":7200}`)
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/open-apis/im/v1/messages/om_test/reply" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var body larkim.ReplyMessageReqBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		requests = append(requests, &body)
		fmt.Fprintf(w, `{"code":%d,"msg":"test response","data":{"message_id":"om_reply"}}`, code)
	}))
	defer server.Close()
	client := lark.NewClient("reply-test-app", "test-secret", lark.WithOpenBaseUrl(server.URL))
	for range 2 {
		if err := ReplyCard(context.Background(), client, "om_test"); err != nil {
			t.Fatal(err)
		}
	}
	if len(requests) != 2 {
		t.Fatalf("expected two replies, got %d", len(requests))
	}
	for _, body := range requests {
		if body.MsgType == nil || *body.MsgType != "interactive" || body.Content == nil || !json.Valid([]byte(*body.Content)) {
			t.Fatalf("invalid card body: %+v", body)
		}
		var card struct {
			Elements []struct {
				Actions []struct {
					Value map[string]string `json:"value"`
				} `json:"actions"`
			} `json:"elements"`
		}
		if err := json.Unmarshal([]byte(*body.Content), &card); err != nil {
			t.Fatal(err)
		}
		if len(card.Elements) != 2 || len(card.Elements[1].Actions) != 1 || card.Elements[1].Actions[0].Value["action"] != "ping" {
			t.Fatal("card is missing callback button")
		}
	}
	if requests[0].Uuid == nil || requests[1].Uuid == nil || *requests[0].Uuid == "" || *requests[0].Uuid != *requests[1].Uuid {
		t.Fatal("repeated event should use the same deduplication UUID")
	}
	code = 230001
	if err := ReplyCard(context.Background(), client, "om_test"); err == nil || !strings.Contains(err.Error(), "230001") || !strings.Contains(err.Error(), "log_id=reply-log-id") {
		t.Fatalf("API failure not propagated: %v", err)
	}
	if err := ReplyCard(context.Background(), client, ""); err == nil {
		t.Fatal("missing message ID accepted")
	}
	if len(requests) != 3 {
		t.Fatal("invalid message should not send a request")
	}
}
