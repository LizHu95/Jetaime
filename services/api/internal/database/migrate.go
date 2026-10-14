package database

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/LizHu95/Jetaime/services/api/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type migration struct {
	version             int64
	name, sql, checksum string
}

func loadMigrations(source fs.FS) ([]migration, error) {
	names, err := fs.Glob(source, "*.up.sql")
	if err != nil {
		return nil, err
	}
	result := make([]migration, 0, len(names))
	seen := map[int64]bool{}
	for _, name := range names {
		prefix, _, ok := strings.Cut(name, "_")
		version, err := strconv.ParseInt(prefix, 10, 64)
		if !ok || err != nil || version <= 0 || seen[version] {
			return nil, fmt.Errorf("invalid or duplicate migration version: %s", name)
		}
		body, err := fs.ReadFile(source, name)
		if err != nil {
			return nil, err
		}
		seen[version] = true
		result = append(result, migration{version: version, name: name, sql: string(body), checksum: fmt.Sprintf("%x", sha256.Sum256(body))})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].version < result[j].version })
	if len(result) == 0 {
		return nil, errors.New("no embedded database migrations")
	}
	return result, nil
}

const migrationTable = `CREATE TABLE IF NOT EXISTS schema_migrations (
 version bigint PRIMARY KEY,
 name text NOT NULL,
 checksum text NOT NULL,
 applied_at timestamptz NOT NULL DEFAULT now()
)`

// Migrate serializes competing runners and commits DDL with its version record.
// It never adopts unversioned tables or runs destructive down migrations.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	return migrate(ctx, pool, migrations.Files)
}

func migrate(ctx context.Context, pool *pgxpool.Pool, source fs.FS) error {
	steps, err := loadMigrations(source)
	if err != nil {
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin migration: %w", err)
	}
	defer rollback(tx)
	// Transaction-scoped lock is released even on cancellation or process failure.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(1785033061)`); err != nil {
		return fmt.Errorf("lock migrations: %w", err)
	}
	if _, err := tx.Exec(ctx, migrationTable); err != nil {
		return fmt.Errorf("create migration ledger: %w", err)
	}
	applied, err := readVersions(ctx, tx, steps)
	if err != nil {
		return err
	}
	for _, step := range steps {
		if applied[step.version] {
			continue
		}
		if _, err := tx.Exec(ctx, step.sql); err != nil {
			return fmt.Errorf("apply %s: %w", step.name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations(version, name, checksum) VALUES ($1,$2,$3)`, step.version, step.name, step.checksum); err != nil {
			return fmt.Errorf("record %s: %w", step.name, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}
	return nil
}

func rollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}

func readVersions(ctx context.Context, tx pgx.Tx, steps []migration) (map[int64]bool, error) {
	known := make(map[int64]migration, len(steps))
	for _, step := range steps {
		known[step.version] = step
	}
	rows, err := tx.Query(ctx, `SELECT version, name, checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, fmt.Errorf("read migration versions: %w", err)
	}
	defer rows.Close()
	applied := map[int64]bool{}
	var previous int64
	for rows.Next() {
		var version int64
		var name, checksum string
		if err := rows.Scan(&version, &name, &checksum); err != nil {
			return nil, err
		}
		step, ok := known[version]
		if !ok {
			return nil, fmt.Errorf("database migration %d is newer than or unknown to this binary", version)
		}
		if name != step.name || checksum != step.checksum {
			return nil, fmt.Errorf("database migration %d checksum or name mismatch", version)
		}
		applied[version] = true
		previous = version
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, step := range steps {
		if step.version < previous && !applied[step.version] {
			return nil, fmt.Errorf("database migration %d missing before applied version %d", step.version, previous)
		}
	}
	return applied, nil
}

// CheckSchema is read-only. API startup never implicitly changes the schema.
func CheckSchema(ctx context.Context, pool *pgxpool.Pool) error {
	steps, err := loadMigrations(migrations.Files)
	if err != nil {
		return err
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer rollback(tx)
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('schema_migrations') IS NOT NULL`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return errors.New("database schema not initialized; run API with -migrate")
	}
	applied, err := readVersions(ctx, tx, steps)
	if err != nil {
		return err
	}
	for _, step := range steps {
		if !applied[step.version] {
			return fmt.Errorf("database migration %d pending; run API with -migrate", step.version)
		}
	}
	return tx.Commit(ctx)
}
