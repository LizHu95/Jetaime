package feishu

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

//go:embed reply-card.json
var replyCard string

// ReplyCard replies to the received message with the callback test card.
func ReplyCard(ctx context.Context, client *lark.Client, messageID string) error {
	if strings.TrimSpace(messageID) == "" {
		return errors.New("received message has no message_id")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	response, err := client.Im.Message.Reply(ctx, larkim.NewReplyMessageReqBuilder().
		MessageId(messageID).
		Body(larkim.NewReplyMessageReqBodyBuilder().
			MsgType("interactive").
			Content(replyCard).
			Uuid(uuid.NewSHA1(uuid.NameSpaceURL, []byte("jetaime:reply-card:"+messageID)).String()).
			Build()).
		Build())
	if err != nil {
		return fmt.Errorf("reply card: %w", err)
	}
	if response == nil {
		return errors.New("reply card: empty response")
	}
	if !response.Success() {
		var logID string
		if response.ApiResp != nil {
			logID = response.LogId()
		}
		return fmt.Errorf("reply card: code=%d message=%s log_id=%s", response.Code, response.Msg, logID)
	}
	return nil
}
