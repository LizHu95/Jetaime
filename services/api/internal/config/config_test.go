package config

import (
	"errors"
	"flag"
	"path/filepath"
	"testing"
)

func TestModesResolveOnlyRequiredDependencies(t *testing.T) {
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
