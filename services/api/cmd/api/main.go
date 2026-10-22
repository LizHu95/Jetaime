package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/LizHu95/Jetaime/services/api/internal/app"
	"github.com/LizHu95/Jetaime/services/api/internal/config"
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
	if err := app.Run(ctx, cfg); err != nil {
		log.Fatal(err)
	}
}
