package feishu

import (
	"log/slog"
	"net/http"
	"strings"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
)

// NewAPIClient records response IDs even for the SDK's internal token requests.
func NewAPIClient(config Config, logger *slog.Logger) *lark.Client {
	return lark.NewClient(config.AppID, config.AppSecret,
		lark.WithHttpClient(&diagnosticHTTPClient{client: http.DefaultClient, logger: logger, config: config}))
}

type diagnosticHTTPClient struct {
	client larkcore.HttpClient
	logger *slog.Logger
	config Config
}

func (c *diagnosticHTTPClient) Do(req *http.Request) (*http.Response, error) {
	if req.URL.Path == larkcore.TenantAccessTokenInternalUrlPath {
		c.logger.InfoContext(req.Context(), "feishu token request credentials",
			"app_id", c.config.AppID,
			"app_id_present", strings.TrimSpace(c.config.AppID) != "",
			"app_secret_present", strings.TrimSpace(c.config.AppSecret) != "",
			"app_secret_length", len(c.config.AppSecret))
	}
	resp, err := c.client.Do(req)
	if resp != nil {
		logID := (larkcore.ApiResp{Header: resp.Header}).LogId()
		c.logger.InfoContext(req.Context(), "feishu API response",
			"method", req.Method, "path", req.URL.Path,
			"status", resp.StatusCode, "log_id", logID)
	}
	return resp, err
}
