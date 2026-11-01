// eval-intervention runs the production Discover judge in isolation. It cannot
// retrieve history, execute tools, send messages, or open business storage.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/intervention"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
)

type suite struct {
	FormatVersion string     `json:"formatVersion"`
	Version       string     `json:"version"`
	Cases         []testCase `json:"cases"`
}

type testCase struct {
	ID      string              `json:"id"`
	Input30 ai.RecognitionInput `json:"input30"`
}

// References deliberately have no representation in the inference types.
func loadSuite(path string) (suite, string, error) {
	var s suite
	raw, err := os.ReadFile(path)
	if err != nil {
		return s, "", err
	}
	if err = json.Unmarshal(raw, &s); err != nil {
		return s, "", err
	}
	if s.FormatVersion != "proactive-window-dataset-v1" || s.Version == "" || len(s.Cases) == 0 {
		return s, "", errors.New("expected nonempty proactive-window-dataset-v1")
	}
	seen := map[string]bool{}
	for _, c := range s.Cases {
		if c.ID == "" || seen[c.ID] {
			return s, "", errors.New("empty or duplicate case ID")
		}
		seen[c.ID] = true
		in := c.Input30
		if in.TriggerMode != "proactive" || len(in.Messages) != 30 || in.Messages[29].ID != in.CurrentMessageID {
			return s, "", fmt.Errorf("case %s requires 30 ordered messages including current trigger", c.ID)
		}
		ids := map[string]bool{}
		for i, m := range in.Messages {
			if m.ID == "" || ids[m.ID] || m.GroupID == "" || m.GroupID != in.Messages[0].GroupID || m.SenderID == "" || strings.TrimSpace(m.Text) == "" || m.SentAt.IsZero() || (m.SenderType != "user" && m.SenderType != "assistant") {
				return s, "", fmt.Errorf("case %s has invalid or cross-group messages", c.ID)
			}
			if i > 0 && m.SentAt.Before(in.Messages[i-1].SentAt) {
				return s, "", fmt.Errorf("case %s has unordered timestamps", c.ID)
			}
			ids[m.ID] = true
		}
		if in.Messages[29].SenderType != "user" {
			return s, "", fmt.Errorf("case %s trigger must be human", c.ID)
		}
	}
	hash := sha256.Sum256(raw)
	return s, hex.EncodeToString(hash[:]), nil
}

type task struct {
	CaseIndex  int `json:"caseIndex"`
	Window     int `json:"window"`
	Repetition int `json:"repetition"`
}

func schedule(count, repetitions int, seed int64) []task {
	var result []task
	for i := 0; i < count; i++ {
		for rep := 1; rep <= repetitions; rep++ {
			for _, window := range []int{15, 30} {
				result = append(result, task{i, window, rep})
			}
		}
	}
	rand.New(rand.NewSource(seed)).Shuffle(len(result), func(i, j int) { result[i], result[j] = result[j], result[i] })
	return result
}

type modelCall struct {
	Request          json.RawMessage `json:"request"`
	Response         string          `json:"response"`
	DurationMS       int64           `json:"durationMs"`
	PromptTokens     *int            `json:"promptTokens"`
	CompletionTokens *int            `json:"completionTokens"`
	Error            string          `json:"error,omitempty"`
}

type recordingModel struct {
	inner llm.ChatModel
	calls []modelCall
}

func (m *recordingModel) Call(ctx context.Context, req llm.Request) (llm.Result, error) {
	// Marshal before Call/repair can mutate the message slice.
	raw, err := json.Marshal(req)
	if err != nil {
		return llm.Result{}, err
	}
	started := time.Now()
	out, err := m.inner.Call(ctx, req)
	c := modelCall{Request: raw, Response: out.Content, DurationMS: time.Since(started).Milliseconds(), PromptTokens: out.PromptTokens, CompletionTokens: out.CompletionTokens}
	if err != nil {
		c.Error = err.Error()
	}
	m.calls = append(m.calls, c)
	return out, err
}

type row struct {
	CaseID        string                   `json:"caseId"`
	Window        int                      `json:"window"`
	Repetition    int                      `json:"repetition"`
	Status        string                   `json:"status"`
	DurationMS    int64                    `json:"durationMs"`
	Decision      ai.InterventionCandidate `json:"decision"`
	PromptVersion string                   `json:"promptVersion"`
	Steps         []ai.Step                `json:"steps"`
	Calls         []modelCall              `json:"modelCalls"`
	Error         string                   `json:"error,omitempty"`
}

func runOne(ctx context.Context, c testCase, t task, model llm.ChatModel, name string, tokens int) row {
	input := c.Input30
	input.Messages = append([]ai.Message(nil), input.Messages[len(input.Messages)-t.Window:]...)
	recorder := &recordingModel{inner: model, calls: []modelCall{}}
	r := row{CaseID: c.ID, Window: t.Window, Repetition: t.Repetition, Status: "failed"}
	started := time.Now()
	judge, err := intervention.New(recorder, name, tokens)
	if err == nil {
		var result ai.CandidateResult
		result, err = judge.Discover(ctx, input)
		r.Decision, r.Steps, r.PromptVersion = result.Decision, result.Steps, result.PromptVersion
	}
	r.DurationMS, r.Calls = time.Since(started).Milliseconds(), recorder.calls
	if err != nil {
		r.Error = err.Error()
	} else {
		r.Status = "succeeded"
	}
	return r
}

type report struct {
	FormatVersion string      `json:"formatVersion"`
	SuiteVersion  string      `json:"suiteVersion"`
	SuiteSHA256   string      `json:"suiteSha256"`
	StartedAt     time.Time   `json:"startedAt"`
	DryRun        bool        `json:"dryRun"`
	Seed          int64       `json:"seed"`
	Planned       int         `json:"plannedExecutions"`
	Completed     bool        `json:"completed"`
	Model         *llm.Config `json:"model,omitempty"`
	Order         []task      `json:"order"`
	Results       []row       `json:"results"`
}

func execute(args []string) error {
	fs := flag.NewFlagSet("eval-intervention", flag.ContinueOnError)
	casesPath := fs.String("cases", "", "proactive-window-dataset-v1 JSON")
	output := fs.String("output", "", "new private report path, or - for stdout")
	dry := fs.Bool("dry-run", false, "validate only; never construct or call a model")
	repetitions := fs.Int("repetitions", 1, "repetitions per case and window (1-20)")
	seed := fs.Int64("seed", 20261101, "deterministic shuffled execution order")
	timeout := fs.Duration("case-timeout", 90*time.Second, "per-execution deadline, including repair")
	modelFlags := llm.RegisterFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *casesPath == "" || *output == "" || *repetitions < 1 || *repetitions > 20 || *timeout <= 0 {
		return errors.New("cases/output required, repetitions 1-20, timeout positive; no positional arguments")
	}
	s, hash, err := loadSuite(*casesPath)
	if err != nil {
		return err
	}
	r := report{FormatVersion: "intervention-discover-report-v1", SuiteVersion: s.Version, SuiteSHA256: hash,
		StartedAt: time.Now().UTC(), DryRun: *dry, Seed: *seed, Order: schedule(len(s.Cases), *repetitions, *seed), Results: []row{}}
	r.Planned = len(r.Order)
	var cfg llm.Config
	if !*dry {
		cfg, err = modelFlags.Resolve(32768)
		if err != nil {
			return err
		}
		cfg.Fallbacks = nil
		// This validates credentials without sending an inference request.
		if _, err = cfg.New(*timeout); err != nil {
			return err
		}
		r.Model = &cfg
	}
	var out io.Writer = os.Stdout
	if *output != "-" {
		if err := os.MkdirAll(filepath.Dir(*output), 0700); err != nil {
			return err
		}
		f, err := os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		defer f.Close()
		out = f
	}
	failed := false
	if !*dry {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		for index, t := range r.Order {
			if ctx.Err() != nil {
				failed = true
				break
			}
			model, err := cfg.New(*timeout)
			if err != nil {
				return err
			}
			callCtx, cancel := context.WithTimeout(ctx, *timeout)
			result := runOne(callCtx, s.Cases[t.CaseIndex], t, model, cfg.Model, cfg.ContextTokens)
			cancel()
			r.Results = append(r.Results, result)
			failed = failed || result.Status != "succeeded"
			fmt.Fprintf(os.Stderr, "%d/%d %s window=%d status=%s action=%s calls=%d\n", index+1, r.Planned, result.CaseID, t.Window, result.Status, result.Decision.Action, len(result.Calls))
		}
		r.Completed = len(r.Results) == r.Planned
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(r); err != nil {
		return err
	}
	if failed {
		return errors.New("one or more executions failed or run interrupted; private report preserved")
	}
	return nil
}

func main() {
	if err := execute(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
