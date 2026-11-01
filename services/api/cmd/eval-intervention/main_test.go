package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
)

func exampleCase() testCase {
	c := testCase{ID: "test", Input30: ai.RecognitionInput{TriggerMode: "proactive", CurrentMessageID: "m30"}}
	for i := 1; i <= 30; i++ {
		c.Input30.Messages = append(c.Input30.Messages, ai.Message{ID: fmt.Sprintf("m%d", i), GroupID: "g", SenderID: fmt.Sprint(i % 2), SenderType: "user", Text: fmt.Sprintf("message %d", i), SentAt: time.Date(2026, 11, 1, 12, i, 0, 0, time.FixedZone("Shanghai", 8*3600))})
	}
	c.Input30.Messages[0].Text = "hidden-only-in-30"
	return c
}

func writeSuite(t *testing.T, cases []testCase) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cases.json")
	raw, _ := json.Marshal(suite{FormatVersion: "proactive-window-dataset-v1", Version: "test-v1", Cases: cases})
	if err := os.WriteFile(p, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSuiteRejectsBrokenIsolation(t *testing.T) {
	for name, mutate := range map[string]func(*testCase){
		"cross group":    func(c *testCase) { c.Input30.Messages[2].GroupID = "other" },
		"wrong trigger":  func(c *testCase) { c.Input30.CurrentMessageID = "m29" },
		"duplicate id":   func(c *testCase) { c.Input30.Messages[0].ID = "m30" },
		"future message": func(c *testCase) { c.Input30.Messages[0].SentAt = c.Input30.Messages[29].SentAt.Add(time.Minute) },
		"short window":   func(c *testCase) { c.Input30.Messages = c.Input30.Messages[1:] },
		"AI trigger":     func(c *testCase) { c.Input30.Messages[29].SenderType = "assistant" },
	} {
		t.Run(name, func(t *testing.T) {
			c := exampleCase()
			mutate(&c)
			if _, _, err := loadSuite(writeSuite(t, []testCase{c})); err == nil {
				t.Fatal("invalid isolated input accepted")
			}
		})
	}
	if _, _, err := loadSuite(writeSuite(t, []testCase{exampleCase(), exampleCase()})); err == nil {
		t.Fatal("duplicate case accepted")
	}
}

type fakeModel struct{ requests []llm.Request }

func (m *fakeModel) Call(_ context.Context, req llm.Request) (llm.Result, error) {
	m.requests = append(m.requests, req)
	return llm.Result{Content: `{"action":"skip","reason":"普通闲聊"}`}, nil
}

func TestSuffixAndPrivateRecording(t *testing.T) {
	c := exampleCase()
	for _, window := range []int{15, 30} {
		m := &fakeModel{}
		r := runOne(context.Background(), c, task{Window: window, Repetition: 1}, m, "fake", 32768)
		if r.Status != "succeeded" || len(r.Calls) != 1 {
			t.Fatalf("recognition failed: %+v", r)
		}
		var in ai.RecognitionInput
		if err := json.Unmarshal([]byte(m.requests[0].Messages[1].Content), &in); err != nil {
			t.Fatal(err)
		}
		actualMessages, _ := json.Marshal(in.Messages)
		expectedMessages, _ := json.Marshal(c.Input30.Messages[30-window:])
		if len(in.Messages) != window || in.CurrentMessageID != "m30" || string(actualMessages) != string(expectedMessages) {
			t.Fatal("paired suffix/trigger changed")
		}
		if strings.Contains(m.requests[0].Messages[1].Content, "hidden-only-in-30") != (window == 30) {
			t.Fatal("hidden history leaked")
		}
		if r.Calls[0].PromptTokens != nil || r.Calls[0].CompletionTokens != nil {
			t.Fatal("missing provider usage became zero")
		}
	}
}

func TestDryRunSkipsModelConfigAndRefusesOverwrite(t *testing.T) {
	path := writeSuite(t, []testCase{exampleCase()})
	// Include an annotation with recognizable text; it must never enter model input.
	raw, _ := os.ReadFile(path)
	var input map[string]any
	_ = json.Unmarshal(raw, &input)
	input["cases"].([]any)[0].(map[string]any)["reference"] = map[string]any{"secret": "reference-do-not-send"}
	raw, _ = json.Marshal(input)
	_ = os.WriteFile(path, raw, 0600)
	s, _, err := loadSuite(path)
	if err != nil {
		t.Fatal(err)
	}
	m := &fakeModel{}
	runOne(context.Background(), s.Cases[0], task{Window: 15}, m, "fake", 32768)
	if strings.Contains(m.requests[0].Messages[1].Content, "reference-do-not-send") {
		t.Fatal("annotation leaked")
	}
	out := filepath.Join(t.TempDir(), "report.json")
	args := []string{"-cases", path, "-output", out, "-dry-run", "-model-config", "/nonexistent/model.json"}
	if err = execute(args); err != nil {
		t.Fatal("dry-run tried to resolve a model", err)
	}
	var reportData report
	raw, _ = os.ReadFile(out)
	_ = json.Unmarshal(raw, &reportData)
	if !reportData.DryRun || reportData.Model != nil || len(reportData.Results) != 0 || reportData.Planned != 2 {
		t.Fatal("dry-run executed inference")
	}
	if err := execute(args); err == nil {
		t.Fatal("overwrote report")
	}
	info, _ := os.Stat(out)
	if info.Mode().Perm() != 0600 {
		t.Fatal("report is not private")
	}
}

func TestScheduleIsSeededAndBalanced(t *testing.T) {
	a := schedule(20, 3, 42)
	if len(a) != 120 || !reflect.DeepEqual(a, schedule(20, 3, 42)) || reflect.DeepEqual(a, schedule(20, 3, 43)) {
		t.Fatal("schedule not balanced or reproducible")
	}
	seen := map[task]bool{}
	for _, v := range a {
		if seen[v] || v.CaseIndex < 0 || v.CaseIndex >= 20 || v.Repetition < 1 || v.Repetition > 3 || (v.Window != 15 && v.Window != 30) {
			t.Fatal("invalid schedule")
		}
		seen[v] = true
	}
}
