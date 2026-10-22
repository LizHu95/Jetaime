// Package database owns PostgreSQL connections and schema version management.
package database

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Open verifies a connection before returning. Errors deliberately exclude the
// connection string, which may contain a password.
func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	if strings.TrimSpace(url) == "" {
		return nil, errors.New("JETAIME_DATABASE_URL is required")
	}
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, errors.New("invalid JETAIME_DATABASE_URL")
	}
	config.MaxConns = 10
	config.MinConns = 0
	config.MaxConnLifetime = time.Hour
	config.MaxConnIdleTime = 5 * time.Minute
	config.ConnConfig.ConnectTimeout = 5 * time.Second
	config.ConnConfig.RuntimeParams["application_name"] = "jetaime-api"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, errors.New("could not configure PostgreSQL connection pool")
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, errors.New("could not connect to PostgreSQL; check connectivity and credentials")
	}
	return pool, nil
}
