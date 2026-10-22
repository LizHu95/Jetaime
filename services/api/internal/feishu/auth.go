package feishu

import (
	"context"
	"errors"
	"fmt"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
)

// CheckAuthorization verifies application credentials without exposing the token.
// The SDK manages token caching and renewal for subsequent message requests.
func CheckAuthorization(ctx context.Context, client *lark.Client, config Config) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	response, err := client.GetTenantAccessTokenBySelfBuiltApp(ctx, &larkcore.SelfBuiltTenantAccessTokenReq{
		AppID: config.AppID, AppSecret: config.AppSecret,
	})
	if err != nil {
		return 0, fmt.Errorf("get tenant_access_token: %w", err)
	}
	if response == nil {
		return 0, errors.New("get tenant_access_token: empty response")
	}
	if !response.Success() {
		var logID string
		if response.ApiResp != nil {
			logID = response.LogId()
		}
		return 0, fmt.Errorf("get tenant_access_token: code=%d message=%s log_id=%s", response.Code, response.Msg, logID)
	}
	if response.TenantAccessToken == "" || response.Expire <= 0 {
		return 0, errors.New("get tenant_access_token: missing token or invalid expiry")
	}
	return response.Expire, nil
}
