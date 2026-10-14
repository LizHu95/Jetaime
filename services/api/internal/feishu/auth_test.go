package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
)

func TestCheckAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name, response, wantError string
	}{
		{"success", `{"code":0,"expire":7200,"tenant_access_token":"private-token"}`, ""},
		{"denied", `{"code":21001,"msg":"Access denied by access control policy"}`, "code=21001"},
		{"missing token", `{"code":0,"expire":7200}`, "missing token"},
		{"invalid expiry", `{"code":0,"tenant_access_token":"private-token"}`, "invalid expiry"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/open-apis/auth/v3/tenant_access_token/internal" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				var credentials larkcore.SelfBuiltTenantAccessTokenReq
				if err := json.NewDecoder(r.Body).Decode(&credentials); err != nil {
					t.Error(err)
				}
				if credentials.AppID != "test-app" || credentials.AppSecret != "test-secret" {
					t.Error("incorrect application credentials")
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Tt-Logid", "test-log-id")
				fmt.Fprint(w, tc.response)
			}))
			defer server.Close()
			config := Config{AppID: "test-app", AppSecret: "test-secret"}
			client := lark.NewClient(config.AppID, config.AppSecret, lark.WithOpenBaseUrl(server.URL))
			expire, err := CheckAuthorization(context.Background(), client, config)
			if tc.wantError == "" {
				if err != nil || expire != 7200 {
					t.Fatalf("expire=%d error=%v", expire, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("expected %q, got %v", tc.wantError, err)
			}
			if tc.name == "denied" && !strings.Contains(err.Error(), "log_id=test-log-id") {
				t.Fatal("missing diagnostic log ID")
			}
		})
	}
}
