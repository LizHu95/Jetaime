// eval-replay is an isolated local entry point. It does not load application
// Config, open PostgreSQL, start workers or send messages to a live server.
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
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/replay"
	"github.com/LizHu95/Jetaime/services/api/internal/telemetry"
)

var buildBranch, buildCommit, buildDirty, buildHarnessSHA256, buildToolPolicy string
var failedCases = errors.New("one or more replay cases failed; see report")

type source struct {
	HarnessSHA256 string `json:"harnessSha256"`
	ToolPolicy    string `json:"toolPolicy"`
	Branch        string `json:"branch"`
	Commit        string `json:"commit"`
	Dirty         string `json:"dirty"`
}

func identity() source {
	result := source{Branch: buildBranch, Commit: buildCommit, Dirty: buildDirty, HarnessSHA256: buildHarnessSHA256, ToolPolicy: buildToolPolicy}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" && result.Commit == "" {
				result.Commit = s.Value
			}
			if s.Key == "vcs.modified" && result.Dirty == "" {
				result.Dirty = s.Value
			}
		}
	}
	return result
}

type report struct {
	Version        string          `json:"formatVersion"`
	StartedAt      time.Time       `json:"startedAt"`
	Source         source          `json:"source"`
	SuiteVersion   string          `json:"suiteVersion"`
	SuiteSHA256    string          `json:"suiteSha256"`
	SnapshotSHA256 string          `json:"snapshotSha256"`
	Model          *llm.Config     `json:"model,omitempty"`
	DryRun         bool            `json:"dryRun"`
	ValidatedCases int             `json:"validatedCases"`
	Results        []replay.Result `json:"results"`
}

func main() {
	if err := execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func execute() error {
	fs := flag.NewFlagSet("eval-replay", flag.ContinueOnError)
	snapshotPath := fs.String("snapshot", "", "exported messages.jsonl (read only)")
	casesPath := fs.String("cases", "", "frozen suite JSON with matching snapshot SHA256")
	only := fs.String("case-id", "", "run just one case")
	output := fs.String("output", "", "new report path, or - for stdout")
	dry := fs.Bool("dry-run", false, "validate only: no model or trace network calls")
	info := fs.Bool("build-info", false, "print binary source identity only")
	repetitions := fs.Int("repetitions", 1, "independent repetitions per case (1-20)")
	tokens := fs.Int("context-tokens", 98304, "default model context capacity")
	timeout := fs.Duration("case-timeout", 5*time.Minute, "entire case deadline")
	traceEnabled := fs.Bool("trace", false, "explicitly export replay traces using Phoenix environment settings")
	modelFlags := llm.RegisterFlags(fs)
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments")
	}
	if *info {
		return json.NewEncoder(os.Stdout).Encode(identity())
	}
	if *snapshotPath == "" || *casesPath == "" || *output == "" || *repetitions < 1 || *repetitions > 20 || *timeout <= 0 {
		return fmt.Errorf("snapshot, cases and output required; repetitions must be 1-20 and timeout positive")
	}
	if *output != "-" {
		if _, err := os.Stat(*output); !os.IsNotExist(err) {
			return fmt.Errorf("output must be a new file")
		}
	}
	snapshot, err := replay.LoadSnapshot(*snapshotPath)
	if err != nil {
		return err
	}
	suite, err := replay.LoadSuite(*casesPath, snapshot)
	if err != nil {
		return err
	}
	selected := []replay.Case{}
	for _, c := range suite.Cases {
		if *only == "" || c.ID == *only {
			selected = append(selected, c)
		}
	}
	if len(selected) == 0 {
		return fmt.Errorf("case-id not found")
	}
	raw, err := os.ReadFile(*casesPath)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(raw)
	result := report{Version: "replay-v1", StartedAt: time.Now().UTC(), Source: identity(), SuiteVersion: suite.Version, SuiteSHA256: hex.EncodeToString(digest[:]), SnapshotSHA256: snapshot.SHA256, DryRun: *dry, ValidatedCases: len(selected), Results: []replay.Result{}}
	if *dry {
		return writeReport(*output, result)
	}
	cfg, err := modelFlags.Resolve(*tokens)
	if err != nil {
		return err
	}
	// Fixed model per experiment: fallback switching would confound A/B/C results.
	cfg.Fallbacks = nil
	result.Model = &cfg
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *traceEnabled {
		if os.Getenv("JETAIME_PHOENIX_TRACES_ENDPOINT") == "" {
			return fmt.Errorf("trace requires JETAIME_PHOENIX_TRACES_ENDPOINT")
		}
		_ = os.Setenv("JETAIME_DEPLOY_BRANCH", result.Source.Branch)
		_ = os.Setenv("JETAIME_DEPLOY_COMMIT", result.Source.Commit)
		_ = os.Setenv("JETAIME_DEPLOY_DIRTY", result.Source.Dirty)
		shutdown, e := telemetry.Init(ctx)
		if e != nil {
			return e
		}
		defer func() {
			flush, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = shutdown(flush)
		}()
	}
	// Parent context is used by Phoenix's task wrapper, never by business transport.
	ctx = telemetry.Restore(ctx, os.Getenv("JETAIME_EVAL_TRACEPARENT"), "")
	failed := false
	for _, c := range selected {
		for rep := 1; rep <= *repetitions; rep++ {
			if ctx.Err() != nil {
				failed = true
				break
			}
			model, e := cfg.New(*timeout)
			if e != nil {
				return e
			}
			caseCtx, cancel := context.WithTimeout(ctx, *timeout)
			row, e := replay.Run(caseCtx, snapshot, c, model, cfg.Model, cfg.ContextTokens, rep)
			cancel()
			if e != nil {
				return e
			}
			result.Results = append(result.Results, row)
			failed = failed || row.Status != "succeeded"
		}
	}
	if ctx.Err() != nil {
		failed = true
	}
	if err := writeReport(*output, result); err != nil {
		return err
	}
	if failed {
		return failedCases
	}
	return nil
}
func writeReport(path string, value any) error {
	var out io.Writer = os.Stdout
	if path == "-" {
		encoder := json.NewEncoder(out)
		return encoder.Encode(value)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path+".partial", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "  ")
	err = encoder.Encode(value)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	// Link refuses to replace an existing report even if another run raced us.
	if err = os.Link(path+".partial", path); err != nil {
		return err
	}
	return os.Remove(path + ".partial")
}
