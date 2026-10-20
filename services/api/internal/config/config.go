// Package config translates command-line arguments into explicit application settings.
package config

import (
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/llm"
)

// Config is fully parsed before the application starts. No flag pointers escape here.
type Config struct {
	Address          string
	DatabaseURL      string
	EnableDatabase   bool
	EnableAccountAPI bool // Accounts, collaboration, chat and device notifications.
	EnableChatAI     bool // Background main Agent for authenticated group chat.
	MigrateOnly      bool
	DebugConsole     DebugConsole
	Model            llm.Config
	ModelTimeout     time.Duration
}

// DebugConsole is the local simulation UI, separate from the production main Agent.
type DebugConsole struct {
	Enabled     bool
	HistoryFile string
	StateFile   string
	RunLogFile  string
}

func Parse(args []string) (Config, error) {
	var cfg Config
	commandLine := flag.NewFlagSet("api", flag.ContinueOnError)
	commandLine.StringVar(&cfg.Address, "addr", "127.0.0.1:8081", "HTTP listen IP and port")
	commandLine.BoolVar(&cfg.EnableDatabase, "database", false, "check PostgreSQL even when account APIs are disabled")
	commandLine.BoolVar(&cfg.EnableAccountAPI, "auth", true, "enable account, collaboration, chat and notification APIs")
	commandLine.BoolVar(&cfg.MigrateOnly, "migrate", false, "apply PostgreSQL migrations and exit")
	commandLine.BoolVar(&cfg.EnableChatAI, "chat-ai", true, "enable the main Agent for authenticated group chat")
	commandLine.BoolVar(&cfg.DebugConsole.Enabled, "debug-console", false, "enable the local simulation UI at /page/assistant/; loopback only")
	commandLine.BoolVar(&cfg.DebugConsole.Enabled, "assistant", false, "legacy alias for -debug-console")
	commandLine.StringVar(&cfg.DebugConsole.HistoryFile, "fixture", "../../local/data/chat-history/history.fixture.json", "debug console history JSON")
	commandLine.StringVar(&cfg.DebugConsole.StateFile, "state", "../../local/data/assistant/state.json", "debug console persisted messages and tasks")
	commandLine.StringVar(&cfg.DebugConsole.RunLogFile, "log", "../../local/data/assistant/runs.jsonl", "debug console execution log")
	commandLine.DurationVar(&cfg.ModelTimeout, "model-timeout", 5*time.Minute, "maximum model execution time")
	contextTokens := commandLine.Int("context-tokens", 98304, "model context capacity")
	modelOptions := llm.RegisterFlags(commandLine)
	if err := commandLine.Parse(args); err != nil {
		return cfg, err
	}
	if commandLine.NArg() != 0 {
		return cfg, fmt.Errorf("unexpected arguments")
	}
	explicitChatAI := false
	commandLine.Visit(func(option *flag.Flag) {
		if option.Name == "chat-ai" {
			explicitChatAI = true
		}
	})
	if explicitChatAI && cfg.EnableChatAI && !cfg.EnableAccountAPI {
		return cfg, fmt.Errorf("chat-ai requires auth")
	}
	// The implicit chat-ai default applies only when account APIs are enabled.
	cfg.EnableChatAI = cfg.EnableAccountAPI && cfg.EnableChatAI
	cfg.DatabaseURL = os.Getenv("JETAIME_DATABASE_URL")
	if err := cfg.validateAddress(); err != nil {
		return cfg, err
	}
	if cfg.DebugConsole.Enabled {
		if err := cfg.DebugConsole.resolvePaths(); err != nil {
			return cfg, err
		}
	}
	if (cfg.EnableChatAI || cfg.DebugConsole.Enabled) && !cfg.MigrateOnly {
		var err error
		cfg.Model, err = modelOptions.Resolve(*contextTokens)
		if err != nil {
			return cfg, err
		}
	}
	return cfg, nil
}

func (c Config) validateAddress() error {
	host, _, err := net.SplitHostPort(c.Address)
	if err != nil || net.ParseIP(host) == nil {
		return fmt.Errorf("addr must use an IP and port")
	}
	if c.DebugConsole.Enabled && !net.ParseIP(host).IsLoopback() {
		return fmt.Errorf("debug console requires a loopback listen IP")
	}
	return nil
}

func (c *DebugConsole) resolvePaths() error {
	seen := map[string]bool{}
	for _, path := range []*string{&c.HistoryFile, &c.StateFile, &c.RunLogFile} {
		absolute, err := filepath.Abs(*path)
		if err != nil {
			return err
		}
		if seen[absolute] {
			return fmt.Errorf("fixture, state and log must be separate files")
		}
		seen[absolute] = true
		*path = absolute
	}
	return nil
}
