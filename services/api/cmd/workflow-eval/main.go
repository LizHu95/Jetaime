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

func execute(ctx context.Context, input request, base, model string, timeout time.Duration, tokens int) (result, error) {
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
	provider, err := assistant.NewOllama(base, timeout)
	if err != nil {
		return result{}, err
	}
	workflow, err := assistant.NewWorkflow(store, provider, model, tokens, "")
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
	base := flag.String("ollama-url", "http://127.0.0.1:11434", "Ollama URL")
	model := flag.String("model", "qwen3.5:9b", "installed model")
	timeout := flag.Duration("model-timeout", 3*time.Minute, "model timeout")
	tokens := flag.Int("context-tokens", 8192, "context capacity")
	flag.Parse()
	if flag.NArg() != 0 {
		fatal(fmt.Errorf("unexpected arguments"))
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
	output, err := execute(ctx, input, *base, *model, *timeout, *tokens)
	if err != nil {
		fatal(err)
	}
	if err = json.NewEncoder(os.Stdout).Encode(output); err != nil {
		fatal(err)
	}
}
func fatal(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
