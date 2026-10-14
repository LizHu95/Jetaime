package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/LizHu95/Jetaime/services/api/internal/feishu"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

func main() {
	var (
		logger *slog.Logger
		client *larkws.Client
		err    error
		ctx    context.Context
		stop   context.CancelFunc
	)
	logger = slog.New(slog.NewJSONHandler(os.Stdout, nil))
	config := feishu.Config{
		AppID:     os.Getenv("FEISHU_APP_ID"),
		AppSecret: os.Getenv("FEISHU_APP_SECRET"),
	}
	apiClient := lark.NewClient(config.AppID, config.AppSecret)
	client, err = feishu.NewLongConnection(config, feishu.Handlers{
		Message: func(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
			if event == nil || event.Event == nil || event.Event.Message == nil {
				logger.WarnContext(ctx, "feishu message event has no message")
				return nil
			}
			message := event.Event.Message
			var content, messageID, chatID, chatType string
			if message.Content != nil {
				content = *message.Content
			}
			if message.MessageId != nil {
				messageID = *message.MessageId
			}
			if message.ChatId != nil {
				chatID = *message.ChatId
			}
			if message.ChatType != nil {
				chatType = *message.ChatType
			}
			logger.InfoContext(ctx, "feishu message received", "request_id", event.RequestId(),
				"message_id", messageID, "chat_id", chatID, "chat_type", chatType, "content", content)
			if err := feishu.ReplyCard(ctx, apiClient, messageID); err != nil {
				logger.ErrorContext(ctx, "feishu card reply failed", "message_id", messageID, "error", err)
				return err
			}
			logger.InfoContext(ctx, "feishu card replied", "message_id", messageID)
			return nil
		},
		CardAction: func(ctx context.Context, event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
			logger.InfoContext(ctx, "feishu card action received", "request_id", event.RequestId())
			return &callback.CardActionTriggerResponse{Toast: &callback.Toast{Type: "success", Content: "飞书回调已接通"}}, nil
		},
	},
		larkws.WithOnReady(func() { logger.Info("feishu long connection ready") }),
		larkws.WithOnReconnecting(func() { logger.Info("feishu long connection reconnecting") }),
		larkws.WithOnReconnected(func() { logger.Info("feishu long connection reconnected") }),
		larkws.WithOnDisconnected(func() { logger.Info("feishu long connection disconnected") }),
	)
	if err != nil {
		logger.Error("invalid feishu configuration", "error", err)
		os.Exit(1)
	}
	ctx, stop = signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger.Info("starting feishu long connection")
	err = client.Start(ctx)
	if err != nil && ctx.Err() == nil {
		logger.Error("feishu long connection failed", "error", err)
		os.Exit(1)
	}
}
