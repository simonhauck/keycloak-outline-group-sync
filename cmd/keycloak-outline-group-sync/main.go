package main

import (
	"context"
	"flag"
	"log/slog"
	"os"

	"github.com/simonhauck/keycloak-outline-group-sync/internal/app"
)

func main() {
	flag.Bool("once", false, "perform a single Sync Run and exit")
	flag.Parse()

	if err := app.Run(context.Background()); err != nil {
		slog.Error("sync run failed", "error", err)
		os.Exit(1)
	}
}
