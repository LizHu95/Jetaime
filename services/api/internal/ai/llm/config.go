package llm

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config contains no credential value. Environment variables are resolved once
// at startup; configured fallback profiles may be selected after a call fails.
type Config struct {
	Provider       string   `json:"provider"`
	BaseURL        string   `json:"base_url"`
	Model          string   `json:"model"`
	APIKeyEnv      string   `json:"api_key_env,omitempty"`
	OutputMode     string   `json:"output_mode,omitempty"`
	EnableThinking *bool    `json:"enable_thinking,omitempty"`
	ContextTokens  int      `json:"context_tokens,omitempty"`
	Profile        string   `json:"-"`
	Fallbacks      []Config `json:"-"`
}

type Flags struct {
	Provider, BaseURL, OllamaURL, Model, APIKeyEnv, OutputMode, File, Profile, EnableThinking string
	flags                                                                                     *flag.FlagSet
}

const defaultConfigPath = "../../local/models.json"

func RegisterFlags(fs *flag.FlagSet) *Flags {
	f := &Flags{flags: fs}
	fs.StringVar(&f.Provider, "provider", "ollama", "ollama or openai-compatible")
	fs.StringVar(&f.BaseURL, "model-url", "", "provider base URL (defaults to OLLAMA or LLM_BASE_URL)")
	fs.StringVar(&f.OllamaURL, "ollama-url", "http://127.0.0.1:11434", "Ollama base URL; legacy alias")
	fs.StringVar(&f.Model, "model", "qwen3.5:9b", "primary model selected at startup")
	fs.StringVar(&f.APIKeyEnv, "api-key-env", "DASHSCOPE_API_KEY", "environment variable containing cloud API key")
	fs.StringVar(&f.OutputMode, "output-mode", "json_schema", "cloud structured output: json_schema or json_object")
	fs.StringVar(&f.EnableThinking, "enable-thinking", "", "optional cloud enable_thinking: true or false")
	fs.StringVar(&f.File, "model-config", "", "JSON model profile configuration file")
	fs.StringVar(&f.Profile, "model-profile", "", "profile ID selected from model-config")
	return f
}

func (f *Flags) Resolve(tokens int) (Config, error) {
	c := Config{Provider: f.Provider, BaseURL: f.BaseURL, Model: f.Model, APIKeyEnv: f.APIKeyEnv, OutputMode: f.OutputMode, ContextTokens: tokens}
	filePath := f.File
	if filePath == "" && !f.directConfiguration() {
		if _, err := os.Stat(defaultConfigPath); err == nil {
			filePath = defaultConfigPath
		} else if !os.IsNotExist(err) {
			return c, err
		}
	}
	if filePath != "" {
		file, err := os.Open(filePath)
		if err != nil {
			return c, err
		}
		defer func() { _ = file.Close() }()
		var profiles struct {
			DefaultProfile   string            `json:"default_profile"`
			FallbackProfiles []string          `json:"fallback_profiles,omitempty"`
			Models           map[string]Config `json:"models"`
		}
		d := json.NewDecoder(io.LimitReader(file, 1<<20))
		d.DisallowUnknownFields()
		if err := d.Decode(&profiles); err != nil {
			return c, fmt.Errorf("invalid model configuration")
		}
		if err := d.Decode(new(any)); err != io.EOF {
			return c, fmt.Errorf("model configuration must contain one JSON object")
		}
		var ok bool
		profile := f.Profile
		if profile == "" {
			profile = profiles.DefaultProfile
		}
		if profile == "" {
			return c, fmt.Errorf("model configuration requires default_profile or model-profile")
		}
		c, ok = profiles.Models[profile]
		if !ok {
			return c, fmt.Errorf("unknown model profile %q", profile)
		}
		c.Profile = profile
		if c.ContextTokens == 0 {
			c.ContextTokens = tokens
		}
		seen := map[string]bool{profile: true}
		for _, name := range profiles.FallbackProfiles {
			if seen[name] {
				continue
			}
			fallback, exists := profiles.Models[name]
			if !exists {
				return c, fmt.Errorf("unknown fallback model profile %q", name)
			}
			seen[name] = true
			fallback.Profile = name
			if fallback.ContextTokens == 0 {
				fallback.ContextTokens = tokens
			}
			if fallback.OutputMode == "" {
				fallback.OutputMode = "json_schema"
			}
			c.Fallbacks = append(c.Fallbacks, fallback)
		}
	} else {
		if f.EnableThinking != "" {
			value, err := strconv.ParseBool(f.EnableThinking)
			if err != nil {
				return c, fmt.Errorf("enable-thinking must be true or false")
			}
			c.EnableThinking = &value
		}
		if f.Profile != "" {
			return c, fmt.Errorf("model-profile requires model-config")
		}
		if c.BaseURL == "" {
			if c.Provider == "ollama" {
				c.BaseURL = f.OllamaURL
			} else {
				c.BaseURL = os.Getenv("LLM_BASE_URL")
			}
		}
	}
	if c.OutputMode == "" {
		c.OutputMode = "json_schema"
	}
	return c, c.Validate()
}

// Explicit provider arguments preserve legacy use without silently selecting a
// different local profile. A profile override still selects the default file.
func (f *Flags) directConfiguration() bool {
	if f.Profile != "" {
		return false
	}
	direct := false
	if f.flags != nil {
		f.flags.Visit(func(v *flag.Flag) {
			switch v.Name {
			case "model-config", "provider", "model-url", "ollama-url", "model", "api-key-env", "output-mode", "enable-thinking":
				direct = true
			}
		})
	}
	return direct
}

func (c Config) Validate() error {
	for _, fallback := range c.Fallbacks {
		if len(fallback.Fallbacks) != 0 {
			return fmt.Errorf("nested model fallbacks are not supported")
		}
		if err := fallback.Validate(); err != nil {
			return fmt.Errorf("fallback profile %q: %w", fallback.Profile, err)
		}
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("invalid model base URL")
	}
	if c.Provider != "ollama" && c.Provider != "openai-compatible" {
		return fmt.Errorf("unsupported model provider %q", c.Provider)
	}
	if strings.TrimSpace(c.Model) == "" || len(c.Model) > 150 {
		return fmt.Errorf("model name is required and must be <=150 bytes")
	}
	if c.ContextTokens < 4096 {
		return fmt.Errorf("context-tokens must be >=4096")
	}
	if c.Provider == "openai-compatible" {
		if c.OutputMode != "json_schema" && c.OutputMode != "json_object" {
			return fmt.Errorf("output-mode must be json_schema or json_object")
		}
		if c.APIKeyEnv == "" {
			return fmt.Errorf("api_key_env is required")
		}
	}
	return nil
}

func (c Config) Identity() Identity {
	return Identity{Profile: c.Profile, Provider: c.Provider, Model: c.Model, BaseURL: strings.TrimRight(c.BaseURL, "/"), OutputMode: c.OutputMode}
}

func (c Config) New(timeout time.Duration) (*Adapter, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("model timeout must be positive")
	}
	key := ""
	if c.Provider == "openai-compatible" {
		key = os.Getenv(c.APIKeyEnv)
		if strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("missing API key environment variable %s", c.APIKeyEnv)
		}
	}
	adapter := &Adapter{config: c, timeout: timeout, key: key}
	if len(c.Fallbacks) > 0 {
		configs := append([]Config{c}, c.Fallbacks...)
		attemptTimeout := timeout / time.Duration(len(configs))
		if attemptTimeout <= 0 {
			return nil, fmt.Errorf("model timeout too short for fallback candidates")
		}
		for _, candidate := range configs {
			candidate.Fallbacks = nil
			client, err := candidate.New(attemptTimeout)
			if err != nil {
				return nil, err
			}
			adapter.candidates = append(adapter.candidates, client)
		}
	}
	return adapter, nil
}
