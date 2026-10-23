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
	Address          string
	DatabaseURL      string
	EnableDatabase   bool
	EnableAccountAPI bool // Accounts, collaboration, chat and device notifications.
	EnableChatAI     bool // Background main Agent for authenticated group chat.
	MigrateOnly      bool
	Model            llm.Config
	ModelTimeout     time.Duration
	GaodeAPIKey      string `json:"-"` // Server-only credential for the official Amap MCP.
	Memory           memory.Config
	MemoryBackfill   bool
}

func Parse(args []string) (Config, error) {
	var cfg Config
	var memoryConfig string
	commandLine := flag.NewFlagSet("api", flag.ContinueOnError)
	commandLine.StringVar(&cfg.Address, "addr", "127.0.0.1:8081", "HTTP listen IP and port")
	commandLine.BoolVar(&cfg.EnableDatabase, "database", false, "check PostgreSQL even when account APIs are disabled")
	commandLine.BoolVar(&cfg.EnableAccountAPI, "auth", true, "enable account, collaboration, chat and notification APIs")
	commandLine.BoolVar(&cfg.MigrateOnly, "migrate", false, "apply PostgreSQL migrations and exit")
	commandLine.StringVar(&memoryConfig, "memory-config", os.Getenv("JETAIME_MEMORY_CONFIG"), "memory JSON configuration; defaults to memory.json or ../../local/memory.json")
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
	cfg.GaodeAPIKey = os.Getenv("JETAIME_GAODE_API_KEY")
	if err := cfg.validateAddress(); err != nil {
		return cfg, err
	}
	if cfg.MigrateOnly && cfg.MemoryBackfill {
		return cfg, fmt.Errorf("migrate and memory-backfill must run separately")
	}
	if !cfg.MigrateOnly && (cfg.EnableAccountAPI || cfg.MemoryBackfill) {
		var err error
		cfg.Memory, err = memory.LoadConfig(memoryConfig)
		if err != nil {
			return cfg, err
		}
	}
	if cfg.EnableChatAI && !cfg.MigrateOnly && !cfg.MemoryBackfill {
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
