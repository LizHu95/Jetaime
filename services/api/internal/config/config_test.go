package config

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

func TestModesResolveOnlyRequiredDependencies(t *testing.T) {
	t.Setenv("JETAIME_MEMORY_CONFIG", writeMemoryConfig(t))
	for _, tc := range []struct {
		name                               string
		args                               []string
		accounts, chatAI, console, migrate bool
	}{
		{"production", []string{"-provider", "ollama", "-model", "test"}, true, true, false, false},
		{"accounts without AI", []string{"-chat-ai=false", "-model-config", "missing.json"}, true, false, false, false},
		{"health only", []string{"-auth=false", "-model-config", "missing.json"}, false, false, false, false},
		{"migration", []string{"-migrate", "-model-config", "missing.json"}, true, true, false, true},
		{"local debug console", []string{"-auth=false", "-debug-console", "-provider", "ollama", "-model", "test"}, false, false, true, false},
		{"legacy console alias", []string{"-auth=false", "-assistant", "-provider", "ollama", "-model", "test"}, false, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Parse(tc.args)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.EnableAccountAPI != tc.accounts || cfg.EnableChatAI != tc.chatAI || cfg.DebugConsole.Enabled != tc.console || cfg.MigrateOnly != tc.migrate {
				t.Fatalf("wrong mode: accounts=%t chatAI=%t console=%t migrate=%t", cfg.EnableAccountAPI, cfg.EnableChatAI, cfg.DebugConsole.Enabled, cfg.MigrateOnly)
			}
			if (tc.chatAI || tc.console) && !tc.migrate {
				if cfg.Model.Model != "test" {
					t.Fatal("model configuration missing")
				}
			} else if cfg.Model.Model != "" {
				t.Fatal("unused model was resolved")
			}
			if tc.console && !filepath.IsAbs(cfg.DebugConsole.HistoryFile) {
				t.Fatal("console paths not resolved")
			}
		})
	}
}

func TestInvalidModeAndConsoleBoundary(t *testing.T) {
	for _, args := range [][]string{
		{"-auth=false", "-chat-ai=true"},
		{"-auth=false", "-debug-console", "-addr", "0.0.0.0:8081"},
		{"-auth=false", "-assistant", "-addr", "0.0.0.0:8081"},
		{"-auth=false", "-debug-console", "-fixture", "same.json", "-state", "same.json"},
		{"-auth=false", "-addr", "localhost:8081"},
		{"-auth=false", "unexpected"},
	} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("invalid configuration accepted: %v", args)
		}
	}
	if _, err := Parse([]string{"-help"}); !errors.Is(err, flag.ErrHelp) {
		t.Fatal("help exit lost", err)
	}
}

func TestMemoryPlaceholderAndBackfillModes(t *testing.T) {
	if _, err := Parse([]string{"-chat-ai=false", "-memory-config", "../../config/memory.example.json", "-model-config", "missing.json"}); err == nil {
		t.Fatal("API accepted placeholder memory config")
	}
	if _, err := Parse([]string{"-chat-ai=false", "-memory-config", "missing-memory.json"}); err == nil {
		t.Fatal("API silently skipped memory when config missing")
	}
	if _, err := Parse([]string{"-memory-backfill", "-memory-config", "../../config/memory.example.json"}); err == nil {
		t.Fatal("backfill accepted unconfigured models")
	}
	if _, err := Parse([]string{"-migrate", "-memory-backfill"}); err == nil {
		t.Fatal("migration and backfill mixed")
	}
	// Health/debug modes do not require an unused memory configuration.
	if _, err := Parse([]string{"-auth=false", "-memory-config", "missing.json"}); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryBackfillDoesNotResolveMainAgentModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.json")
	data := `{"embedding":{"base_url":"http://unused","model":"embed-test","revision":"v1","api_key_env":"TEST_KEY","dimension":3,"max_input_bytes":24576},"extraction":{"provider":"ollama","base_url":"http://unused","model":"extract-test","context_tokens":32768}}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Parse([]string{"-memory-backfill", "-memory-config", path, "-model-config", "missing-main-model.json"})
	if err != nil || !cfg.MemoryBackfill || cfg.Model.Model != "" {
		t.Fatal("backfill resolved unused main Agent dependencies", err)
	}
}

func writeMemoryConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "memory.json")
	data := `{"embedding":{"base_url":"http://unused","model":"embed-test","revision":"v1","api_key_env":"TEST_KEY","dimension":3,"max_input_bytes":24576},"extraction":{"provider":"ollama","base_url":"http://unused","model":"extract-test","context_tokens":32768}}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
