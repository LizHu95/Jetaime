package webpush

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/LizHu95/Jetaime/services/api/internal/auth"
	"github.com/LizHu95/Jetaime/services/api/internal/httpapi"
	push "github.com/SherClockHolmes/webpush-go"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Handler struct {
	pool   *pgxpool.Pool
	auth   *auth.Handler
	config Config
}

func NewHandler(pool *pgxpool.Pool, authentication *auth.Handler, c Config) *Handler {
	return &Handler{pool, authentication, c}
}
func (h *Handler) Authenticate(next http.HandlerFunc) http.HandlerFunc {
	return h.auth.Authenticate(next)
}

func respond(w http.ResponseWriter, status int, v any) {
	httpapi.WriteJSON(w, status, v)
}
func fail(w http.ResponseWriter, status int, code string) {
	respond(w, status, map[string]any{"error": map[string]string{"code": code, "message": code}})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || kind != "application/json" {
		fail(w, 415, "unsupported_media_type")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err = d.Decode(v); err == nil {
		var extra any
		if errors.Is(d.Decode(&extra), io.EOF) {
			return true
		}
	}
	fail(w, 400, "invalid_input")
	return false
}
func sessionHash(r *http.Request) []byte {
	parts := strings.Fields(r.Header.Get("Authorization"))
	sum := sha256.Sum256([]byte(parts[1]))
	return sum[:]
}
func (h *Handler) Subscribe(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if !h.config.Enabled {
		fail(w, 503, "push_disabled")
		return
	}
	var in struct {
		Endpoint       string    `json:"endpoint"`
		ExpirationTime *float64  `json:"expirationTime"`
		Keys           push.Keys `json:"keys"`
	}
	if !decode(w, r, &in) {
		return
	}
	s := push.Subscription{Endpoint: in.Endpoint, Keys: in.Keys}
	if !validSubscription(s) {
		fail(w, 400, "invalid_subscription")
		return
	}
	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		fail(w, 500, "push_failed")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	// Serialize registrations of the same device, including account switches.
	_, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, s.Endpoint)
	if err != nil {
		fail(w, 500, "push_failed")
		return
	}
	var previousKey, previousAuth string
	err = tx.QueryRow(r.Context(), `SELECT p256dh,auth FROM web_push_subscriptions WHERE endpoint=$1`, s.Endpoint).Scan(&previousKey, &previousAuth)
	if err == nil && (previousKey != s.Keys.P256dh || previousAuth != s.Keys.Auth) {
		fail(w, 409, "invalid_subscription")
		return
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		fail(w, 500, "push_failed")
		return
	}
	var session string
	err = tx.QueryRow(r.Context(), `SELECT id::text FROM auth_sessions WHERE token_hash=$1 AND user_id=$2 AND revoked_at IS NULL AND expires_at>now() FOR UPDATE`, sessionHash(r), user.ID).Scan(&session)
	if err != nil {
		fail(w, 401, "unauthorized")
		return
	}
	_, err = tx.Exec(r.Context(), `DELETE FROM web_push_subscriptions WHERE endpoint=$1 AND (session_id<>$2 OR user_id<>$3)`, s.Endpoint, session, user.ID)
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO web_push_subscriptions(id,user_id,session_id,endpoint,p256dh,auth) VALUES($1,$2,$3,$4,$5,$6)
      ON CONFLICT(endpoint) DO UPDATE SET p256dh=EXCLUDED.p256dh,auth=EXCLUDED.auth`, uuid.NewString(), user.ID, session, s.Endpoint, s.Keys.P256dh, s.Keys.Auth)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		fail(w, 500, "push_failed")
		return
	}
	respond(w, 204, nil)
}
func (h *Handler) Unsubscribe(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	var in struct {
		Endpoint string `json:"endpoint"`
	}
	if !decode(w, r, &in) {
		return
	}
	_, err := h.pool.Exec(r.Context(), `DELETE FROM web_push_subscriptions WHERE endpoint=$1 AND user_id=$2`, in.Endpoint, user.ID)
	if err != nil {
		fail(w, 500, "push_failed")
		return
	}
	respond(w, 204, nil)
}
func (h *Handler) SendTest(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if !h.config.Enabled {
		fail(w, 503, "push_disabled")
		return
	}
	var in struct {
		Endpoint string `json:"endpoint"`
	}
	if !decode(w, r, &in) {
		return
	}
	// Atomic rate limit per device; a test cannot target another account/session.
	var id string
	err := h.pool.QueryRow(r.Context(), `WITH target AS (
      UPDATE web_push_subscriptions s SET last_test_at=now() FROM auth_sessions a
      WHERE s.endpoint=$1 AND s.user_id=$2 AND a.id=s.session_id AND a.token_hash=$3
      AND a.revoked_at IS NULL AND a.expires_at>now() AND (s.last_test_at IS NULL OR s.last_test_at<now()-interval '30 seconds') RETURNING s.id)
      INSERT INTO web_push_jobs(subscription_id) SELECT id FROM target RETURNING subscription_id::text`, in.Endpoint, user.ID, sessionHash(r)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		fail(w, 429, "push_test_wait")
		return
	}
	if err != nil {
		fail(w, 500, "push_failed")
		return
	}
	respond(w, 202, map[string]bool{"queued": true})
}

func (h *Handler) GetPublicKey(w http.ResponseWriter, r *http.Request) {
	respond(w, 200, map[string]any{"enabled": h.config.Enabled, "publicKey": h.config.PublicKey})
}
