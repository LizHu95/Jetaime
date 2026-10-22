package llm

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStartupProfilesAndValidation(t *testing.T) {
	p := filepath.Join(t.TempDir(), "models.json")
	if err := os.WriteFile(p, []byte(`{"models":{"cloud":{"provider":"openai-compatible","base_url":"https://example.com/v1","model":"cloud-test","api_key_env":"TEST_PROFILE_KEY","context_tokens":16384}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	f := RegisterFlags(fs)
	if err := fs.Parse([]string{"-model-config", p, "-model-profile", "cloud"}); err != nil {
		t.Fatal(err)
	}
	c, err := f.Resolve(8192)
	if err != nil {
		t.Fatal(err)
	}
	if c.Model != "cloud-test" || c.ContextTokens != 16384 || c.Profile != "cloud" || c.OutputMode != "json_schema" {
		t.Fatalf("wrong profile: %+v", c)
	}
	t.Setenv("TEST_PROFILE_KEY", "")
	if _, err := c.New(1); err == nil {
		t.Fatal("missing key accepted")
	}
	for _, base := range []string{"", "file:///tmp/foo", "https://user:secret@example.com/v1", "https://example.com/v1?key=secret", "https://example.com/v1#secret"} {
		c.BaseURL = base
		if c.Validate() == nil {
			t.Errorf("invalid URL accepted %s", base)
		}
	}
	f.Profile = "missing"
	if _, err := f.Resolve(8192); err == nil {
		t.Fatal("unknown profile accepted")
	}
	if err := os.WriteFile(p, []byte(`{"models":{"cloud":{"api_key":"secret"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	f.Profile = "cloud"
	if _, err := f.Resolve(8192); err == nil {
		t.Fatal("literal key accepted")
	}
}

func TestLegacyStartupFlags(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	f := RegisterFlags(fs)
	if err := fs.Parse([]string{"-ollama-url", "http://localhost:1234", "-model", "local-test"}); err != nil {
		t.Fatal(err)
	}
	c, err := f.Resolve(8192)
	if err != nil || c.Provider != "ollama" || c.Model != "local-test" || c.BaseURL != "http://localhost:1234" {
		t.Fatalf("legacy flags broken %+v %v", c, err)
	}
}

func TestAutomaticDefaultProfileAndOverrides(t *testing.T) {
	root := t.TempDir()
	api := filepath.Join(root, "services", "api")
	if err := os.MkdirAll(api, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "local"), 0700); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"default_profile":"cloud","models":{"cloud":{"provider":"openai-compatible","base_url":"https://example.com/v1","model":"cloud-test","api_key_env":"TEST_KEY"},"local":{"provider":"ollama","base_url":"http://localhost:11434","model":"local-test"}}}`)
	if err := os.WriteFile(filepath.Join(root, "local", "models.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(api)
	for _, tc := range []struct {
		args           []string
		model, profile string
	}{
		{nil, "cloud-test", "cloud"},
		{[]string{"-model-profile", "local"}, "local-test", "local"},
		{[]string{"-model", "explicit-test"}, "explicit-test", ""},
	} {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		f := RegisterFlags(fs)
		if err := fs.Parse(tc.args); err != nil {
			t.Fatal(err)
		}
		c, err := f.Resolve(8192)
		if err != nil || c.Model != tc.model || c.Profile != tc.profile {
			t.Fatalf("wrong default/override %+v %v", c, err)
		}
	}
}

func TestFallbackProfiles(t *testing.T) {
	p := filepath.Join(t.TempDir(), "models.json")
	data := `{"default_profile":"primary","fallback_profiles":["primary","backup","backup","local"],"models":{"primary":{"provider":"ollama","base_url":"http://localhost:11434","model":"first"},"backup":{"provider":"openai-compatible","base_url":"https://example.com/v1","model":"second","api_key_env":"FALLBACK_TEST_KEY"},"local":{"provider":"ollama","base_url":"http://localhost:11434","model":"last","context_tokens":4096}}}`
	if err := os.WriteFile(p, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	f := RegisterFlags(fs)
	f.File = p
	c, err := f.Resolve(8192)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Fallbacks) != 2 || c.Fallbacks[0].Profile != "backup" || c.Fallbacks[0].ContextTokens != 8192 || c.Fallbacks[0].OutputMode != "json_schema" || c.Fallbacks[1].ContextTokens != 4096 {
		t.Fatalf("fallbacks: %+v", c)
	}
	t.Setenv("FALLBACK_TEST_KEY", "")
	if _, err := c.New(time.Second); err == nil {
		t.Fatal("missing fallback key accepted")
	}
	t.Setenv("FALLBACK_TEST_KEY", "test")
	if _, err := c.New(time.Second); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{strings.Replace(data, `"backup","backup"`, `"missing","backup"`, 1), strings.Replace(data, `"context_tokens":4096`, `"context_tokens":1`, 1)} {
		if err := os.WriteFile(p, []byte(invalid), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := f.Resolve(8192); err == nil {
			t.Fatal("invalid fallback accepted")
		}
	}
}
