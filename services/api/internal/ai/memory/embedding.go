package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/LizHu95/Jetaime/services/api/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"time"
)

// Compatible with the Bailian float Embedding API; model and dimensions have no defaults.
type embeddingClient struct {
	config EmbeddingConfig
	key    string
	client *http.Client
}

func newEmbedding(c EmbeddingConfig) (*embeddingClient, error) {
	key := os.Getenv(c.APIKeyEnv)
	if strings.TrimSpace(key) == "" {
		return nil, fmt.Errorf("missing embedding API key environment variable %s", c.APIKeyEnv)
	}
	return &embeddingClient{c, key, &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (e *embeddingClient) Embed(ctx context.Context, text string) (vector []float32, callErr error) {
	ctx, span := telemetry.Start(ctx, "memory.embedding", "EMBEDDING", attribute.String("langfuse.observation.model.name", e.config.Model), attribute.Int("memory.embedding_dimension", e.config.Dimension))
	defer func() { telemetry.End(span, callErr) }()
	if strings.TrimSpace(text) == "" || len(text) > e.config.MaxInputBytes {
		return nil, ErrInvalid
	}
	b, err := json.Marshal(map[string]any{"model": e.config.Model, "input": []string{text}, "dimensions": e.config.Dimension, "encoding_format": "float"})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(e.config.BaseURL, "/")+"/embeddings", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+e.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedding transport failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embedding HTTP status %d", resp.StatusCode)
	}
	var out struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
	}
	d := json.NewDecoder(io.LimitReader(resp.Body, 1<<20))
	if err = d.Decode(&out); err != nil {
		return nil, fmt.Errorf("invalid embedding response")
	}
	if len(out.Data) != 1 || out.Data[0].Index != 0 {
		return nil, ErrInvalid
	}
	v := out.Data[0].Embedding
	span.SetAttributes(attribute.Int("memory.embedding_tokens", out.Usage.TotalTokens))
	if err = validateVector(v, e.config.Dimension); err != nil {
		return nil, err
	}
	return v, nil
}
func validateVector(v []float32, dim int) error {
	if len(v) != dim {
		return ErrInvalid
	}
	norm := float64(0)
	for _, x := range v {
		if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
			return ErrInvalid
		}
		norm += float64(x) * float64(x)
	}
	if norm == 0 {
		return ErrInvalid
	}
	return nil
}
func vectorText(v []float32) string { b, _ := json.Marshal(v); return string(b) }
