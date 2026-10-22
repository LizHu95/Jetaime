package llm

import (
	"context"
	"encoding/json"
)

// ChatModel provides non-streaming text chat generation.
type ChatModel interface {
	Call(context.Context, Request) (Result, error)
}

// Request retains the workflow trace format. Provider-specific wire parameters
// are translated by the adapter; cloud requests never receive Ollama options.
type Request struct {
	Model    string          `json:"model"`
	Messages []Message       `json:"messages"`
	Format   json.RawMessage `json:"format"`
	Stream   bool            `json:"stream"`
	Think    bool            `json:"think"`
	Options  Options         `json:"options"`
}
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type Options struct {
	Temperature float64 `json:"temperature"`
	NumCtx      int     `json:"num_ctx"`
	NumPredict  int     `json:"num_predict"`
}
type Result struct {
	Content          string
	Raw              any
	PromptTokens     *int
	CompletionTokens *int
}
type Identity struct {
	Profile    string `json:"profile,omitempty"`
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	BaseURL    string `json:"baseUrl"`
	OutputMode string `json:"outputMode"`
}
