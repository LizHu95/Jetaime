package main

import (
	"context"
	"io"
	"slices"
	"testing"

	"github.com/LizHu95/Jetaime/services/api/internal/testutil"
)

// Ordinary CLI tests use the real Ollama adapter against a local test server.
func runWithTestModel(t *testing.T, ctx context.Context, args []string, in io.Reader, out io.Writer) error {
	t.Helper()
	if !slices.Contains(args, "-ollama-url") {
		args = append(slices.Clone(args), "-ollama-url", testutil.OllamaServer(t).URL)
	}
	return run(ctx, args, in, out)
}
