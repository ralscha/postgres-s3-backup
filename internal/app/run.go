package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/robfig/cron/v3"
)

func Run() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.logLevel}))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	storage, err := newStorageClient(ctx, cfg)
	if err != nil {
		return err
	}

	if cfg.mode == "restore" {
		return doRestore(ctx, cfg, storage, cfg.restoreTimestamp)
	}

	if cfg.mode == "list" {
		return doList(ctx, cfg, storage)
	}

	if cfg.schedule == "" {
		return doBackup(ctx, cfg, storage)
	}

	// Cron expression: any schedule containing spaces (e.g. "0 2 * * *")
	if strings.ContainsRune(cfg.schedule, ' ') {
		return runWithCron(ctx, cfg, storage)
	}

	// Interval-based: Go duration (e.g. "24h") or @-shorthands (@daily, @hourly, …)
	interval, err := parseSimpleSchedule(cfg.schedule)
	if err != nil {
		return err
	}

	slog.Info("schedule set", "schedule", cfg.schedule, "interval", interval.String())
	if err := doBackup(ctx, cfg, storage); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		slog.Error("backup failed", "error", err)
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("shutting down")
			return nil
		case <-ticker.C:
			if err := doBackup(ctx, cfg, storage); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				slog.Error("backup failed", "error", err)
			}
		}
	}
}

func runWithCron(ctx context.Context, cfg config, storage *storageClient) error {
	c := cron.New()

	if _, err := c.AddFunc(cfg.schedule, func() {
		if err := doBackup(ctx, cfg, storage); err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Error("backup failed", "error", err)
		}
	}); err != nil {
		return fmt.Errorf("invalid cron expression %q: %w", cfg.schedule, err)
	}

	slog.Info("cron schedule set", "schedule", cfg.schedule)

	c.Start()
	<-ctx.Done()
	slog.Info("shutting down")
	stopCtx := c.Stop()
	<-stopCtx.Done()
	return nil
}
