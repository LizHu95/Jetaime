// Package app assembles the service and manages its lifetime.
package app

import (
	"context"

	"github.com/LizHu95/Jetaime/services/api/internal/config"
	"github.com/LizHu95/Jetaime/services/api/internal/httpapi"
	"github.com/LizHu95/Jetaime/services/api/internal/telemetry"
)

// Run starts tracing, assembles enabled modules, then serves HTTP until cancellation.
// Configuration parsing lives in config; endpoint bindings live in httpapi/router.go.
func Run(ctx context.Context, cfg config.Config) error {
	ctx, stop := context.WithCancel(ctx)
	defer stop()

	shutdownTracing, err := telemetry.Init(ctx)
	if err != nil {
		return err
	}
	defer flushTracing(shutdownTracing)

	if cfg.MigrateOnly {
		return migrateDatabase(ctx, cfg.DatabaseURL)
	}

	modules, err := buildModules(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() {
		stop()
		modules.close()
	}()

	modules.startWorkers(ctx)
	return serveHTTP(ctx, cfg, httpapi.NewRouter(modules.handlers))
}
