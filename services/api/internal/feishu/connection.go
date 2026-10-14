package feishu

import (
	"errors"
	"strings"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

// NewLongConnection uses SDK authentication, heartbeat and automatic reconnection.
// Start blocks until the context is canceled or a terminal connection error occurs.
func NewLongConnection(config Config, handlers Handlers, options ...larkws.ClientOption) (*larkws.Client, error) {
	var (
		d             *dispatcher.EventDispatcher
		err           error
		clientOptions []larkws.ClientOption
	)
	if strings.TrimSpace(config.AppID) == "" {
		return nil, errors.New("FEISHU_APP_ID is required")
	}
	if strings.TrimSpace(config.AppSecret) == "" {
		return nil, errors.New("FEISHU_APP_SECRET is required")
	}
	d, err = newDispatcher(handlers)
	if err != nil {
		return nil, err
	}
	clientOptions = append([]larkws.ClientOption{larkws.WithLogLevel(larkcore.LogLevelInfo)}, options...)
	clientOptions = append(clientOptions, larkws.WithEventHandler(d))
	return larkws.NewClient(config.AppID, config.AppSecret, clientOptions...), nil
}

func newDispatcher(handlers Handlers) (*dispatcher.EventDispatcher, error) {
	var d *dispatcher.EventDispatcher
	if handlers.Message == nil || handlers.CardAction == nil {
		return nil, errors.New("message and card action handlers are required")
	}
	d = dispatcher.NewEventDispatcher("", "")
	d.OnP2MessageReceiveV1(handlers.Message).OnP2CardActionTrigger(handlers.CardAction)
	return d, nil
}
