package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/auth"
	"github.com/LizHu95/Jetaime/services/api/internal/notification"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5"
)

type subscriber struct {
	user   string
	events chan Event
}
type Realtime struct {
	store   Store
	auth    *auth.Service
	ctx     context.Context
	mu      sync.Mutex
	clients map[*subscriber]bool
}

func New(ctx context.Context, store Store, authentication *auth.Service) *Realtime {
	return &Realtime{store: store, auth: authentication, ctx: ctx, clients: map[*subscriber]bool{}}
}
func (rt *Realtime) broadcast(event Event, users map[string]bool) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	for client := range rt.clients {
		if users != nil && !users[client.user] {
			continue
		}
		select {
		case client.events <- event:
		default:
			// Slow clients get a resync hint instead of an unbounded in-memory queue.
			select {
			case <-client.events:
			default:
			}
			select {
			case client.events <- Event{Type: "resync"}:
			default:
			}
		}
	}
}
func (rt *Realtime) Listen(ctx context.Context) {
	for ctx.Err() == nil {
		conn, err := rt.store.ConnectEvents(ctx)
		if err == nil {
			_, err = conn.Exec(ctx, "LISTEN "+notification.Channel)
		}
		if err == nil {
			rt.broadcast(Event{Type: "resync"}, nil)
		}
		for err == nil && ctx.Err() == nil {
			notification, e := conn.WaitForNotification(ctx)
			err = e
			if err != nil {
				break
			}
			var event Event
			if json.Unmarshal([]byte(notification.Payload), &event) != nil || !validID(event.GroupID) {
				continue
			}
			query, cancel := context.WithTimeout(ctx, 5*time.Second)
			users, e := rt.store.EventUsers(query, event)
			cancel()
			if e != nil {
				continue
			}

			rt.broadcast(event, users)
		}
		if conn != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
			_ = conn.Close(cleanup)
			cancel()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}
func (rt *Realtime) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool {
		origin, err := url.Parse(r.Header.Get("Origin"))
		return err == nil && (origin.Scheme == "http" || origin.Scheme == "https") && origin.Host == r.Host
	}}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	conn.SetReadLimit(8192)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	// Browser WebSocket cannot set an Authorization header. Authenticate in the
	// first frame; never put reusable credentials into URLs or subprotocols.
	var input struct {
		Type  string `json:"type"`
		Token string `json:"token"`
	}
	if err = conn.ReadJSON(&input); err != nil || input.Type != "authenticate" {
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(1008, "unauthorized"), time.Now().Add(time.Second))
		return
	}
	authenticate := func() (auth.User, error) {
		ctx, cancel := context.WithTimeout(rt.ctx, 5*time.Second)
		defer cancel()
		return rt.auth.Authenticate(ctx, input.Token)
	}
	user, err := authenticate()
	if err != nil {
		code := 1013
		if errors.Is(err, auth.ErrUnauthorized) {
			code = 1008
		}
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, "authentication unavailable"), time.Now().Add(time.Second))
		return
	}
	client := &subscriber{user: user.ID, events: make(chan Event, 32)}
	rt.mu.Lock()
	rt.clients[client] = true
	rt.mu.Unlock()
	defer func() { rt.mu.Lock(); delete(rt.clients, client); rt.mu.Unlock() }()
	send := func(event Event) error {
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		return conn.WriteJSON(event)
	}
	if send(Event{Type: "ready"}) != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Now().Add(45 * time.Second))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(45 * time.Second)) })
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	sessionCheck := time.NewTicker(2 * time.Second)
	defer sessionCheck.Stop()
	for {
		select {
		case <-rt.ctx.Done():
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(1001, "server restart"), time.Now().Add(time.Second))
			return
		case <-done:
			return
		case <-sessionCheck.C:
			if _, err := authenticate(); err != nil {
				code := 1013
				if errors.Is(err, auth.ErrUnauthorized) {
					code = 1008
				}
				_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, "authentication unavailable"), time.Now().Add(time.Second))
				return
			}
		case <-heartbeat.C:
			if conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)) != nil {
				return
			}
		case event := <-client.events:
			if _, err := authenticate(); err != nil {
				code := 1013
				if errors.Is(err, auth.ErrUnauthorized) {
					code = 1008
				}
				_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, "authentication unavailable"), time.Now().Add(time.Second))
				return
			}
			if send(event) != nil {
				return
			}
		}
	}
}

type Event = notification.Event
type Store interface {
	ConnectEvents(context.Context) (*pgx.Conn, error)
	EventUsers(context.Context, Event) (map[string]bool, error)
}

func validID(id string) bool { value, err := uuid.Parse(id); return err == nil && value.String() == id }
