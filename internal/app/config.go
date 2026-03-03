package app

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

func loadConfig() (config, error) {
	var cfg config

	cfg.s3Bucket = strings.TrimSpace(os.Getenv("S3_BUCKET"))
	if cfg.s3Bucket == "" {
		return cfg, errors.New("you need to set the S3_BUCKET environment variable")
	}

	cfg.postgresDatabase = strings.TrimSpace(os.Getenv("POSTGRES_DATABASE"))
	if cfg.postgresDatabase == "" {
		return cfg, errors.New("you need to set the POSTGRES_DATABASE environment variable")
	}

	cfg.postgresHost = strings.TrimSpace(os.Getenv("POSTGRES_HOST"))
	if cfg.postgresHost == "" {
		return cfg, errors.New("you need to set the POSTGRES_HOST environment variable")
	}

	cfg.postgresUser = strings.TrimSpace(os.Getenv("POSTGRES_USER"))
	if cfg.postgresUser == "" {
		return cfg, errors.New("you need to set the POSTGRES_USER environment variable")
	}

	cfg.postgresPassword = os.Getenv("POSTGRES_PASSWORD")

	if cfg.postgresPort == "" {
		cfg.postgresPort = strings.TrimSpace(os.Getenv("POSTGRES_PORT"))
	}
	if cfg.postgresPort == "" {
		cfg.postgresPort = defaultPostgresPort
	}

	cfg.pgDumpExtraOpts = splitArgs(strings.TrimSpace(os.Getenv("PGDUMP_EXTRA_OPTS")))
	cfg.pgDumpCompressionLevel = defaultPgDumpCompressionLevel
	if raw := strings.TrimSpace(os.Getenv("PGDUMP_COMPRESS_LEVEL")); raw != "" {
		value, parseErr := strconv.Atoi(raw)
		if parseErr != nil || value < 0 || value > 9 {
			return cfg, fmt.Errorf("invalid PGDUMP_COMPRESS_LEVEL value %q (allowed: 0-9)", raw)
		}
		cfg.pgDumpCompressionLevel = value
	}
	cfg.s3AccessKeyID = strings.TrimSpace(os.Getenv("S3_ACCESS_KEY_ID"))
	cfg.s3SecretAccessKey = strings.TrimSpace(os.Getenv("S3_SECRET_ACCESS_KEY"))
	cfg.s3Region = strings.TrimSpace(os.Getenv("S3_REGION"))
	if cfg.s3Region == "" {
		return cfg, errors.New("you need to set the S3_REGION environment variable")
	}

	cfg.s3Prefix = strings.Trim(strings.TrimSpace(os.Getenv("S3_PREFIX")), "/")

	cfg.s3Endpoint = strings.TrimSpace(os.Getenv("S3_ENDPOINT"))
	cfg.schedule = strings.TrimSpace(os.Getenv("SCHEDULE"))
	cfg.passphrase = os.Getenv("PASSPHRASE")
	cfg.agePublicKey = strings.TrimSpace(os.Getenv("AGE_PUBLIC_KEY"))
	if cfg.agePublicKey != "" && cfg.passphrase != "" {
		return cfg, errors.New("AGE_PUBLIC_KEY and PASSPHRASE are mutually exclusive; set only one")
	}

	cfg.ageWorkFactor = defaultAgeWorkFactor
	if raw := strings.TrimSpace(os.Getenv("AGE_WORK_FACTOR")); raw != "" {
		wf, parseErr := strconv.Atoi(raw)
		if parseErr != nil || wf < 1 || wf > 30 {
			return cfg, fmt.Errorf("invalid AGE_WORK_FACTOR value %q (allowed: 1-30)", raw)
		}
		cfg.ageWorkFactor = wf
	}

	cfg.restoreTimestamp = strings.TrimSpace(os.Getenv("RESTORE_TIMESTAMP"))

	rawMode := strings.ToLower(strings.TrimSpace(os.Getenv("MODE")))
	if rawMode == "" {
		rawMode = "backup"
	}
	if rawMode != "backup" && rawMode != "restore" && rawMode != "list" {
		return cfg, fmt.Errorf("invalid MODE %q (allowed: backup, restore, list)", rawMode)
	}
	cfg.mode = rawMode

	level, err := parseLogLevel(strings.TrimSpace(os.Getenv("LOG_LEVEL")))
	if err != nil {
		return cfg, err
	}
	cfg.logLevel = level

	mode := strings.ToLower(strings.TrimSpace(os.Getenv("S3_ADDRESSING_MODE")))
	if mode == "" {
		mode = string(addressingPath)
	}
	if mode != string(addressingPath) && mode != string(addressingVirtual) && mode != string(addressingAuto) {
		return cfg, fmt.Errorf("invalid S3_ADDRESSING_MODE %q (allowed: auto,path,virtual)", mode)
	}
	cfg.s3AddressingMode = addressingMode(mode)

	keepDays := strings.TrimSpace(os.Getenv("BACKUP_KEEP_DAYS"))
	if keepDays != "" {
		value, parseErr := strconv.Atoi(keepDays)
		if parseErr != nil || value < 0 {
			return cfg, fmt.Errorf("invalid BACKUP_KEEP_DAYS value %q", keepDays)
		}
		cfg.backupKeepDays = value
	}

	return cfg, nil
}

func parseLogLevel(raw string) (slog.Level, error) {
	if raw == "" {
		return slog.LevelInfo, nil
	}

	var level slog.Level
	if err := level.UnmarshalText([]byte(strings.ToLower(raw))); err != nil {
		return 0, fmt.Errorf("invalid LOG_LEVEL %q (allowed: debug,info,warn,error)", raw)
	}

	return level, nil
}

func parseSimpleSchedule(schedule string) (time.Duration, error) {
	s := strings.TrimSpace(schedule)
	if s == "" {
		return 0, errors.New("empty schedule")
	}

	if strings.HasPrefix(s, "@") {
		switch strings.ToLower(s) {
		case "@hourly":
			return time.Hour, nil
		case "@daily", "@midnight":
			return 24 * time.Hour, nil
		case "@weekly":
			return 7 * 24 * time.Hour, nil
		case "@monthly":
			return 30 * 24 * time.Hour, nil
		case "@yearly", "@annually":
			return 365 * 24 * time.Hour, nil
		default:
			return 0, fmt.Errorf("unsupported schedule %q (supported: @hourly,@daily,@weekly,@monthly,@yearly, Go duration like 24h, or cron expression like \"0 2 * * *\")", schedule)
		}
	}

	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("unsupported schedule %q (use predefined @..., Go duration like 24h, or cron expression like \"0 2 * * *\"): %w", schedule, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("schedule duration must be > 0: %q", schedule)
	}
	return d, nil
}

func splitArgs(raw string) []string {
	if raw == "" {
		return nil
	}
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return nil
	}
	return fields
}
