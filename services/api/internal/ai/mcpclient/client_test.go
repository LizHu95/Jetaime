package mcpclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestStreamableHTTPCall(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	type args struct {
		City string `json:"city"`
	}
	mcp.AddTool(server, &mcp.Tool{Name: "maps_weather"}, func(_ context.Context, _ *mcp.CallToolRequest, in args) (*mcp.CallToolResult, any, error) {
		if in.City != "上海" {
			t.Error("arguments not forwarded")
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: `{"city":"上海","forecasts":[]}`}}}, nil, nil
	})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{JSONResponse: true})
	s := httptest.NewServer(h)
	defer s.Close()
	c := New(s.URL + "?key=secret")
	data, err := c.Call(context.Background(), "maps_weather", map[string]string{"city": "上海"})
	if err != nil || !json.Valid(data) || !strings.Contains(string(data), "上海") {
		t.Fatal(string(data), err)
	}
}

func TestResultJSONOnlyAcceptsBoundedObject(t *testing.T) {
	for _, result := range []*mcp.CallToolResult{
		{},
		{Content: []mcp.Content{&mcp.TextContent{Text: "not JSON"}}},
		{Content: []mcp.Content{&mcp.TextContent{Text: "[]"}}},
		{Content: []mcp.Content{&mcp.TextContent{Text: "null"}}},
		{Content: []mcp.Content{&mcp.TextContent{Text: "{}"}, &mcp.TextContent{Text: "{}"}}},
		{Content: []mcp.Content{&mcp.TextContent{Text: strings.Repeat("x", MaxResponseBytes+1)}}},
	} {
		if _, err := resultJSON(result); err == nil {
			t.Fatal("malformed result accepted")
		}
	}
	if data, err := resultJSON(&mcp.CallToolResult{StructuredContent: map[string]any{"pois": []any{}}}); err != nil || string(data) != `{"pois":[]}` {
		t.Fatal(string(data), err)
	}
}

func TestErrorsDoNotLeakEndpointOrBody(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503); _, _ = w.Write([]byte("secret")) }))
	defer s.Close()
	c := New(s.URL + "?key=secret")
	_, err := c.Call(context.Background(), "maps_weather", map[string]string{"city": "上海"})
	if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), s.URL) {
		t.Fatal("unsafe error", err)
	}
}

func TestProviderErrorAndOversizedResponse(t *testing.T) {
	for _, oversized := range []bool{false, true} {
		server := mcp.NewServer(&mcp.Implementation{Name: "test"}, nil)
		mcp.AddTool(server, &mcp.Tool{Name: "query"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			text := "provider error containing secret"
			if oversized {
				text = strings.Repeat("x", MaxResponseBytes+1)
			}
			return &mcp.CallToolResult{IsError: !oversized, Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil, nil
		})
		s := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{JSONResponse: true}))
		_, err := New(s.URL+"?key=secret").Call(context.Background(), "query", struct{}{})
		s.Close()
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatal("unsafe provider response", err)
		}
	}
}
