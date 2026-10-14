// workflow-eval executes the same V2 Workflow with isolated, disposable state.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/assistant"
	"github.com/LizHu95/Jetaime/services/api/internal/llm"
)

type request struct {
	Fixture  assistant.Fixture `json:"fixture"`
	State    json.RawMessage   `json:"state,omitempty"`
	GroupID  string            `json:"groupId"`
	ActorID  string            `json:"actorId"`
	Kind     string            `json:"kind"`
	Question string            `json:"question,omitempty"`
}
type result struct {
	Run   assistant.Run `json:"run"`
	Error string        `json:"error,omitempty"`
}

func executeConfigured(ctx context.Context, input request, config llm.Config, timeout time.Duration) (result, error) {
	dir, err := os.MkdirTemp("", "jetaime-workflow-eval-*")
	if err != nil {
		return result{}, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	b, err := json.Marshal(input.Fixture)
	if err != nil {
		return result{}, err
	}
	fixture, state := filepath.Join(dir, "history.json"), filepath.Join(dir, "state.json")
	if err = os.WriteFile(fixture, b, 0600); err != nil {
		return result{}, err
	}
	if len(input.State) > 0 {
		if err = os.WriteFile(state, input.State, 0600); err != nil {
			return result{}, err
		}
	}
	store, err := assistant.OpenStore(fixture, state)
	if err != nil {
		return result{}, err
	}
	provider, err := config.New(timeout)
	if err != nil {
		return result{}, err
	}
	workflow, err := assistant.NewWorkflow(store, provider, config.Model, config.ContextTokens, "")
	if err != nil {
		return result{}, err
	}
	run, err := workflow.Execute(ctx, input.GroupID, input.ActorID, input.Kind, input.Question, "")
	output := result{Run: run}
	if err != nil {
		output.Error = err.Error()
	}
	return output, nil
}
func main() {
	modelFlags := llm.RegisterFlags(flag.CommandLine)
	printConfig := flag.Bool("print-model-config", false, "print validated public model configuration without inference")
	timeout := flag.Duration("model-timeout", 3*time.Minute, "model timeout")
	tokens := flag.Int("context-tokens", 8192, "context capacity")
	flag.Parse()
	if flag.NArg() != 0 {
		fatal(fmt.Errorf("unexpected arguments"))
	}
	config, err := modelFlags.Resolve(*tokens)
	if err != nil {
		fatal(err)
	}
	if _, err := config.New(*timeout); err != nil {
		fatal(err)
	}
	if *printConfig {
		public := struct {
			llm.Config
			SelectedProfile string `json:"selected_profile,omitempty"`
		}{config, config.Profile}
		if err := json.NewEncoder(os.Stdout).Encode(public); err != nil {
			fatal(err)
		}
		return
	}
	decoder := json.NewDecoder(io.LimitReader(os.Stdin, 4<<20))
	decoder.DisallowUnknownFields()
	var input request
	if err := decoder.Decode(&input); err != nil {
		fatal(err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		fatal(fmt.Errorf("expected one JSON request"))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	output, err := executeConfigured(ctx, input, config, *timeout)
	if err != nil {
		fatal(err)
	}
	if err = json.NewEncoder(os.Stdout).Encode(output); err != nil {
		fatal(err)
	}
}
func fatal(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
