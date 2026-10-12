package assistant

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
)

type Model interface {
	Call(context.Context, ModelRequest) (ModelResult, error)
}
type Ollama struct {
	endpoint string
	client   *http.Client
}

func NewOllama(base string, timeout time.Duration) (*Ollama, error) {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || timeout <= 0 {
		return nil, fmt.Errorf("invalid Ollama URL or timeout")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/chat"
	return &Ollama{endpoint: u.String(), client: &http.Client{Timeout: timeout}}, nil
}
func (o *Ollama) Call(ctx context.Context, input ModelRequest) (ModelResult, error) {
	var result ModelResult
	b, err := json.Marshal(input)
	if err != nil {
		return result, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.endpoint, bytes.NewReader(b))
	if err != nil {
		return result, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.client.Do(req)
	if err != nil {
		return result, fmt.Errorf("Ollama 调用失败，请检查服务 / 超时：%w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	const limit = 1 << 20
	b, err = io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return result, err
	}
	if len(b) > limit {
		return result, fmt.Errorf("Ollama response too large")
	}
	if json.Valid(b) {
		result.Raw = json.RawMessage(b)
	} else {
		result.Raw = string(b)
	}
	if resp.StatusCode != http.StatusOK {
		return result, fmt.Errorf("Ollama HTTP %d，请检查模型是否已下载", resp.StatusCode)
	}
	var output struct {
		Message          ChatMessage `json:"message"`
		Done             bool        `json:"done"`
		DoneReason       string      `json:"done_reason"`
		Error            string      `json:"error"`
		PromptTokens     *int        `json:"prompt_eval_count"`
		CompletionTokens *int        `json:"eval_count"`
	}
	if err = json.Unmarshal(b, &output); err != nil {
		return result, fmt.Errorf("invalid Ollama envelope: %w", err)
	}
	result.Content = output.Message.Content
	result.PromptTokens = output.PromptTokens
	result.CompletionTokens = output.CompletionTokens
	if output.Error != "" || !output.Done || output.DoneReason == "length" || strings.TrimSpace(result.Content) == "" {
		return result, fmt.Errorf("模型输出失败、为空或已截断")
	}
	return result, nil
}

// Resolve the current digest on each lookup so replacing a model under the same
// alias cannot reuse a cached verdict. Failure disables caching, not inference.
func (o *Ollama) ReviewCacheIdentity(ctx context.Context, name string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	endpoint := strings.TrimSuffix(o.endpoint, "/api/chat") + "/api/tags"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("model fingerprint HTTP %d", resp.StatusCode)
	}
	var tags struct {
		Models []struct {
			Name   string `json:"name"`
			Digest string `json:"digest"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&tags); err != nil {
		return "", err
	}
	for _, model := range tags.Models {
		if model.Digest != "" && (model.Name == name || model.Name == name+":latest") {
			return o.endpoint + "|" + model.Name + "|" + model.Digest, nil
		}
	}
	return "", fmt.Errorf("model fingerprint unavailable")
}
