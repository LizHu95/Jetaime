package webpush

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	push "github.com/SherClockHolmes/webpush-go"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Worker struct {
	pool   *pgxpool.Pool
	config Config
	client push.HTTPClient
}

func NewWorker(pool *pgxpool.Pool, c Config) *Worker {
	return &Worker{pool: pool, config: c, client: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (w *Worker) Work(ctx context.Context) {
	if !w.config.Enabled {
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	cleanup := time.NewTicker(time.Hour)
	defer cleanup.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-cleanup.C:
			_, _ = w.pool.Exec(ctx, `DELETE FROM web_push_subscriptions s USING auth_sessions a WHERE s.session_id=a.id AND (a.revoked_at IS NOT NULL OR a.expires_at<=now())`)
			_, _ = w.pool.Exec(ctx, `DELETE FROM web_push_jobs WHERE expires_at<now()-interval '1 day'`)
		case <-ticker.C:
			for i := 0; i < 20 && ctx.Err() == nil; i++ {
				if !w.deliver(ctx) {
					break
				}
			}
		}
	}
}
func (w *Worker) deliver(ctx context.Context) bool {
	call, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, err := w.pool.Begin(call)
	if err != nil {
		return false
	}
	defer tx.Rollback(call)
	var id int64
	var subscriptionID, group, messageID string
	var attempt int
	var s push.Subscription
	err = tx.QueryRow(call, `SELECT j.id,j.subscription_id::text,j.attempts,s.endpoint,s.p256dh,s.auth,COALESCE(m.group_id::text,''),COALESCE(m.id::text,'')
      FROM web_push_jobs j JOIN web_push_subscriptions s ON s.id=j.subscription_id JOIN auth_sessions a ON a.id=s.session_id AND a.user_id=s.user_id
      LEFT JOIN messages m ON m.id=j.message_id LEFT JOIN chat_groups g ON g.id=m.group_id LEFT JOIN group_members gm ON gm.group_id=m.group_id AND gm.user_id=s.user_id
      WHERE j.completed_at IS NULL AND j.available_at<=now() AND j.expires_at>now() AND j.attempts<5
      AND a.revoked_at IS NULL AND a.expires_at>now() AND (j.message_id IS NULL OR (g.status='active' AND gm.last_read_seq<m.seq))
      ORDER BY j.available_at,j.id FOR UPDATE OF j SKIP LOCKED LIMIT 1`).Scan(&id, &subscriptionID, &attempt, &s.Endpoint, &s.Keys.P256dh, &s.Keys.Auth, &group, &messageID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false
	}
	if err != nil {
		log.Print("web push claim failed")
		return false
	}
	attempt++
	_, err = tx.Exec(call, `UPDATE web_push_jobs SET attempts=$2,available_at=now()+interval '1 minute' WHERE id=$1`, id, attempt)
	if err == nil {
		err = tx.Commit(call)
	}
	if err != nil {
		return false
	}
	if !validSubscription(s) {
		_, _ = w.pool.Exec(call, `DELETE FROM web_push_subscriptions WHERE id=$1`, subscriptionID)
		return true
	}
	body := "你收到一条新消息"
	path := "/page/chat/"
	tag := "test-" + strconv.FormatInt(id, 10)
	if group != "" {
		path += group + "/"
		tag = messageID
	} else {
		body = "测试通知已送达，Web Push 可以使用了"
	}
	payload, _ := json.Marshal(map[string]string{"title": "Jetaime", "body": body, "url": path, "tag": tag})
	response, sendErr := push.SendNotificationWithContext(call, payload, &s, &push.Options{Subscriber: strings.TrimPrefix(w.config.Subject, "mailto:"), VAPIDPublicKey: w.config.PublicKey, VAPIDPrivateKey: w.config.PrivateKey, TTL: 3600, HTTPClient: w.client})
	status := 0
	retryAfter := time.Duration(1<<attempt) * time.Minute
	if response != nil {
		status = response.StatusCode
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		_ = response.Body.Close()
		if seconds, e := strconv.Atoi(response.Header.Get("Retry-After")); e == nil && seconds > 0 && seconds <= 3600 {
			retryAfter = time.Duration(seconds) * time.Second
		}
	}
	// Finish in a fresh context if the network request exhausted its timeout.
	finish, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	if status == 404 || status == 410 {
		_, err = w.pool.Exec(finish, `DELETE FROM web_push_subscriptions WHERE id=$1`, subscriptionID)
	} else {
		done := (sendErr == nil && status >= 200 && status < 300) || (status >= 300 && status < 500 && status != 408 && status != 429) || attempt >= 5
		_, err = w.pool.Exec(finish, `UPDATE web_push_jobs SET last_status=$3,completed_at=CASE WHEN $4 THEN now() ELSE NULL END,available_at=now()+$5*interval '1 second' WHERE id=$1 AND attempts=$2`, id, attempt, status, done, int64(retryAfter/time.Second))
	}
	if err != nil {
		log.Print("web push completion failed")
	}
	if sendErr != nil || status < 200 || status >= 300 {
		log.Printf("web push delivery job=%d attempt=%d status=%d", id, attempt, status)
	}
	return true
}
