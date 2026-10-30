// Package config translates command-line arguments into explicit application settings.
package config

import (
	"flag"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/memory"
)

// Config is fully parsed before the application starts. No flag pointers escape here.
type Config struct {
	DeployBranch   string
	DeployCommit   string
	DeployDirty    string
	Address        string
	DatabaseURL    string
	EnableChatAI   bool // Background main Agent for authenticated group chat.
	MigrateOnly    bool
	Model          llm.Config
	ModelTimeout   time.Duration
	GaodeAPIKey    string `json:"-"` // Server-only credential for the official Amap MCP.
	Memory         memory.Config
	MemoryBackfill bool
	MemoryCheck    bool
}

func Parse(args []string) (Config, error) {
	var cfg Config
	var memoryConfig string
	commandLine := flag.NewFlagSet("api", flag.ContinueOnError)
	commandLine.StringVar(&cfg.Address, "addr", "127.0.0.1:8081", "HTTP listen IP and port")
	commandLine.BoolVar(&cfg.MigrateOnly, "migrate", false, "apply PostgreSQL migrations and exit")
	commandLine.StringVar(&memoryConfig, "memory-config", os.Getenv("JETAIME_MEMORY_CONFIG"), "memory JSON configuration; defaults to memory.json or ../../local/memory.json")
	commandLine.BoolVar(&cfg.MemoryCheck, "memory-check", false, "check memory configuration against the database without changing memory or running workers")
	commandLine.BoolVar(&cfg.MemoryBackfill, "memory-backfill", false, "resume historical memory backfill and exit; never reset progress")
	commandLine.BoolVar(&cfg.EnableChatAI, "chat-ai", true, "enable the main Agent for authenticated group chat")
	commandLine.DurationVar(&cfg.ModelTimeout, "model-timeout", 5*time.Minute, "maximum model execution time")
	contextTokens := commandLine.Int("context-tokens", 98304, "model context capacity")
	modelOptions := llm.RegisterFlags(commandLine)
	if err := commandLine.Parse(args); err != nil {
		return cfg, err
	}
	if commandLine.NArg() != 0 {
		return cfg, fmt.Errorf("unexpected arguments")
	}
	cfg.DeployBranch = os.Getenv("JETAIME_DEPLOY_BRANCH")
	cfg.DeployCommit = os.Getenv("JETAIME_DEPLOY_COMMIT")
	cfg.DeployDirty = os.Getenv("JETAIME_DEPLOY_DIRTY")
	cfg.DatabaseURL = os.Getenv("JETAIME_DATABASE_URL")
	cfg.GaodeAPIKey = os.Getenv("JETAIME_GAODE_API_KEY")
	if err := cfg.validateAddress(); err != nil {
		return cfg, err
	}
	if (cfg.MigrateOnly && (cfg.MemoryBackfill || cfg.MemoryCheck)) || (cfg.MemoryCheck && cfg.MemoryBackfill) {
		return cfg, fmt.Errorf("migrate, memory-check and memory-backfill must run separately")
	}
	if !cfg.MigrateOnly {
		var err error
		cfg.Memory, err = memory.LoadConfig(memoryConfig)
		if err != nil {
			return cfg, err
		}
	}
	if cfg.EnableChatAI && !cfg.MigrateOnly && !cfg.MemoryBackfill && !cfg.MemoryCheck {
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
	return nil
}
