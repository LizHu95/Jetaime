package feishu

import (
	"context"
	"errors"
	"testing"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

func TestLongConnectionDispatch(t *testing.T) {
	var (
		d               *dispatcher.EventDispatcher
		handlers        Handlers
		err             error
		response        any
		card            *callback.CardActionTriggerResponse
		ok              bool
		messages, cards int
		ctx             context.Context
	)
	ctx = context.WithValue(context.Background(), testContextKey{}, "long-connection")
	handlers = Handlers{
		Message: func(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
			if ctx.Value(testContextKey{}) != "long-connection" {
				t.Error("context lost")
			}
			if event.Event == nil || event.Event.Message == nil || event.Event.Message.MessageId == nil || *event.Event.Message.MessageId != "om_ws" {
				t.Error("invalid message")
			}
			messages++
			return nil
		},
		CardAction: func(_ context.Context, event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
			if event.Event == nil || event.Event.Action == nil || event.Event.Action.Value["action"] != "ping" {
				t.Error("invalid action")
			}
			cards++
			return &callback.CardActionTriggerResponse{Toast: &callback.Toast{Type: "success", Content: "connected"}}, nil
		},
	}
	d, err = newDispatcher(handlers)
	if err != nil {
		t.Fatal(err)
	}
	// The WebSocket SDK invokes Do with plaintext events, without webhook credentials.
	response, err = d.Do(ctx, []byte(`{"schema":"2.0","header":{"event_type":"im.message.receive_v1"},"event":{"message":{"message_id":"om_ws"}}}`))
	if err != nil || response != nil || messages != 1 {
		t.Fatalf("message dispatch: %v, count=%d", err, messages)
	}
	response, err = d.Do(ctx, []byte(`{"schema":"2.0","header":{"event_type":"card.action.trigger"},"event":{"action":{"value":{"action":"ping"}}}}`))
	card, ok = response.(*callback.CardActionTriggerResponse)
	if err != nil || !ok || card.Toast == nil || card.Toast.Content != "connected" || cards != 1 {
		t.Fatalf("card dispatch: %v, response=%v", err, response)
	}
	handlers.Message = func(context.Context, *larkim.P2MessageReceiveV1) error { return errors.New("business failed") }
	d, err = newDispatcher(handlers)
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.Do(ctx, []byte(`{"schema":"2.0","header":{"event_type":"im.message.receive_v1"},"event":{}}`))
	if err == nil {
		t.Fatal("handler failure lost")
	}
}

func TestLongConnectionConfigurationAndCancellation(t *testing.T) {
	var (
		handlers Handlers
		config   Config
		client   *larkws.Client
		err      error
		ctx      context.Context
		cancel   context.CancelFunc
	)
	handlers = Handlers{
		Message: func(context.Context, *larkim.P2MessageReceiveV1) error { return nil },
		CardAction: func(context.Context, *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
			return nil, nil
		},
	}
	if _, err = NewLongConnection(Config{}, handlers); err == nil {
		t.Fatal("missing app id accepted")
	}
	if _, err = NewLongConnection(Config{AppID: "test"}, handlers); err == nil {
		t.Fatal("missing app secret accepted")
	}
	config = Config{AppID: "test", AppSecret: "test"}
	if _, err = NewLongConnection(config, Handlers{}); err == nil {
		t.Fatal("missing handlers accepted")
	}
	client, err = NewLongConnection(config, handlers)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	err = client.Start(ctx)
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

type testContextKey struct{}
