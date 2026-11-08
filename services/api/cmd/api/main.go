package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/app"
	"github.com/LizHu95/Jetaime/services/api/internal/config"
	"github.com/LizHu95/Jetaime/services/api/internal/telemetry"
)

func main() {
	cfg, err := config.Parse(os.Args[1:])
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg); err != nil {
		log.Fatal(err)
	}
}

// run owns command dispatch and tracing for both one-shot commands and the server.
func run(ctx context.Context, cfg config.Config) error {
	shutdownTracing, err := telemetry.Init(ctx)
	if err != nil {
		return err
	}
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownTracing(shutdown); err != nil {
			log.Print("trace shutdown failed")
		}
	}()

	switch {
	case cfg.MigrateOnly:
		return app.MigrateDatabase(ctx, cfg.DatabaseURL)
	case cfg.MemoryCheck:
		return app.CheckMemory(ctx, cfg)
	case cfg.MemoryBackfill:
		return app.BackfillMemory(ctx, cfg)
	default:
		return app.Run(ctx, cfg)
	}
}
