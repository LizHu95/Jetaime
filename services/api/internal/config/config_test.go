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
		name            string
		args            []string
		chatAI, migrate bool
	}{
		{"production", []string{"-provider", "ollama", "-model", "test"}, true, false},
		{"accounts without AI", []string{"-chat-ai=false", "-model-config", "missing.json"}, false, false},
		{"migration", []string{"-migrate", "-model-config", "missing.json"}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Parse(tc.args)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.EnableChatAI != tc.chatAI || cfg.MigrateOnly != tc.migrate {
				t.Fatalf("wrong mode: chatAI=%t migrate=%t", cfg.EnableChatAI, cfg.MigrateOnly)
			}
			if tc.chatAI && !tc.migrate {
				if cfg.Model.Model != "test" {
					t.Fatal("model configuration missing")
				}
			} else if cfg.Model.Model != "" {
				t.Fatal("unused model was resolved")
			}
		})
	}
}

func TestInvalidModeAndAddress(t *testing.T) {
	for _, args := range [][]string{
		{"-auth=false"},
		{"-database"},
		{"-addr", "localhost:8081"},
		{"unexpected"},
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
	// Migration does not require memory configuration.
	if _, err := Parse([]string{"-migrate", "-memory-config", "missing.json", "-model-config", "missing.json"}); err != nil {
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

func TestMemoryCheckMode(t *testing.T) {
	t.Setenv("JETAIME_MEMORY_CONFIG", writeMemoryConfig(t))
	cfg, err := Parse([]string{"-memory-check", "-model-config", "missing.json"})
	if err != nil || !cfg.MemoryCheck || cfg.Model.Model != "" {
		t.Fatalf("check mode: %+v %v", cfg, err)
	}
	for _, args := range [][]string{{"-memory-check", "-migrate"}, {"-memory-check", "-memory-backfill"}, {"-memory-check", "-memory-config", "missing.json"}} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("accepted invalid check: %v", args)
		}
	}
}
