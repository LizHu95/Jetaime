// Package ollama 将本地 Ollama HTTP 接口适配为决策服务的推荐生成能力。
package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/decisions"
	"github.com/LizHu95/Jetaime/services/api/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// Config 是模型运行配置，不属于用户的决策条件。
type Config struct {
	BaseURL       string        // Ollama 服务地址，例如 http://localhost:11434。
	Model         string        // 已下载的模型标签，例如 qwen3.5:9b。
	Timeout       time.Duration // 单次请求总超时，包含模型加载和生成。
	ContextTokens int           // 上下文窗口大小，包含输入和输出。
}

// Provider 只接收服务层已授权、已通过硬约束检查的 Context。
// 不读取 Store，也不自行扩大候选、查询记忆或执行工具。
type Provider struct {
	endpoint string       // 完整 /api/chat 地址。
	model    string       // 每次请求使用的固定模型。
	tokens   int          // num_ctx 参数，控制上下文内存开销。
	client   *http.Client // 复用连接，并限制请求时长。
}

// 编译时确认本类型符合业务接口；实际注入位置在 cmd/decision/main.go。
var _ decisions.Provider = (*Provider)(nil)

// NewProvider 仅检查配置，不连接服务或下载模型。
func NewProvider(config Config) (*Provider, error) {
	base, err := url.Parse(strings.TrimRight(config.BaseURL, "/"))
	if err != nil || base == nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, fmt.Errorf("ollama: invalid base URL")
	}
	if strings.TrimSpace(config.Model) == "" || config.Timeout <= 0 || config.ContextTokens <= 0 {
		return nil, fmt.Errorf("ollama: model, positive timeout and context tokens are required")
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/api/chat"
	return &Provider{endpoint: base.String(), model: config.Model, tokens: config.ContextTokens, client: &http.Client{Timeout: config.Timeout}}, nil
}

// chatMessage 是 Ollama 的传输格式，与业务 Context 分开。
type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model    string          `json:"model"`
	Messages []chatMessage   `json:"messages"`
	Stream   bool            `json:"stream"` // 一次读取完整 JSON，不处理流式分片。
	Think    bool            `json:"think"`  // 本期直接生成推荐，不输出思考过程。
	Format   json.RawMessage `json:"format"` // JSON Schema 限定模型输出形状。
	Options  struct {
		Temperature float64 `json:"temperature"`
		NumCtx      int     `json:"num_ctx"`
		NumPredict  int     `json:"num_predict"`
	} `json:"options"`
}

type chatResponse struct {
	Message          chatMessage `json:"message"`
	Done             bool        `json:"done"`
	DoneReason       string      `json:"done_reason"`
	Error            string      `json:"error"`
	PromptTokens     *int        `json:"prompt_eval_count"`
	CompletionTokens *int        `json:"eval_count"`
	TotalDuration    *int64      `json:"total_duration"` // Ollama 返回纳秒；span 自身也记录端到端耗时。
}

// Generate 的顺序：构建提示词 → 请求 Ollama → 解码结果。
// OptionID 和最终业务校验由 DecisionService.generateResult 负责。
func (p *Provider) Generate(ctx context.Context, input decisions.Context) (result decisions.Result, generationErr error) {
	ctx, span := telemetry.Start(ctx, "ollama.generate", "LLM", nil)
	span.SetAttributes(attribute.String("llm.system", "ollama"), attribute.String("llm.model_name", p.model), attribute.String("llm.prompt_template.version", "select-v1"))
	defer func() { telemetry.Finish(span, generationErr) }()
	if err := ctx.Err(); err != nil {
		return decisions.Result{}, err
	}
	if input.Task != decisions.TaskSelect || len(input.Candidates) == 0 || len(input.Participants) == 0 {
		return decisions.Result{}, fmt.Errorf("ollama: select requires candidates and participants")
	}
	content, err := json.Marshal(input)
	if err != nil {
		return decisions.Result{}, fmt.Errorf("ollama: encode context: %w", err)
	}
	request := chatRequest{Model: p.model, Format: selectSchema, Messages: []chatMessage{
		{Role: "system", Content: selectPrompt + "\n输出 JSON Schema：\n" + string(selectSchema)},
		{Role: "user", Content: "以下 JSON 是本次决策数据：\n" + string(content)},
	}}
	request.Options.NumCtx, request.Options.NumPredict = p.tokens, 2048
	telemetry.Input(span, request) // 包含实际 Prompt、Schema、候选与运行参数。
	if span.IsRecording() {
		for i, message := range request.Messages {
			prefix := fmt.Sprintf("llm.input_messages.%d.message.", i)
			span.SetAttributes(attribute.String(prefix+"role", message.Role), attribute.String(prefix+"content", message.Content))
		}
	}
	body, err := json.Marshal(request)
	if err != nil {
		return decisions.Result{}, fmt.Errorf("ollama: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return decisions.Result{}, fmt.Errorf("ollama: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return decisions.Result{}, fmt.Errorf("ollama: request failed (check service and timeout): %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	// 限制响应大小，避免异常服务无限返回数据；错误中不打印上下文或原始模型内容。
	const maxResponseBytes = 1 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return decisions.Result{}, fmt.Errorf("ollama: read response: %w", err)
	}
	span.SetAttributes(attribute.Int("http.response.status_code", resp.StatusCode))
	if len(data) <= maxResponseBytes {
		if json.Valid(data) {
			telemetry.Output(span, json.RawMessage(data))
		} else {
			telemetry.Output(span, string(data))
		}
	}
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusNotFound {
			return decisions.Result{}, fmt.Errorf("ollama: HTTP 404; check model %q is downloaded and base URL is correct", p.model)
		}
		return decisions.Result{}, fmt.Errorf("ollama: HTTP %d", resp.StatusCode)
	}
	if len(data) > maxResponseBytes {
		return decisions.Result{}, fmt.Errorf("ollama: response exceeds size limit")
	}
	var response chatResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return decisions.Result{}, fmt.Errorf("ollama: invalid response JSON: %w", err)
	}
	span.SetAttributes(attribute.String("llm.output_messages.0.message.role", response.Message.Role), attribute.String("llm.output_messages.0.message.content", response.Message.Content))
	if response.PromptTokens != nil {
		span.SetAttributes(attribute.Int("llm.token_count.prompt", *response.PromptTokens))
	}
	if response.CompletionTokens != nil {
		span.SetAttributes(attribute.Int("llm.token_count.completion", *response.CompletionTokens))
	}
	if response.PromptTokens != nil && response.CompletionTokens != nil {
		span.SetAttributes(attribute.Int("llm.token_count.total", *response.PromptTokens+*response.CompletionTokens))
	}
	if response.TotalDuration != nil {
		span.SetAttributes(attribute.Int64("ollama.total_duration_ns", *response.TotalDuration))
	}
	if response.Error != "" || !response.Done || response.DoneReason == "length" || strings.TrimSpace(response.Message.Content) == "" {
		return decisions.Result{}, fmt.Errorf("ollama: generation failed, incomplete or exceeded output token limit")
	}
	return decodeResult(response.Message.Content)
}

// modelResult 不接收 OptionID 或规划内容，防止模型控制服务端标识及任务类型。
type modelResult struct {
	Outcome     decisions.Outcome `json:"outcome"`
	Explanation string            `json:"explanation"`
	Options     []struct {
		Title              string                       `json:"title"`
		Selection          *decisions.Selection         `json:"selection"`
		Reason             string                       `json:"reason"`
		ParticipantMatches []decisions.ParticipantMatch `json:"participantMatches"`
		Unknowns           []string                     `json:"unknowns"`
	} `json:"options"`
}

// decodeResult 严格读取一个 JSON 对象，不自动修补 Markdown 或损坏的输出。
func decodeResult(content string) (decisions.Result, error) {
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.DisallowUnknownFields()
	var model modelResult
	if err := decoder.Decode(&model); err != nil {
		return decisions.Result{}, fmt.Errorf("ollama: invalid result JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return decisions.Result{}, fmt.Errorf("ollama: result must contain exactly one JSON object")
	}
	result := decisions.Result{Outcome: model.Outcome, Explanation: model.Explanation, Options: make([]decisions.Option, 0, len(model.Options))}
	for _, option := range model.Options {
		result.Options = append(result.Options, decisions.Option{Title: option.Title, Selection: option.Selection, Reason: option.Reason, ParticipantMatches: option.ParticipantMatches, Unknowns: option.Unknowns})
	}
	return result, nil
}
