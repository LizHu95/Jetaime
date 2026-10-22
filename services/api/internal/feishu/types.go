// Package feishu provides Feishu SDK long-connection event and callback adapters.
package feishu

import (
	"context"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

type Config struct {
	AppID     string
	AppSecret string
}

type Handlers struct {
	Message     func(context.Context, *larkim.P2MessageReceiveV1) error
	ChatEntered func(context.Context, *larkim.P2ChatAccessEventBotP2pChatEnteredV1) error
	CardAction  func(context.Context, *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error)
}
