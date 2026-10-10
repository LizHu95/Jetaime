package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LizHu95/Jetaime/services/api/internal/decisions"
	"github.com/LizHu95/Jetaime/services/api/internal/demo"
	"github.com/LizHu95/Jetaime/services/api/internal/memory"
	"github.com/LizHu95/Jetaime/services/api/internal/telemetry"
	"github.com/LizHu95/Jetaime/services/api/internal/testutil"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// 每次只提供一个命令，模拟用户在两轮操作之间编辑文件。
type commandSteps struct {
	steps []func() string
}

func (s *commandSteps) Read(p []byte) (int, error) {
	if len(s.steps) == 0 {
		return 0, io.EOF
	}
	command := s.steps[0]() + "\n"
	s.steps = s.steps[1:]
	return copy(p, command), nil
}

func writeFixture(t *testing.T, path string, fixture demo.Fixture) {
	t.Helper()
	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestInteractiveReloadAndHistory(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = tracerProvider.Shutdown(context.Background()) })
	ctx := telemetry.WithTracer(context.Background(), tracerProvider.Tracer("reload-test"))
	path := filepath.Join(t.TempDir(), "fixture.json")
	fixture := demo.NewFixture()
	writeFixture(t, path, fixture)
	var out bytes.Buffer
	var oldID, newSession string
	input := &commandSteps{steps: []func() string{
		func() string {
			oldID = strings.Split(strings.Split(out.String(), "Decision: ")[1], "\n")[0]
			for i := range fixture.Dataset.Notes {
				fixture.Dataset.Notes[i].Title = "重载-" + fixture.Dataset.Notes[i].Title
				fixture.Dataset.Notes[i].Tags = []string{}
				if fixture.Dataset.Notes[i].ID == "restaurant-03" {
					fixture.Dataset.Notes[i].Tags = []string{"辣"}
				}
			}
			for i := range fixture.Dataset.Memories {
				if fixture.Dataset.Memories[i].Type == memory.TypePreference {
					fixture.Dataset.Memories[i].Content = "喜欢辣"
				}
			}
			// 修改事实也必须生效：原第一条餐厅变为无法核实，不再推荐。
			fact := fixture.Facts["restaurant-01"]
			fact.Verified = false
			fixture.Facts["restaurant-01"] = fact
			writeFixture(t, path, fixture)
			return "reload"
		},
		func() string {
			parts := strings.Split(out.String(), "[资料已重载，新 Session]")
			if len(parts) != 2 || !strings.Contains(parts[1], "1. 重载-虚构餐厅 02") || strings.Contains(parts[1], "[restaurant-01]") {
				t.Fatal("new notes/facts were not used", out.String())
			}
			newSession = strings.Split(strings.Split(parts[1], "Session: ")[1], "\n")[0]
			return "history " + oldID
		},
		func() string {
			if !strings.Contains(out.String(), `"title":"虚构餐厅 01"`) {
				t.Fatal("old history snapshot lost or rewritten")
			}
			// 语法正确但实体不合法，也必须拒绝重载。
			fixture.Dataset.Notes[0].Title = ""
			writeFixture(t, path, fixture)
			return "reload"
		},
		func() string { return "session" },
		func() string {
			if !strings.Contains(out.String(), "资料校验失败") || !strings.Contains(out.String(), `"id":"`+newSession+`"`) {
				t.Fatal("failed reload replaced current session", out.String())
			}
			return "quit"
		},
	}}
	if err := runWithTestModel(t, ctx, []string{"-fixture", path, "-interactive"}, input, &out); err != nil {
		t.Fatal(err)
	}
	var versions []string
	for _, span := range exporter.GetSpans() {
		if span.Name == "decision.generate" {
			for _, attr := range span.Attributes {
				if attr.Key == "fixture.sha256" {
					versions = append(versions, attr.Value.AsString())
				}
			}
		}
	}
	if len(versions) != 2 || versions[0] == versions[1] {
		t.Fatal("reload did not change trace data version", versions)
	}
}

func TestReloadRequiresFileAndRejectsMalformedJSON(t *testing.T) {
	var out bytes.Buffer
	if err := runWithTestModel(t, context.Background(), []string{"-interactive"}, strings.NewReader("reload\nsession\nquit\n"), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "需要启动时指定 -fixture") || !strings.Contains(out.String(), "latestDecisionId") {
		t.Fatal(out.String())
	}
	path := filepath.Join(t.TempDir(), "fixture.json")
	for _, content := range []string{`{"dataset":`, `{"unknown":true}`, `{} {}`} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadFixture(path); err == nil {
			t.Fatalf("invalid fixture accepted: %s", content)
		}
	}
}

func TestReloadModelFailureKeepsOldDataAndCanRetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture.json")
	fixture := demo.NewFixture()
	writeFixture(t, path, fixture)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var request struct {
			Messages []struct{ Content string }
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		var input decisions.Context
		if err := json.Unmarshal([]byte(strings.TrimPrefix(request.Messages[1].Content, "以下 JSON 是本次决策数据：\n")), &input); err != nil {
			t.Error(err)
			return
		}
		result, err := (testutil.Provider{}).Generate(r.Context(), input)
		if err != nil {
			t.Error(err)
			return
		}
		// 测试服务模拟模型输出，不提供由真正服务端生成的 OptionID。
		options := make([]map[string]any, 0, len(result.Options))
		for _, o := range result.Options {
			options = append(options, map[string]any{"title": o.Title, "selection": o.Selection, "reason": o.Reason, "participantMatches": o.ParticipantMatches, "unknowns": o.Unknowns})
		}
		content, err := json.Marshal(map[string]any{"outcome": result.Outcome, "explanation": result.Explanation, "options": options})
		if err != nil {
			t.Error(err)
			return
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"done": true, "message": map[string]string{"content": string(content)}}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	var out bytes.Buffer
	input := &commandSteps{steps: []func() string{
		func() string {
			for i := range fixture.Dataset.Notes {
				fixture.Dataset.Notes[i].Title = "更新-" + fixture.Dataset.Notes[i].Title
			}
			writeFixture(t, path, fixture)
			return "reload"
		},
		func() string {
			if !strings.Contains(out.String(), "操作失败，原结果保留") || strings.Contains(out.String(), "[资料已重载") {
				t.Fatal("model failure switched state", out.String())
			}
			return "history"
		},
		func() string {
			if !strings.Contains(out.String(), `"title":"虚构餐厅 01"`) {
				t.Fatal("old decision unavailable after failure")
			}
			return "reload"
		},
		func() string {
			if !strings.Contains(out.String(), "1. 更新-虚构餐厅") {
				t.Fatal("retry did not use new data")
			}
			return "quit"
		},
	}}
	if err := runWithTestModel(t, context.Background(), []string{"-fixture", path, "-provider", "ollama", "-ollama-url", server.URL, "-interactive"}, input, &out); err != nil {
		t.Fatal(err)
	}
}
