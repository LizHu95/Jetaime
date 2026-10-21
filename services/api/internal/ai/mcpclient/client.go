// Package mcpclient provides bounded Streamable HTTP calls using the official MCP SDK.
package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const MaxResponseBytes = 2 * 1024 * 1024

var (
	ErrUnavailable = errors.New("MCP service unavailable")
	ErrTool        = errors.New("MCP tool rejected request")
	ErrResponse    = errors.New("MCP result is not a bounded JSON object")
)

type Client struct {
	endpoint string
	http     *http.Client
}

// Endpoint may contain credentials. It is never returned in errors or logs.
func New(endpoint string) *Client {
	return &Client{endpoint: endpoint, http: &http.Client{Timeout: 15 * time.Second,
		Transport:     boundedTransport{http.DefaultTransport},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// Each call owns its session; no process-wide credentials/session state is exposed
// to tools. Optional server-initiated sampling, roots and SSE are disabled.
func (c *Client) Call(ctx context.Context, name string, args any) (json.RawMessage, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "jetaime", Version: "0.1.0"}, &mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}})
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: c.endpoint, HTTPClient: c.http, MaxRetries: -1, DisableStandaloneSSE: true, MaxEventSize: MaxResponseBytes}, nil)
	if err != nil {
		return nil, safeError(ctx, ErrUnavailable)
	}
	defer func() { _ = session.Close() }()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return nil, safeError(ctx, ErrUnavailable)
	}
	if result == nil {
		return nil, ErrResponse
	}
	if result.IsError {
		return nil, ErrTool
	}
	return resultJSON(result)
}

func safeError(ctx context.Context, fallback error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fallback
}

func resultJSON(result *mcp.CallToolResult) (json.RawMessage, error) {
	var data []byte
	if result.StructuredContent != nil {
		var err error
		data, err = json.Marshal(result.StructuredContent)
		if err != nil {
			return nil, ErrResponse
		}
	} else {
		// Reject ambiguous multi-content/non-text output instead of forwarding it.
		if len(result.Content) != 1 {
			return nil, ErrResponse
		}
		content, ok := result.Content[0].(*mcp.TextContent)
		if !ok {
			return nil, ErrResponse
		}
		data = []byte(content.Text)
	}
	if len(data) > MaxResponseBytes {
		return nil, ErrResponse
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(data, &object) != nil || object == nil {
		return nil, ErrResponse
	}
	return data, nil
}

type boundedTransport struct{ base http.RoundTripper }

func (t boundedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	resp.Body = &boundedBody{ReadCloser: resp.Body, remaining: MaxResponseBytes}
	return resp, nil
}

type boundedBody struct {
	io.ReadCloser
	remaining int
}

func (b *boundedBody) Read(p []byte) (int, error) {
	if b.remaining <= 0 {
		return 0, ErrResponse
	}
	if len(p) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.ReadCloser.Read(p)
	b.remaining -= n
	return n, err
}
