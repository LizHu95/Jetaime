package feishu

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	lark "github.com/larksuite/oapi-sdk-go/v3"
)

func TestTokenFailureLogsResponseID(t *testing.T) {
	for _, header := range []string{"X-Tt-Logid", "X-Request-Id"} {
		t.Run(header, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/open-apis/auth/v3/tenant_access_token/internal" {
					t.Errorf("unexpected API call: %s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set(header, "denied-log-id")
				fmt.Fprint(w, `{"code":21001,"msg":"Access denied by access control policy"}`)
			}))
			defer server.Close()
			var logs bytes.Buffer
			config := Config{AppID: "diagnostic-test-app", AppSecret: "private-secret"}
			client := lark.NewClient("diagnostic-test-app", "private-secret",
				lark.WithOpenBaseUrl(server.URL),
				lark.WithHttpClient(&diagnosticHTTPClient{client: server.Client(), logger: slog.New(slog.NewJSONHandler(&logs, nil)), config: config}))
			if err := ReplyCard(context.Background(), client, "om_test"); err == nil {
				t.Fatal("expected token denial")
			}
			if !strings.Contains(logs.String(), `"log_id":"denied-log-id"`) || !strings.Contains(logs.String(), "/auth/v3/tenant_access_token/internal") {
				t.Fatalf("missing token request diagnostics: %s", logs.String())
			}
			if strings.Contains(logs.String(), "private-secret") {
				t.Fatal("credentials leaked into diagnostics")
			}
			if !strings.Contains(logs.String(), `"app_secret_present":true`) || !strings.Contains(logs.String(), `"app_id_present":true`) {
				t.Fatal("missing credential presence diagnostics")
			}
			if strings.Index(logs.String(), "feishu token request credentials") > strings.Index(logs.String(), "feishu API response") {
				t.Fatal("credential diagnostics must precede the response")
			}
		})
	}
}
