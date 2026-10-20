package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/telemetry"
	"github.com/cloudwego/eino-ext/components/model/ollama"
	"github.com/cloudwego/eino-ext/components/model/openai"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"go.opentelemetry.io/otel/attribute"
)

type Adapter struct {
	config  Config
	timeout time.Duration
	key     string
}

var _ ChatModel = (*Adapter)(nil)

func (a *Adapter) ModelIdentity() Identity { return a.config.Identity() }

// The SDK normalizes envelopes and Ollama drops Done. Capture the bounded wire
// response per call so incomplete responses and failure traces remain visible.
type captureTransport struct {
	body              []byte
	status            int
	ollamaTemperature *float64
}

func (t *captureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	// The Ollama SDK's float field uses omitempty, so temperature=0 would
	// silently become the model's default sampling temperature. Preserve the
	// workflow's explicit value on the wire while retaining the Eino adapter.
	if t.ollamaTemperature != nil && r.Method == http.MethodPost && r.URL.Path == "/api/chat" {
		data, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
		_ = r.Body.Close()
		if err != nil || len(data) > 1<<20 {
			return nil, fmt.Errorf("invalid model request size")
		}
		var payload map[string]json.RawMessage
		if err = json.Unmarshal(data, &payload); err != nil {
			return nil, fmt.Errorf("invalid model request")
		}
		var options map[string]json.RawMessage
		if len(payload["options"]) > 0 {
			if err = json.Unmarshal(payload["options"], &options); err != nil {
				return nil, fmt.Errorf("invalid model options")
			}
		}
		if options == nil {
			options = map[string]json.RawMessage{}
		}
		options["temperature"], err = json.Marshal(*t.ollamaTemperature)
		if err != nil {
			return nil, err
		}
		payload["options"], err = json.Marshal(options)
		if err != nil {
			return nil, err
		}
		data, err = json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		r = r.Clone(r.Context())
		r.Body = io.NopCloser(bytes.NewReader(data))
		r.ContentLength = int64(len(data))
		r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }
	}
	resp, err := http.DefaultTransport.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	t.status = resp.StatusCode
	b, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(b) > 1<<20 {
		return nil, fmt.Errorf("model response too large")
	}
	t.body = b
	resp.Body = io.NopCloser(bytes.NewReader(b))
	return resp, nil
}

func (a *Adapter) Call(ctx context.Context, in Request) (out Result, callErr error) {
	ctx, span := telemetry.Start(ctx, "llm.call", "LLM", attribute.String("langfuse.observation.model.name", a.config.Model), attribute.String("langfuse.observation.metadata.provider", a.config.Provider))
	if span.IsRecording() && strings.EqualFold(os.Getenv("JETAIME_TRACE_CAPTURE_CONTENT"), "true") {
		messages, err := json.Marshal(in.Messages)
		if err == nil {
			telemetry.Content(span, "langfuse.observation.input", string(messages))
		}
	}
	defer func() {
		telemetry.Content(span, "langfuse.observation.output", out.Content)
		usage := map[string]int{}
		if out.PromptTokens != nil {
			usage["input"] = *out.PromptTokens
		}
		if out.CompletionTokens != nil {
			usage["output"] = *out.CompletionTokens
		}
		if len(usage) > 0 {
			data, _ := json.Marshal(usage)
			span.SetAttributes(attribute.String("langfuse.observation.usage_details", string(data)))
		}
		telemetry.End(span, callErr)
	}()
	if in.Model != a.config.Model {
		return out, fmt.Errorf("model is fixed at startup; restart to switch")
	}
	if in.Stream || in.Think {
		return out, fmt.Errorf("workflow does not support streaming or per-request thinking overrides")
	}
	if !json.Valid(in.Format) {
		return out, fmt.Errorf("valid output schema is required")
	}
	if err := CheckContextBudget(in, a.config.ContextTokens); err != nil {
		return out, err
	}
	ctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()
	t := &captureTransport{}
	if a.config.Provider == "ollama" {
		temperature := in.Options.Temperature
		t.ollamaTemperature = &temperature
	}
	client := &http.Client{Transport: t, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var model einomodel.BaseChatModel
	var err error
	if a.config.Provider == "ollama" {
		options := &ollama.Options{Temperature: float32(in.Options.Temperature), NumPredict: in.Options.NumPredict}
		options.NumCtx = in.Options.NumCtx
		model, err = ollama.NewChatModel(ctx, &ollama.ChatModelConfig{BaseURL: a.config.BaseURL, HTTPClient: client, Model: in.Model, Format: in.Format, Thinking: &ollama.ThinkValue{Value: false}, Options: options})
	} else {
		format := &openai.ChatCompletionResponseFormat{Type: openai.ChatCompletionResponseFormatTypeJSONObject}

		temp := float32(in.Options.Temperature)
		model, err = openai.NewChatModel(ctx, &openai.ChatModelConfig{BaseURL: strings.TrimRight(a.config.BaseURL, "/"), APIKey: a.key, HTTPClient: client, Model: in.Model, Temperature: &temp, MaxTokens: &in.Options.NumPredict, ResponseFormat: format})
	}
	if err != nil {
		return out, fmt.Errorf("failed to initialize model component")
	}
	msgs := make([]*schema.Message, len(in.Messages))
	for i, m := range in.Messages {
		msgs[i] = &schema.Message{Role: schema.RoleType(m.Role), Content: m.Content}
	}
	var opts []einomodel.Option
	if a.config.Provider == "openai-compatible" {
		extra := map[string]any{}
		if a.config.OutputMode == "json_schema" {
			extra["response_format"] = map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "workflow_output", "strict": true, "schema": in.Format}}
		}
		if a.config.EnableThinking != nil {
			extra["enable_thinking"] = *a.config.EnableThinking
		}
		if len(extra) > 0 {
			opts = append(opts, openai.WithExtraFields(extra))
		}
	}

	msg, err := model.Generate(ctx, msgs, opts...)
	if len(t.body) > 0 {
		if json.Valid(t.body) {
			out.Raw = json.RawMessage(t.body)
		} else {
			out.Raw = string(t.body)
		}
	}
	if err != nil {
		// Do not persist provider error bodies or SDK errors, which may echo keys.
		out.Raw = nil
		if ctx.Err() != nil {
			return out, fmt.Errorf("model call interrupted: %w", ctx.Err())
		}
		if t.status != 0 && t.status != 200 {
			return out, fmt.Errorf("model provider HTTP %d", t.status)
		}
		return out, fmt.Errorf("model call failed (connection, response format or size)")
	}
	if msg == nil {
		return out, fmt.Errorf("model returned no message")
	}
	out.Content = msg.Content
	if a.config.Provider == "openai-compatible" && (msg.ResponseMeta == nil || msg.ResponseMeta.FinishReason == "") {
		return out, fmt.Errorf("incomplete cloud response: missing finish reason")
	}
	if msg.ResponseMeta != nil {
		if u := msg.ResponseMeta.Usage; u != nil {
			out.PromptTokens = &u.PromptTokens
			out.CompletionTokens = &u.CompletionTokens
		}
		if reason := msg.ResponseMeta.FinishReason; reason != "" && reason != "stop" {
			return out, fmt.Errorf("model output truncated or stopped unexpectedly (%s)", reason)
		}
	}
	if a.config.Provider == "ollama" {
		var envelope struct {
			Done  bool   `json:"done"`
			Error string `json:"error"`
		}
		if json.Unmarshal(t.body, &envelope) != nil || !envelope.Done || envelope.Error != "" {
			return out, fmt.Errorf("incomplete Ollama response")
		}
	}
	if strings.TrimSpace(out.Content) == "" {
		return out, fmt.Errorf("model output is empty")
	}
	return out, nil
}
