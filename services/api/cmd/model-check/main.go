// model-check validates model configuration without running inference.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
)

func main() {
	modelFlags := llm.RegisterFlags(flag.CommandLine)
	timeout := flag.Duration("model-timeout", 3*time.Minute, "model timeout")
	tokens := flag.Int("context-tokens", 16384, "context capacity")
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
	public := struct {
		llm.Config
		SelectedProfile string `json:"selected_profile,omitempty"`
	}{config, config.Profile}
	if err := json.NewEncoder(os.Stdout).Encode(public); err != nil {
		fatal(err)
	}
}

func fatal(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
