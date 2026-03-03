package main

import (
	"log/slog"
	"os"

	"postgres-s3-backup/internal/app"
)

func main() {
	if err := app.Run(); err != nil {
		slog.Error("application failed", "error", err)
		os.Exit(1)
	}
}
