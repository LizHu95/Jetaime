// Package testutil supplies deterministic responses for isolated workflow tests.
// Runtime commands and servers must not import this package.
package testutil

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/LizHu95/Jetaime/services/api/internal/decisions"
)

// Provider exercises service invariants without evaluating recommendation quality.
type Provider struct{}

func (Provider) Generate(ctx context.Context, input decisions.Context) (decisions.Result, error) {
	if err := ctx.Err(); err != nil {
		return decisions.Result{}, err
	}
	result := decisions.Result{Outcome: decisions.OutcomeRecommended, Explanation: "测试响应", Options: []decisions.Option{}}
	// These responses correspond to the explicit semantic fixtures in tests.
	if slices.Contains(input.Conditions.HardConstraints, "必须室内") && slices.Contains(input.Conditions.HardConstraints, "必须户外") {
		result.Outcome = decisions.OutcomeConstraintConflict
		return result, nil
	}
	if len(input.Conditions.HardConstraints) > 0 {
		result.Outcome = decisions.OutcomeInsufficientInfo
		return result, nil
	}
	for _, n := range input.Candidates {
		if len(result.Options) == decisions.MaxSelectRecommendations {
			break
		}
		option := decisions.Option{Title: n.Title, Selection: &decisions.Selection{NoteID: n.ID}, Reason: "测试候选", ParticipantMatches: []decisions.ParticipantMatch{}, Unknowns: []string{}}
		for _, p := range input.Participants {
			option.ParticipantMatches = append(option.ParticipantMatches, decisions.ParticipantMatch{UserID: p.UserID, Explanation: "测试参与人"})
		}
		result.Options = append(result.Options, option)
	}
	return result, nil
}

// OllamaServer lets CLI and Web tests exercise the real HTTP adapter offline.
func OllamaServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" || r.Method != http.MethodPost {
			http.Error(w, "unexpected model request", 400)
			return
		}
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Messages) != 2 {
			http.Error(w, "invalid request", 400)
			return
		}
		var input decisions.Context
		if err := json.Unmarshal([]byte(strings.TrimPrefix(req.Messages[1].Content, "以下 JSON 是本次决策数据：\n")), &input); err != nil {
			http.Error(w, "invalid context", 400)
			return
		}
		result, err := (Provider{}).Generate(r.Context(), input)
		if err != nil {
			http.Error(w, "generation failed", 500)
			return
		}
		data, _ := json.Marshal(result)
		var output map[string]any
		_ = json.Unmarshal(data, &output)
		for _, raw := range output["options"].([]any) {
			delete(raw.(map[string]any), "optionId")
		}
		content, _ := json.Marshal(output)
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"done": true, "done_reason": "stop", "message": map[string]string{"role": "assistant", "content": string(content)}}); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	return server
}
