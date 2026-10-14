package llm

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
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
