package app

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/config"
	"github.com/LizHu95/Jetaime/services/api/internal/database"
	"github.com/jackc/pgx/v5/pgxpool"
)

func openDatabase(ctx context.Context, url string) (*pgxpool.Pool, error) {
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	pool, err := database.Open(startup, url)
	if err != nil {
		return nil, err
	}
	if err = database.CheckSchema(startup, pool); err != nil {
		pool.Close()
		return nil, err
	}
	fmt.Println("PostgreSQL connected; schema verified")
	return pool, nil
}

// MigrateDatabase applies schema migrations and closes the connection.
func MigrateDatabase(ctx context.Context, url string) error {
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	pool, err := database.Open(startup, url)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err = database.Migrate(startup, pool); err != nil {
		return err
	}
	fmt.Println("PostgreSQL migrations are current")
	return nil
}

func serveHTTP(ctx context.Context, cfg config.Config, handler http.Handler) error {
	listener, err := net.Listen("tcp", cfg.Address)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	defer func() { _ = server.Close() }()
	fmt.Printf("HTTP API: http://%s\n", listener.Addr())
	serveError := make(chan error, 1)
	go func() { serveError <- server.Serve(listener) }()
	select {
	case err := <-serveError:
		if err != http.ErrServerClosed {
			return err
		}
		return nil
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
			return err
		}
		return nil
	}
}
