package contacts

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/auth"
	"github.com/LizHu95/Jetaime/services/api/internal/database"
	"github.com/LizHu95/Jetaime/services/api/internal/httpapi"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func contactTestPool(t *testing.T) *pgxpool.Pool {
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
	schema := "contacts_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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

func TestPostgresContactsLifecycle(t *testing.T) {
	pool := contactTestPool(t)
	ctx := context.Background()
	accounts, err := auth.NewService(auth.NewPostgresStore(pool))
	if err != nil {
		t.Fatal(err)
	}
	people := []auth.User{}
	tokens := []string{}
	for _, name := range []string{"Alice", "Bob", "Carol"} {
		u, e := accounts.Register(ctx, name, "pass123", name)
		if e != nil {
			t.Fatal(e)
		}
		people = append(people, u)
		login, e := accounts.Login(ctx, name, "pass123")
		if e != nil {
			t.Fatal(e)
		}
		tokens = append(tokens, login.Token)
	}
	authentication := auth.NewHandler(accounts)
	router := httpapi.NewRouter(authentication, NewHandler(NewService(NewPostgresStore(pool)), authentication))
	call := func(method, path, body, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		out := httptest.NewRecorder()
		router.ServeHTTP(out, r)
		return out
	}
	for _, path := range []string{"/api/contacts/users?username=Bob", "/api/contacts/requests", "/api/groups"} {
		if out := call("GET", path, "", ""); out.Code != 401 {
			t.Fatalf("unauthenticated %s: %d", path, out.Code)
		}
	}
	if out := call("GET", "/api/contacts/users?username=bob", "", tokens[0]); out.Code != 404 {
		t.Fatal("case-insensitive lookup")
	}
	if out := call("POST", "/api/contacts/requests", `{"username":"Alice"}`, tokens[0]); out.Code != 400 {
		t.Fatal("self add accepted")
	}
	if out := call("POST", "/api/contacts/requests", `{"username":"Nobody"}`, tokens[0]); out.Code != 404 {
		t.Fatal("unknown user accepted")
	}
	if out := call("POST", "/api/contacts/requests", `{"username":"Bob","fromUserId":"fake"}`, tokens[0]); out.Code != 400 {
		t.Fatal("actor spoof accepted")
	}
	// Simultaneous reverse requests must converge on one pending request.
	var wg sync.WaitGroup
	outs := make([]*httptest.ResponseRecorder, 2)
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			target := []string{"Bob", "Alice"}[i]
			outs[i] = call("POST", "/api/contacts/requests", `{"username":"`+target+`"}`, tokens[i])
		}(i)
	}
	wg.Wait()
	var req Request
	for _, out := range outs {
		if out.Code != 200 {
			t.Fatalf("send: %d %s", out.Code, out.Body.String())
		}
		var r Request
		if err := json.Unmarshal(out.Body.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		if req.ID != "" && req.ID != r.ID {
			t.Fatal("reverse send duplicated request")
		}
		req = r
	}
	sender, receiver := 0, 1
	if req.From.ID == people[1].ID {
		sender, receiver = 1, 0
	}
	base := "/api/contacts/requests/" + req.ID + "/"
	if out := call("POST", base+"accept", "", tokens[sender]); out.Code != 403 {
		t.Fatal("sender accepted own request")
	}
	if out := call("POST", base+"cancel", "", tokens[receiver]); out.Code != 403 {
		t.Fatal("receiver cancelled request")
	}
	if out := call("POST", base+"accept", "", tokens[2]); out.Code != 404 {
		t.Fatal("third-party resolved request")
	}
	for i := range 2 {
		wg.Add(1)
		go func(i int) { defer wg.Done(); outs[i] = call("POST", base+"accept", "", tokens[receiver]) }(i)
	}
	wg.Wait()
	var accepted Request
	for _, out := range outs {
		if out.Code != 200 {
			t.Fatalf("accept: %d %s", out.Code, out.Body.String())
		}
		var r Request
		_ = json.Unmarshal(out.Body.Bytes(), &r)
		if r.GroupID == nil {
			t.Fatal("no group")
		}
		if accepted.GroupID != nil && *accepted.GroupID != *r.GroupID {
			t.Fatal("duplicate group")
		}
		accepted = r
	}
	for table, want := range map[string]int{"chat_groups": 1, "group_members": 2, "group_assistants": 1} {
		var count int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != want {
			t.Fatalf("%s: %d %v", table, count, err)
		}
	}
	for i := range 3 {
		out := call("GET", "/api/groups", "", tokens[i])
		var groups []Group
		if out.Code != 200 {
			t.Fatal(out.Body.String())
		}
		_ = json.Unmarshal(out.Body.Bytes(), &groups)
		want := 1
		if i == 2 {
			want = 0
		}
		if len(groups) != want {
			t.Fatal("groups leaked or missing")
		}
	}
	if out := call("POST", "/api/contacts/requests", `{"username":"Bob"}`, tokens[0]); out.Code != 409 {
		t.Fatal("connected pair request accepted")
	}
	if out := call("POST", base+"reject", "", tokens[receiver]); out.Code != 409 {
		t.Fatal("accepted request changed")
	}
	// Reject and cancel are idempotent and never create additional groups.
	for _, action := range []string{"reject", "cancel"} {
		out := call("POST", "/api/contacts/requests", `{"username":"Carol"}`, tokens[0])
		var r Request
		_ = json.Unmarshal(out.Body.Bytes(), &r)
		actor := 2
		if action == "cancel" {
			actor = 0
		}
		for range 2 {
			out = call("POST", "/api/contacts/requests/"+r.ID+"/"+action, "", tokens[actor])
			if out.Code != 200 {
				t.Fatal(out.Body.String())
			}
		}
	}
	// Session revocation gates all business endpoints.
	if err := accounts.Logout(ctx, tokens[0]); err != nil {
		t.Fatal(err)
	}
	if out := call("GET", "/api/groups", "", tokens[0]); out.Code != 401 {
		t.Fatal("revoked session accepted")
	}
}
