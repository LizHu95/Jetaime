package memory

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
)

type EmbeddingConfig struct {
	BaseURL       string `json:"base_url"`
	Model         string `json:"model"`
	Revision      string `json:"revision"`
	APIKeyEnv     string `json:"api_key_env"`
	Dimension     int    `json:"dimension"`
	MaxInputBytes int    `json:"max_input_bytes"`
}
type Config struct {
	Embedding  EmbeddingConfig `json:"embedding"`
	Extraction llm.Config      `json:"extraction"`
}

func LoadConfig(path string) (Config, error) {
	var cfg Config
	if path == "" {
		path = "memory.json"
		if _, err := os.Stat(path); os.IsNotExist(err) {
			path = "../../local/memory.json"
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return cfg, err
	}
	defer func() { _ = f.Close() }()
	d := json.NewDecoder(io.LimitReader(f, 1<<20))
	d.DisallowUnknownFields()
	if err = d.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("invalid memory configuration")
	}
	if err = d.Decode(new(any)); err != io.EOF {
		return cfg, fmt.Errorf("memory configuration must contain one object")
	}
	return cfg, cfg.Validate()
}
func (c Config) Validate() error {
	e := c.Embedding
	u, err := url.Parse(e.BaseURL)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Contains(e.BaseURL, "YOUR_") {
		return fmt.Errorf("invalid embedding base URL")
	}
	for _, v := range []string{e.Model, e.Revision, e.APIKeyEnv} {
		if strings.TrimSpace(v) == "" || strings.Contains(v, "YOUR_") {
			return fmt.Errorf("embedding model, revision and api_key_env must be configured")
		}
	}
	if e.Dimension < 1 || e.Dimension > 16000 || e.MaxInputBytes < MaxBatchBytes+2048 || e.MaxInputBytes > 1<<20 {
		return fmt.Errorf("invalid embedding dimension or max_input_bytes")
	}
	if strings.Contains(c.Extraction.Model, "YOUR_") || strings.Contains(c.Extraction.BaseURL, "YOUR_") {
		return fmt.Errorf("extraction model must be configured")
	}
	return c.Extraction.Validate()
}
func (c Config) ModelVersion() string {
	// Credentials and timeouts do not change the vector space.
	return hash(c.Embedding.BaseURL + "\n" + c.Embedding.Model + "\n" + c.Embedding.Revision)
}
func (c Config) Pipeline() string {
	b, _ := json.Marshal(c.Extraction.Identity())
	return PipelineVersion + ":" + hash(string(b)+c.ModelVersion()+fmt.Sprint(c.Embedding.Dimension))
}
func (c Config) Build() (*Service, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	model, err := c.Extraction.New(90 * time.Second)
	if err != nil {
		return nil, err
	}
	embed, err := newEmbedding(c.Embedding)
	if err != nil {
		return nil, err
	}
	return &Service{Config: c, Embedder: embed, Extractor: ModelExtractor{Model: model, Config: c.Extraction}}, nil
}
