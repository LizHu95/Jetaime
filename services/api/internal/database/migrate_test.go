package database

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/LizHu95/Jetaime/services/api/migrations"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresCaseSensitiveUsernameUpgrade(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	name := "000001_chat_schema.up.sql"
	baseline, err := migrations.Files.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, pool, fstest.MapFS{name: {Data: baseline}}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,nickname) VALUES ('00000000-0000-0000-0000-000000000001','existing');
 INSERT INTO user_credentials(user_id,username,password_hash) VALUES ('00000000-0000-0000-0000-000000000001','liz','fixture')`); err != nil {
		t.Fatal(err)
	}
	if err := CheckSchema(ctx, pool); err == nil {
		t.Fatal("startup accepted missing case-sensitive migration")
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := CheckSchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var username string
	if err := pool.QueryRow(ctx, `SELECT username FROM user_credentials WHERE user_id='00000000-0000-0000-0000-000000000001'`).Scan(&username); err != nil || username != "liz" {
		t.Fatal("migration renamed or removed existing user")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,nickname) VALUES ('00000000-0000-0000-0000-000000000002','new');
 INSERT INTO user_credentials(user_id,username,password_hash) VALUES ('00000000-0000-0000-0000-000000000002','Liz','fixture')`); err != nil {
		t.Fatalf("different case blocked after upgrade: %v", err)
	}
}

func TestMigrationFiles(t *testing.T) {
	for _, files := range []fstest.MapFS{
		{},
		{"bad.up.sql": {Data: []byte("SELECT 1")}},
		{"000000_zero.up.sql": {Data: []byte("SELECT 1")}},
		{"1_a.up.sql": {}, "01_b.up.sql": {}},
	} {
		if _, err := loadMigrations(files); err == nil {
			t.Fatal("accepted invalid migrations")
		}
	}
	steps, err := loadMigrations(fstest.MapFS{
		"10_later.up.sql":   {Data: []byte("SELECT 10")},
		"2_first.up.sql":    {Data: []byte("SELECT 2")},
		"1_check.check.sql": {Data: []byte("ignored")},
	})
	if err != nil || len(steps) != 2 || steps[0].version != 2 || steps[1].version != 10 {
		t.Fatalf("numeric ordering: %v, %v", steps, err)
	}
}

func TestConnectionErrorsHideCredentials(t *testing.T) {
	for _, url := range []string{"", "postgres://secret:super-secret@%invalid", "postgres://secret:super-secret@127.0.0.1:1/test?sslmode=disable"} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		pool, err := Open(ctx, url)
		cancel()
		if pool != nil {
			pool.Close()
		}
		if err == nil || strings.Contains(err.Error(), "super-secret") || strings.Contains(err.Error(), "postgres://") {
			t.Fatalf("unsafe or missing error: %v", err)
		}
	}
}

// Every integration test owns a fresh schema; no existing tables are modified.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("JETAIME_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set JETAIME_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx := context.Background()
	admin, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	schema := "test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
	return pool
}

func TestPostgresBaselineAndConcurrentMigration(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if err := CheckSchema(ctx, pool); err == nil {
		t.Fatal("empty schema passed startup check")
	}
	var wg sync.WaitGroup
	errors := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() { defer wg.Done(); errors <- Migrate(ctx, pool) }()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := CheckSchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("ledger count %d: %v", count, err)
	}
	checks, err := os.ReadFile("../../migrations/000001_chat_schema.check.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(checks)); err != nil {
		t.Fatalf("baseline constraint checks: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("fictional fixtures were not rolled back: %d, %v", count, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE schema_migrations SET checksum = 'modified'`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool); err == nil {
		t.Fatal("accepted altered checksum")
	}
	if err := CheckSchema(ctx, pool); err == nil {
		t.Fatal("startup accepted altered checksum")
	}
}

func TestPostgresMigrationRollbackAndVersionChecks(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	broken := fstest.MapFS{"1_broken.up.sql": {Data: []byte("CREATE TABLE partial (id int); SELECT missing_column FROM partial;")}}
	if err := migrate(ctx, pool, broken); err == nil {
		t.Fatal("broken migration succeeded")
	}
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('partial') IS NOT NULL OR to_regclass('schema_migrations') IS NOT NULL`).Scan(&exists); err != nil || exists {
		t.Fatalf("failed migration left partial DDL or ledger: %v, %v", exists, err)
	}
	one := fstest.MapFS{"1_one.up.sql": {Data: []byte("CREATE TABLE example (id int)")}}
	if err := migrate(ctx, pool, one); err != nil {
		t.Fatal(err)
	}
	two := fstest.MapFS{"1_one.up.sql": one["1_one.up.sql"], "2_two.up.sql": {Data: []byte("ALTER TABLE example ADD COLUMN name text")}}
	if err := migrate(ctx, pool, two); err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, pool, one); err == nil {
		t.Fatal("older binary accepted newer database")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM schema_migrations WHERE version = 1`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, pool, two); err == nil {
		t.Fatal("accepted missing earlier migration")
	}
}

func TestPostgresUnversionedSchemaRejected(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `CREATE TABLE users (id int)`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool); err == nil {
		t.Fatal("adopted unversioned tables")
	}
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('schema_migrations') IS NOT NULL`).Scan(&exists); err != nil || exists {
		t.Fatalf("failed baseline created ledger: %v, %v", exists, err)
	}
}

func TestPostgresMigrationLockCancellation(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollback(tx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(1785033061)`); err != nil {
		t.Fatal(err)
	}
	blocked, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if err := Migrate(blocked, pool); err == nil {
		t.Fatal("migration bypassed competing runner")
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("lock not released after cancellation: %v", err)
	}
}
