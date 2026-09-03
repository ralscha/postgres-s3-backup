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

// Run loads configuration and executes the requested backup, restore, or list operation.
func Run() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.logLevel}))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	var interval time.Duration
	cronSchedule := false
	if cfg.mode == "backup" && cfg.schedule != "" {
		cronSchedule = strings.HasPrefix(cfg.schedule, "@") || len(strings.Fields(cfg.schedule)) > 1
		if cronSchedule {
			if _, err := cron.ParseStandard(cfg.schedule); err != nil {
				return fmt.Errorf("invalid cron expression %q: %w", cfg.schedule, err)
			}
		} else {
			interval, err = parseInterval(cfg.schedule)
			if err != nil {
				return err
			}
		}
	}

	storage, err := newStorageClient(ctx, cfg)
	if err != nil {
		return resultAfterCancellation(ctx, err)
	}

	if cfg.mode == "restore" {
		return resultAfterCancellation(ctx, doRestore(ctx, cfg, storage, cfg.restoreTimestamp))
	}

	if cfg.mode == "list" {
		return resultAfterCancellation(ctx, doList(ctx, cfg, storage))
	}

	if cfg.schedule == "" {
		return resultAfterCancellation(ctx, doBackup(ctx, cfg, storage))
	}

	if cronSchedule {
		return runWithCron(ctx, cfg, storage)
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

func resultAfterCancellation(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		slog.Info("shutting down")
		//nolint:nilerr // OS signal cancellation is a successful, graceful shutdown.
		return nil
	}
	return err
}

func runWithCron(ctx context.Context, cfg config, storage objectStorage) error {
	logger := cronSlogLogger{}
	c := cron.New(
		cron.WithLogger(logger),
		cron.WithChain(cron.SkipIfStillRunning(logger), cron.Recover(logger)),
	)

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

type cronSlogLogger struct{}

func (cronSlogLogger) Info(msg string, keysAndValues ...any) {
	slog.Info(msg, keysAndValues...)
}

func (cronSlogLogger) Error(err error, msg string, keysAndValues ...any) {
	args := make([]any, 0, len(keysAndValues)+2)
	args = append(args, keysAndValues...)
	args = append(args, "error", err)
	slog.Error(msg, args...)
}
