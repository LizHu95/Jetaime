// Package app assembles the service and manages its lifetime.
package app

import (
	"context"

	"github.com/LizHu95/Jetaime/services/api/internal/config"
	"github.com/LizHu95/Jetaime/services/api/internal/httpapi"
)

// Run assembles the HTTP service and its workers, then serves until cancellation.
// Command dispatch and tracing lifetime belong to cmd/api.
func Run(ctx context.Context, cfg config.Config) error {
	ctx, stop := context.WithCancel(ctx)
	defer stop()

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
