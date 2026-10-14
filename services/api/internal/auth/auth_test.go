package auth

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/database"
	"github.com/LizHu95/Jetaime/services/api/internal/httpapi"
	"github.com/alexedwards/argon2id"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const testPassword = "abc123"

func TestPostgresCaseSensitiveUsernames(t *testing.T) {
	pool := authTestPool(t)
	service, err := NewService(NewPostgresStore(pool))
	if err != nil {
		t.Fatal(err)
	}
	router := httpapi.NewRouter(NewHandler(service))
	ids := map[string]bool{}
	for i, username := range []string{"Liz", "liz", "LIZ"} {
		password := fmt.Sprintf("pass0%d", i)
		// A different case cannot authenticate an existing account.
		if out := request(router, "/api/auth/login", loginRequest{Username: username, Password: "pass00"}, ""); out.Code != 401 {
			t.Fatalf("unregistered case %s matched an existing account: %d", username, out.Code)
		}
		out := request(router, "/api/auth/register", registerRequest{Username: " " + username + " ", Password: password}, "")
		if out.Code != 201 {
			t.Fatalf("register %s: %d", username, out.Code)
		}
		var user User
		if err := json.Unmarshal(out.Body.Bytes(), &user); err != nil {
			t.Fatal(err)
		}
		if user.Username != username || user.Nickname != username || ids[user.ID] {
			t.Fatal("username case was changed or shared user ID")
		}
		ids[user.ID] = true
		out = request(router, "/api/auth/register", registerRequest{Username: username, Password: password}, "")
		if out.Code != 409 {
			t.Fatalf("duplicate %s accepted: %d", username, out.Code)
		}
		out = request(router, "/api/auth/login", loginRequest{Username: " " + username + " ", Password: password}, "")
		var result LoginResult
		if out.Code != 200 {
			t.Fatalf("login %s: %d", username, out.Code)
		}
		if err := json.Unmarshal(out.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.User.ID != user.ID || result.User.Username != username {
			t.Fatal("login matched a different case")
		}
	}
	if out := request(router, "/api/auth/login", loginRequest{Username: "liz", Password: "pass00"}, ""); out.Code != 401 {
		t.Fatal("login accepted another case's password")
	}
}

func authTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("JETAIME_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set JETAIME_TEST_DATABASE_URL for PostgreSQL auth integration tests")
	}
	ctx := context.Background()
	admin, err := database.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	schema := "auth_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return pool
}

func request(handler http.Handler, path string, body any, token string) *httptest.ResponseRecorder {
	data, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	r := httptest.NewRequest("POST", path, strings.NewReader(string(data)))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	out := httptest.NewRecorder()
	handler.ServeHTTP(out, r)
	return out
}

func TestPostgresAuthLifecycle(t *testing.T) {
	pool := authTestPool(t)
	store := NewPostgresStore(pool)
	service, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	router := httpapi.NewRouter(NewHandler(service))
	out := request(router, "/api/auth/register", registerRequest{Username: " alice_1 ", Password: testPassword, Nickname: " 测试昵称 "}, "")
	if out.Code != 201 {
		t.Fatalf("register: %d %s", out.Code, out.Body.String())
	}
	var user User
	if err := json.Unmarshal(out.Body.Bytes(), &user); err != nil {
		t.Fatal(err)
	}
	if user.Username != "alice_1" || user.Nickname != "测试昵称" || user.ID == "" {
		t.Fatalf("bad user: %+v", user)
	}
	var hash string
	if err := pool.QueryRow(context.Background(), `SELECT password_hash FROM user_credentials WHERE user_id=$1`, user.ID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	match, err := argon2id.ComparePasswordAndHash(testPassword, hash)
	if err != nil || !match || !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("password hash: %v", err)
	}
	for _, username := range []string{"alice_1", " alice_1 "} {
		out = request(router, "/api/auth/register", registerRequest{Username: username, Password: testPassword}, "")
		if out.Code != 409 || !strings.Contains(out.Body.String(), `"code":"username_taken"`) {
			t.Fatalf("duplicate register %q: %d %s", username, out.Code, out.Body.String())
		}
	}
	var count int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM users`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("orphan users after duplicate: %d %v", count, err)
	}
	wrong := request(router, "/api/auth/login", loginRequest{Username: "alice_1", Password: "wrong password"}, "")
	missing := request(router, "/api/auth/login", loginRequest{Username: "nobody", Password: "wrong password"}, "")
	if wrong.Code != 401 || missing.Code != 401 || wrong.Body.String() != missing.Body.String() {
		t.Fatal("credential errors disclose whether account exists")
	}
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM auth_sessions`).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed login created session")
	}
	login := func(handler http.Handler) LoginResult {
		t.Helper()
		out := request(handler, "/api/auth/login", loginRequest{Username: " alice_1 ", Password: testPassword}, "")
		if out.Code != 200 {
			t.Fatalf("login: %d %s", out.Code, out.Body.String())
		}
		var result LoginResult
		if err := json.Unmarshal(out.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.User.ID != user.ID || result.TokenType != "Bearer" || len(result.Token) != 43 || time.Until(result.ExpiresAt) < 6*24*time.Hour {
			t.Fatal("bad login result")
		}
		if strings.Contains(out.Body.String(), hash) || strings.Contains(out.Body.String(), testPassword) {
			t.Fatal("login leaked credentials")
		}
		return result
	}
	first, second := login(router), login(router)
	if first.Token == second.Token {
		t.Fatal("devices share token")
	}
	firstHash := sha256.Sum256([]byte(first.Token))
	var storedHash []byte
	if err := pool.QueryRow(context.Background(), `SELECT token_hash FROM auth_sessions WHERE token_hash=$1`, firstHash[:]).Scan(&storedHash); err != nil || len(storedHash) != 32 {
		t.Fatalf("stored token hash: %v", err)
	}
	// Recreate application state using a fresh pool; sessions live in PostgreSQL.
	reopened, err := pgxpool.NewWithConfig(context.Background(), pool.Config().Copy())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restarted, err := NewService(NewPostgresStore(reopened))
	if err != nil {
		t.Fatal(err)
	}
	restartedRouter := httpapi.NewRouter(NewHandler(restarted))
	for range 2 {
		out := request(restartedRouter, "/api/auth/logout", nil, first.Token)
		if out.Code != 204 || out.Body.Len() != 0 {
			t.Fatalf("logout: %d %s", out.Code, out.Body.String())
		}
	}
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM auth_sessions WHERE revoked_at IS NULL AND expires_at>now()`).Scan(&count); err != nil || count != 1 {
		t.Fatal("logout revoked other device or failed to revoke session")
	}
	// Expire the other session using valid timestamps; cleanup is not required.
	if _, err := pool.Exec(context.Background(), `UPDATE auth_sessions SET created_at=now()-interval '2 days', expires_at=now()-interval '1 day' WHERE revoked_at IS NULL`); err != nil {
		t.Fatal(err)
	}
	if out := request(router, "/api/auth/logout", nil, second.Token); out.Code != 401 {
		t.Fatalf("expired token accepted: %d", out.Code)
	}
	if out := request(router, "/api/auth/logout", nil, strings.Repeat("A", 43)); out.Code != 401 {
		t.Fatalf("unknown token accepted: %d", out.Code)
	}
	// Storage failures return a generic error without SQL or credentials.
	reopened.Close()
	out = request(restartedRouter, "/api/auth/login", loginRequest{Username: "alice_1", Password: testPassword}, "")
	if out.Code != 500 || strings.Contains(out.Body.String(), "pool") || strings.Contains(out.Body.String(), testPassword) {
		t.Fatalf("unsafe storage error: %d %s", out.Code, out.Body.String())
	}
}

func TestPostgresConcurrentRegistration(t *testing.T) {
	pool := authTestPool(t)
	store := NewPostgresStore(pool)
	var wg sync.WaitGroup
	results := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.CreateUser(context.Background(), "same_account", "test", "fixture-not-a-real-hash")
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		switch err {
		case nil:
			successes++
		case ErrUsernameTaken:
			conflicts++
		default:
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 3 {
		t.Fatalf("success %d conflicts %d", successes, conflicts)
	}
	var users, credentials int
	if err := pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM users),(SELECT count(*) FROM user_credentials)`).Scan(&users, &credentials); err != nil || users != 1 || credentials != 1 {
		t.Fatalf("atomic register: %d %d %v", users, credentials, err)
	}
}

type registrationBarrierStore struct {
	*PostgresStore
	ready sync.WaitGroup
}

func (s *registrationBarrierStore) UsernameExists(ctx context.Context, username string) (bool, error) {
	exists, err := s.PostgresStore.UsernameExists(ctx, username)
	s.ready.Done()
	s.ready.Wait()
	return exists, err
}

func TestPostgresRegistrationsRaceAfterPrecheck(t *testing.T) {
	pool := authTestPool(t)
	store := &registrationBarrierStore{PostgresStore: NewPostgresStore(pool)}
	store.ready.Add(2)
	service, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	// Both database lookups finish before either request can create the user.
	for _, username := range []string{"racing_user", " racing_user "} {
		go func() {
			_, err := service.Register(context.Background(), username, testPassword, "test")
			results <- err
		}()
	}
	successes, conflicts := 0, 0
	for range 2 {
		switch err := <-results; err {
		case nil:
			successes++
		case ErrUsernameTaken:
			conflicts++
		default:
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("race after precheck: success %d conflicts %d", successes, conflicts)
	}
	var users, credentials int
	if err := pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM users),(SELECT count(*) FROM user_credentials)`).Scan(&users, &credentials); err != nil || users != 1 || credentials != 1 {
		t.Fatalf("race left orphan user: %d %d %v", users, credentials, err)
	}
}

func TestValidationAndPasswordWorkLimit(t *testing.T) {
	service, err := NewService(usernameLookupStub{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []registerRequest{
		{Username: "a", Password: testPassword},
		{Username: "bad@account", Password: testPassword},
		{Username: "valid", Password: "short"},
		{Username: "valid", Password: strings.Repeat("长", 129)},
		{Username: "valid", Password: testPassword, Nickname: strings.Repeat("名", 25)},
		{Username: "valid", Password: testPassword, Nickname: "invalid\x00nickname"},
	} {
		if _, err := service.Register(context.Background(), tc.Username, tc.Password, tc.Nickname); err != ErrInvalidInput {
			t.Fatalf("invalid input accepted: %v", err)
		}
	}
	for range cap(service.passwordWork) {
		service.passwordWork <- struct{}{}
	}
	if _, err := service.Login(context.Background(), "valid", testPassword); err != ErrBusy {
		t.Fatal("password work was not bounded")
	}
	if _, err := service.Register(context.Background(), "valid", testPassword, ""); err != ErrBusy {
		t.Fatal("registration bypassed password work limit")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := service.beginPasswordWork(cancelled); err != context.Canceled {
		t.Fatalf("cancelled request: %v", err)
	}
	out := httptest.NewRecorder()
	respondError(out, fmt.Errorf("database error containing secret"))
	if strings.Contains(out.Body.String(), "secret") {
		t.Fatal("internal error leaked")
	}
}

type usernameLookupStub struct {
	Store
	exists bool
	err    error
}

func (s usernameLookupStub) UsernameExists(context.Context, string) (bool, error) {
	return s.exists, s.err
}

func TestRegistrationPrecheckBeforeHashing(t *testing.T) {
	// With all password slots occupied, an existing account still returns a
	// conflict instead of busy; no password work or user insertion is attempted.
	service, err := NewService(usernameLookupStub{exists: true})
	if err != nil {
		t.Fatal(err)
	}
	for range cap(service.passwordWork) {
		service.passwordWork <- struct{}{}
	}
	if _, err := service.Register(context.Background(), " Existing_User ", testPassword, ""); err != ErrUsernameTaken {
		t.Fatalf("existing username did not fail early: %v", err)
	}
	service.store = usernameLookupStub{err: context.DeadlineExceeded}
	if _, err := service.Register(context.Background(), "existing_user", testPassword, ""); err != context.DeadlineExceeded {
		t.Fatalf("failed lookup treated as available: %v", err)
	}
}
