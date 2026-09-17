package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/simonhauck/keycloak-outline-group-sync/internal/app"
)

func main() {
	once := flag.Bool("once", false, "perform a single Sync Run and exit")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	if *once {
		err = app.RunOnce(ctx)
	} else {
		err = app.Run(ctx)
	}
	if err != nil {
		slog.Error("sync service failed", "error", err)
		os.Exit(1)
	}
}
